package main

import (
	"context"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type harness struct {
	t   *testing.T
	s   *Store
	b   *Bot
	cap *capture
	dir string
}

func newHarness(t *testing.T) *harness {
	s, dir := setup(t)
	h := &harness{t: t, s: s, cap: &capture{name: "fake", account: "a"}, dir: dir}
	hub := NewHub(nil)
	hub.Add(h.cap)
	h.b = &Bot{S: s, Send: hub}
	return h
}

// say sends text with the model forced to output raw; returns all reply texts joined.
func (h *harness) say(raw, text string) string {
	h.b.M = stub(raw)
	var parts []string
	for _, o := range h.b.Handle(context.Background(), Inbound{Channel: "fake", Account: "a", SenderID: "chat-9", Phone: "+31600000009", Name: "Test Buyer", Text: text}) {
		parts = append(parts, o.Text+imgNote(o))
	}
	return strings.Join(parts, "\n")
}

func (h *harness) stock(name string) int {
	var n int
	h.s.DB.QueryRow(`SELECT stock FROM products WHERE name=?`, name).Scan(&n)
	return n
}

func (h *harness) orders() int {
	var n int
	h.s.DB.QueryRow(`SELECT COUNT(*) FROM orders WHERE status<>'cart'`).Scan(&n)
	return n
}

func (h *harness) handoffs() int {
	hs, _ := h.s.Handoffs()
	return len(hs)
}

func eq(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s:\n got: %q\nwant: %q", what, got, want)
	}
}

const handoffText = "Thanks for your message. The shop will reply shortly."

func call(name, args string) string { return "<start_function_call>call:" + name + "{" + args + "}" }
func e(v string) string             { return "<escape>" + v + "<escape>" }

// buy fills a cart with 2 raw honey + 1 coconut oil and picks slot 1.
func (h *harness) buy() {
	h.say(call("add_to_order", "product:"+e("raw honey")+",quantity:2"), "add 2 raw honey")
	h.say(call("add_to_order", "product:"+e("coconut oil")+",quantity:1"), "1 coconut oil")
	h.say(call("pick_slot", "slot:"+e("1")), "slot 1")
}

// ---- 1. Inventory ----

func TestImportSetsStock(t *testing.T) {
	h := newHarness(t)
	if got := h.stock("Raw honey 500g"); got != 20 {
		t.Fatalf("stock after import = %d", got)
	}
	ps, _ := h.s.Products()
	if len(ps) != 15 {
		t.Fatalf("want 15 products, got %d", len(ps))
	}
}

func TestConfirmCutsStock(t *testing.T) {
	h := newHarness(t)
	h.buy()
	if h.stock("Raw honey 500g") != 20 {
		t.Fatal("stock must not move before YES")
	}
	h.say(call("confirm_order", "answer:"+e("YES")), "YES")
	if h.stock("Raw honey 500g") != 18 || h.stock("Cold-pressed coconut oil 500ml") != 24 {
		t.Fatalf("stock not cut: honey %d oil %d", h.stock("Raw honey 500g"), h.stock("Cold-pressed coconut oil 500ml"))
	}
}

func TestCannotOversell(t *testing.T) {
	h := newHarness(t)
	got := h.say(call("add_to_order", "product:"+e("medjool dates")+",quantity:13"), "13 dates")
	eq(t, "oversell reply", got, "Sorry, we only have 12 x Medjool dates 500g left.")
	cart, _ := h.s.Cart(1)
	if cart != nil && len(cart.Items) != 0 {
		t.Fatal("oversell put items in the cart")
	}
	if h.orders() != 0 {
		t.Fatal("oversell created an order")
	}
	// Stock that drops after the cart was filled is caught at confirm.
	h.say(call("add_to_order", "product:"+e("medjool dates")+",quantity:2"), "2 dates")
	h.say(call("pick_slot", "slot:"+e("1")), "slot 1")
	h.s.DB.Exec(`UPDATE products SET stock=1 WHERE name='Medjool dates 500g'`)
	got = h.say(call("confirm_order", "answer:"+e("yes")), "yes")
	eq(t, "confirm with no stock", got, handoffText)
	if h.orders() != 0 || h.stock("Medjool dates 500g") != 1 {
		t.Fatal("confirm without stock must not create an order or cut stock")
	}
}

func TestOutOfStockSearch(t *testing.T) {
	h := newHarness(t)
	h.s.DB.Exec(`UPDATE products SET stock=0 WHERE name='Turmeric powder 100g'`)
	got := h.say(call("search_product", "query:"+e("turmeric")), "turmeric?")
	if !strings.Contains(got, "Turmeric powder 100g - INR 3.40 (out of stock)") {
		t.Fatalf("search reply: %q", got)
	}
}

