package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Inbound is one message a customer sent to the shop on some channel.
type Inbound struct {
	Channel   string // "whatsapp", "telegram", "terminal", ...
	Account   string // which shop number or bot received it (a wacli account name, a bot username)
	SenderID  string // the address to reply to on this channel (WhatsApp JID, Telegram chat id)
	Phone     string // optional customer key (WhatsApp: +31...); empty means Channel-prefixed SenderID
	Name      string // display name
	Text      string
	Voice     bool    // a voice note arrived
	AudioPath string  // the voice note as a local file, when the channel could fetch it
	Lat, Lng  float64 // a shared location, when HasLocation
	LocLabel  string  // the place name or address sent with the location, if any
	Time      time.Time
}

// HasLocation: the message is a shared location.
func (in Inbound) HasLocation() bool { return in.Lat != 0 || in.Lng != 0 }

const mapPrefix = "https://maps.google.com/?q="

// MapLink is a map link for a point; no geocoding service is used.
func MapLink(lat, lng float64) string { return fmt.Sprintf("%s%.6f,%.6f", mapPrefix, lat, lng) }

// LocationText is how a shared location is stored as an address: the label, then the map link.
func (in Inbound) LocationText() string {
	return strings.TrimSpace(strings.TrimSpace(in.LocLabel) + " " + MapLink(in.Lat, in.Lng))
}

// Key is the unique customer key for this sender.
func (in Inbound) Key() string {
	if in.Phone != "" {
		return in.Phone
	}
	return in.Channel + ":" + in.SenderID
}

// Channel is one connected messaging account (one WhatsApp number, one Telegram bot).
// The bot never talks to a channel directly; it goes through the Hub, behind the Guard.
type Channel interface {
	Name() string    // channel kind, e.g. "whatsapp"
	Account() string // account within the kind; together with Name it is unique in the Hub
	Start(ctx context.Context, onMessage func(Inbound)) error
	Send(to, text string) error
	SendImage(to, path, caption string) error
	Status() string
}

// Typer is implemented by channels that can show "typing..." in a chat.
type Typer interface {
	Typing(to string, on bool)
}

func chanKey(name, account string) string { return name + "/" + account }

// Hub holds the running channels and routes each reply to the channel and account the
// customer (or the order) belongs to. It implements Sender.
type Hub struct {
	OnMessage func(Inbound)

	mu    sync.Mutex
	chans map[string]Channel
	stops map[string]context.CancelFunc
}

func NewHub(on func(Inbound)) *Hub {
	return &Hub{OnMessage: on, chans: map[string]Channel{}, stops: map[string]context.CancelFunc{}}
}

// Add starts ch and registers it, replacing (and stopping) any channel with the same key.
func (h *Hub) Add(ch Channel) error {
	k := chanKey(ch.Name(), ch.Account())
	h.Remove(ch.Name(), ch.Account())
	ctx, cancel := context.WithCancel(context.Background())
	on := func(in Inbound) {
		if h.OnMessage != nil {
			h.OnMessage(in)
		}
	}
	if err := ch.Start(ctx, on); err != nil {
		cancel()
		return err
	}
	h.mu.Lock()
	h.chans[k], h.stops[k] = ch, cancel
	h.mu.Unlock()
	return nil
}

// Remove stops and forgets a channel. It does not delete the channel's own session.
func (h *Hub) Remove(name, account string) {
	k := chanKey(name, account)
	h.mu.Lock()
	stop := h.stops[k]
	delete(h.chans, k)
	delete(h.stops, k)
	h.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (h *Hub) Get(name, account string) Channel {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.chans[chanKey(name, account)]
}

// List returns the channels sorted by key.
func (h *Hub) List() []Channel {
	h.mu.Lock()
	defer h.mu.Unlock()
	keys := make([]string, 0, len(h.chans))
	for k := range h.chans {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Channel, 0, len(keys))
	for _, k := range keys {
		out = append(out, h.chans[k])
	}
	return out
}

func (h *Hub) Close() {
	for _, ch := range h.List() {
		h.Remove(ch.Name(), ch.Account())
	}
}

func (h *Hub) route(c Customer) (Channel, error) {
	if c.Channel == "" {
		return nil, fmt.Errorf("%s has no channel; OwnShop AI only replies to people who wrote first", c.Phone)
	}
	ch := h.Get(c.Channel, c.Account)
	if ch == nil {
		return nil, fmt.Errorf("%s account %q is not connected", c.Channel, c.Account)
	}
	return ch, nil
}

// Send implements Sender: the reply goes out on the customer's own channel and account.
func (h *Hub) Send(c Customer, o Out) error {
	ch, err := h.route(c)
	if err != nil {
		return err
	}
	if o.Image != "" {
		return ch.SendImage(c.JID, o.Image, o.Text)
	}
	return ch.Send(c.JID, o.Text)
}

// Typing shows or clears "typing..." where the channel supports it.
func (h *Hub) Typing(c Customer, on bool) {
	if ch, err := h.route(c); err == nil {
		if t, ok := ch.(Typer); ok {
			t.Typing(c.JID, on)
		}
	}
}
