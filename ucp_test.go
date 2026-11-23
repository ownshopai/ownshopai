package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ucpServer: the sample shop with UCP on, served by httptest (never the real shop on 2007).
func ucpServer(t *testing.T, h *harness) (*httptest.Server, *App) {
	app := &App{S: h.s, Bot: h.b, File: NewShopFile(h.s, filepath.Join(h.dir, "products.xlsx")), Dir: h.dir}
	srv := httptest.NewServer(app.Routes())
	t.Cleanup(srv.Close)
	h.s.Set("ucp_enabled", "on")
	h.s.Set("upi_id", "demo-shop@upi")
	h.s.Set("pay_note", "Bank: Demo Bank, Account: 000-FAKE")
	return srv, app
}

func ucpDo(t *testing.T, method, u, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, u, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return res.StatusCode, m
}

func codes(m map[string]any) string {
	var out []string
	msgs, _ := m["messages"].([]any)
	for _, x := range msgs {
		out = append(out, x.(map[string]any)["code"].(string))
	}
	return strings.Join(out, ",")
}

func slotID(t *testing.T, s *Store) string {
	sl, _ := s.Slots()
	if len(sl) == 0 {
		t.Fatal("sample has no delivery slots")
	}
	return "slot_" + jnum(sl[0].ID)
}

