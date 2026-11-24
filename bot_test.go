package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	llmkit "github.com/muthuishere/toolnexus/golang"
)

func setup(t *testing.T) (*Store, string) {
	dir := t.TempDir()
	if err := WriteSample(dir); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := NewShopFile(s, filepath.Join(dir, "products.xlsx")).Load(); err != nil {
		t.Fatal(err)
	}
	s.Set("qr_path", filepath.Join(dir, "payment-qr-demo.png"))
	s.Set("shop_name", "Test Shop")
	// The usual test buyer already has a delivery address, so YES confirms at once.
	c, _ := s.Customer("+31600000009", "Test Buyer", "chat-9")
	s.SetAddress(c.ID, "12 Test Street")
	return s, dir
}

func TestOrderFlow(t *testing.T) {
	s, dir := setup(t)
	m := fakeModel{
		"opening hours?":    "call:faq{question:<escape>opening hours<escape>}",
		"do you have honey": "call:search_product{query:<escape>honey<escape>}",
		"add 2 raw honey":   "call:add_to_order{product:<escape>raw honey<escape>,quantity:2}",
		"1 coconut oil":     "call:add_to_order{product:<escape>coconut oil<escape>}",
		"slot 1":            "call:pick_slot{slot:<escape>1<escape>}",
		"yes":               "call:confirm_order{answer:<escape>yes<escape>}",
		"maybe":             "call:confirm_order{answer:<escape>maybe<escape>}",
		"where is my order": "call:order_status{order_id:<escape>your_order_id<escape>}",
		"weather?":          "I cannot help with weather.",
	}
	// Replies go bot -> Hub -> the fake channel the message came in on.
	cap := &capture{name: "fake", account: "shop1"}
	hub := NewHub(nil)
	hub.Add(cap)
	b := &Bot{S: s, M: m, Send: hub}
	say := func(text string) string {
		var parts []string
		for _, o := range b.Handle(context.Background(), Inbound{Channel: "fake", Account: "shop1", SenderID: "chat-9", Phone: "+31600000009", Name: "Test Buyer", Text: text}) {
			parts = append(parts, o.Text+imgNote(o))
		}
		return strings.Join(parts, "\n")
	}
	expect := func(text, want string) string {
		got := say(text)
		if !strings.Contains(got, want) {
			t.Fatalf("%q: reply %q does not contain %q", text, got, want)
		}
		return got
	}
	expect("opening hours?", "Monday to Saturday")
	expect("do you have honey", "Raw honey 500g - INR 9.50")
	expect("add 2 raw honey", "Added 2 x Raw honey 500g")
	expect("maybe", "shop will reply shortly") // confirm gate: not YES
	expect("1 coconut oil", "Pick a delivery slot")
	expect("slot 1", "Reply YES to confirm order 1042")
	got := expect("yes", "Invoice INV-1042")
	if !strings.Contains(got, "INR 26.80") || !strings.Contains(got, "Put order 1042 in the payment note or reference") || !strings.Contains(got, "[image") {
		t.Fatalf("invoice/payment reply wrong: %s", got)
	}
	var stock int
	s.DB.QueryRow(`SELECT stock FROM products WHERE name='Raw honey 500g'`).Scan(&stock)
	if stock != 18 {
		t.Fatalf("stock not cut: %d", stock)
	}
	expect("where is my order", "Waiting for payment")

	// Owner clicks Payment received then Packed through the web handler.
	if len(cap.to) == 0 || cap.to[0] != "chat-9" {
		t.Fatalf("replies did not go back to the sender's chat: %v", cap.to)
	}
	app := &App{S: s, Bot: b, Hub: hub, File: NewShopFile(s, filepath.Join(dir, "products.xlsx")), Dir: dir}
	h := app.Routes()
	for _, to := range []string{StPaid, StPacked} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/orders/1042/status", strings.NewReader(url.Values{"to": {to}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusSeeOther || !strings.Contains(rr.Header().Get("Location"), "notified") {
			t.Fatalf("status %s: %d %s", to, rr.Code, rr.Header().Get("Location"))
		}
	}
	last := cap.outs[len(cap.outs)-2:]
	if !strings.Contains(last[0].Text, "Payment received") || last[0].Image == "" || !strings.Contains(last[1].Text, "packed") {
		t.Fatalf("status messages wrong: %+v", last)
	}
	if fi, err := os.Stat(last[0].Image); err != nil || fi.Size() < 1000 {
		t.Fatalf("receipt png missing: %v", err)
	}
	var paid int
	s.DB.QueryRow(`SELECT paid FROM invoices WHERE order_id=1042`).Scan(&paid)
	if paid != 1 {
		t.Fatal("invoice not marked paid")
	}
	expect("where is my order", "Packed")
	expect("weather?", "shop will reply shortly")
	hs, _ := s.Handoffs()
	if len(hs) != 2 {
		t.Fatalf("want 2 hand-offs, got %d", len(hs))
	}
	// Pages render.
	for _, p := range []string{"/orders", "/products", "/chats", "/settings", "/setup/1", "/setup/2", "/setup/3", "/setup/4", "/orders/1042/receipt.png"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", p, nil))
		if rr.Code != 200 {
			t.Fatalf("%s: %d %s", p, rr.Code, rr.Body.String())
		}
		if strings.HasSuffix(p, ".png") {
			continue
		}
		if !strings.Contains(rr.Body.String(), `built with love by <a href="https://deemwar.com">deemwar.com</a>`) {
			t.Fatalf("%s: footer missing", p)
		}
	}
}