func TestFileEditsAreQuoted(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(h.dir, "products.xlsx")
	app := &App{S: h.s, Bot: h.b, File: NewShopFile(h.s, path), Dir: h.dir}
	setCells(t, path, map[string]any{"D12": 9.10, "E12": 3}) // row 12 is Almond butter 250g
	rr := httptest.NewRecorder()
	app.Routes().ServeHTTP(rr, httptest.NewRequest("POST", "/products/reload", nil))
	if !strings.Contains(rr.Header().Get("Location"), "Read+15+products") {
		t.Fatalf("reload: %s", rr.Header().Get("Location"))
	}
	got := h.say(call("search_product", "query:"+e("almond butter")), "almond butter price")
	if !strings.HasPrefix(got, "Here is what we have:\n1. Almond butter 250g - INR 9.10 (3 in stock)") {
		t.Fatalf("bot does not quote the edited price/stock: %q", got)
	}
	got = h.say(call("add_to_order", "product:"+e("almond butter")+",quantity:4"), "4 almond butter")
	eq(t, "edited stock limit", got, "Sorry, we only have 3 x Almond butter 250g left.")
}

// ---- 2. FAQ ----

func TestFAQOnlyFromTable(t *testing.T) {
	h := newHarness(t)
	cases := map[string]string{
		"what time do you open?":         "We are open Monday to Saturday, 09:00 to 18:00. Orders by chat are welcome any time.",
		"are you closed on sunday":       "We are open Monday to Saturday, 09:00 to 18:00. Orders by chat are welcome any time.",
		"do you ship to my town?":        "We deliver within the city and nearby towns. Delivery is free above INR 500, otherwise INR 50.",
		"can i get a refund":             "Unopened items can be returned within 14 days. Write to us here with your order number.",
		"is everything bio / certified?": "Yes. All food items are certified organic under the EU organic label; certificates are available on request.",
	}
	for q, want := range cases {
		eq(t, q, h.say(call("faq", "question:"+e(q)), q), want)
	}
	// Edited in the web page -> the bot uses the new text.
	h.s.DB.Exec(`UPDATE faq SET answer='Open daily 08:00-20:00.' WHERE question LIKE '%opening hours%'`)
	eq(t, "edited faq", h.say(call("faq", "question:"+e("opening hours")), "opening hours?"), "Open daily 08:00-20:00.")
}

func TestFAQUnknownHandsOff(t *testing.T) {
	h := newHarness(t)
	for _, q := range []string{"do you sell gift cards?", "who is your favourite singer"} {
		eq(t, q, h.say(call("faq", "question:"+e(q)), q), handoffText)
	}
	// A payment question gets the methods the shop really has, nothing invented.
	eq(t, "bitcoin", h.say(call("faq", "question:"+e("bitcoin")), "can I pay in bitcoin"), "We accept a payment QR code. You get the details after your order is confirmed.")
	if h.handoffs() != 2 {
		t.Fatalf("want 2 hand-offs, got %d", h.handoffs())
	}
}

// ---- 3 + 4. Each tool, exact reply text ----

func TestSearchReply(t *testing.T) {
	h := newHarness(t)
	eq(t, "search honey", h.say(call("search_product", "query:"+e("honey")), "do you have honey?"),
		"Here is what we have:\n1. Raw honey 500g - INR 9.50 (20 in stock)\n2. Manuka style honey 250g - INR 14.90 (8 in stock)\nTo order, reply for example: add 2 Raw honey 500g")
	eq(t, "search miss", h.say(call("search_product", "query:"+e("bicycle")), "bicycle?"), handoffText)
}

func TestAddAndSlotReplies(t *testing.T) {
	h := newHarness(t)
	eq(t, "add", h.say(call("add_to_order", "product:"+e("raw honey")+",quantity:2"), "add 2 raw honey"),
		"Added 2 x Raw honey 500g.\nYour order 1042:\n- 2 x Raw honey 500g = INR 19.00\nTotal: INR 19.00\nPick a delivery slot:\n1. Saturday 10:00-12:00\n2. Saturday 14:00-16:00\n3. Monday 17:00-19:00\nReply for example: slot 1")
	// quantity missing from the call: taken from the message text
	got := h.say(call("add_to_order", "product:"+e("coconut oil")), "3 coconut oil please")
	if !strings.HasPrefix(got, "Added 3 x Cold-pressed coconut oil 500ml.") {
		t.Fatalf("quantity fallback: %q", got)
	}
	eq(t, "add miss", h.say(call("add_to_order", "product:"+e("unicorn")+",quantity:1"), "1 unicorn"), handoffText)
	eq(t, "slot by words", h.say(call("pick_slot", "slot:"+e("Monday evening")), "monday 17"),
		"Your order 1042:\n- 2 x Raw honey 500g = INR 19.00\n- 3 x Cold-pressed coconut oil 500ml = INR 23.40\nTotal: INR 42.40\nDelivery: Monday 17:00-19:00\nReply YES to confirm order 1042.")
	eq(t, "slot miss", h.say(call("pick_slot", "slot:"+e("Minute 30")), "tell me a joke"), handoffText)
}

