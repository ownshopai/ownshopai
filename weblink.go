package main

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ownshopai/ownshopai/relay"
)

// The Website channel puts the customer web chat on the free link <name>.ownshopai.store. The shop
// keeps ONE outbound connection to the OwnShop link service (package relay); the service relays each
// visitor request over it. No inbound port, no extra program, works behind NAT and on mobile data.
// The relay serves exactly the public listener's handler (pubSite.Handler, website.go), never the
// owner page.

const (
	setWebPublic = "web_public" // "on" while the owner wants the link up (restarted on app start)
	linkRelayURL = "wss://linksapi.ownshopai.store/v1/relay"
)

// WebLink runs at most one relay client.
type WebLink struct {
	S        *Store
	Port     int          // the public listener's port (for the card)
	Public   http.Handler // the public handler the relay serves (the same one as the :2008 listener)
	RelayURL string       // "" means linkRelayURL

	mu     sync.Mutex
	c      *relay.Client
	status relay.Status
	host   string
}

// WebLinkView is what the Website card and /relay/status show. The app key is never in it.
type WebLinkView struct {
	State, URL, Err string // State: off, starting, on, error
	LinkHost, Port  string
	Connected       bool
	LastConnected   string
	Name            string
}

func (t *WebLink) View() WebLinkView {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := WebLinkView{State: "off", Port: strconv.Itoa(t.Port), LinkHost: t.S.Get(setLinkHost), Name: t.S.Get(setLinkName)}
	if t.c != nil {
		switch t.status.State {
		case relay.Online:
			v.State, v.Connected = "on", true
		case relay.Stopped:
			v.State = "error"
		default:
			v.State = "starting"
		}
		v.Err = t.status.LastErr
		if v.State == "on" {
			v.URL = "https://" + t.host
		}
	}
	if !t.status.LastConnected.IsZero() {
		v.LastConnected = t.status.LastConnected.UTC().Format(time.RFC3339)
	}
	return v
}

// URL is the public link while the relay is connected.
func (t *WebLink) URL() string { return t.View().URL }

// Start connects the relay and remembers that the owner wants it on.
func (t *WebLink) Start() error {
	t.S.Set(setWebPublic, "on")
	name, host, key := t.S.Get(setLinkName), t.S.Get(setLinkHost), t.S.Get(setLinkAppKey)
	if name == "" || host == "" {
		return errString("get your free link first")
	}
	if key == "" {
		return errString("sign in with Google first")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.c != nil && t.c.Status().State != relay.Stopped {
		return nil
	}
	// a client stopped by a refusal (401/403/404) is single-use: dial again with a new one
	u := t.RelayURL
	if u == "" {
		u = linkRelayFor()
	}
	var c *relay.Client
	c = relay.New(relay.Config{URL: u, Name: name, Handler: t.Public,
		AppKey: func() string { return t.S.Get(setLinkAppKey) },
		OnState: func(s relay.Status) {
			t.mu.Lock()
			if t.c == c {
				t.status = s
			}
			t.mu.Unlock()
			if s.State == relay.Online {
				log.Printf("website: public chat link https://%s is online", host)
			} else if s.State == relay.Stopped && s.LastErr != "" {
				log.Printf("website: link relay stopped: %s", s.LastErr)
			}
		}})
	t.c, t.host, t.status = c, host, relay.Status{State: relay.Connecting}
	c.Start(context.Background())
	return nil
}

// Stop disconnects and remembers that the owner wants it off.
func (t *WebLink) Stop() {
	t.S.Set(setWebPublic, "")
	t.halt()
}

// halt disconnects without changing the owner's choice (app exit, start over).
func (t *WebLink) halt() {
	t.mu.Lock()
	c := t.c
	t.c = nil
	t.mu.Unlock()
	if c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c.Stop(ctx)
		cancel()
	}
}

// Kick redials now (e.g. the network changed).
func (t *WebLink) Kick() {
	t.mu.Lock()
	c := t.c
	t.mu.Unlock()
	if c != nil {
		c.Kick()
	}
}

// Resume connects on app start when the link was on.
func (t *WebLink) Resume() {
	if t.S.Get(setWebPublic) == "on" {
		if err := t.Start(); err != nil {
			log.Printf("website: %v", err)
		}
	}
}

// restart re-dials with the current settings (after a new claim or sign-in).
func (t *WebLink) restart() error {
	t.halt()
	return t.Start()
}

type errString string

func (e errString) Error() string { return string(e) }