func TestFindCustomerPrivacy(t *testing.T) {
	s, _ := setup(t)
	b := &Bot{S: s, M: fakeModel{"find customer Bob": "call:find_customer{name:<escape>Bob<escape>}"}}
	s.Customer("+31600000002", "Bob Other", "")
	out := b.Handle(context.Background(), Inbound{Phone: "+31600000003", Name: "Ana", Text: "find customer Bob"})
	if !strings.Contains(out[0].Text, "shop will reply shortly") {
		t.Fatalf("leaked another customer: %q", out[0].Text)
	}
}

func TestIsYesOtherLanguages(t *testing.T) {
	for _, y := range []string{"sí", "Ja", "oui", "sim", "نعم"} {
		if !isYes(y) {
			t.Errorf("%q should be a yes", y)
		}
	}
}

// A bare "1" after the numbered delivery slots picks that slot in Go. The model made no tool
// call for it on the Windows test PC, and the customer got "The shop will reply shortly".
func TestBareSlotNumberPicksSlot(t *testing.T) {
	s, _ := setup(t)
	m := fakeModel{"add 2 raw honey": "call:add_to_order{product:<escape>raw honey<escape>,quantity:2}"}
	b := &Bot{S: s, M: m} // any other message: the fake model makes no tool call
	say := func(text string) string {
		var parts []string
		for _, o := range b.Handle(context.Background(), Inbound{Channel: "fake", Account: "shop1", SenderID: "chat-9", Phone: "+31600000009", Name: "Test Buyer", Text: text}) {
			parts = append(parts, o.Text)
		}
		return strings.Join(parts, "\n")
	}
	if got := say("1"); strings.Contains(got, "Reply YES") {
		t.Fatalf("a number with no cart must not pick a slot: %q", got)
	}
	if got := say("add 2 raw honey"); !strings.Contains(got, "Pick a delivery slot") {
		t.Fatalf("cart reply: %q", got)
	}
	if got := say("9"); strings.Contains(got, "Reply YES") {
		t.Fatalf("an out-of-range number must not pick a slot: %q", got)
	}
	if got := say("1"); !strings.Contains(got, "Reply YES to confirm order") {
		t.Fatalf("bare 1 after the slot list: %q", got)
	}
}

// Warm runs the model once at start and must leave no trace: no reply, no order, no chat log.
func TestWarmHasNoSideEffects(t *testing.T) {
	s, _ := setup(t)
	calls := 0
	b := &Bot{S: s, M: countModel{&calls, "call:add_to_order{product:<escape>raw honey<escape>,quantity:2}"}}
	b.Warm(context.Background())
	if calls != 1 {
		t.Fatalf("warm-up ran the model %d times, want 1", calls)
	}
	var n int
	for _, q := range []string{`SELECT COUNT(*) FROM order_items`, `SELECT COUNT(*) FROM chat_log`, `SELECT COUNT(*) FROM handoffs`} {
		s.DB.QueryRow(q).Scan(&n)
		if n != 0 {
			t.Fatalf("%s = %d after warm-up, want 0", q, n)
		}
	}
}

// A slow warm-up (model still loading) must not hold up replies that need no model.
func TestWarmDoesNotBlockReplies(t *testing.T) {
	s, _ := setup(t)
	release := make(chan struct{})
	b := &Bot{S: s, M: blockModel{release}}
	go b.Warm(context.Background())
	time.Sleep(50 * time.Millisecond)
	done := make(chan []Out, 1)
	go func() { done <- b.Handle(context.Background(), Inbound{Phone: "+15550001", Name: "A", Text: "hi"}) }()
	select {
	case outs := <-done:
		if len(outs) == 0 {
			t.Fatal("no reply to hi")
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("hi waited for the model warm-up")
	}
	close(release)
}

type blockModel struct{ release chan struct{} }

func (m blockModel) Run(ctx context.Context, _ string, tk *llmkit.Toolkit) (llmkit.RunResult, error) {
	<-m.release
	return llmkit.RunResult{}, nil
}

type countModel struct {
	n   *int
	out string
}

func (c countModel) Run(ctx context.Context, _ string, tk *llmkit.Toolkit) (llmkit.RunResult, error) {
	*c.n++
	return runCanned(ctx, c.out, tk)
}