func jnum(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestUCPDisabledIs404Everywhere(t *testing.T) {
	h := newHarness(t)
	srv, _ := ucpServer(t, h)
	h.s.Set("ucp_enabled", "")
	for _, c := range [][3]string{
		{"GET", "/.well-known/ucp", ""}, {"POST", "/ucp/catalog/search", "{}"}, {"POST", "/ucp/checkout-sessions", "{}"},
		{"GET", "/ucp/checkout-sessions/x", ""}, {"PUT", "/ucp/checkout-sessions/x", "{}"}, {"POST", "/ucp/checkout-sessions/x/complete", "{}"},
		{"POST", "/ucp/checkout-sessions/x/cancel", "{}"}, {"GET", "/ucp/orders/1042", ""}, {"POST", "/ucp/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`},
	} {
		if st, _ := ucpDo(t, c[0], srv.URL+c[1], c[2]); st != 404 {
			t.Fatalf("%s %s with UCP off: %d, want 404", c[0], c[1], st)
		}
	}
}

func TestUCPDiscoveryProfile(t *testing.T) {
	h := newHarness(t)
	srv, _ := ucpServer(t, h)
	st, m := ucpDo(t, "GET", srv.URL+"/.well-known/ucp", "")
	if st != 200 {
		t.Fatalf("status %d", st)
	}
	u := m["ucp"].(map[string]any)
	eq(t, "version", u["version"].(string), ucpVersion)
	caps := u["capabilities"].(map[string]any)
	for _, c := range []string{"dev.ucp.shopping.checkout", "dev.ucp.shopping.order", "dev.ucp.shopping.catalog.search", "dev.ucp.shopping.fulfillment"} {
		if caps[c] == nil {
			t.Fatalf("capability %s missing: %v", c, caps)
		}
	}
	svcs := u["services"].(map[string]any)["dev.ucp.shopping"].([]any)
	got := map[string]string{}
	for _, s := range svcs {
		s := s.(map[string]any)
		got[s["transport"].(string)] = s["endpoint"].(string)
	}
	eq(t, "rest endpoint", got["rest"], srv.URL+"/ucp")
	eq(t, "mcp endpoint", got["mcp"], srv.URL+"/ucp/mcp")
	hs, _ := json.Marshal(u["payment_handlers"])
	for _, want := range []string{`"com.deemwar.ownshop.offline_payment"`, `"id":"upi"`, `"id":"bank_transfer"`, `"id":"payment_qr"`} {
		if !strings.Contains(string(hs), want) {
			t.Fatalf("handlers %s lack %s", hs, want)
		}
	}
	if strings.Contains(string(hs), "payment_link") {
		t.Fatal("payment_link declared but not set")
	}
	eq(t, "shop", m[ucpNS+".merchant"].(map[string]any)["name"].(string), "Test Shop")
}

func TestUCPCatalogSearch(t *testing.T) {
	h := newHarness(t)
	srv, _ := ucpServer(t, h)
	st, m := ucpDo(t, "POST", srv.URL+"/ucp/catalog/search", `{"query":"honey"}`)
	if st != 200 {
		t.Fatalf("status %d %v", st, m)
	}
	ps := m["products"].([]any)
	if len(ps) != 2 {
		t.Fatalf("honey: %d products", len(ps))
	}
	var hon map[string]any
	for _, p := range ps {
		if p.(map[string]any)["id"] == "HON-500" {
			hon = p.(map[string]any)
		}
	}
	if hon == nil {
		t.Fatal("HON-500 not found")
	}
	v := hon["variants"].([]any)[0].(map[string]any)
	price := v["price"].(map[string]any)
	if price["amount"].(float64) != 950 || price["currency"] != "INR" {
		t.Fatalf("price %v", price)
	}
	if hon["metadata"].(map[string]any)[ucpNS].(map[string]any)["stock"].(float64) != 20 {
		t.Fatalf("stock %v", hon["metadata"])
	}
	// Price filter in minor units, and paging.
	_, m = ucpDo(t, "POST", srv.URL+"/ucp/catalog/search", `{"query":"honey","filters":{"price":{"max":1000}}}`)
	if len(m["products"].([]any)) != 1 {
		t.Fatalf("price filter: %v", m["products"])
	}
	_, m = ucpDo(t, "POST", srv.URL+"/ucp/catalog/search", `{"pagination":{"limit":5}}`)
	pg := m["pagination"].(map[string]any)
	if len(m["products"].([]any)) != 5 || pg["has_next_page"] != true || pg["cursor"] != "5" {
		t.Fatalf("paging %v", pg)
	}
}

func TestUCPCheckoutCreateUpdateComplete(t *testing.T) {
	h := newHarness(t)
	srv, _ := ucpServer(t, h)
	before := h.stock("Raw honey 500g")
	st, m := ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions", `{"line_items":[{"item":{"id":"HON-500"},"quantity":2}]}`)
	if st != 200 || m["status"] != "incomplete" {
		t.Fatalf("create: %d %v", st, m)
	}
	if c := codes(m); !strings.Contains(c, "buyer_contact_required") || !strings.Contains(c, "address_required") || !strings.Contains(c, "fulfillment_option_required") {
		t.Fatalf("missing-data messages: %s", c)
	}
	id := m["id"].(string)
	if !strings.HasPrefix(id, "chk_") || h.orders() != 0 {
		t.Fatalf("id %q, orders %d: a session must not create an order", id, h.orders())
	}
	// Completing too early places nothing.
	_, m = ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions/"+id+"/complete", "{}")
	if m["status"] != "incomplete" || h.orders() != 0 {
		t.Fatalf("early complete: %v", m["status"])
	}
	upd := `{"line_items":[{"item":{"id":"HON-500"},"quantity":3}],"buyer":{"first_name":"Asha","phone_number":"+91 98765 43210"},
	  "fulfillment":{"methods":[{"type":"shipping","destinations":[{"street_address":"12 Lake Road","address_locality":"Chennai","postal_code":"600001"}],"groups":[{"selected_option_id":"` + slotID(t, h.s) + `"}]}]}}`
	st, m = ucpDo(t, "PUT", srv.URL+"/ucp/checkout-sessions/"+id, upd)
	if st != 200 || m["status"] != "ready_for_complete" {
		t.Fatalf("update: %d %v %s", st, m["status"], codes(m))
	}
	if m["totals"].([]any)[1].(map[string]any)["amount"].(float64) != 2850 {
		t.Fatalf("totals %v", m["totals"])
	}
	st, m = ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions/"+id+"/complete",
		`{"payment":{"instruments":[{"id":"i1","handler_id":"upi","type":"upi"}]}}`)
	if st != 200 || m["status"] != "completed" {
		t.Fatalf("complete: %d %v", st, m)
	}
	ord := m["order"].(map[string]any)
	eq(t, "order id", ord["id"].(string), "1042")
	eq(t, "invoice", ord["label"].(string), "INV-1042")
	pay := m[ucpNS+".payment"].(map[string]any)
	if pay["status"] != "unpaid" || !strings.HasPrefix(pay["upi_link"].(string), "upi://pay?") || pay["bank_transfer"] == nil {
		t.Fatalf("payment %v", pay)
	}
	if got := h.stock("Raw honey 500g"); got != before-3 {
		t.Fatalf("stock %d, want %d", got, before-3)
	}
	o, _ := h.s.Order(1042)
	if o.Status != StAwaiting || o.Channel != "ucp" || o.DeliverTo != "12 Lake Road, Chennai, 600001" || !o.SlotID.Valid {
		t.Fatalf("order %+v", o)
	}
	// A retry returns the same order and does not cut stock again.
	_, m = ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions/"+id+"/complete", "{}")
	if m["order"].(map[string]any)["id"] != "1042" || h.stock("Raw honey 500g") != before-3 || h.orders() != 1 {
		t.Fatal("complete is not idempotent")
	}
	// The owner sees it labelled on the orders page.
	app := &App{S: h.s, Bot: h.b, File: NewShopFile(h.s, filepath.Join(h.dir, "products.xlsx")), Dir: h.dir}
	rr := httptest.NewRecorder()
	app.Routes().ServeHTTP(rr, httptest.NewRequest("GET", "/orders", nil))
	if !strings.Contains(rr.Body.String(), "From an AI agent (UCP)") {
		t.Fatal("orders page lacks the UCP label")
	}
}

func TestUCPServiceCheckoutHoldsSlot(t *testing.T) {
	h := svcHarness(t, "services")
	srv, _ := ucpServer(t, h)
	_, m := ucpDo(t, "POST", srv.URL+"/ucp/catalog/search", `{"query":"haircut"}`)
	meta := m["products"].([]any)[0].(map[string]any)["metadata"].(map[string]any)[ucpNS].(map[string]any)
	starts := meta["free_starts"].([]any)
	if meta["kind"] != "service" || meta["stock"] != nil || len(starts) == 0 || starts[0] != "2026-11-20T14:00" {
		t.Fatalf("service metadata %v", meta)
	}
	body := `{"line_items":[{"item":{"id":"SVC-CUT"},"quantity":1}],"buyer":{"first_name":"Ravi","email":"ravi@example.com"},"com.deemwar.ownshop.booking":{"start":"2026-11-21T10:00"}}`
	_, m = ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions", body)
	if m["status"] != "ready_for_complete" || m["fulfillment"] != nil {
		t.Fatalf("service checkout: %v %s", m["status"], codes(m))
	}
	_, m = ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions/"+m["id"].(string)+"/complete", "{}")
	if m["status"] != "completed" {
		t.Fatalf("complete: %v", m)
	}
	o, _ := h.s.Order(1042)
	if o.BookAt != "2026-11-21 10:00" || o.Status != StAwaiting {
		t.Fatalf("booking %+v", o)
	}
	for _, ft := range h.s.FreeTimes(fri.AddDate(0, 0, 1), fri, 30, 0) {
		if ft == "10:00" {
			t.Fatal("10:00 still free after the UCP booking")
		}
	}
	// A second agent asking for the same time is told it is not free.
	_, m = ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions", strings.Replace(body, "ravi@", "mia@", 1))
	if m["status"] != "incomplete" || !strings.Contains(codes(m), "booking_unavailable") {
		t.Fatalf("double booking: %v %s", m["status"], codes(m))
	}
}

func TestUCPOrderLookupNeedsBuyer(t *testing.T) {
	h := svcHarness(t, "services")
	srv, _ := ucpServer(t, h)
	_, m := ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions", `{"line_items":[{"item":{"id":"SVC-CUT"},"quantity":1}],"buyer":{"first_name":"Ravi","phone_number":"+919876543210"},"com.deemwar.ownshop.booking":{"start":"2026-11-21T10:00"}}`)
	chk := m["id"].(string)
	ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions/"+chk+"/complete", "{}")
	// A chat-bot order exists too; it must never be reachable here.
	h.s.ManualBooking("Asha", "+31600000001", "SVC-CUT", "2026-11-21 11:00")
	for _, q := range []string{"", "?phone=%2B910000000000", "?email=ravi@example.com", "?checkout_id=chk_wrong"} {
		if st, m := ucpDo(t, "GET", srv.URL+"/ucp/orders/1042"+q, ""); st != 404 || codes(m) != "not_found" {
			t.Fatalf("lookup %q: %d %v", q, st, m)
		}
	}
	if st, _ := ucpDo(t, "GET", srv.URL+"/ucp/orders/1043?phone=%2B31600000001", ""); st != 404 {
		t.Fatal("a chat order is visible over UCP")
	}
	for _, q := range []string{"?phone=%2B91%2098765%2043210", "?checkout_id=" + chk} {
		st, m := ucpDo(t, "GET", srv.URL+"/ucp/orders/1042"+q, "")
		if st != 200 || m["checkout_id"] != chk || m["label"] != "INV-1042" {
			t.Fatalf("lookup %q: %d %v", q, st, m)
		}
		eq(t, "status label", m[ucpNS+".order"].(map[string]any)["status_label"].(string), "Waiting for payment")
	}
	h.s.Advance(1042, StPaid)
	_, m = ucpDo(t, "GET", srv.URL+"/ucp/orders/1042?checkout_id="+chk, "")
	eq(t, "paid label", m[ucpNS+".order"].(map[string]any)["status_label"].(string), "Payment received")
}

func TestUCPInputValidation(t *testing.T) {
	h := newHarness(t)
	srv, app := ucpServer(t, h)
	co := srv.URL + "/ucp/checkout-sessions"
	for _, c := range []struct{ body, path string }{
		{`not json`, "$"},
		{`{"line_items":[]}`, "$.line_items"},
		{`{"line_items":[{"item":{"id":"HON-500"},"quantity":0}]}`, "$.line_items[0].quantity"},
		{`{"line_items":[{"item":{"id":"HON-500"},"quantity":500}]}`, "$.line_items[0].quantity"},
		{`{"line_items":[{"item":{"id":"<script>"},"quantity":1}]}`, "$.line_items[0].item.id"},
		{`{"line_items":[{"item":{"id":"HON-500"},"quantity":1}],"buyer":{"email":"not-an-email"}}`, "$.buyer.email"},
		{`{"line_items":[{"item":{"id":"HON-500"},"quantity":1}],"buyer":{"phone_number":"call me"}}`, "$.buyer.phone_number"},
		{`{"line_items":[{"item":{"id":"HON-500"},"quantity":1}],"buyer":{"first_name":"` + strings.Repeat("x", 61) + `"}}`, "$.buyer.first_name"},
		{`{"line_items":[{"item":{"id":"HON-500"},"quantity":1}],"fulfillment":{"methods":[{"type":"pickup"}]}}`, "$.fulfillment.methods[0].type"},
		{`{"line_items":[{"item":{"id":"HON-500"},"quantity":1}],"fulfillment":{"methods":[{"type":"shipping","groups":[{"selected_option_id":"slot_999"}]}]}}`, "$.fulfillment.methods[0].groups[0].selected_option_id"},
		{`{"line_items":[{"item":{"id":"HON-500"},"quantity":1}],"com.deemwar.ownshop.booking":{"start":"tomorrow"}}`, "$['com.deemwar.ownshop.booking'].start"},
	} {
		st, m := ucpDo(t, "POST", co, c.body)
		msg, _ := m["messages"].([]any)
		if st != 400 || len(msg) != 1 || msg[0].(map[string]any)["path"] != c.path {
			t.Fatalf("%s: %d %v", c.body, st, m)
		}
	}
	if st, m := ucpDo(t, "POST", co, `{"line_items":[{"item":{"id":"NOPE-1"},"quantity":1}]}`); st != 404 || codes(m) != "not_found" {
		t.Fatalf("unknown item: %d %v", st, m)
	}
	// Too big, not JSON, or from another site's page.
	if st, _ := ucpDo(t, "POST", co, `{"x":"`+strings.Repeat("a", ucpMaxBody)+`"}`); st != 413 && st != 400 {
		t.Fatalf("oversized body: %d", st)
	}
	res, _ := http.PostForm(co, url.Values{"line_items": {"x"}})
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form post: %d", res.StatusCode)
	}
	req, _ := http.NewRequest("POST", co, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", res.StatusCode)
	}
	// Rate limit: checkout creation per minute, for the whole shop.
	app.UCP.mu.Lock()
	for len(app.UCP.creates) < ucpCreatesPer {
		app.UCP.creates = append(app.UCP.creates, time.Now())
	}
	app.UCP.mu.Unlock()
	if st, m := ucpDo(t, "POST", co, `{"line_items":[{"item":{"id":"HON-500"},"quantity":1}]}`); st != 429 || codes(m) != "rate_limited" {
		t.Fatalf("rate limit: %d %v", st, m)
	}
	app.UCP.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if st, _ := ucpDo(t, "POST", co, `{"line_items":[{"item":{"id":"HON-500"},"quantity":1}]}`); st != 200 {
		t.Fatalf("after a minute: %d", st)
	}
}

