package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	llmkit "github.com/muthuishere/toolnexus/golang"
)

// Out is one outgoing chat message. Image is a local file path (sent with Text as caption).
type Out struct {
	Text  string
	Image string
	kind  string // which template made it (cart, handoff, ...)
	added string // cart only: what this reply added, e.g. "2 x Raw honey 500g"
}

// Sender delivers messages to a customer (the channel Hub, behind the Guard).
type Sender interface {
	Send(c Customer, o Out) error
}

// Every reply is one of these templates. The model only picks a tool; it never writes reply text.
var replies = template.Must(template.New("r").Funcs(template.FuncMap{"money": func(cur string, v float64) string {
	return fmt.Sprintf("%s %.2f", cur, v)
}, "inc": func(i int) int { return i + 1 }, "mul": func(q int, p float64) float64 { return float64(q) * p },
	"mod": func(a, b int) int { return a % b }}).Parse(`
{{define "handoff"}}Thanks for your message. The shop will reply shortly.{{end}}
{{define "about"}}Hello, this is {{.Shop}}.{{if .Soul}} {{.Soul}}{{end}}
{{if .Hint}}{{.Hint}}{{else}}Ask for a product (for example: do you have honey) or about hours and delivery.{{end}}{{end}}
{{define "faq"}}{{.Answer}}{{end}}
{{define "search"}}Here is what we have:
{{range $i, $p := .Products}}{{inc $i}}. {{if $p.IsService}}{{$p.Name}}, {{$p.Minutes}} min, {{money $.Cur $p.Price}}{{else}}{{$p.Name}} - {{money $.Cur $p.Price}}{{if gt $p.Stock 0}} ({{$p.Stock}} in stock){{else}} (out of stock){{end}}{{end}}
{{end}}{{if .More}}and {{.More}} more, ask for an item by name.
{{end}}{{with index .Products 0}}{{if .IsService}}To book, reply for example: book {{.Name}}{{else}}To order, reply for example: add 2 {{.Name}}{{end}}{{end}}{{end}}
{{define "cart"}}{{if .Added}}Added {{.Added}}.
{{end}}Your {{.W.order}} {{.Order.ID}}:
{{range .Order.Items}}- {{.Qty}} x {{.Name}} = {{money $.Cur (mul .Qty .Price)}}
{{end}}Total: {{money .Cur .Order.Total}}
{{if .Order.SlotLabel}}{{.W.Slot}}: {{.Order.SlotLabel}}
Reply YES to confirm {{.W.order}} {{.Order.ID}}.{{else if .Times}}{{.Day}}, free times:
{{range $i, $t := .Times}}{{inc $i}}. {{$t}}{{if eq (mod $i 4) 3}}
{{else}}  {{end}}{{end}}
Reply with a number.{{else if .Days}}{{.W.Pick}}:
{{range $i, $d := .Days}}{{inc $i}}. {{$d.Label}}
{{end}}Reply with a day number, or for example: tomorrow 16:00{{else if .Order.HasService}}Sorry, no free times in the next days. The shop will reply shortly.{{else}}{{.W.Pick}}:
{{range $i, $s := .Slots}}{{inc $i}}. {{$s.Label}}
{{end}}Reply for example: slot 1{{end}}{{end}}
{{define "ask_address"}}Where should we deliver? Share your location (tap attach, then Location) or type your address.{{end}}
{{define "delivery_to"}}Delivery to: {{.Address}}{{end}}
{{define "location_saved"}}Saved your delivery location.{{end}}
{{define "taken"}}Sorry, that time was just booked by someone else. Please pick another.{{end}}
{{define "nostock"}}Sorry, we only have {{.Stock}} x {{.Name}} left.{{end}}
{{define "invoice"}}{{.Shop}}{{if .Address}}, {{.Address}}{{end}}
{{.W.Order}} {{.Order.ID}} confirmed. Invoice {{.Invoice}}
{{range .Order.Items}}- {{.Qty}} x {{.Name}} = {{money $.Cur (mul .Qty .Price)}}
{{end}}Total: {{money .Cur .Order.Total}}
{{.W.Slot}}: {{.Order.SlotLabel}}{{if .Order.DeliverTo}}
Deliver to: {{.Order.DeliverTo}}{{end}}{{end}}
{{define "pay"}}{{.Shop}}: please pay {{money .Cur .Order.Total}}{{if .UPI}} by UPI using this QR code.
Scan with any UPI app; amount and order number are filled in: {{.UPI}}{{if .Link}}
Or pay at {{.Link}}{{end}}{{if .QR}}
We also take our other payment QR; ask us for it.{{end}}{{if .Note}}
Bank transfer: {{.Note}}{{end}}
{{else}}{{if and .QR .Link}} using this QR code or at {{.Link}}{{else if .QR}} using this QR code{{else if .Link}} at {{.Link}}{{end}}.{{if .Note}} Bank transfer: {{.Note}}.{{end}} {{end}}Put order {{.Order.ID}} in the payment note or reference.{{end}}
{{define "pay_none"}}{{.Shop}}: please pay {{money .Cur .Order.Total}}. Put order {{.Order.ID}} in the payment note or reference. The shop will send payment details shortly.{{end}}
{{define "status"}}{{.W.Order}} {{.Order.ID}}: {{.Order.StatusLabel}}{{if .Order.SlotLabel}}. {{.W.SlotFull}}: {{.Order.SlotLabel}}{{end}}.{{end}}
{{define "update_paid"}}Payment received for {{.W.order}} {{.Order.ID}}, thank you. Your receipt is attached. Invoice {{.Order.Invoice}} is paid.{{end}}
{{define "update_packed"}}Good news: {{.W.order}} {{.Order.ID}} is packed.{{end}}
{{define "update_shipped"}}{{.W.Order}} {{.Order.ID}} is on its way ({{.Order.SlotLabel}}).{{end}}
{{define "update_cancelled"}}{{.W.Order}} {{.Order.ID}} is cancelled. Write to us here if you have a question.{{end}}
{{define "update_delivered"}}{{.W.Order}} {{.Order.ID}} is delivered. Thank you for shopping with {{.Shop}}!{{end}}
{{define "customer"}}We have you as {{.Name}}, with {{.Count}} order(s).{{end}}
`))

