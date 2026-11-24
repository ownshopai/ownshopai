package main

import (
	"path/filepath"
	"testing"
	"time"
)

type countSender struct{ n int }

func (c *countSender) Send(Customer, Out) error { c.n++; return nil }

func TestGuardRepliesOnlyCapsAndPaces(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	inner := &countSender{}
	g := NewGuard(inner, s)
	clock := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	var slept time.Duration
	g.Now = func() time.Time { return clock }
	g.Sleep = func(d time.Duration) { slept += d; clock = clock.Add(d) }
	typing := 0
	g.Typing = func(Customer, bool) { typing++ }

	// never wrote to the shop: no chat address, nothing is sent
	if err := g.Send(Customer{Phone: "+31600000009"}, Out{Text: "hi"}); err == nil || inner.n != 0 {
		t.Fatalf("sent to a customer who never wrote first")
	}
	c := Customer{Phone: "+31600000001", JID: "31600000001@s.whatsapp.net"}
	g.Send(c, Out{Text: "first"})
	if slept < time.Second || typing != 2 {
		t.Fatalf("no typing pause: slept %v typing %d", slept, typing)
	}
	slept = 0
	g.Send(c, Out{Text: "second"})
	if slept < g.MinGap {
		t.Fatalf("second message in the same chat waited only %v", slept)
	}
	s.Set("daily_send_cap", "3")
	g.Send(c, Out{Text: "third"})
	if err := g.Send(c, Out{Text: "fourth"}); err == nil || inner.n != 3 {
		t.Fatalf("cap not enforced: sent %d", inner.n)
	}
	hs, _ := s.Handoffs()
	if len(hs) != 1 {
		t.Fatalf("cap hit not shown to the owner: %d hand-offs", len(hs))
	}
	clock = clock.Add(24 * time.Hour) // a new day resets the count
	if err := g.Send(c, Out{Text: "next day"}); err != nil || inner.n != 4 {
		t.Fatalf("next day: %v", err)
	}
}

// The local web test chat is never capped; WhatsApp and the public link still are.
func TestGuardWebTestChatNotCapped(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	inner := &countSender{}
	g := NewGuard(inner, s)
	g.Sleep = func(time.Duration) {}
	s.Set("daily_send_cap", "1")
	web := Customer{Channel: webChannel, Phone: "web:a@example.com", JID: "a@example.com"}
	for i := 0; i < 3; i++ {
		if err := g.Send(web, Out{Text: "hi"}); err != nil {
			t.Fatalf("web test reply %d: %v", i, err)
		}
	}
	pub := Customer{Channel: pubChannel, Phone: "pub:1", JID: "1"}
	g.Send(pub, Out{Text: "one"})
	if err := g.Send(pub, Out{Text: "two"}); err == nil {
		t.Fatal("public web chat not capped")
	}
	if inner.n != 4 {
		t.Fatalf("sent %d, want 4", inner.n)
	}
}
