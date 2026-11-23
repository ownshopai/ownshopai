package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	webpush "github.com/SherClockHolmes/webpush-go"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/math/fixed"
)

// The public chat is an installable web app (PWA): a manifest, a service worker that keeps the app
// shell for a fast and offline open, generated icons, and Web Push for new shop replies while the tab
// is closed. Push needs https, so it only works on the public link; on iPhone it needs the app added
// to the home screen (iOS 16.4 or later). VAPID keys are made on this computer and kept in the
// settings table, never printed.

const pubTheme = "#1f6f50"

const (
	setVapidPrivate = "vapid_private" // secret
	setVapidPublic  = "vapid_public"
)

var vapidMu sync.Mutex

// vapidKeys returns the shop's VAPID key pair, making it on first use.
func vapidKeys(s *Store) (priv, pub string, err error) {
	vapidMu.Lock()
	defer vapidMu.Unlock()
	priv, pub = s.Get(setVapidPrivate), s.Get(setVapidPublic)
	if priv != "" && pub != "" {
		return priv, pub, nil
	}
	priv, pub, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", err
	}
	s.Set(setVapidPrivate, priv)
	s.Set(setVapidPublic, pub)
	return priv, pub, nil
}

func vapidPublic(s *Store) (string, error) {
	_, pub, err := vapidKeys(s)
	return pub, err
}

type pushSub struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// SavePushSub stores a browser's push subscription for a web customer (one row per endpoint).
func (s *Store) SavePushSub(user int64, sub pushSub) error {
	if !strings.HasPrefix(sub.Endpoint, "https://") || len(sub.Endpoint) > 1000 || sub.Keys.P256dh == "" || sub.Keys.Auth == "" ||
		len(sub.Keys.P256dh) > 200 || len(sub.Keys.Auth) > 100 {
		return errors.New("bad subscription")
	}
	_, err := s.DB.Exec(`INSERT INTO push_subs(user_id, endpoint, p256dh, auth, created_at) VALUES (?,?,?,?,?)
	  ON CONFLICT(endpoint) DO UPDATE SET user_id=excluded.user_id, p256dh=excluded.p256dh, auth=excluded.auth`,
		user, sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth, now())
	return err
}

func (s *Store) pushSubs(user int64) []pushSub {
	rows, err := s.DB.Query(`SELECT endpoint, p256dh, auth FROM push_subs WHERE user_id=?`, user)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []pushSub
	for rows.Next() {
		var p pushSub
		rows.Scan(&p.Endpoint, &p.Keys.P256dh, &p.Keys.Auth)
		out = append(out, p)
	}
	return out
}

func (p *pubSite) subscribe(w http.ResponseWriter, r *http.Request, u PubUser) {
	var sub pushSub
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		jsonOut(w, 400, map[string]any{"error": "bad JSON"})
		return
	}
	if err := p.a.S.SavePushSub(u.ID, sub); err != nil {
		jsonOut(w, 400, map[string]any{"error": err.Error()})
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true})
}

// sendPush notifies every browser of a web customer; a subscription the push service says is gone
// (404 or 410) is deleted. hc nil means the default client.
func sendPush(s *Store, hc webpush.HTTPClient, user int64, title, body string) {
	subs := s.pushSubs(user)
	if len(subs) == 0 {
		return
	}
	priv, pub, err := vapidKeys(s)
	if err != nil {
		return
	}
	if utf8.RuneCountInString(body) > 140 {
		body = string([]rune(body)[:140]) + "..."
	}
	if title == "" {
		title = "New message"
	}
	msg, _ := json.Marshal(map[string]string{"title": title, "body": body})
	for _, sub := range subs {
		ws := &webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{P256dh: sub.Keys.P256dh, Auth: sub.Keys.Auth}}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		res, err := webpush.SendNotificationWithContext(ctx, msg, ws, &webpush.Options{HTTPClient: hc,
			Subscriber: "https://ownshopai.store", VAPIDPublicKey: pub, VAPIDPrivateKey: priv, TTL: 24 * 3600, Urgency: webpush.UrgencyNormal})
		cancel()
		if err != nil {
			log.Printf("website: push failed")
			continue
		}
		res.Body.Close()
		if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone {
			s.DB.Exec(`DELETE FROM push_subs WHERE endpoint=?`, sub.Endpoint)
		}
	}
}