// words is the one wording switch: services shops take bookings, others take orders.
func (b *Bot) words() map[string]string {
	switch b.S.Get("sells") {
	case "services":
		return map[string]string{"order": "booking", "Order": "Booking", "Pick": "Pick a time", "Slot": "Time", "SlotFull": "Time"}
	}
	return map[string]string{"order": "order", "Order": "Order", "Pick": "Pick a delivery slot", "Slot": "Delivery", "SlotFull": "Delivery slot"}
}

// renderW renders with the shop's wording.
func (b *Bot) renderW(name string, data map[string]any) string {
	data["W"] = b.words()
	return render(name, data)
}

func render(name string, data map[string]any) string {
	var b bytes.Buffer
	if err := replies.ExecuteTemplate(&b, name, data); err != nil {
		return "Thanks for your message. The shop will reply shortly."
	}
	return strings.TrimSpace(b.String())
}

// Bot is the one pipeline shared by every chat channel and `ownshop chat`.
type Bot struct {
	S     *Store
	M     Brain
	Send  Sender
	Debug func(string)     // optional: shows the model's tool calls and hand-off reasons
	Now   func() time.Time // clock for booking times; nil = time.Now
	Dir   string           // data folder (products.xlsx, images/, invoices/); "" = no photos or invoice image
	mu    sync.Mutex       // one message at a time
	tk    *llmkit.Toolkit
	turn  *turnState
}

// turnState is the message being handled, read by the tool functions.
type turnState struct {
	c     Customer
	text  string
	outs  []Out
	calls []string
}

func (b *Bot) debug(f string, a ...any) {
	if b.Debug != nil {
		b.Debug(fmt.Sprintf(f, a...))
	}
}

func (b *Bot) cur() string { return b.S.Get("currency") }

// Toolkit registers the shop tools for the model client.
func (b *Bot) Toolkit() (*llmkit.Toolkit, error) {
	if b.tk != nil {
		return b.tk, nil
	}
	tk, err := llmkit.CreateToolkit(context.Background(), llmkit.Options{Builtins: false})
	if err != nil {
		return nil, err
	}
	for _, t := range tools {
		name := t.Name
		tk.Register(llmkit.NativeTool(name, t.Desc, toolSchema(t),
			func(ctx context.Context, args map[string]any) (string, error) { return b.exec(name, args), nil }))
	}
	b.tk = tk
	return tk, nil
}

