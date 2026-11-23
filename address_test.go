package main

import (
	"context"
	"strings"
	"testing"
)

// as sends one message from another customer (no saved address).
func (h *harness) as(phone string, in Inbound) string {
	in.Channel, in.Account, in.SenderID, in.Phone = "fake", "a", "chat-"+phone, phone
	var parts []string
	for _, o := range h.b.Handle(context.Background(), in) {
		parts = append(parts, o.Text)
	}
	return strings.Join(parts, "\n")
}

const askAddr = "Where should we deliver? Share your location (tap attach, then Location) or type your address."

func (h *harness) newBuyerCart(phone string) {
	for _, m := range []struct{ raw, text string }{
		{call("add_to_order", "product:"+e("raw honey")+",quantity:1"), "1 raw honey"},
		{call("pick_slot", "slot:"+e("1")), "slot 1"},
	} {
		h.b.M = stub(m.raw)
		h.as(phone, Inbound{Text: m.text})
	}
	h.b.M = stub(call("faq", "question:"+e("x")))
}

func TestYesWithoutAddressAsks(t *testing.T) {
	h := newHarness(t)
	h.newBuyerCart("+31611111111")
	eq(t, "yes", h.as("+31611111111", Inbound{Text: "YES"}), askAddr)
	eq(t, "too short", h.as("+31611111111", Inbound{Text: "yes"}), askAddr)
	if h.orders() != 0 {
		t.Fatal("confirmed without an address")
	}
}

func TestLocationWhileWaitingConfirms(t *testing.T) {
	h := newHarness(t)
	h.newBuyerCart("+31611111112")
	h.as("+31611111112", Inbound{Text: "yes"})
	got := h.as("+31611111112", Inbound{Lat: 12.9715987, Lng: 77.5945627, LocLabel: "MG Road"})
	want := "Delivery to: MG Road https://maps.google.com/?q=12.971599,77.594563"
	if !strings.HasPrefix(got, want+"\n") || !strings.Contains(got, "Invoice INV-1042") ||
		!strings.Contains(got, "Deliver to: MG Road https://maps.google.com/?q=12.971599,77.594563") {
		t.Fatalf("got %q", got)
	}
	o, _ := h.s.Order(1042)
	if o.DeliverTo != "MG Road https://maps.google.com/?q=12.971599,77.594563" || o.MapURL() != "https://maps.google.com/?q=12.971599,77.594563" {
		t.Fatalf("order deliver_to %q", o.DeliverTo)
	}
}

func TestTypedAddressWhileWaitingConfirms(t *testing.T) {
	h := newHarness(t)
	h.newBuyerCart("+31611111113")
	h.as("+31611111113", Inbound{Text: "yes"})
	got := h.as("+31611111113", Inbound{Text: "4 Orchard Road, Leeds"})
	if !strings.HasPrefix(got, "Delivery to: 4 Orchard Road, Leeds\n") || !strings.Contains(got, "Invoice INV-1042") {
		t.Fatalf("got %q", got)
	}
	c, _ := h.s.Customer("+31611111113", "", "")
	if c.Address != "4 Orchard Road, Leeds" || c.WaitAddr {
		t.Fatalf("customer %+v", c)
	}
}

func TestYesWithSavedAddressConfirmsAtOnce(t *testing.T) {
	h := newHarness(t)
	h.buy()
	got := h.say(call("confirm_order", "answer:"+e("yes")), "yes")
	if !strings.Contains(got, "Invoice INV-1042") || !strings.Contains(got, "Deliver to: 12 Test Street") || strings.Contains(got, "Where should") {
		t.Fatalf("got %q", got)
	}
}

func TestLocationAnyTimeIsSaved(t *testing.T) {
	h := newHarness(t)
	eq(t, "loc", h.as("+31611111114", Inbound{Lat: 52.37, Lng: 4.89}), "Saved your delivery location.")
	c, _ := h.s.Customer("+31611111114", "", "")
	if c.Address != "https://maps.google.com/?q=52.370000,4.890000" {
		t.Fatalf("address %q", c.Address)
	}
	if h.handoffs() != 0 {
		t.Fatal("a location must not be handed off")
	}
}

func TestMapLinkFormat(t *testing.T) {
	eq(t, "link", MapLink(-33.8688197, 151.2093), "https://maps.google.com/?q=-33.868820,151.209300")
	eq(t, "text", Inbound{Lat: 1, Lng: 2, LocLabel: " Cafe "}.LocationText(), "Cafe https://maps.google.com/?q=1.000000,2.000000")
}

func TestWacliLocationIsNotText(t *testing.T) {
	w := NewWacli("wacli", "shop")
	var got Inbound
	w.on = func(in Inbound) { got = in }
	w.deliver([]byte(`{"Chat":"31600000001@s.whatsapp.net","ID":"L1","Text":"location","Media":{"Type":"location","Caption":"Home"},"Location":{"Latitude":12.5,"Longitude":77.25,"Name":"Home","Address":"1 Main St"}}`))
	if !got.HasLocation() || got.Text != "" || got.LocLabel != "Home 1 Main St" || got.Lat != 12.5 {
		t.Fatalf("got %+v", got)
	}
}
