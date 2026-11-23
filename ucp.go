package main

// Universal Commerce Protocol (UCP): any AI agent can discover the shop, search the catalog, check
// out and look up its orders. Off by default (setting ucp_enabled); every route answers 404 when off.
//
// Followed: UCP release v2026-08-25, github.com/Universal-Commerce-Protocol/ucp at tag v2026-08-25
// (commit 87dd4b1fcff9105d68173120897237fa784c74ef), spec pages at https://ucp.dev/2026-08-25/specification/
// and schemas under source/schemas there. Field names come from those schemas; everything ownshop adds
// lives under the reverse-domain name "com.deemwar.ownshop..." as the spec allows. docs/ucp.md lists
// what is implemented, what is not, and how an agent connects.
//
// Money: ownshop never takes payment. Completing a checkout places a normal order (status "Waiting for
// payment") with the owner's UPI link, payment link and bank text; the owner clicks Payment received.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ucpVersion = "2026-08-25"
	ucpNS      = "com.deemwar.ownshop"
	ucpSpec    = "https://ucp.dev/" + ucpVersion

	ucpMaxBody     = 64 << 10 // bytes in one request
	ucpMaxLines    = 20
	ucpMaxQty      = 99
	ucpMaxSessions = 200 // open checkouts held in memory
	ucpSessionTTL  = 6 * time.Hour
	ucpCreatesPer  = 30 // checkout creations per minute, for the whole shop (all callers are 127.0.0.1)
)

// ucpState holds checkout sessions. They live in memory until completed: a session never touches the
// orders table, so an abandoned one burns no order or invoice number. A restart forgets open sessions.
type ucpState struct {
	mu       sync.Mutex
	sessions map[string]*ucpSession
	creates  []time.Time
	now      func() time.Time
}

func newUCPState() *ucpState {
	return &ucpState{sessions: map[string]*ucpSession{}, now: time.Now}
}

type ucpSession struct {
	ID      string
	Expires time.Time
	Lines   []ucpLine
	Buyer   ucpBuyer
	Addr    *ucpAddr
	SlotID  int64  // delivery slot (products)
	BookAt  string // service booking start, bookFmt
	Status  string // "" (open), "completed", "canceled"
	OrderID int64
}

type ucpLine struct {
	SKU string
	Qty int
}

// ---- request shapes (subsets of the UCP schemas) ----

type ucpBuyer struct {
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Email     string `json:"email,omitempty"`
	Phone     string `json:"phone_number,omitempty"`
}

// ucpAddr is common/types/postal_address.json.
type ucpAddr struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Street   string `json:"street_address,omitempty"`
	Extended string `json:"extended_address,omitempty"`
	Locality string `json:"address_locality,omitempty"`
	Region   string `json:"address_region,omitempty"`
	Postal   string `json:"postal_code,omitempty"`
	Country  string `json:"address_country,omitempty"`
}

type ucpCheckoutIn struct {
	LineItems []struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
		Quantity int `json:"quantity"`
	} `json:"line_items"`
	Buyer       *ucpBuyer `json:"buyer"`
	Fulfillment *struct {
		Methods []struct {
			Type         string    `json:"type"`
			Destinations []ucpAddr `json:"destinations"`
			Groups       []struct {
				SelectedOptionID *string `json:"selected_option_id"`
			} `json:"groups"`
		} `json:"methods"`
	} `json:"fulfillment"`
	Booking *struct {
		Start string `json:"start"`
	} `json:"com.deemwar.ownshop.booking"`
	Payment *struct {
		Instruments []struct {
			HandlerID string `json:"handler_id"`
		} `json:"instruments"`
	} `json:"payment"`
}

// ---- results ----

// ucpErr is a rejected request: an HTTP status and a UCP error_response body.
type ucpErr struct {
	Status int
	Code   string
	Path   string
	Msg    string
}

func (e *ucpErr) Error() string { return e.Msg }

func badInput(path, msg string) *ucpErr {
	return &ucpErr{Status: http.StatusBadRequest, Code: "invalid_input", Path: path, Msg: msg}
}

var errUCPNotFound = &ucpErr{Status: http.StatusNotFound, Code: "not_found", Msg: "not found"}

func (e *ucpErr) body() map[string]any {
	m := map[string]any{"type": "error", "code": e.Code, "content": e.Msg, "severity": "unrecoverable"}
	if e.Path != "" {
		m["path"] = e.Path
	}
	if e.Status == http.StatusBadRequest {
		m["severity"] = "recoverable"
	}
	return map[string]any{"ucp": map[string]any{"version": ucpVersion, "status": "error"}, "messages": []any{m}}
}

// ---- on/off, safety ----

func (a *App) ucpOn() bool { return a.S.Get("ucp_enabled") == "on" }

func localHost(h string) bool {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// ucpGuard: 404 when UCP is off; refuses browser pages from other sites (an Origin that is not this
// machine), non-JSON POSTs (so a web form cannot post here), and oversized bodies.
func (a *App) ucpGuard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.ucpOn() {
			http.NotFound(w, r)
			return
		}
		if !localHost(r.Host) {
			// Local only: a request that arrived under another host name (tunnel, LAN) is refused.
			http.Error(w, "UCP is served on this computer only", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || !localHost(u.Host) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		if (r.Method == http.MethodPost || r.Method == http.MethodPut) && r.ContentLength != 0 {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "send Content-Type: application/json", http.StatusUnsupportedMediaType)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, ucpMaxBody)
		}
		h(w, r)
	}
}