// exec runs one tool call for the current message. Replies are templates, collected for sending.
func (b *Bot) exec(name string, args map[string]any) string {
	ts := b.turn
	if ts == nil {
		return "no message in progress"
	}
	// Several calls per message are allowed only as a run of add_to_order ("2 honey and 1 oil").
	// Anything else after the first call is skipped, so one message can never add and confirm.
	if len(ts.calls) > 0 && (name != "add_to_order" || !allAdds(ts.calls)) {
		ts.calls = append(ts.calls, name+" (skipped)")
		b.debug("skipped extra call %s", name)
		return "skipped: one action per message"
	}
	ts.calls = append(ts.calls, name)
	outs := b.run(ts.c, Call{Name: name, Args: stringArgs(args)}, ts.text)
	for _, o := range outs {
		n := len(ts.outs)
		switch {
		// several add_to_order calls: keep only the latest cart summary
		// and name every item added in this message on its first line
		case o.kind == "cart" && n > 0 && ts.outs[n-1].kind == "cart":
			if prev := ts.outs[n-1].added; prev != "" && o.added != "" {
				o.added = prev + " and " + o.added
				if i := strings.Index(o.Text, "\n"); i >= 0 {
					o.Text = "Added " + o.added + "." + o.Text[i:]
				}
			}
			ts.outs[n-1] = o
		// several hand-offs: one "the shop will reply" is enough
		case o.kind == "handoff" && hasKind(ts.outs, "handoff"):
		default:
			ts.outs = append(ts.outs, o)
		}
	}
	var texts []string
	for _, o := range outs {
		texts = append(texts, o.Text)
	}
	return strings.Join(texts, "\n")
}

func allAdds(calls []string) bool {
	for _, c := range calls {
		if c != "add_to_order" {
			return false
		}
	}
	return true
}

// toolSchema is the JSON schema of one shop tool's arguments.
func toolSchema(t toolDef) llmkit.JSONSchema {
	props := llmkit.JSONSchema{}
	for _, p := range t.Params {
		typ := "string"
		if intParams[p] {
			typ = "integer"
		}
		props[p] = llmkit.JSONSchema{"type": typ}
	}
	req := t.Required
	if req == nil {
		req = []string{}
	}
	return llmkit.JSONSchema{"type": "object", "properties": props, "required": req}
}

func hasKind(outs []Out, k string) bool {
	for _, o := range outs {
		if o.kind == k {
			return true
		}
	}
	return false
}

// Handle runs one incoming customer message through the model and the tools, and sends the replies.
func (b *Bot) Handle(ctx context.Context, in Inbound) []Out {
	b.mu.Lock()
	defer b.mu.Unlock()
	phone, text := in.Key(), strings.TrimPrefix(in.Text, "\ufeff") // a byte-order mark from a piped or pasted text
	c, err := b.S.Customer(phone, in.Name, in.SenderID)
	if err == nil && in.Channel != "" && (c.Channel != in.Channel || c.Account != in.Account) {
		b.S.SetCustomerChannel(c.ID, in.Channel, in.Account)
		c, err = b.S.CustomerByID(c.ID)
	}
	if err != nil {
		b.debug("customer: %v", err)
		return nil
	}
	if b.S.isBlocked(phone) {
		b.debug("%s is blocked: no reply", phone)
		return nil
	}
	if in.HasLocation() {
		text = in.LocationText()
	}
	b.S.Log(phone, "in", text, "", "")
	var outs []Out
	tool, calls := "none", ""
	if in.HasLocation() || c.WaitAddr {
		// An address arrives: a shared location any time, or any text after we asked for one.
		// It never goes to the model.
		tool, calls = "address", "go:address"
		outs = b.address(c, text, in.HasLocation())
	} else if o := b.awaitingTime(c); o != nil && b.isPick(c, o, text, &outs) {
		tool, calls = "pick_slot", "go:pick_time"
	} else if n, ok := b.awaitingSlotNumber(c, text); ok {
		// "1" right after the numbered delivery slots is that slot; the small model often
		// makes no tool call for a bare number, and the customer got a hand-off.
		tool, calls = "pick_slot", "go:pick_slot"
		outs = b.pickSlot(c, n, text)
	} else if isYes(text) && b.awaitingYes(c) {
		// The reply asked for exactly "YES": a bare yes with a cart ready to confirm is answered
		// in Go. The small model often routes a lone YES elsewhere.
		b.debug("YES with a cart waiting: confirm in Go")
		tool, calls = "confirm_order", "go:confirm_order"
		outs = b.confirm(c, text)
	} else if t, cl, o, ok := b.goFirst(c, text); ok {
		tool, calls, outs = t, cl, o
	} else {
		if rest, greeted := splitGreeting(text); greeted {
			text = rest // "hi do you have honey" is the question
		}
		tool, calls, outs = b.ask(ctx, c, text)
	}
	for _, o := range outs {
		b.S.Log(phone, "out", o.Text+imgNote(o), tool, calls)
		if b.Send != nil {
			if err := b.Send.Send(c, o); err != nil {
				b.debug("send: %v", err)
			}
		}
	}
	return outs
}