func TestPickSlotEmptyCart(t *testing.T) {
	h := newHarness(t)
	eq(t, "empty", h.say(call("pick_slot", "slot:"+e("1")), "slot 1"), handoffText)
}

func TestConfirmOnlyAfterYes(t *testing.T) {
	h := newHarness(t)
	h.buy()
	for _, t2 := range []string{"maybe", "no", "what is the total", "yes but change the slot"} {
		eq(t, t2, h.say(call("confirm_order", "answer:"+e("YES")), t2), handoffText)
	}
	if h.orders() != 0 {
		t.Fatal("order confirmed without YES")
	}
	h.s.Set("shop_name", "Hill Farm Store")
	h.s.Set("address", "4 Orchard Road")
	h.s.Set("currency", "GBP")
	eq(t, "confirm", h.say(call("confirm_order", "answer:"+e("yes")), "Yes please"),
		"Hill Farm Store, 4 Orchard Road\nOrder 1042 confirmed. Invoice INV-1042\n- 2 x Raw honey 500g = GBP 19.00\n- 1 x Cold-pressed coconut oil 500ml = GBP 7.80\nTotal: GBP 26.80\nDelivery: Saturday 10:00-12:00\nDeliver to: 12 Test Street\n"+
			"Hill Farm Store: please pay GBP 26.80 using this QR code. Put order 1042 in the payment note or reference. [image "+h.s.Get("qr_path")+"]")
	// YES with no cart: nothing to confirm.
	eq(t, "yes again", h.say(call("confirm_order", "answer:"+e("yes")), "yes"), handoffText)
}

func TestConfirmNeedsSlot(t *testing.T) {
	h := newHarness(t)
	h.say(call("add_to_order", "product:"+e("raw honey")+",quantity:1"), "1 raw honey")
	got := h.say(call("confirm_order", "answer:"+e("yes")), "yes")
	if !strings.Contains(got, "Pick a delivery slot") || h.orders() != 0 {
		t.Fatalf("confirm without slot: %q", got)
	}
}

func TestSendPaymentQR(t *testing.T) {
	h := newHarness(t)
	eq(t, "no order", h.say(call("send_payment_qr", ""), "send the qr"), handoffText)
	h.buy()
	h.say(call("confirm_order", "answer:"+e("yes")), "yes")
	eq(t, "qr", h.say(call("send_payment_qr", "order_id:"+e("your_order_id")), "send me the payment qr"),
		"Test Shop: please pay INR 26.80 using this QR code. Put order 1042 in the payment note or reference. [image "+h.s.Get("qr_path")+"]")
	h.s.Set("qr_path", "")
	eq(t, "no qr uploaded", h.say(call("send_payment_qr", ""), "qr?"),
		"Test Shop: please pay INR 26.80. Put order 1042 in the payment note or reference. The shop will send payment details shortly.")
	h.s.Set("pay_link", "https://pay.example.test/greenleaf")
	eq(t, "link only", h.say(call("send_payment_qr", ""), "how do I pay"),
		"Test Shop: please pay INR 26.80 at https://pay.example.test/greenleaf. Put order 1042 in the payment note or reference.")
	h.s.Set("qr_path", "q.png")
	eq(t, "qr and link", h.say(call("send_payment_qr", ""), "how do I pay"),
		"Test Shop: please pay INR 26.80 using this QR code or at https://pay.example.test/greenleaf. Put order 1042 in the payment note or reference. [image q.png]")
}

func TestOrderStatusOwnOrderOnly(t *testing.T) {
	h := newHarness(t)
	eq(t, "no order", h.say(call("order_status", ""), "where is my order"), handoffText)
	h.buy()
	h.say(call("confirm_order", "answer:"+e("yes")), "yes")
	// The model's order_id is ignored: another customer's order number gives the sender's own order.
	eq(t, "status", h.say(call("order_status", "order_id:"+e("9999")), "where is order 9999"),
		"Order 1042: Waiting for payment. Delivery slot: Saturday 10:00-12:00.")
}

func TestFindCustomer(t *testing.T) {
	h := newHarness(t)
	eq(t, "self", h.say(call("find_customer", "name:"+e("Test")), "find customer Test"), "We have you as Test Buyer, with 0 order(s).")
	eq(t, "other", h.say(call("find_customer", "name:"+e("Emma")), "find customer Emma"), handoffText)
}