func (a *App) ucpRoutes(m *http.ServeMux) {
	if a.UCP == nil {
		a.UCP = newUCPState()
	}
	m.HandleFunc("GET /.well-known/ucp", a.ucpGuard(func(w http.ResponseWriter, r *http.Request) {
		ucpJSON(w, 200, a.ucpProfile(baseURL(r)))
	}))
	m.HandleFunc("POST /ucp/catalog/search", a.ucpGuard(a.ucpREST(func(r *http.Request, body []byte) (any, error) {
		return a.ucpSearch(body, baseURL(r))
	})))
	m.HandleFunc("POST /ucp/checkout-sessions", a.ucpGuard(a.ucpREST(func(r *http.Request, body []byte) (any, error) {
		return a.ucpCreate(body, baseURL(r))
	})))
	m.HandleFunc("GET /ucp/checkout-sessions/{id}", a.ucpGuard(a.ucpREST(func(r *http.Request, _ []byte) (any, error) {
		return a.ucpGet(r.PathValue("id"), baseURL(r))
	})))
	m.HandleFunc("PUT /ucp/checkout-sessions/{id}", a.ucpGuard(a.ucpREST(func(r *http.Request, body []byte) (any, error) {
		return a.ucpUpdate(r.PathValue("id"), body, baseURL(r))
	})))
	m.HandleFunc("POST /ucp/checkout-sessions/{id}/complete", a.ucpGuard(a.ucpREST(func(r *http.Request, body []byte) (any, error) {
		return a.ucpComplete(r.PathValue("id"), body, baseURL(r))
	})))
	m.HandleFunc("POST /ucp/checkout-sessions/{id}/cancel", a.ucpGuard(a.ucpREST(func(r *http.Request, _ []byte) (any, error) {
		return a.ucpCancel(r.PathValue("id"), baseURL(r))
	})))
	m.HandleFunc("GET /ucp/orders/{id}", a.ucpGuard(a.ucpREST(func(r *http.Request, _ []byte) (any, error) {
		q := r.URL.Query()
		return a.ucpOrder(r.PathValue("id"), ucpBuyer{Phone: q.Get("phone"), Email: q.Get("email")}, q.Get("checkout_id"), baseURL(r))
	})))
	m.HandleFunc("POST /ucp/mcp", a.ucpGuard(a.ucpMCP))
	m.HandleFunc("GET /ucp/mcp", a.ucpGuard(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		http.Error(w, "this MCP endpoint takes POST only (no SSE stream)", http.StatusMethodNotAllowed)
	}))
}

func (a *App) ucpREST(f func(r *http.Request, body []byte) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				ucpJSON(w, http.StatusRequestEntityTooLarge, badInput("", "request too large").body())
				return
			}
			body = b
		}
		out, err := f(r, body)
		var ue *ucpErr
		if errors.As(err, &ue) {
			ucpJSON(w, ue.Status, ue.body())
			return
		}
		if err != nil {
			ucpJSON(w, 500, (&ucpErr{Status: 500, Code: "internal_error", Msg: err.Error()}).body())
			return
		}
		ucpJSON(w, 200, out)
	}
}

func ucpJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func baseURL(r *http.Request) string {
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}

// ---- discovery ----

// ucpHandlers are the offline payment methods the owner set: there is no UCP handler for paying
// outside the agent (see docs/ucp.md), so they are declared under ownshop's own name.
func (a *App) ucpHandlers() map[string]any {
	var hs []any
	add := func(id, kind string) {
		hs = append(hs, map[string]any{"id": id, "version": ucpVersion, "available_instruments": []any{map[string]any{"type": kind}}})
	}
	if strings.TrimSpace(a.S.Get("upi_id")) != "" {
		add("upi", "upi")
	}
	if strings.TrimSpace(a.S.Get("pay_note")) != "" {
		add("bank_transfer", "bank_transfer")
	}
	if strings.TrimSpace(a.S.Get("pay_link")) != "" {
		add("payment_link", "payment_link")
	}
	if a.S.Get("qr_path") != "" {
		add("payment_qr", "qr_code")
	}
	if len(hs) == 0 {
		return map[string]any{}
	}
	return map[string]any{ucpNS + ".offline_payment": hs}
}

func ucpCap(path, schema string, extends string) map[string]any {
	m := map[string]any{"version": ucpVersion, "spec": ucpSpec + "/specification/" + path, "schema": ucpSpec + "/schemas/" + schema}
	if extends != "" {
		m["extends"] = extends
	}
	return m
}

func (a *App) ucpProfile(base string) map[string]any {
	svc := func(transport, schema, endpoint string) map[string]any {
		return map[string]any{"version": ucpVersion, "spec": ucpSpec + "/specification/overview", "transport": transport,
			"schema": ucpSpec + "/services/shopping/" + schema, "endpoint": endpoint}
	}
	return map[string]any{
		"ucp": map[string]any{
			"version": ucpVersion,
			"services": map[string]any{"dev.ucp.shopping": []any{
				svc("rest", "rest.openapi.json", base+"/ucp"),
				svc("mcp", "mcp.openrpc.json", base+"/ucp/mcp"),
			}},
			"capabilities": map[string]any{
				"dev.ucp.shopping.catalog.search": []any{ucpCap("shopping/catalog/search", "shopping/catalog_search.json", "")},
				"dev.ucp.shopping.checkout":       []any{ucpCap("shopping/checkout", "shopping/checkout.json", "")},
				"dev.ucp.shopping.fulfillment":    []any{ucpCap("shopping/extensions/fulfillment", "shopping/fulfillment.json", "dev.ucp.shopping.checkout")},
				"dev.ucp.shopping.order":          []any{ucpCap("shopping/order", "shopping/order.json", "")},
			},
			"payment_handlers": a.ucpHandlers(),
		},
		ucpNS + ".merchant": map[string]any{
			"name": a.S.Get("shop_name"), "address": a.S.Get("address"), "currency": a.currency(), "sells": a.S.Get("sells"),
			"payment": "offline: the shop confirms each payment by hand; orders stay unpaid until then",
			"docs":    "docs/ucp.md in the OwnShop AI repository",
		},
	}
}