// ask runs the message through the model; the tool functions collect the replies.
func (b *Bot) ask(ctx context.Context, c Customer, text string) (tool, calls string, outs []Out) {
	tk, err := b.Toolkit()
	if err != nil {
		b.debug("toolkit: %v", err)
		return "none", "", b.handoff(c, text, "model unavailable")
	}
	b.turn = &turnState{c: c, text: text}
	defer func() { b.turn = nil }()
	res, err := b.M.Run(ctx, text, tk)
	if err != nil {
		b.debug("model error: %v", err)
	}
	var names []string
	for _, tc := range res.ToolCalls {
		names = append(names, callString(tc.Name, tc.Args))
	}
	b.debug("model: %s", strings.Join(names, " + "))
	outs = b.turn.outs
	if len(outs) == 0 {
		// No shop tool ran: the model answered in text, failed, or invented a tool name.
		if len(res.ToolCalls) > 0 {
			outs = b.run(c, Call{Name: res.ToolCalls[0].Name, Args: stringArgs(res.ToolCalls[0].Args)}, text)
		} else {
			outs = b.handoff(c, text, "model made no tool call")
		}
	}
	tool = "none"
	if len(res.ToolCalls) > 0 {
		tool = res.ToolCalls[0].Name
	}
	return tool, strings.Join(names, " + "), outs
}

// awaitingYes: the customer's cart has items and a slot, so the last reply asked for YES.
func (b *Bot) awaitingYes(c Customer) bool {
	o, _ := b.S.Cart(c.ID)
	return o != nil && o.Ready()
}

// awaitingSlotNumber: a products-only cart has no delivery slot yet and the message is just
// a slot number from the list ("1", "slot 2", "#3") that is in range.
func (b *Bot) awaitingSlotNumber(c Customer, text string) (string, bool) {
	m := pickNumRe.FindStringSubmatch(strings.ToLower(strings.TrimSpace(text)))
	if m == nil {
		return "", false
	}
	o, _ := b.S.Cart(c.ID)
	if o == nil || len(o.Items) == 0 || o.HasService() || o.SlotID.Valid {
		return "", false
	}
	slots, _ := b.S.Slots()
	i, _ := strconv.Atoi(m[1])
	return m[1], i >= 1 && i <= len(slots)
}

// Warm sends one throwaway message through the model at start, so the first customer does not
// wait for the cold model (about 40 s on a small PC). Tool calls do nothing: no turn is open.
func (b *Bot) Warm(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tk, err := b.Toolkit()
	if err != nil || b.M == nil {
		return
	}
	start := time.Now()
	if _, err := b.M.Run(ctx, "hello", tk); err != nil {
		b.debug("model warm-up: %v", err)
		return
	}
	b.debug("model warm-up done in %s", time.Since(start).Round(time.Second))
}

// awaitingTime: the cart holds a service and no time is booked yet.
func (b *Bot) awaitingTime(c Customer) *Order {
	o, _ := b.S.Cart(c.ID)
	if o != nil && o.HasService() && o.BookAt == "" {
		return o
	}
	return nil
}

func (b *Bot) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Bot) isPick(c Customer, o *Order, text string, outs *[]Out) bool {
	r, ok := b.pickTime(c, o, text)
	*outs = r
	return ok
}

// cartText is the cart summary; a service cart offers days, then that day's free times.
func (b *Bot) cartText(o *Order, added string) string {
	d := map[string]any{"Order": o, "Added": added, "Cur": b.cur()}
	if o.HasService() {
		if o.BookAt == "" {
			min := o.ServiceMinutes(b.S.Schedule().Step)
			if o.BookDay != "" {
				day, _ := time.ParseInLocation(dayFmt, o.BookDay, time.Local)
				if ts := b.S.FreeTimes(day, b.now(), min, 0); len(ts) > 0 {
					d["Day"], d["Times"] = dayLabel(day, b.now()), ts
				}
			}
			if d["Times"] == nil {
				d["Days"] = b.S.OfferDays(b.now(), min, 3)
			}
		}
	} else {
		d["Slots"], _ = b.S.Slots()
	}
	return b.renderW("cart", d)
}

