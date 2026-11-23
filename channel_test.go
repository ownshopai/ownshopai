package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// capture is the fake channel: it records what the shop sends through the Hub.
type capture struct {
	name, account string
	outs          []Out
	to            []string
	typing        int
}

func (c *capture) Name() string                               { return c.name }
func (c *capture) Account() string                            { return c.account }
func (c *capture) Start(context.Context, func(Inbound)) error { return nil }
func (c *capture) Status() string                             { return "fake" }
func (c *capture) Typing(string, bool)                        { c.typing++ }
func (c *capture) Send(to, text string) error {
	c.to, c.outs = append(c.to, to), append(c.outs, Out{Text: text})
	return nil
}
func (c *capture) SendImage(to, path, caption string) error {
	c.to, c.outs = append(c.to, to), append(c.outs, Out{Text: caption, Image: path})
	return nil
}

func TestHubRoutesToCustomersOwnChannelAndAccount(t *testing.T) {
	a := &capture{name: "whatsapp", account: "shop1"}
	b := &capture{name: "whatsapp", account: "shop2"}
	tg := &capture{name: "telegram", account: "42"}
	h := NewHub(nil)
	for _, c := range []*capture{a, b, tg} {
		if err := h.Add(c); err != nil {
			t.Fatal(err)
		}
	}
	h.Send(Customer{Phone: "+31", JID: "31@s.whatsapp.net", Channel: "whatsapp", Account: "shop2"}, Out{Text: "hi"})
	h.Send(Customer{Phone: "tg:7", JID: "7", Channel: "telegram", Account: "42"}, Out{Text: "pay", Image: "qr.png"})
	h.Typing(Customer{JID: "7", Channel: "telegram", Account: "42"}, true)
	if len(a.outs) != 0 || len(b.outs) != 1 || b.to[0] != "31@s.whatsapp.net" {
		t.Fatalf("whatsapp routing: a=%v b=%v", a.outs, b.outs)
	}
	if len(tg.outs) != 1 || tg.outs[0].Image != "qr.png" || tg.typing != 1 {
		t.Fatalf("telegram routing: %+v typing %d", tg.outs, tg.typing)
	}
	if err := h.Send(Customer{Phone: "+1", JID: "x"}, Out{Text: "no channel"}); err == nil {
		t.Fatal("sent to a customer with no channel")
	}
	h.Remove("whatsapp", "shop2")
	if err := h.Send(Customer{JID: "x", Channel: "whatsapp", Account: "shop2"}, Out{Text: "x"}); err == nil {
		t.Fatal("sent on a removed account")
	}
}

func TestOrderRepliesGoBackOnTheOrdersChannel(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.Customer("+31600000001", "Emma", "31600000001@s.whatsapp.net")
	s.SetCustomerChannel(c.ID, "whatsapp", "shop2")
	o, _ := s.EnsureCart(c.ID)
	// the customer later writes from another number of the shop; the order keeps its own
	s.SetCustomerChannel(c.ID, "whatsapp", "shop1")
	o, _ = s.Order(o.ID)
	if o.Customer.Channel != "whatsapp" || o.Customer.Account != "shop2" {
		t.Fatalf("order channel %q/%q", o.Customer.Channel, o.Customer.Account)
	}
}

func TestGuardIsPerChannelAccount(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	inner := &countSender{}
	g := NewGuard(inner, s)
	g.Sleep = func(time.Duration) {}
	s.Set("daily_send_cap", "1")
	a := Customer{Phone: "+31", JID: "j", Channel: "whatsapp", Account: "shop1"}
	b := Customer{Phone: "tg:1", JID: "1", Channel: "telegram", Account: "42"}
	if g.Send(a, Out{Text: "1"}) != nil || g.Send(b, Out{Text: "1"}) != nil {
		t.Fatal("each channel account has its own cap")
	}
	if g.Send(a, Out{Text: "2"}) == nil || inner.n != 2 {
		t.Fatalf("cap per account not enforced: %d", inner.n)
	}
}
