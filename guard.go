package main

import (
	"fmt"
	"strconv"
	"sync"
	"time"
)

// Guard sits in front of every chat channel (per channel, account and chat) and keeps sending slow, personal and small:
//   - it only reaches customers who wrote to the shop first (a customer without a chat address
//     from an inbound message cannot be sent to; there is no broadcast or bulk feature);
//   - at most one message per chat every MinGap, with a short typing pause before each message;
//   - a daily send cap per channel account (Settings > daily_send_cap). When the cap is reached nothing more is sent
//     that day and the owner sees it under Hand-offs.
type Guard struct {
	Inner  Sender
	S      *Store
	Typing func(c Customer, on bool) // optional: show "typing..." in the chat
	Now    func() time.Time
	Sleep  func(time.Duration)
	MinGap time.Duration

	mu   sync.Mutex
	last map[string]time.Time
}

const defaultDailyCap = 200

func NewGuard(inner Sender, s *Store) *Guard {
	return &Guard{Inner: inner, S: s, Now: time.Now, Sleep: time.Sleep, MinGap: 2 * time.Second, last: map[string]time.Time{}}
}

func (g *Guard) DailyCap() int {
	if n, err := strconv.Atoi(g.S.Get("daily_send_cap")); err == nil && n >= 0 {
		return n
	}
	return defaultDailyCap
}

// sentToday counts per channel and account: each shop number or bot has its own cap.
func (g *Guard) sentToday(c Customer) (string, int) {
	key := "sent_" + g.Now().Format("2006-01-02")
	if c.Channel != "" {
		key += "_" + c.Channel + "_" + c.Account
	}
	n, _ := strconv.Atoi(g.S.Get(key))
	return key, n
}

// typingDelay is a human-like pause: about 25 ms per character, 1 to 4 seconds.
func typingDelay(text string) time.Duration {
	d := time.Duration(len(text)) * 25 * time.Millisecond
	return min(max(d, time.Second), 4*time.Second)
}

func (g *Guard) Send(c Customer, o Out) error {
	if c.JID == "" {
		return fmt.Errorf("%s never wrote to the shop; OwnShop AI only replies", c.Phone)
	}
	g.mu.Lock()
	key, n := g.sentToday(c)
	if n >= g.DailyCap() {
		g.mu.Unlock()
		g.S.Handoff(c.Phone, o.Text, "not sent: daily send cap reached (Settings)")
		return fmt.Errorf("daily send cap of %d reached", g.DailyCap())
	}
	g.S.Set(key, strconv.Itoa(n+1))
	wait := time.Duration(0)
	chat := c.Channel + "|" + c.Account + "|" + c.Phone
	if t, ok := g.last[chat]; ok {
		wait = t.Add(g.MinGap).Sub(g.Now())
	}
	g.last[chat] = g.Now().Add(max(wait, 0) + typingDelay(o.Text))
	g.mu.Unlock()
	if c.Channel == webChannel || c.Channel == pubChannel {
		return g.Inner.Send(c, o) // web chats need no human pacing (the cap above still counts)
	}

	if wait > 0 {
		g.Sleep(wait)
	}
	if g.Typing != nil {
		g.Typing(c, true)
	}
	g.Sleep(typingDelay(o.Text))
	if g.Typing != nil {
		g.Typing(c, false)
	}
	return g.Inner.Send(c, o)
}