// pickTime reads a day number, a time number, or "tomorrow 16:00" for a service cart, in Go.
// Only times from the free list are ever booked. ok=false: the message is not a pick.
func (b *Bot) pickTime(c Customer, o *Order, text string) ([]Out, bool) {
	now, sc := b.now(), b.S.Schedule()
	min := o.ServiceMinutes(sc.Step)
	t := strings.ToLower(strings.TrimSpace(text))
	if m := pickNumRe.FindStringSubmatch(t); m != nil {
		n, _ := strconv.Atoi(m[1])
		if o.BookDay != "" {
			day, _ := time.ParseInLocation(dayFmt, o.BookDay, time.Local)
			ts := b.S.FreeTimes(day, now, min, 0)
			if n >= 1 && n <= len(ts) {
				b.S.SetBookAt(o.ID, o.BookDay+" "+ts[n-1], min)
			}
		} else if days := b.S.OfferDays(now, min, 3); n >= 1 && n <= len(days) {
			b.S.SetBookDay(o.ID, days[n-1].Date)
		}
	} else if day, hhmm, ok := parseWhen(t, now); ok {
		if day.IsZero() && o.BookDay != "" {
			day, _ = time.ParseInLocation(dayFmt, o.BookDay, time.Local)
		}
		if day.IsZero() || day.After(startOfDay(now).AddDate(0, 0, sc.Ahead)) {
			return nil, false
		}
		b.S.SetBookDay(o.ID, day.Format(dayFmt))
		for _, ft := range b.S.FreeTimes(day, now, min, 0) {
			if ft == hhmm {
				b.S.SetBookAt(o.ID, day.Format(dayFmt)+" "+hhmm, min)
			}
		}
	} else {
		return nil, false
	}
	o, _ = b.S.Cart(c.ID)
	return []Out{{Text: b.cartText(o, "")}}, true
}

func imgNote(o Out) string {
	if o.Image == "" {
		return ""
	}
	return " [image " + o.Image + "]"
}

func (b *Bot) handoff(c Customer, text, reason string) []Out {
	b.debug("hand-off: %s", reason)
	b.S.Handoff(c.Phone, text, reason)
	return []Out{{Text: render("handoff", nil), kind: "handoff"}}
}

func (b *Bot) run(c Customer, call Call, text string) []Out {
	a := call.Args
	switch call.Name {
	case "faq":
		return b.faq(c, a["question"]+" "+text, text)
	case "search_product":
		return b.search(c, a["query"], text)
	case "add_to_order":
		return b.add(c, a["product"], a["quantity"], text)
	case "pick_slot":
		return b.pickSlot(c, a["slot"], text)
	case "confirm_order":
		return b.confirm(c, text)
	case "send_payment_qr":
		o, _ := b.S.LatestOrder(c.ID)
		if o == nil || o.Status != StAwaiting {
			return b.handoff(c, text, "send_payment_qr: no order waiting for payment")
		}
		return b.payQR(o)
	case "order_status":
		// The model's order_id is ignored on purpose: a customer only ever sees their own latest order.
		o, _ := b.S.LatestOrder(c.ID)
		if o == nil {
			return b.handoff(c, text, "order_status: customer has no order")
		}
		return []Out{{Text: b.renderW("status", map[string]any{"Order": o})}}
	case "find_customer":
		// Privacy: only answers about the sender's own record.
		q := strings.ToLower(strings.TrimSpace(a["name"]))
		if q == "" || c.Name == "" || !strings.Contains(strings.ToLower(c.Name), q) {
			return b.handoff(c, text, "find_customer: not the sender's own record")
		}
		var n int
		b.S.DB.QueryRow(`SELECT COUNT(*) FROM orders WHERE customer_id=? AND status<>'cart'`, c.ID).Scan(&n)
		return []Out{{Text: render("customer", map[string]any{"Name": c.Name, "Count": n})}}
	case "about_shop":
		// Gate: the model also picks about_shop for off-topic text, so it only runs on greeting/identity words.
		if !isAboutShop(text) {
			return b.handoff(c, text, "about_shop: message is not a greeting or a question about the shop")
		}
		soul := strings.TrimSpace(b.S.Get("soul"))
		return []Out{{Text: render("about", map[string]any{"Shop": b.S.Get("shop_name"), "Soul": soul})}}
	case "none":
		return b.handoff(c, text, "model chose none")
	default:
		// The model often answers "hello" with an invented tool call:hello{}. A bare greeting is routed to about_shop in Go.
		if isGreeting(text) {
			return b.run(c, Call{Name: "about_shop"}, text)
		}
		return b.handoff(c, text, "unknown tool "+call.Name)
	}
}