// ---- manifest, service worker, icons ----

func (p *pubSite) manifest(w http.ResponseWriter, r *http.Request) {
	name := p.a.S.Get("shop_name")
	if name == "" {
		name = "Shop chat"
	}
	short := name
	if utf8.RuneCountInString(short) > 12 {
		short = string([]rune(short)[:12])
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	json.NewEncoder(w).Encode(map[string]any{
		"name": name, "short_name": short, "start_url": "/", "scope": "/", "display": "standalone",
		"background_color": "#ffffff", "theme_color": pubTheme, "description": "Chat with " + name,
		"icons": []map[string]string{
			{"src": "/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any maskable"},
			{"src": "/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any maskable"},
		},
	})
}

// swVersion changes with each release so the browser picks up a new app shell.
const swVersion = "ownshop-pub-v1"

const serviceWorker = `const C = '` + swVersion + `';
const SHELL = ['/', '/style.css', '/manifest.webmanifest', '/icon-192.png', '/icon-512.png'];
self.addEventListener('install', e => { e.waitUntil(caches.open(C).then(c => c.addAll(SHELL)).then(() => self.skipWaiting())); });
self.addEventListener('activate', e => { e.waitUntil(caches.keys().then(ks => Promise.all(ks.filter(k => k !== C).map(k => caches.delete(k)))).then(() => self.clients.claim())); });
self.addEventListener('fetch', e => {
  const u = new URL(e.request.url);
  if (e.request.method !== 'GET' || u.origin !== location.origin || u.pathname.startsWith('/api/')) return;
  // network first, so the shop is always fresh; the cached shell when offline
  e.respondWith(fetch(e.request).then(r => { if (r.ok) { const cp = r.clone(); caches.open(C).then(c => c.put(e.request, cp)); } return r; })
    .catch(() => caches.match(e.request).then(m => m || caches.match('/'))));
});
self.addEventListener('push', e => {
  let d = {}; try { d = e.data.json(); } catch (_) {}
  e.waitUntil(self.registration.showNotification(d.title || 'New message', {body: d.body || '', icon: '/icon-192.png', badge: '/icon-192.png', tag: 'shop-reply'}));
});
self.addEventListener('notificationclick', e => {
  e.notification.close();
  e.waitUntil(self.clients.matchAll({type: 'window'}).then(ws => { for (const w of ws) { if ('focus' in w) return w.focus(); } return self.clients.openWindow('/'); }));
});
`

func (p *pubSite) serviceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(serviceWorker))
}

var iconCache sync.Map // "size|initial" -> []byte

func (p *pubSite) icon(w http.ResponseWriter, r *http.Request) {
	size := 0
	switch r.URL.Path {
	case "/icon-192.png":
		size = 192
	case "/icon-512.png":
		size = 512
	default:
		http.NotFound(w, r)
		return
	}
	b := shopIcon(p.a.S.Get("shop_name"), size)
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(b)
}

// shopIcon is a square in the theme colour with the shop's first letter (a chat mark when there is none).
func shopIcon(name string, size int) []byte {
	initial := ""
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			initial = strings.ToUpper(string(r))
			break
		}
	}
	key := strconv.Itoa(size) + "|" + initial
	if b, ok := iconCache.Load(key); ok {
		return b.([]byte)
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{0x1f, 0x6f, 0x50, 0xff}}, image.Point{}, draw.Src)
	white := &image.Uniform{color.White}
	if initial == "" {
		// a simple speech bubble
		m := size / 4
		draw.Draw(img, image.Rect(m, m, size-m, size-m-size/8), white, image.Point{}, draw.Src)
		draw.Draw(img, image.Rect(m+size/10, size-m-size/8, m+size/4, size-m), white, image.Point{}, draw.Src)
	} else {
		f := face(gobold.TTF, float64(size)*0.55)
		d := &font.Drawer{Dst: img, Src: white, Face: f}
		adv := d.MeasureString(initial)
		met := f.Metrics()
		h := met.Ascent + met.Descent
		d.Dot = fixed.Point26_6{X: (fixed.I(size) - adv) / 2, Y: (fixed.I(size)+h)/2 - met.Descent}
		d.DrawString(initial)
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	iconCache.Store(key, buf.Bytes())
	return buf.Bytes()
}