func mcp(t *testing.T, srv *httptest.Server, id int, method, params string) map[string]any {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":` + jnum(int64(id)) + `,"method":"` + method + `"`
	if params != "" {
		body += `,"params":` + params
	}
	st, m := ucpDo(t, "POST", srv.URL+"/ucp/mcp", body+"}")
	if st != 200 {
		t.Fatalf("%s: http %d %v", method, st, m)
	}
	if m["error"] != nil {
		t.Fatalf("%s: %v", method, m["error"])
	}
	return m["result"].(map[string]any)
}

func TestUCPMCPFlow(t *testing.T) {
	h := newHarness(t)
	srv, _ := ucpServer(t, h)
	r := mcp(t, srv, 1, "initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`)
	if r["protocolVersion"] != "2025-06-18" || r["capabilities"].(map[string]any)["tools"] == nil {
		t.Fatalf("initialize %v", r)
	}
	if st, _ := ucpDo(t, "POST", srv.URL+"/ucp/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); st != 202 {
		t.Fatalf("notification: %d", st)
	}
	var names []string
	for _, tl := range mcp(t, srv, 2, "tools/list", "")["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	eq(t, "tools", strings.Join(names, ","), "search_catalog,create_checkout,get_checkout,update_checkout,complete_checkout,cancel_checkout,get_order")
	meta := `"meta":{"ucp-agent":{"profile":"https://agent.example/profile.json"}}`
	r = mcp(t, srv, 3, "tools/call", `{"name":"search_catalog","arguments":{`+meta+`,"catalog":{"query":"coconut"}}}`)
	sc := r["structuredContent"].(map[string]any)
	if r["isError"] != false || sc["products"].([]any)[0].(map[string]any)["id"] != "OIL-500" {
		t.Fatalf("search %v", r)
	}
	if txt := r["content"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(txt, `"OIL-500"`) {
		t.Fatal("text content does not mirror structuredContent")
	}
	co := `{"line_items":[{"item":{"id":"OIL-500"},"quantity":1}],"buyer":{"first_name":"Lee","email":"lee@example.com"},` +
		`"fulfillment":{"methods":[{"type":"shipping","destinations":[{"street_address":"3 Hill St","address_locality":"Madurai"}],"groups":[{"selected_option_id":"` + slotID(t, h.s) + `"}]}]}}`
	sc = mcp(t, srv, 4, "tools/call", `{"name":"create_checkout","arguments":{`+meta+`,"checkout":`+co+`}}`)["structuredContent"].(map[string]any)
	if sc["status"] != "ready_for_complete" {
		t.Fatalf("create %v %s", sc["status"], codes(sc))
	}
	id := sc["id"].(string)
	sc = mcp(t, srv, 5, "tools/call", `{"name":"complete_checkout","arguments":{`+meta+`,"id":"`+id+`","checkout":{}}}`)["structuredContent"].(map[string]any)
	if sc["status"] != "completed" || sc["order"].(map[string]any)["id"] != "1042" {
		t.Fatalf("complete %v", sc)
	}
	sc = mcp(t, srv, 6, "tools/call", `{"name":"get_order","arguments":{`+meta+`,"id":"1042","buyer":{"email":"LEE@example.com"}}}`)["structuredContent"].(map[string]any)
	if sc["checkout_id"] != id {
		t.Fatalf("get_order %v", sc)
	}
	r = mcp(t, srv, 7, "tools/call", `{"name":"get_order","arguments":{`+meta+`,"id":"1042","buyer":{"email":"x@example.com"}}}`)
	if r["isError"] != true || codes(r["structuredContent"].(map[string]any)) != "not_found" {
		t.Fatalf("get_order wrong buyer %v", r)
	}
	// Bad input comes back as a tool error, unknown methods as JSON-RPC errors.
	r = mcp(t, srv, 8, "tools/call", `{"name":"create_checkout","arguments":{`+meta+`,"checkout":{"line_items":[{"item":{"id":"OIL-500"},"quantity":-1}]}}}`)
	if r["isError"] != true {
		t.Fatalf("invalid qty accepted %v", r)
	}
	_, m := ucpDo(t, "POST", srv.URL+"/ucp/mcp", `{"jsonrpc":"2.0","id":9,"method":"resources/list"}`)
	if m["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatalf("unknown method %v", m)
	}
	if st, _ := ucpDo(t, "GET", srv.URL+"/ucp/mcp", ""); st != 405 {
		t.Fatalf("GET mcp: %d", st)
	}
}

func TestUCPSettingsToggle(t *testing.T) {
	h := newHarness(t)
	srv, _ := ucpServer(t, h)
	h.s.Set("ucp_enabled", "")
	http.PostForm(srv.URL+"/settings", url.Values{"ucp_enabled": {"on", ""}})
	eq(t, "on", h.s.Get("ucp_enabled"), "on")
	http.PostForm(srv.URL+"/settings", url.Values{"ucp_enabled": {""}})
	eq(t, "off", h.s.Get("ucp_enabled"), "")
	res, _ := http.Get(srv.URL + "/settings")
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), "Let AI agents shop here") {
		t.Fatal("settings page lacks the UCP toggle")
	}
}

func TestUCPBookingWindow(t *testing.T) {
	h := svcHarness(t, "services")
	srv, _ := ucpServer(t, h)
	for _, start := range []string{"2026-11-16T10:00", "2027-03-01T10:00"} { // last Monday; beyond days_ahead
		body := `{"line_items":[{"item":{"id":"SVC-CUT"},"quantity":1}],"buyer":{"first_name":"Ravi","email":"ravi@example.com"},"com.deemwar.ownshop.booking":{"start":"` + start + `"}}`
		_, m := ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions", body)
		if m["status"] != "incomplete" || !strings.Contains(codes(m), "booking_unavailable") {
			t.Fatalf("%s: %v %s", start, m["status"], codes(m))
		}
		_, m = ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions/"+m["id"].(string)+"/complete", "")
		if m["status"] != "incomplete" || h.orders() != 0 {
			t.Fatalf("%s booked: %v", start, m["status"])
		}
	}
}

func TestUCPLocalHostOnly(t *testing.T) {
	h := newHarness(t)
	srv, _ := ucpServer(t, h)
	req, _ := http.NewRequest("GET", srv.URL+"/.well-known/ucp", nil)
	req.Host = "shop.example.com"
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 403 {
		t.Fatalf("foreign host: %d", res.StatusCode)
	}
}

func TestUCPDuplicateSKUNeverAmbiguous(t *testing.T) {
	h := newHarness(t)
	srv, _ := ucpServer(t, h)
	h.s.UpsertProduct(Product{SKU: "HON-500", Name: "Raw honey 500g, second batch", Price: 10, Stock: 5, Keywords: "honey"})
	if st, m := ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions", `{"line_items":[{"item":{"id":"HON-500"},"quantity":1}]}`); st != 404 {
		t.Fatalf("shared SKU still resolves: %d %v", st, m)
	}
	_, m := ucpDo(t, "POST", srv.URL+"/ucp/catalog/search", `{"query":"honey"}`)
	ids := map[string]bool{}
	for _, p := range m["products"].([]any) {
		ids[p.(map[string]any)["id"].(string)] = true
	}
	if len(ids) != 3 || ids["HON-500"] {
		t.Fatalf("ids %v", ids)
	}
}