var stop = map[string]bool{"the": true, "a": true, "an": true, "do": true, "you": true, "your": true, "have": true, "is": true,
	"are": true, "what": true, "of": true, "to": true, "i": true, "can": true, "me": true, "my": true, "for": true, "and": true,
	"it": true, "any": true, "how": true, "much": true, "please": true, "want": true, "add": true, "in": true, "on": true, "we": true}

// stem keeps the first 5 letters so deliver/delivery and return/returns match.
func stem(w string) string {
	if len(w) > 5 {
		return w[:5]
	}
	return w
}

func keySet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words(s) {
		if !stop[w] && len(w) > 1 {
			m[stem(w)] = true
		}
	}
	return m
}

func (b *Bot) faq(c Customer, q, text string) []Out {
	fs, _ := b.S.FAQs()
	want := keySet(q)
	best, score := -1, 0
	for i, f := range fs {
		n := 0
		for k := range keySet(f.Keywords + " " + f.Question) {
			if want[k] {
				n++
			}
		}
		if n > score {
			best, score = i, n
		}
	}
	if best < 0 {
		if outs, ok := b.fallback(c, text); ok {
			return outs
		}
		return b.handoff(c, text, "faq: no matching answer in the faq table")
	}
	return []Out{{Text: render("faq", map[string]any{"Answer": fs[best].Answer})}}
}

// matchProducts scores products by query words found in name and keywords.
func (b *Bot) matchProducts(q string) []Product {
	ps, _ := b.S.Sellable()
	sort.Slice(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID }) // ties keep the sheet's order
	want := keySet(q)
	type sc struct {
		p Product
		n int
	}
	var hits []sc
	for _, p := range ps {
		n := 0
		name := keySet(p.Name)
		for k := range keySet(p.Name + " " + p.Keywords) {
			if want[k] {
				n++
				if name[k] {
					n++ // a name hit counts double
				}
			}
		}
		if n > 0 {
			hits = append(hits, sc{p, n})
		}
	}
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].n > hits[j-1].n; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	var out []Product
	for _, h := range hits {
		out = append(out, h.p)
	}
	return out
}

func (b *Bot) search(c Customer, q, text string) []Out {
	ps := b.matchProducts(q)
	if len(ps) == 0 {
		if outs, ok := b.fallback(c, text); ok {
			return outs
		}
		return b.handoff(c, text, "search_product: no product matches "+strconv.Quote(q))
	}
	if len(ps) > 5 {
		ps = ps[:5]
	}
	out := Out{Text: render("search", map[string]any{"Products": ps, "Cur": b.cur()})}
	for _, p := range ps { // the photo of the best match that has one
		if out.Image = b.photo(p); out.Image != "" {
			break
		}
	}
	return []Out{out}
}

var numRe = regexp.MustCompile(`\b(\d{1,3})\b`)

func (b *Bot) add(c Customer, product, qty, text string) []Out {
	ps := b.matchProducts(product)
	if len(ps) == 0 {
		return b.handoff(c, text, "add_to_order: no product matches "+strconv.Quote(product))
	}
	p := ps[0]
	n, err := strconv.Atoi(strings.TrimSpace(qty))
	if err != nil || n <= 0 {
		// The model sometimes drops the quantity; take the first number in the message, else 1.
		n = 1
		if m := numRe.FindStringSubmatch(text); m != nil {
			n, _ = strconv.Atoi(m[1])
		}
	}
	if n > 50 {
		return b.handoff(c, text, "add_to_order: unusual quantity")
	}
	o, err := b.S.EnsureCart(c.ID)
	if err != nil {
		return b.handoff(c, text, "cart: "+err.Error())
	}
	inCart := 0
	for _, it := range o.Items {
		if it.ProductID == p.ID {
			inCart = it.Qty
		}
	}
	if !p.IsService() && inCart+n > p.Stock {
		return []Out{{Text: render("nostock", map[string]any{"Stock": p.Stock, "Name": p.Name})}}
	}
	if err := b.S.AddItem(o.ID, p, n); err != nil {
		return b.handoff(c, text, "cart: "+err.Error())
	}
	o, _ = b.S.Cart(c.ID)
	added := fmt.Sprintf("%d x %s", n, p.Name)
	return []Out{{Text: b.cartText(o, added), Image: b.photo(p), kind: "cart", added: added}}
}

