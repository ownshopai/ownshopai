package main

import (
	"strings"
	"testing"
)

// bad is a model answer that would hand off: Go must answer before or after it.
var bad = call("faq", "question:"+e("x"))

func has(t *testing.T, name, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("%s: %q lacks %q", name, got, w)
		}
	}
}

func TestCatalogQuestion(t *testing.T) {
	h := svcHarness(t, "services")
	h.s.DB.Exec(`UPDATE products SET price=300 WHERE sku='SVC-CUT'`)
	for _, q := range []string{"hi what are the services available", "what do you offer", "menu", "price list please", "what can I book"} {
		got := h.from("+3170", bad, q)
		has(t, q, got, "Here is what we have:\n1. Hair colour, 60 min, INR 900.00\n2. Haircut, 30 min, INR 300.00\nTo book, reply for example: book Hair colour")
		if strings.Contains(got, "honey") {
			t.Fatalf("%s: products in a services shop", q)
		}
	}
	// Also when the model goes to faq or search and they find nothing.
	got := h.from("+3170", call("search_product", "query:"+e("services")), "your services list?")
	has(t, "fallback", got, "2. Haircut, 30 min")
	if h.handoffs() != 0 {
		t.Fatalf("hand-offs %d", h.handoffs())
	}
	// A products shop keeps the product wording.
	p := newHarness(t)
	has(t, "products", p.say(bad, "what products do you have"), "Here is what we have:\n1. ", " in stock)", "To order, reply for example: add 2")
}

func TestGreeting(t *testing.T) {
	h := svcHarness(t, "services")
	for _, g := range []string{"hi", "Hello!", "hey", "good morning", "vanakkam", "namaste", "hi how are you"} {
		eq(t, g, h.from("+3171", bad, g), "Hello, this is Test Shop.\nReply services to see what you can book.")
	}
	p := newHarness(t)
	eq(t, "products", p.say(bad, "hi"), "Hello, this is Test Shop.\nAsk for a product (for example: do you have honey) or about hours and delivery.")
	// A greeting with a question is the question.
	has(t, "hi + question", p.say(call("search_product", "query:"+e("honey")), "hi do you have honey"), "Raw honey")
}

func TestBookByChatFullFlow(t *testing.T) {
	h := svcHarness(t, "services")
	p := "+3172000000"
	h.from(p, bad, "hi")
	has(t, "services", h.from(p, bad, "what services do you have"), "2. Haircut, 30 min")
	got := h.from(p, bad, "book haircut")
	has(t, "book", got, "Added 1 x Haircut.", "Pick a time:", "2. Tomorrow (Sat 21)")
	has(t, "day", h.from(p, bad, "2"), "Tomorrow (Sat 21), free times:", "1. 10:00")
	has(t, "time", h.from(p, bad, "1"), "Reply YES to confirm booking 1042")
	got = h.from(p, bad, "YES")
	has(t, "confirmed", got, "1042 confirmed")
	if strings.Contains(got, "deliver") || strings.Contains(got, "Where should") {
		t.Fatalf("asked for an address: %q", got)
	}
	o, _ := h.s.Order(1042)
	if o.BookAt != "2026-11-21 10:00" || o.Status != StAwaiting {
		t.Fatalf("order %+v", o)
	}
	has(t, "I want", h.from("+3172000001", bad, "I want a hair colour"), "Added 1 x Hair colour.")
}