// ---- money ----

func (a *App) currency() string { return strings.ToUpper(strings.TrimSpace(a.S.Get("currency"))) }

var zeroDecimal = map[string]bool{"JPY": true, "KRW": true, "VND": true, "CLP": true, "ISK": true, "UGX": true}
var threeDecimal = map[string]bool{"KWD": true, "BHD": true, "OMR": true, "JOD": true, "TND": true, "IQD": true, "LYD": true}

// minor turns a price into the currency's minor unit (amount.json: ISO 4217 exponent).
func minor(cur string, v float64) int64 {
	exp := 2.0
	if zeroDecimal[cur] {
		exp = 0
	} else if threeDecimal[cur] {
		exp = 3
	}
	return int64(math.Round(v * math.Pow(10, exp)))
}

func totals(sub int64) []any {
	return []any{map[string]any{"type": "subtotal", "amount": sub}, map[string]any{"type": "total", "amount": sub}}
}

// ---- catalog ----

// ucpIDs are the UCP ids of the active products: the SKU, or p<rowid> when the SKU is blank or shared
// by two products (only the name is unique in the products table), so an id never means two items.
func ucpIDs(ps []Product) map[int64]string {
	n := map[string]int{}
	for _, p := range ps {
		n[p.SKU]++
	}
	ids := map[int64]string{}
	for _, p := range ps {
		ids[p.ID] = p.SKU
		if p.SKU == "" || n[p.SKU] > 1 {
			ids[p.ID] = fmt.Sprintf("p%d", p.ID)
		}
	}
	return ids
}

// findProduct is an active product by its UCP id.
func (a *App) findProduct(id string) *Product {
	ps, _ := a.S.Sellable()
	ids := ucpIDs(ps)
	for i := range ps {
		if ids[ps[i].ID] == id {
			return &ps[i]
		}
	}
	return nil
}

func (a *App) imageURL(p Product, base string) string {
	if a.Dir == "" || p.SKU == "" || ProductImage(ImagesDir(a.Dir), p.SKU) == "" {
		return ""
	}
	return base + "/products/" + url.PathEscape(p.SKU) + "/photo"
}

// freeStarts are the next free booking starts for a service (at most n), "2006-01-02T15:04".
func (a *App) freeStarts(p Product, n int) []string {
	sc := a.S.Schedule()
	min := p.Minutes
	if min <= 0 {
		min = sc.Step
	}
	now := a.now()
	out := []string{}
	for i := 0; i <= sc.Ahead && len(out) < n; i++ {
		d := startOfDay(now).AddDate(0, 0, i)
		for _, st := range a.S.FreeTimes(d, now, min, 0) {
			if len(out) < n {
				out = append(out, d.Format(dayFmt)+"T"+st)
			}
		}
	}
	return out
}

func (a *App) ucpProduct(p Product, id, base string) map[string]any {
	cur := a.currency()
	price := map[string]any{"amount": minor(cur, p.Price), "currency": cur}
	desc := p.Description
	if desc == "" {
		desc = p.Name
	}
	avail := map[string]any{"available": true, "status": "in_stock"}
	ext := map[string]any{"kind": "product"}
	if p.IsService() {
		ext = map[string]any{"kind": "service", "minutes": p.Minutes, "free_starts": a.freeStarts(p, 30)}
		if len(ext["free_starts"].([]string)) == 0 {
			avail = map[string]any{"available": false, "status": "no_free_time"}
		}
	} else {
		ext["stock"] = p.Stock
		if p.Stock <= 0 {
			avail = map[string]any{"available": false, "status": "out_of_stock"}
		}
	}
	variant := map[string]any{"id": id, "sku": p.SKU, "title": p.Name, "description": map[string]any{"plain": desc}, "price": price, "availability": avail}
	out := map[string]any{"id": id, "title": p.Name, "description": map[string]any{"plain": desc},
		"price_range": map[string]any{"min": price, "max": price}, "variants": []any{variant},
		"tags": []string{ext["kind"].(string)}, "metadata": map[string]any{ucpNS: ext}}
	if u := a.imageURL(p, base); u != "" {
		media := []any{map[string]any{"type": "image", "url": u, "alt_text": p.Name}}
		out["media"], variant["media"] = media, media
	}
	return out
}