func (b *Bot) pickSlot(c Customer, want, text string) []Out {
	if o, _ := b.S.Cart(c.ID); o != nil && o.HasService() {
		// A service is booked on the date-time grid; the model's slot words are not trusted as times.
		if outs, ok := b.pickTime(c, o, text); ok {
			return outs
		}
		return []Out{{Text: b.cartText(o, "")}}
	}
	slots, _ := b.S.Slots()
	var pick *Slot
	if m := numRe.FindStringSubmatch(want); m != nil && strings.TrimSpace(want) == m[1] || strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "slot") {
		// "2" or "slot 2" means the 2nd slot in the list we showed.
		if m := numRe.FindStringSubmatch(want + " " + text); m != nil {
			if i, _ := strconv.Atoi(m[1]); i >= 1 && i <= len(slots) {
				pick = &slots[i-1]
			}
		}
	}
	if pick == nil {
		wk := keySet(want)
		best := 0
		for i, s := range slots {
			n := 0
			for k := range keySet(s.Label) {
				if wk[k] {
					n++
				}
			}
			if n > best {
				best, pick = n, &slots[i]
			}
		}
	}
	if pick == nil {
		return b.handoff(c, text, "pick_slot: no slot matches "+strconv.Quote(want))
	}
	o, _ := b.S.Cart(c.ID)
	if o == nil || len(o.Items) == 0 {
		// A slot pick with an empty cart is usually the model misreading the message.
		return b.handoff(c, text, "pick_slot: cart is empty")
	}
	b.S.SetSlot(o.ID, pick.ID)
	o, _ = b.S.Cart(c.ID)
	return []Out{{Text: b.cartText(o, "")}}
}

var greetings = map[string]bool{"hello": true, "hi": true, "hey": true, "hallo": true, "hola": true, "bonjour": true,
	"good morning": true, "good afternoon": true, "good evening": true, "hello there": true, "hi there": true}

func isGreeting(text string) bool { return greetings[strings.Join(words(text), " ")] }

func isAboutShop(text string) bool {
	if isGreeting(text) {
		return true
	}
	for _, w := range words(text) {
		switch w {
		case "who", "sell", "shop", "store", "about":
			return true
		}
	}
	return false
}

func isYes(text string) bool {
	switch strings.Join(words(text), " ") {
	case "yes", "y", "yes please", "yes confirm", "confirm", "ok yes", "yes i confirm",
		// a bare yes in the other languages tested in docs/test-report.md
		"si", "sí", "ja", "oui", "sim", "نعم":
		return true
	}
	return false
}

// confirm is gated in Go: it only runs when the message really is YES and the cart is complete.
func (b *Bot) confirm(c Customer, text string) []Out {
	if !isYes(text) {
		return b.handoff(c, text, "confirm_order: message is not YES")
	}
	o, _ := b.S.Cart(c.ID)
	if o == nil || len(o.Items) == 0 {
		return b.handoff(c, text, "confirm_order: nothing in the cart")
	}
	if !o.Ready() {
		return []Out{{Text: b.cartText(o, "")}}
	}
	if c.Address == "" && o.HasProduct() {
		// Asked once, at the moment of buying; the next message is the address.
		b.S.SetWaitAddr(c.ID, true)
		return []Out{{Text: render("ask_address", nil), kind: "ask_address"}}
	}
	inv, err := b.S.Confirm(o)
	if errors.Is(err, errTaken) {
		b.S.SetBookDay(o.ID, "")
		o, _ = b.S.Cart(c.ID)
		return []Out{{Text: render("taken", nil)}, {Text: b.cartText(o, "")}}
	}
	if err != nil {
		return b.handoff(c, text, "confirm_order: "+err.Error())
	}
	o, _ = b.S.Order(o.ID)
	outs := []Out{{Text: b.renderW("invoice", map[string]any{"Order": o, "Invoice": inv, "Cur": b.cur(), "Shop": b.S.Get("shop_name"), "Address": b.S.Get("address")})}}
	if b.Dir != "" {
		if path, err := WriteInvoice(b.S, o, b.Dir); err == nil {
			outs[0].Image = path
		} else {
			b.debug("invoice: %v", err)
		}
	}
	return append(outs, b.payQR(o)...)
}