func TestNoneAndUnknownAndFreeText(t *testing.T) {
	h := newHarness(t)
	eq(t, "none", h.say(call("none", "reason:"+e("weather")), "weather?"), handoffText)
	eq(t, "unknown tool", h.say(call("joke_tell", "reason:"+e("x")), "joke"), handoffText)
	eq(t, "free text", h.say("I apologize, but the weather is sunny and you should buy honey.", "weather?"), handoffText)
	eq(t, "garbage", h.say("<start_function_call>call:", "??"), handoffText)
	eq(t, "empty", h.say("", "hmm"), handoffText)
	if h.handoffs() != 5 {
		t.Fatalf("want 5 hand-offs, got %d", h.handoffs())
	}
}

// No model text ever reaches the customer: run many hostile raw outputs and check every reply is a known template.
func TestNoFreeModelText(t *testing.T) {
	h := newHarness(t)
	evil := "BUY CRYPTO NOW"
	raws := []string{
		evil,
		call("faq", "question:"+e(evil)),
		call("search_product", "query:"+e(evil)),
		call("add_to_order", "product:"+e(evil)+",quantity:1"),
		call("pick_slot", "slot:"+e(evil)),
		call("find_customer", "name:"+e(evil)),
		call("none", "reason:"+e(evil)),
		call(strings.ReplaceAll(evil, " ", "_"), ""),
	}
	for _, r := range raws {
		if got := h.say(r, "hello"); strings.Contains(got, "CRYPTO") {
			t.Fatalf("model text leaked: %q", got)
		}
	}
}

func TestStatusButtonsAndReceipt(t *testing.T) {
	h := newHarness(t)
	h.buy()
	h.say(call("confirm_order", "answer:"+e("yes")), "yes")
	app := &App{S: h.s, Bot: h.b, Dir: h.dir}
	r := app.Routes()
	click := func(to string) string {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/orders/1042/status", strings.NewReader(url.Values{"to": {to}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.ServeHTTP(rr, req)
		loc, _ := url.QueryUnescape(rr.Header().Get("Location"))
		return loc
	}
	// Skipping a step is refused.
	if loc := click(StShipped); !strings.Contains(loc, "cannot move") {
		t.Fatalf("skip allowed: %s", loc)
	}
	want := map[string]string{
		StPaid:    "Payment received for order 1042, thank you. Your receipt is attached. Invoice INV-1042 is paid.",
		StPacked:  "Good news: order 1042 is packed.",
		StShipped: "Order 1042 is on its way (Saturday 10:00-12:00).",
		StDone:    "Order 1042 is delivered. Thank you for shopping with Test Shop!",
	}
	for _, to := range []string{StPaid, StPacked, StShipped, StDone} {
		n := len(h.cap.outs)
		if loc := click(to); !strings.Contains(loc, "Customer notified") {
			t.Fatalf("%s: %s", to, loc)
		}
		if len(h.cap.outs) != n+1 {
			t.Fatalf("%s: no WhatsApp message sent", to)
		}
		o := h.cap.outs[n]
		eq(t, to, o.Text, want[to])
		if to == StPaid {
			if o.Image == "" {
				t.Fatal("receipt image not attached")
			}
			var paid int
			var rpath string
			h.s.DB.QueryRow(`SELECT paid FROM invoices WHERE order_id=1042`).Scan(&paid)
			h.s.DB.QueryRow(`SELECT path FROM receipts WHERE order_id=1042`).Scan(&rpath)
			if paid != 1 || rpath != o.Image {
				t.Fatalf("invoice paid=%d receipt=%q", paid, rpath)
			}
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, httptest.NewRequest("GET", "/orders/1042/receipt.png?download=1", nil))
			if rr.Code != 200 || !strings.HasPrefix(rr.Body.String(), "\x89PNG") || !strings.Contains(rr.Header().Get("Content-Disposition"), "receipt-1042.png") {
				t.Fatalf("receipt download: %d", rr.Code)
			}
		} else if o.Image != "" {
			t.Fatalf("%s: unexpected image", to)
		}
	}
	o, _ := h.s.Order(1042)
	if o.Status != StDone {
		t.Fatalf("final status %s", o.Status)
	}
	if loc := click(StDone); !strings.Contains(loc, "cannot move") {
		t.Fatalf("double click allowed: %s", loc)
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

// One message that adds two products names both on the first reply line.
func TestMultiAddNamesEveryItem(t *testing.T) {
	h := newHarness(t)
	got := h.say(call("add_to_order", "product:"+e("raw honey")+",quantity:2")+" | "+call("add_to_order", "product:"+e("coconut oil")+",quantity:1"),
		"add 2 raw honey and 1 coconut oil")
	want := "Added 2 x Raw honey 500g and 1 x Cold-pressed coconut oil 500ml.\nYour order 1042:"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("got %q", got)
	}
}