func TestUCPGetCancelAndOwnerPaid(t *testing.T) {
	h := newHarness(t)
	srv, _ := ucpServer(t, h)
	meta := `"meta":{"ucp-agent":{"profile":"https://agent.example/p.json"}}`
	sc := mcp(t, srv, 1, "tools/call", `{"name":"create_checkout","arguments":{`+meta+`,"checkout":{"line_items":[{"item":{"id":"TEA-GRN"},"quantity":1}]}}}`)["structuredContent"].(map[string]any)
	id := sc["id"].(string)
	if g := mcp(t, srv, 2, "tools/call", `{"name":"get_checkout","arguments":{`+meta+`,"id":"`+id+`"}}`)["structuredContent"].(map[string]any); g["id"] != id {
		t.Fatalf("get_checkout %v", g)
	}
	if c := mcp(t, srv, 3, "tools/call", `{"name":"cancel_checkout","arguments":{`+meta+`,"id":"`+id+`"}}`)["structuredContent"].(map[string]any); c["status"] != "canceled" {
		t.Fatalf("cancel %v", c)
	}
	if st, _ := ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions/"+id+"/complete", ""); st != 409 || h.orders() != 0 {
		t.Fatalf("complete after cancel: %d", st)
	}
	// REST get and cancel, body-less POST.
	body := `{"line_items":[{"item":{"id":"TEA-GRN"},"quantity":2}],"buyer":{"first_name":"Lee","phone_number":"+31 6 1234 5678"},` +
		`"fulfillment":{"methods":[{"type":"shipping","destinations":[{"street_address":"3 Hill St"}],"groups":[{"selected_option_id":"` + slotID(t, h.s) + `"}]}]}}`
	_, m := ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions", body)
	id = m["id"].(string)
	if st, g := ucpDo(t, "GET", srv.URL+"/ucp/checkout-sessions/"+id, ""); st != 200 || g["status"] != "ready_for_complete" {
		t.Fatalf("REST get: %d %v", st, g["status"])
	}
	if st, m := ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions/"+id+"/complete", ""); st != 200 || m["status"] != "completed" {
		t.Fatalf("body-less complete: %d %v", st, m)
	}
	if st, _ := ucpDo(t, "POST", srv.URL+"/ucp/checkout-sessions/"+id+"/cancel", ""); st != 409 {
		t.Fatalf("cancel after complete: %d", st)
	}
	if st, _ := ucpDo(t, "GET", srv.URL+"/ucp/checkout-sessions/chk_nope", ""); st != 404 {
		t.Fatal("unknown checkout")
	}
	// The owner clicks Payment received on the agent's order: it is paid; the chat message cannot go out.
	res, _ := http.PostForm(srv.URL+"/orders/1042/status", url.Values{"to": {StPaid}})
	page, _ := io.ReadAll(res.Body)
	if o, _ := h.s.Order(1042); !o.Paid || o.Status != StPaid {
		t.Fatalf("not paid: %+v", o)
	}
	if !strings.Contains(string(page), "NOT sent") {
		t.Fatal("owner not told the customer message did not go out")
	}
	_, m = ucpDo(t, "GET", srv.URL+"/ucp/orders/1042?phone=%2B31612345678", "")
	o := m[ucpNS+".order"].(map[string]any)
	if o["paid"] != true || o["status_label"] != "Payment received" || m[ucpNS+".payment"] != nil {
		t.Fatalf("after paid: %v", m)
	}
}