// address saves a delivery address. When it was asked for at YES, the order is confirmed straight away.
func (b *Bot) address(c Customer, text string, located bool) []Out {
	addr := strings.TrimSpace(text)
	if !located && (len([]rune(addr)) < 6 || isYes(addr)) {
		return []Out{{Text: render("ask_address", nil), kind: "ask_address"}}
	}
	if err := b.S.SetAddress(c.ID, addr); err != nil {
		return b.handoff(c, text, "address: "+err.Error())
	}
	if !c.WaitAddr {
		return []Out{{Text: render("location_saved", nil)}}
	}
	c.Address, c.WaitAddr = addr, false
	outs := b.confirm(c, "yes")
	if len(outs) > 0 && outs[0].kind != "handoff" {
		outs = append([]Out{{Text: render("delivery_to", map[string]any{"Address": addr})}}, outs...)
	}
	return outs
}

// photo is the product's photo file, or "".
func (b *Bot) photo(p Product) string {
	if b.Dir == "" {
		return ""
	}
	return ProductImage(ImagesDir(b.Dir), p.SKU)
}

// payQR asks for payment with the owner's QR image, payment link, or both (Settings).
// ownshop never touches money: the owner checks the payment and clicks Payment received.
func (b *Bot) payQR(o *Order) []Out {
	d := payDetails(b.S, o)
	qr, link, note := b.S.Get("qr_path"), d["Link"].(string), d["Note"].(string)
	if u, _ := d["UPI"].(string); u != "" {
		img := ""
		if b.Dir != "" {
			if p, err := WriteUPIQR(u, filepath.Join(b.Dir, "invoices"), o.ID); err == nil {
				img = p
			} else {
				b.debug("upi qr: %v", err)
			}
		}
		return []Out{{Text: render("pay", d), Image: img}}
	}
	if qr == "" && link == "" && note == "" {
		return []Out{{Text: render("pay_none", d)}}
	}
	return []Out{{Text: render("pay", d), Image: qr}}
}

// payDetails are the payment methods the owner set (Settings), for the "pay" message and UCP:
// UPI is a per-order UPI link (amount and order filled in), Link the payment link, Note the bank text.
func payDetails(s *Store, o *Order) map[string]any {
	qr, link := s.Get("qr_path"), s.Get("pay_link")
	note := strings.TrimRight(strings.TrimSpace(s.Get("pay_note")), ".")
	d := map[string]any{"Order": o, "Cur": s.Get("currency"), "Shop": s.Get("shop_name"), "QR": qr != "", "Link": link, "Note": note}
	if id := strings.TrimSpace(s.Get("upi_id")); id != "" {
		// A per-order UPI QR, amount and order filled in, wins over the uploaded QR image.
		name := strings.TrimSpace(s.Get("upi_name"))
		if name == "" {
			name = s.Get("shop_name")
		}
		d["UPI"] = UPILink(id, name, o.Total, o.ID)
	}
	return d
}

// StatusMessage is what the customer gets when the owner clicks a status button.
// On Payment received it also renders the PNG receipt, stores it, and attaches it.
func (b *Bot) StatusMessage(o *Order, dir string) Out {
	out := Out{Text: b.renderW("update_"+o.Status, map[string]any{"Order": o, "Shop": b.S.Get("shop_name")})}
	if o.Status == StPaid {
		if path, err := WriteReceipt(b.S, o, dir); err == nil {
			out.Image = path
		} else {
			b.debug("receipt: %v", err)
		}
	}
	return out
}

// Notify sends an owner-triggered message (status update or manual reply) and logs it.
func (b *Bot) Notify(c Customer, o Out) error {
	b.S.Log(c.Phone, "out", o.Text+imgNote(o), "owner", "")
	if b.Send == nil {
		return fmt.Errorf("no sender connected")
	}
	return b.Send.Send(c, o)
}