func (a *App) ucpSearch(body []byte, base string) (any, error) {
	var in struct {
		Query   string `json:"query"`
		Filters struct {
			Price struct {
				Min *int64 `json:"min"`
				Max *int64 `json:"max"`
			} `json:"price"`
		} `json:"filters"`
		Pagination struct {
			Limit  int    `json:"limit"`
			Cursor string `json:"cursor"`
		} `json:"pagination"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, badInput("$", "body is not valid JSON for a catalog search")
		}
	}
	if len(in.Query) > 200 {
		return nil, badInput("$.query", "query is longer than 200 characters")
	}
	limit := in.Pagination.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	offset := 0
	if in.Pagination.Cursor != "" {
		n, err := strconv.Atoi(in.Pagination.Cursor)
		if err != nil || n < 0 {
			return nil, badInput("$.pagination.cursor", "unknown cursor")
		}
		offset = n
	}
	ps, err := a.S.Sellable()
	if err != nil {
		return nil, err
	}
	cur, want, ids := a.currency(), words(in.Query), ucpIDs(ps)
	var hits []Product
	for _, p := range ps {
		if len(want) > 0 {
			have := keySet(p.Name + " " + p.Description + " " + p.Keywords + " " + p.SKU)
			ok := false
			for _, w := range want {
				ok = ok || have[stem(w)] || have[w]
			}
			if !ok {
				continue
			}
		}
		amt := minor(cur, p.Price)
		if f := in.Filters.Price; (f.Min != nil && amt < *f.Min) || (f.Max != nil && amt > *f.Max) {
			continue
		}
		hits = append(hits, p)
	}
	products := []any{}
	for i := offset; i < len(hits) && i < offset+limit; i++ {
		products = append(products, a.ucpProduct(hits[i], ids[hits[i].ID], base))
	}
	page := map[string]any{"has_next_page": offset+limit < len(hits), "total_count": len(hits)}
	if offset+limit < len(hits) {
		page["cursor"] = strconv.Itoa(offset + limit)
	}
	return map[string]any{
		"ucp":        map[string]any{"version": ucpVersion, "capabilities": map[string]any{"dev.ucp.shopping.catalog.search": []any{map[string]any{"version": ucpVersion}}}},
		"products":   products,
		"pagination": page,
	}, nil
}

// ---- input validation ----

var (
	reEmail = regexp.MustCompile(`^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$`)
	rePhone = regexp.MustCompile(`^\+?[0-9][0-9 ()\-]{5,24}$`)
	reSKU   = regexp.MustCompile(`^[\pL\pN._\-/ ]{1,64}$`)
)

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func clean(path, v string, max int) (string, error) {
	v = strings.TrimSpace(v)
	if len([]rune(v)) > max {
		return "", badInput(path, fmt.Sprintf("longer than %d characters", max))
	}
	if strings.ContainsAny(v, "\x00\r\n") {
		return "", badInput(path, "contains control characters")
	}
	return v, nil
}

func (b *ucpBuyer) validate() error {
	var err error
	if b.FirstName, err = clean("$.buyer.first_name", b.FirstName, 60); err != nil {
		return err
	}
	if b.LastName, err = clean("$.buyer.last_name", b.LastName, 60); err != nil {
		return err
	}
	if b.Email, err = clean("$.buyer.email", strings.ToLower(b.Email), 120); err != nil {
		return err
	}
	if b.Email != "" && !reEmail.MatchString(b.Email) {
		return badInput("$.buyer.email", "not an email address")
	}
	if b.Phone, err = clean("$.buyer.phone_number", b.Phone, 25); err != nil {
		return err
	}
	if b.Phone != "" && (!rePhone.MatchString(b.Phone) || len(digits(b.Phone)) < 6 || len(digits(b.Phone)) > 15) {
		return badInput("$.buyer.phone_number", "not a phone number")
	}
	return nil
}

func (d *ucpAddr) validate(path string) error {
	for _, f := range []*string{&d.Street, &d.Extended, &d.Locality, &d.Region, &d.Postal, &d.Country} {
		v, err := clean(path, *f, 200)
		if err != nil {
			return err
		}
		*f = v
	}
	if d.Street == "" && d.Locality == "" {
		return badInput(path, "give at least street_address or address_locality")
	}
	return nil
}

func (d ucpAddr) text() string {
	var parts []string
	for _, v := range []string{d.Street, d.Extended, d.Locality, d.Region, d.Postal, d.Country} {
		if v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, ", ")
}

// apply validates a create/update body and writes it into the session. Line items are replaced
// (they are required on create and update); buyer, fulfillment and booking replace only when sent.
func (a *App) apply(se *ucpSession, body []byte) error {
	var in ucpCheckoutIn
	if err := json.Unmarshal(body, &in); err != nil {
		return badInput("$", "body is not valid JSON for a checkout")
	}
	if len(in.LineItems) == 0 {
		return badInput("$.line_items", "line_items is required")
	}
	if len(in.LineItems) > ucpMaxLines {
		return badInput("$.line_items", fmt.Sprintf("at most %d line items", ucpMaxLines))
	}
	var lines []ucpLine
	seen := map[string]int{}
	for i, li := range in.LineItems {
		path := fmt.Sprintf("$.line_items[%d]", i)
		id := strings.TrimSpace(li.Item.ID)
		if !reSKU.MatchString(id) {
			return badInput(path+".item.id", "item.id is missing or malformed")
		}
		if li.Quantity < 1 || li.Quantity > ucpMaxQty {
			return badInput(path+".quantity", fmt.Sprintf("quantity must be 1 to %d", ucpMaxQty))
		}
		if a.findProduct(id) == nil {
			return &ucpErr{Status: http.StatusNotFound, Code: "not_found", Path: path + ".item.id", Msg: "no such item: " + id}
		}
		if j, ok := seen[id]; ok {
			lines[j].Qty += li.Quantity
			if lines[j].Qty > ucpMaxQty {
				return badInput(path+".quantity", fmt.Sprintf("quantity must be 1 to %d", ucpMaxQty))
			}
			continue
		}
		seen[id] = len(lines)
		lines = append(lines, ucpLine{SKU: id, Qty: li.Quantity})
	}
	next := *se
	next.Lines = lines
	if in.Buyer != nil {
		if err := in.Buyer.validate(); err != nil {
			return err
		}
		next.Buyer = *in.Buyer
	}
	if f := in.Fulfillment; f != nil {
		if len(f.Methods) > 1 {
			return badInput("$.fulfillment.methods", "one fulfillment method (shipping) only")
		}
		if len(f.Methods) == 1 {
			m := f.Methods[0]
			if m.Type != "" && m.Type != "shipping" {
				return badInput("$.fulfillment.methods[0].type", "only shipping (local delivery) is offered")
			}
			if len(m.Destinations) > 1 {
				return badInput("$.fulfillment.methods[0].destinations", "one destination only")
			}
			if len(m.Destinations) == 1 {
				d := m.Destinations[0]
				if err := d.validate("$.fulfillment.methods[0].destinations[0]"); err != nil {
					return err
				}
				next.Addr = &d
			}
			if len(m.Groups) > 0 && m.Groups[0].SelectedOptionID != nil {
				opt := *m.Groups[0].SelectedOptionID
				next.SlotID = 0
				if opt != "" {
					id, err := strconv.ParseInt(strings.TrimPrefix(opt, "slot_"), 10, 64)
					if err != nil || !strings.HasPrefix(opt, "slot_") || !a.slotOpen(id) {
						return badInput("$.fulfillment.methods[0].groups[0].selected_option_id", "not one of the offered options")
					}
					next.SlotID = id
				}
			}
		}
	}
	if in.Booking != nil {
		next.BookAt = ""
		if s := strings.TrimSpace(in.Booking.Start); s != "" {
			t, err := time.ParseInLocation("2006-01-02T15:04", strings.Replace(s, " ", "T", 1), time.Local)
			if err != nil {
				return badInput("$['"+ucpNS+".booking'].start", "start must look like 2026-11-21T10:00")
			}
			next.BookAt = t.Format(bookFmt)
		}
	}
	*se = next
	return nil
}

func (a *App) slotOpen(id int64) bool {
	sl, _ := a.S.Slots()
	for _, s := range sl {
		if s.ID == id {
			return true
		}
	}
	return false
}

// ---- checkout view ----

// draft is the session as a ownshop order (not saved), so the same rules as the chat bot apply.
func (a *App) draft(se *ucpSession) Order {
	var o Order
	for _, l := range se.Lines {
		p := a.findProduct(l.SKU)
		if p == nil {
			continue
		}
		o.Items = append(o.Items, Item{ProductID: p.ID, Name: p.Name, Qty: l.Qty, Price: p.Price, Service: p.IsService(), Minutes: p.Minutes})
		o.Total += float64(l.Qty) * p.Price
	}
	if se.SlotID != 0 {
		o.SlotID.Valid, o.SlotID.Int64 = true, se.SlotID
	}
	o.BookAt = se.BookAt
	return o
}

func msgErr(code, path, content string) map[string]any {
	m := map[string]any{"type": "error", "code": code, "content": content, "severity": "recoverable"}
	if path != "" {
		m["path"] = path
	}
	return m
}

// view is the UCP checkout object for a session, with status worked out from the shop's rules.
func (a *App) view(se *ucpSession, base string, extra ...map[string]any) map[string]any {
	cur := a.currency()
	o := a.draft(se)
	msgs := []any{}
	for _, m := range extra {
		msgs = append(msgs, m)
	}
	lines := []any{}
	var productLines []string
	var sub int64
	for i, l := range se.Lines {
		id := fmt.Sprintf("li_%d", i+1)
		p := a.findProduct(l.SKU)
		if p == nil {
			msgs = append(msgs, msgErr("item_unavailable", fmt.Sprintf("$.line_items[%d]", i), l.SKU+" is no longer sold"))
			continue
		}
		amt := minor(cur, p.Price)
		item := map[string]any{"id": l.SKU, "title": p.Name, "price": amt}
		if u := a.imageURL(*p, base); u != "" {
			item["image_url"] = u
		}
		lines = append(lines, map[string]any{"id": id, "item": item, "quantity": l.Qty, "totals": totals(amt * int64(l.Qty))})
		sub += amt * int64(l.Qty)
		if !p.IsService() {
			productLines = append(productLines, id)
			if se.Status == "" && p.Stock < l.Qty {
				msgs = append(msgs, msgErr("out_of_stock", fmt.Sprintf("$.line_items[%d].quantity", i), fmt.Sprintf("only %d of %s in stock", max(p.Stock, 0), p.Name)))
			}
		}
	}
	out := map[string]any{
		"ucp": map[string]any{"version": ucpVersion, "capabilities": map[string]any{
			"dev.ucp.shopping.checkout": []any{map[string]any{"version": ucpVersion}}, "dev.ucp.shopping.fulfillment": []any{map[string]any{"version": ucpVersion}},
		}, "payment_handlers": a.ucpHandlers()},
		"id": se.ID, "line_items": lines, "currency": cur, "totals": totals(sub), "links": []any{},
		"expires_at": se.Expires.UTC().Format(time.RFC3339),
	}
	if se.Buyer != (ucpBuyer{}) {
		out["buyer"] = se.Buyer
	}
	if se.Status == "" {
		if se.Buyer.FirstName == "" && se.Buyer.LastName == "" {
			msgs = append(msgs, msgErr("buyer_name_required", "$.buyer.first_name", "give the buyer's name"))
		}
		if se.Buyer.Phone == "" && se.Buyer.Email == "" {
			msgs = append(msgs, msgErr("buyer_contact_required", "$.buyer", "give the buyer's phone_number or email"))
		}
	}
	// Delivery (products): one shipping method, the address as its destination, the owner's
	// delivery slots as the options of its one group.
	if len(productLines) > 0 {
		method := map[string]any{"id": "delivery", "type": "shipping", "line_item_ids": productLines, "selected_destination_id": nil}
		if se.Addr != nil {
			d := *se.Addr
			d.ID, d.Type = "dest_1", "shipping_address"
			method["destinations"] = []any{d}
			method["selected_destination_id"] = "dest_1"
		} else if se.Status == "" {
			msgs = append(msgs, msgErr("address_required", "$.fulfillment.methods[0].destinations", "give a delivery address"))
		}
		slots, _ := a.S.Slots()
		if len(slots) > 0 {
			opts := []any{}
			for _, s := range slots {
				opts = append(opts, map[string]any{"id": fmt.Sprintf("slot_%d", s.ID), "title": s.Label, "totals": []any{map[string]any{"type": "total", "amount": 0}}})
			}
			g := map[string]any{"id": "group_1", "line_item_ids": productLines, "options": opts, "selected_option_id": nil}
			if se.SlotID != 0 {
				g["selected_option_id"] = fmt.Sprintf("slot_%d", se.SlotID)
			} else if se.Status == "" && !o.HasService() {
				msgs = append(msgs, msgErr("fulfillment_option_required", "$.fulfillment.methods[0].groups[0].selected_option_id", "pick a delivery slot"))
			}
			method["groups"] = []any{g}
		}
		out["fulfillment"] = map[string]any{"methods": []any{method}}
	}
	// Booking (services): ownshop's own extension; free starts come from the same calendar as chat.
	if o.HasService() {
		sc := a.S.Schedule()
		min := o.ServiceMinutes(sc.Step)
		bk := map[string]any{"minutes": min}
		if se.BookAt != "" {
			bk["start"] = strings.Replace(se.BookAt, " ", "T", 1)
			bk["label"] = bookLabel(se.BookAt)
		}
		if se.Status == "" {
			if !a.bookable(sc, se.BookAt, min) {
				if se.BookAt != "" {
					msgs = append(msgs, msgErr("booking_unavailable", "$['"+ucpNS+".booking'].start", bookLabel(se.BookAt)+" is not a free time"))
				} else {
					msgs = append(msgs, msgErr("booking_required", "$['"+ucpNS+".booking'].start", "pick a start time from free_starts"))
				}
				bk["free_starts"] = a.freeStartsFor(min, 20)
			}
		}
		out[ucpNS+".booking"] = bk
	}
	switch se.Status {
	case "completed":
		out["status"] = "completed"
		if ord, _ := a.S.Order(se.OrderID); ord != nil {
			out["order"] = map[string]any{"id": strconv.FormatInt(ord.ID, 10), "label": ord.Invoice, "permalink_url": fmt.Sprintf("%s/ucp/orders/%d", base, ord.ID)}
			out[ucpNS+".payment"] = a.payment(ord, base)
			msgs = append(msgs, map[string]any{"type": "info", "code": "payment_offline", "content": a.payment(ord, base)["instructions"]})
		}
	case "canceled":
		out["status"] = "canceled"
	default:
		out["status"] = "ready_for_complete"
		for _, m := range msgs {
			if m.(map[string]any)["type"] == "error" {
				out["status"] = "incomplete"
			}
		}
	}
	out["messages"] = msgs
	return out
}

// bookable: the start is free on the shop's grid, still ahead (minimum notice included), and within the days the shop offers.
func (a *App) bookable(sc Schedule, at string, min int) bool {
	t, err := time.ParseInLocation(bookFmt, at, time.Local)
	now := a.now()
	if err != nil || !t.After(now) || t.Before(now.Add(time.Duration(sc.Notice)*time.Minute)) || !t.Before(startOfDay(now).AddDate(0, 0, sc.Ahead+1)) {
		return false
	}
	return a.S.timeFree(a.S.DB, sc, at, min, 0)
}

func (a *App) freeStartsFor(min, n int) []string {
	return a.freeStarts(Product{Minutes: min}, n)
}

// payment is how to pay a placed order: the same UPI link, payment link and bank text the chat bot
// sends. Status stays unpaid until the owner clicks Payment received.
func (a *App) payment(o *Order, base string) map[string]any {
	d := payDetails(a.S, o)
	text := render("pay", d)
	if d["UPI"] == nil && d["Link"] == "" && d["Note"] == "" && !d["QR"].(bool) {
		text = render("pay_none", d)
	}
	p := map[string]any{"status": "unpaid", "reference": strconv.FormatInt(o.ID, 10), "invoice": o.Invoice,
		"amount": minor(a.currency(), o.Total), "currency": a.currency(), "instructions": text}
	if o.Paid {
		p["status"] = "paid"
	}
	if u, _ := d["UPI"].(string); u != "" {
		p["upi_link"] = u
	}
	if l := d["Link"].(string); l != "" {
		p["payment_link"] = l
	}
	if n := d["Note"].(string); n != "" {
		p["bank_transfer"] = n
	}
	if d["QR"].(bool) {
		p["payment_qr_url"] = base + "/payment-qr"
	}
	return p
}

// ---- checkout operations ----

func newID(prefix string) string {
	b := make([]byte, 16)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// prune drops expired sessions. Caller holds mu.
func (u *ucpState) prune() {
	t := u.now()
	for id, se := range u.sessions {
		if t.After(se.Expires) {
			delete(u.sessions, id)
		}
	}
	keep := u.creates[:0]
	for _, c := range u.creates {
		if t.Sub(c) < time.Minute {
			keep = append(keep, c)
		}
	}
	u.creates = keep
}

func (a *App) ucpCreate(body []byte, base string) (any, error) {
	u := a.UCP
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prune()
	if len(u.creates) >= ucpCreatesPer {
		return nil, &ucpErr{Status: http.StatusTooManyRequests, Code: "rate_limited", Msg: "too many checkouts, try again in a minute"}
	}
	open := 0
	for _, se := range u.sessions {
		if se.Status == "" {
			open++
		}
	}
	if open >= ucpMaxSessions {
		return nil, &ucpErr{Status: http.StatusTooManyRequests, Code: "rate_limited", Msg: "too many open checkouts, try again later"}
	}
	u.creates = append(u.creates, u.now())
	se := &ucpSession{ID: newID("chk_"), Expires: u.now().Add(ucpSessionTTL)}
	if err := a.apply(se, body); err != nil {
		return nil, err
	}
	u.sessions[se.ID] = se
	return a.view(se, base), nil
}

func (a *App) session(id string) (*ucpSession, error) {
	a.UCP.prune()
	se := a.UCP.sessions[id]
	if se == nil {
		return nil, errUCPNotFound
	}
	return se, nil
}

func (a *App) ucpGet(id, base string) (any, error) {
	a.UCP.mu.Lock()
	defer a.UCP.mu.Unlock()
	se, err := a.session(id)
	if err != nil {
		return nil, err
	}
	return a.view(se, base), nil
}

func (a *App) ucpUpdate(id string, body []byte, base string) (any, error) {
	a.UCP.mu.Lock()
	defer a.UCP.mu.Unlock()
	se, err := a.session(id)
	if err != nil {
		return nil, err
	}
	if se.Status != "" {
		return nil, &ucpErr{Status: http.StatusConflict, Code: "checkout_closed", Msg: "this checkout is " + se.Status}
	}
	if err := a.apply(se, body); err != nil {
		return nil, err
	}
	return a.view(se, base), nil
}

func (a *App) ucpCancel(id, base string) (any, error) {
	a.UCP.mu.Lock()
	defer a.UCP.mu.Unlock()
	se, err := a.session(id)
	if err != nil {
		return nil, err
	}
	if se.Status == "completed" {
		return nil, &ucpErr{Status: http.StatusConflict, Code: "checkout_closed", Msg: "this checkout is completed; ask the shop to cancel the order"}
	}
	se.Status = "canceled"
	return a.view(se, base), nil
}

// ucpComplete places the order through the same Store calls as the chat bot and walk-in booking:
// Customer, AddItem, SetSlot/SetBookAt, Confirm (stock check and cut, time hold, invoice number).
// A retry of a completed checkout returns the same order and changes nothing.
func (a *App) ucpComplete(id string, body []byte, base string) (any, error) {
	a.UCP.mu.Lock()
	defer a.UCP.mu.Unlock()
	se, err := a.session(id)
	if err != nil {
		return nil, err
	}
	switch se.Status {
	case "completed":
		return a.view(se, base), nil
	case "canceled":
		return nil, &ucpErr{Status: http.StatusConflict, Code: "checkout_closed", Msg: "this checkout is canceled"}
	}
	if len(body) > 0 {
		var in ucpCheckoutIn
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, badInput("$", "body is not valid JSON for a checkout")
		}
		// payment is the only field a complete request carries; ownshop takes no credentials.
		if in.Payment != nil {
			hs := a.ucpHandlers()
			for i, ins := range in.Payment.Instruments {
				if !handlerDeclared(hs, ins.HandlerID) {
					return nil, badInput(fmt.Sprintf("$.payment.instruments[%d].handler_id", i), "not a payment handler this shop declared")
				}
			}
		}
	}
	v := a.view(se, base)
	if v["status"] != "ready_for_complete" {
		return v, nil
	}
	o := a.draft(se)
	key := "ucp:" + se.Buyer.Email
	if se.Buyer.Phone != "" {
		key = "ucp:+" + digits(se.Buyer.Phone)
	}
	c, err := a.S.Customer(key, strings.TrimSpace(se.Buyer.FirstName+" "+se.Buyer.LastName), "")
	if err != nil {
		return nil, err
	}
	a.S.SetCustomerChannel(c.ID, "ucp", "")
	if o.HasProduct() && se.Addr != nil {
		a.S.SetAddress(c.ID, se.Addr.text())
	}
	res, err := a.S.DB.Exec(`INSERT INTO orders(customer_id, status, created, updated, channel, account, ucp_checkout, ucp_phone, ucp_email)
	  VALUES (?, 'cart', ?, ?, 'ucp', '', ?, ?, ?)`, c.ID, now(), now(), se.ID, digits(se.Buyer.Phone), se.Buyer.Email)
	if err != nil {
		return nil, err
	}
	oid, _ := res.LastInsertId()
	undo := func() {
		a.S.DB.Exec(`DELETE FROM order_items WHERE order_id=?`, oid)
		a.S.DB.Exec(`DELETE FROM orders WHERE id=?`, oid)
	}
	for _, l := range se.Lines {
		p := a.findProduct(l.SKU)
		if p == nil {
			undo()
			return a.view(se, base), nil
		}
		if err := a.S.AddItem(oid, *p, l.Qty); err != nil {
			undo()
			return nil, err
		}
	}
	if se.SlotID != 0 {
		a.S.SetSlot(oid, se.SlotID)
	}
	if o.HasService() {
		a.S.SetBookAt(oid, se.BookAt, o.ServiceMinutes(a.S.Schedule().Step))
	}
	ord, err := a.S.Order(oid)
	if err != nil || ord == nil {
		undo()
		return nil, fmt.Errorf("order %d: %v", oid, err)
	}
	if _, err := a.S.Confirm(ord); err != nil {
		undo()
		if errors.Is(err, errTaken) {
			return a.view(se, base, msgErr("booking_unavailable", "$['"+ucpNS+".booking'].start", err.Error())), nil
		}
		return a.view(se, base, msgErr("out_of_stock", "$.line_items", err.Error())), nil
	}
	se.Status, se.OrderID = "completed", oid
	return a.view(se, base), nil
}

func handlerDeclared(hs map[string]any, id string) bool {
	for _, list := range hs {
		for _, h := range list.([]any) {
			if h.(map[string]any)["id"] == id {
				return true
			}
		}
	}
	return false
}

// ---- orders ----

// ucpOrder returns an order placed through UCP. Order numbers are sequential, so the caller must also
// prove it placed the order: the buyer's phone or email, or the checkout id (random, only the
// creator has it). Every miss looks the same, so it cannot be used to probe which orders exist.
func (a *App) ucpOrder(id string, who ucpBuyer, checkoutID, base string) (any, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	if err != nil || len(id) > 20 {
		return nil, errUCPNotFound
	}
	var chk, phone, email string
	if err := a.S.DB.QueryRow(`SELECT ucp_checkout, ucp_phone, ucp_email FROM orders WHERE id=? AND channel='ucp' AND status<>'cart'`, n).Scan(&chk, &phone, &email); err != nil {
		return nil, errUCPNotFound
	}
	ok := (checkoutID != "" && checkoutID == chk) ||
		(who.Phone != "" && phone != "" && digits(who.Phone) == phone) ||
		(who.Email != "" && email != "" && strings.EqualFold(strings.TrimSpace(who.Email), email))
	if !ok {
		return nil, errUCPNotFound
	}
	o, err := a.S.Order(n)
	if err != nil || o == nil {
		return nil, errUCPNotFound
	}
	cur := a.currency()
	lines := []any{}
	var all []any
	for i, it := range o.Items {
		lid := fmt.Sprintf("li_%d", i+1)
		q := map[string]any{"original": it.Qty, "total": it.Qty, "fulfilled": 0}
		status := "processing"
		switch o.Status {
		case StCancel:
			q["total"], status = 0, "removed"
		case StDone:
			q["fulfilled"], status = it.Qty, "fulfilled"
		}
		amt := minor(cur, it.Price)
		lines = append(lines, map[string]any{"id": lid, "item": map[string]any{"id": a.skuOf(it.ProductID), "title": it.Name, "price": amt},
			"quantity": q, "totals": totals(amt * int64(it.Qty)), "status": status})
		all = append(all, map[string]any{"id": lid, "quantity": it.Qty})
	}
	// Fulfillment events (append-only in the spec) are derived from the status the owner set.
	events := []any{}
	ev := map[string]string{StPacked: "processing", StShipped: "shipped", StDone: "delivered"}
	steps := []string{StPacked, StShipped, StDone}
	reached := map[string]int{StPacked: 1, StShipped: 2, StDone: 3}[o.Status]
	for i := 0; i < reached; i++ {
		events = append(events, map[string]any{"id": "ev_" + steps[i], "occurred_at": ucpTime(o.Created), "type": ev[steps[i]], "line_items": all})
	}
	out := map[string]any{
		"ucp":         map[string]any{"version": ucpVersion, "capabilities": map[string]any{"dev.ucp.shopping.order": []any{map[string]any{"version": ucpVersion}}}},
		"id":          strconv.FormatInt(o.ID, 10),
		"label":       o.Invoice,
		"checkout_id": chk, "permalink_url": fmt.Sprintf("%s/ucp/orders/%d", base, o.ID),
		"line_items": lines, "fulfillment": map[string]any{"events": events},
		"currency": cur, "totals": totals(minor(cur, o.Total)),
		"messages": []any{map[string]any{"type": "info", "code": "order_status", "content": o.StatusLabel()}},
		ucpNS + ".order": map[string]any{"status": o.Status, "status_label": o.StatusLabel(), "paid": o.Paid,
			"invoice": o.Invoice, "booking": o.SlotLabel, "deliver_to": o.DeliverTo},
	}
	if o.Status == StCancel {
		out["adjustments"] = []any{map[string]any{"id": "adj_cancel", "type": "cancellation", "occurred_at": ucpTime(o.Created), "status": "completed"}}
	}
	if !o.Paid && o.Status != StCancel {
		out[ucpNS+".payment"] = a.payment(o, base)
	}
	return out, nil
}

func (a *App) skuOf(productID int64) string {
	var sku string
	a.S.DB.QueryRow(`SELECT sku FROM products WHERE id=?`, productID).Scan(&sku)
	if sku == "" {
		return fmt.Sprintf("p%d", productID)
	}
	return sku
}

func ucpTime(local string) string {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", local, time.Local)
	if err != nil {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return t.UTC().Format(time.RFC3339)
}