func TestBuiltinFAQ(t *testing.T) {
	h := svcHarness(t, "services") // Fri 20 Nov 12:10, Mon-Sat 10-12 and 14-16
	h.s.DB.Exec(`DELETE FROM faq`) // the owner's sheet is tested separately
	ask := func(q string) string { return h.from("+3173", bad, q) }
	has(t, "hours", ask("what are your timings"), "Our hours: Mon–Sat 10:00 AM–12:00 PM, 2:00 PM–4:00 PM.", "Open today 10:00 AM–12:00 PM, 2:00 PM–4:00 PM.")
	h.s.SetOverride("2026-11-20", nil)
	has(t, "closed today", ask("are you open today?"), "Closed today, next open Sat 21 Nov 10:00 AM.")
	h.s.DelOverride("2026-11-20")
	has(t, "sunday", ask("sunday open?"), "Sorry, we are closed on Sundays.\nOur hours: Mon–Sat")
	has(t, "saturday", ask("are you open on saturday"), "Yes, on Saturday we are open 10:00 AM–12:00 PM, 2:00 PM–4:00 PM.")
	eq(t, "no address", ask("where are you located"), handoffText)
	h.s.Set("address", "4 Orchard Road")
	eq(t, "address", ask("address?"), "You can find us at: 4 Orchard Road")
	has(t, "price list", ask("how much"), "1. Hair colour, 60 min, INR 900.00", "2. Haircut, 30 min, INR 300.00")
	eq(t, "price of", ask("price of hair colour"), "Here is what we have:\n1. Hair colour, 60 min, INR 900.00\nTo book, reply for example: book Hair colour")
	has(t, "kitna", ask("haircut kitna"), "1. Haircut, 30 min, INR 300.00")
	eq(t, "duration", ask("how long does hair colour take"), "Hair colour takes 60 min. To book, reply for example: book Hair colour")
	has(t, "slots", ask("can I book today"), "Next free times for Hair colour, Today (Fri 20): 14:00, 14:30, 15:00.")
	h.s.Set("qr_path", "")
	eq(t, "no payment", ask("how do I pay"), "The shop will tell you how to pay here shortly.")
	h.s.Set("upi_id", "shop@okhdfc")
	h.s.Set("pay_note", "Bank X")
	eq(t, "payment", ask("gpay?"), "We accept UPI (shop@okhdfc) and bank transfer. You get the details after your order is confirmed.")
	eq(t, "delivery", ask("do you deliver"), "Ask us about delivery to your area: the shop will reply here shortly.")
	eq(t, "cancel", ask("I want to cancel"), "To cancel or change your time, the shop will help you here shortly.")
	eq(t, "status none", ask("my booking status"), handoffText)
	eq(t, "human", ask("can I talk to a person"), "The shop will reply to you here shortly.")
	eq(t, "thanks", ask("thank you"), "You're welcome! Write any time.")
	eq(t, "ok", ask("ok"), "You're welcome! Write any time.")
	// stock, in a products shop
	p := newHarness(t)
	has(t, "stock", p.say(bad, "is raw honey in stock"), "Yes, Raw honey 500g is in stock")
	p.s.DB.Exec(`UPDATE products SET stock=0 WHERE name LIKE 'Raw honey%'`)
	has(t, "out", p.say(bad, "raw honey available?"), "out of stock")
	// order status after an order
	p.s.DB.Exec(`UPDATE products SET stock=9 WHERE name LIKE 'Raw honey%'`)
	p.buy()
	p.say(call("confirm_order", ""), "yes")
	has(t, "status", p.say(bad, "what is my order status"), "Order 1042: Waiting for payment.")
}

func TestOwnerFAQBeatsBuiltin(t *testing.T) {
	h := newHarness(t) // the sample sheet has opening hours and delivery answers
	eq(t, "hours", h.say(bad, "what are your opening hours"), "We are open Monday to Saturday, 09:00 to 18:00. Orders by chat are welcome any time.")
	has(t, "delivery", h.say(bad, "do you deliver"), "Delivery is free above INR 500")
	// ...but "where are you" is not the delivery row just because it says "where".
	eq(t, "address", h.say(bad, "where are you located"), handoffText)
}

func TestE2EFixes(t *testing.T) {
	h := newHarness(t)
	// A byte-order mark in front of the first message (piped text on Windows) is not part of it.
	eq(t, "bom", h.say(bad, "\ufeffhi"), "Hello, this is Test Shop.\nAsk for a product (for example: do you have honey) or about hours and delivery.")
	// Mid-order, a plain question is still answered; "slot 1" still picks the slot.
	h.say(call("add_to_order", "product:"+e("raw honey")+",quantity:1"), "add 1 raw honey")
	has(t, "pay mid-order", h.say(bad, "how do I pay"), "We accept a payment QR code.")
	has(t, "bye mid-order", h.say(bad, "bye"), "You're welcome!")
	has(t, "slot", h.say(call("pick_slot", "slot:"+e("1")), "slot 1"), "Reply YES to confirm order 1042")
}
