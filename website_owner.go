package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// Owner side of the Website channel: the card in Settings and in the setup "Chat channels" step.
// These routes live on the owner page (localhost only), never on the public listener.

func (a *App) websiteData(d map[string]any) {
	if a.Tunnel == nil {
		return
	}
	v := a.Tunnel.View()
	d["Web"] = v
	d["SignedIn"] = a.S.Get(setLinkAppKey) != ""
	d["LinkUp"] = a.linkServiceUp()
	if st, ok := strings.CutPrefix(a.S.Get(setLinkStatus), "disabled:"); ok {
		d["LinkOff"] = map[string]string{"admin": "it was turned off by the link service team", "banned": "the account is blocked",
			"inactive": "it was not used for a long time"}[st]
		if d["LinkOff"] == "" {
			d["LinkOff"] = "it is turned off"
		}
	}
	d["LinkDomain"] = linkDomain
	if v.State == "starting" {
		d["Refresh"] = true
	}
}

// restartTunnel applies a changed mode when the link is on.
func (a *App) restartTunnel() error {
	if a.S.Get(setWebPublic) != "on" {
		return nil
	}
	a.Tunnel.halt()
	return a.Tunnel.Start()
}

func (a *App) websiteRoutes(m *http.ServeMux) {
	need := func(h func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// owner page only: no other host name, and no form posted from another web site
			if !localHost(r.Host) {
				http.Error(w, "the shop page is served on this computer only", http.StatusForbidden)
				return
			}
			if o := r.Header.Get("Origin"); o != "" && o != "null" {
				if u, err := url.Parse(o); err != nil || !localHost(u.Host) {
					http.Error(w, "forbidden origin", http.StatusForbidden)
					return
				}
			} else if o == "null" {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
			if a.Tunnel == nil {
				back(w, r, ret(r)+"#website", "The website channel is not running in this mode.")
				return
			}
			h(w, r)
		}
	}
	backTo := func(w http.ResponseWriter, r *http.Request, msg string) {
		http.Redirect(w, r, ret(r)+"?msg="+urlQ(msg)+"#website", http.StatusSeeOther)
	}
	m.HandleFunc("POST /channels/website/start", need(func(w http.ResponseWriter, r *http.Request) {
		if err := a.Tunnel.Start(); err != nil {
			backTo(w, r, "Not started: "+err.Error())
			return
		}
		time.Sleep(4 * time.Second) // most links are ready by now; the page refreshes until it is
		backTo(w, r, "Starting your public web chat link.")
	}))
	m.HandleFunc("POST /channels/website/stop", need(func(w http.ResponseWriter, r *http.Request) {
		a.Tunnel.Stop()
		backTo(w, r, "Your public web chat link is off.")
	}))
	m.HandleFunc("POST /channels/website/check", need(func(w http.ResponseWriter, r *http.Request) {
		a.Tunnel.Detect()
		if a.Tunnel.View().Found {
			backTo(w, r, "Found Cloudflare's tunnel program.")
			return
		}
		backTo(w, r, "Still not found. Install it, then press Check again.")
	}))
	m.HandleFunc("GET /channels/website/qr.png", need(func(w http.ResponseWriter, r *http.Request) {
		u := a.Tunnel.URL()
		if u == "" {
			http.NotFound(w, r)
			return
		}
		png, err := qrcode.Encode(u, qrcode.Medium, 260)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(png)
	}))
	// Free fixed link: register, verify, claim, release.
	m.HandleFunc("GET /channels/website/available", need(func(w http.ResponseWriter, r *http.Request) {
		name := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
		if !validLinkName(name) {
			jsonOut(w, 200, map[string]any{"available": false, "reason": "3 to 30 small letters, digits and dashes"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		ok, why, err := a.Links.Available(ctx, name)
		if err != nil {
			jsonOut(w, 200, map[string]any{"available": false, "reason": friendly(err).Error()})
			return
		}
		why = map[string]string{"invalid_name": "not a valid name", "reserved_name": "reserved", "name_taken": "taken"}[why]
		jsonOut(w, 200, map[string]any{"available": ok, "reason": why, "hostname": name + "." + linkDomain})
	}))
	m.HandleFunc("POST /channels/website/signin", need(func(w http.ResponseWriter, r *http.Request) {
		if !localHost(r.Host) {
			http.Error(w, "sign in from the shop page on this computer", http.StatusForbidden)
			return
		}
		u := StartLinkLogin(a.S, a.Links, "http://"+r.Host+"/website/linked")
		if err := openBrowser(u); err != nil {
			http.Redirect(w, r, u, http.StatusSeeOther) // no browser to open: go there in this tab
			return
		}
		backTo(w, r, "Finish signing in in the browser window that opened, then come back here.")
	}))
	// The browser comes back here from the link service. Owner page only (localhost).
	m.HandleFunc("GET /website/linked", need(func(w http.ResponseWriter, r *http.Request) {
		if !localHost(r.Host) {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		q := r.URL.Query()
		if err := FinishLinkLogin(ctx, a.S, a.Links, q.Get("code"), q.Get("state")); err != nil {
			http.Redirect(w, r, "/settings?msg="+urlQ("Not signed in: "+err.Error())+"#website", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/settings?msg="+urlQ("Signed in. Now pick the name for your free link.")+"#website", http.StatusSeeOther)
	}))
	m.HandleFunc("POST /channels/website/signout", need(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		msg := "Signed out of the link service. Your link keeps working."
		if err := SignOutLink(ctx, a.S, a.Links); err != nil {
			msg += " (The service could not be told: " + err.Error() + ")"
		}
		backTo(w, r, msg)
	}))
	m.HandleFunc("POST /channels/website/claim", need(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		host, err := ClaimLink(ctx, a.S, a.Links, r.FormValue("name"))
		if err != nil {
			backTo(w, r, "No link yet: "+err.Error())
			return
		}
		a.Tunnel.halt()
		if err := a.Tunnel.Start(); err != nil {
			backTo(w, r, "Your link is https://"+host+" but it did not start: "+err.Error())
			return
		}
		time.Sleep(3 * time.Second)
		backTo(w, r, "Your link is https://"+host+". It stays the same after a restart.")
	}))
	m.HandleFunc("POST /channels/website/rotate", need(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := RotateLink(ctx, a.S, a.Links); err != nil {
			backTo(w, r, "Not changed: "+err.Error())
			return
		}
		a.restartTunnel()
		backTo(w, r, "Your link has a new tunnel key; the old one no longer works. The link itself is the same.")
	}))
	m.HandleFunc("POST /channels/website/release", need(func(w http.ResponseWriter, r *http.Request) {
		a.Tunnel.Stop()
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		msg := "Your free link is released. Someone else can take the name now."
		if err := ReleaseLink(ctx, a.S, a.Links); err != nil {
			msg += " (The link service could not be told: " + err.Error() + ". It frees unused names on its own.)"
		}
		backTo(w, r, msg)
	}))
}

func urlQ(s string) string { return url.QueryEscape(s) }

var linkUpCache struct {
	sync.Mutex
	up bool
	at time.Time
}

// linkServiceUp says whether the link service answers (GET /up, short timeout, remembered 30 s).
// While it does not, the card says "Coming soon".
func (a *App) linkServiceUp() bool {
	if a.Links == nil {
		return false
	}
	linkUpCache.Lock()
	defer linkUpCache.Unlock()
	if !linkUpCache.at.IsZero() && time.Since(linkUpCache.at) < 30*time.Second {
		return linkUpCache.up
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	linkUpCache.up, linkUpCache.at = a.Links.Up(ctx), time.Now()
	return linkUpCache.up
}
