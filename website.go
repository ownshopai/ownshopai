package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Website channel: the customer web chat on a public link (see tunnel.go). It is served by its own
// listener (127.0.0.1:2008 or the next free port) that knows only the customer chat routes; the
// owner page on :2007 is never reachable through the tunnel.
//
// Sign-in is a name and a phone number or an email (at least one), no password. The identity is the BROWSER: a long-lived random
// token in an HttpOnly cookie (only its SHA-256 is stored). A browser sees only its own chat; the
// same phone typed in another browser is another customer, never merged, and never merged with a
// WhatsApp or Telegram customer. The owner sees the name, phone and email marked "web, not verified".

const (
	pubChannel = "webpub"
	pubAccount = "public"
	pubCookie  = "__Host-ownshop_pub" // __Host-: Secure, Path=/, no Domain, so a sibling *.ownshopai.store cannot plant it
	pubKeyPfx  = "webpub:"            // customer key: webpub:<id>

	pubDefaultPort   = 2008
	pubMaxText       = 1000 // characters per message
	pubMaxName       = 60
	pubCookieMaxAge  = 400 * 24 * 3600
	pubSigninPerIP   = 10 // sign-ins per IP per pubSigninWindow
	pubSigninPerBrow = 5  // sign-ins per browser (cookie) per pubSigninWindow
	pubSigninWindow  = 10 * time.Minute
	pubMsgPerBrowser = 12 // messages per browser per minute
	pubMsgPerIP      = 40 // messages per IP per minute
	pubMsgWindow     = time.Minute
)

const pubSchema = `
CREATE TABLE IF NOT EXISTS pub_users (
  id INTEGER PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, name TEXT NOT NULL, phone TEXT NOT NULL,
  created_at TEXT NOT NULL, last_seen TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS pub_messages (
  id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, ts TEXT NOT NULL, direction TEXT NOT NULL,
  text TEXT NOT NULL DEFAULT '', image TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS push_subs (
  id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, endpoint TEXT NOT NULL UNIQUE, p256dh TEXT NOT NULL,
  auth TEXT NOT NULL, created_at TEXT NOT NULL);
`

type PubUser struct {
	ID                                    int64
	Name, Phone, Email, Created, LastSeen string
	Blocked                               bool
}

// Contact is the phone and email the customer typed, for the owner pages.
func (u PubUser) Contact() string { return joinContact(u.Phone, u.Email) }

func joinContact(phone, email string) string {
	switch {
	case phone != "" && email != "":
		return phone + " · " + email
	case phone != "":
		return phone
	}
	return email
}

// cleanEmail is a loose check: one @, something before it, a dot in the domain, no spaces, 254 at most.
func cleanEmail(e string) (string, bool) {
	e = strings.TrimSpace(e)
	at := strings.LastIndex(e, "@")
	if len(e) > 254 || at < 1 || strings.Count(e, "@") != 1 || strings.ContainsAny(e, " \t\r\n<>,;\"") {
		return "", false
	}
	dom := e[at+1:]
	if i := strings.Index(dom, "."); i < 1 || i == len(dom)-1 {
		return "", false
	}
	return e, true
}

func (u PubUser) Key() string { return pubKeyPfx + strconv.FormatInt(u.ID, 10) }

// cleanPhone accepts digits with spaces, dashes, dots, brackets and one leading +; 7 to 15 digits.
func cleanPhone(p string) (string, bool) {
	p = strings.TrimSpace(p)
	var b strings.Builder
	for i, r := range p {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return "", false
		}
	}
	out := b.String()
	n := len(strings.TrimPrefix(out, "+"))
	return out, n >= 7 && n <= 15
}

func hashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// AddPubUser makes a new web customer for a new browser token and its customer row.
func (s *Store) AddPubUser(name, phone, email string) (PubUser, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > pubMaxName || strings.ContainsAny(name, "\r\n\t<>") {
		return PubUser{}, "", errors.New("type your name (up to 60 letters)")
	}
	if strings.TrimSpace(phone) == "" && strings.TrimSpace(email) == "" {
		return PubUser{}, "", errors.New("type your phone number or your email")
	}
	ph, em := "", ""
	if strings.TrimSpace(phone) != "" {
		var ok bool
		if ph, ok = cleanPhone(phone); !ok {
			return PubUser{}, "", errors.New("type your phone number: 7 to 15 digits, + allowed")
		}
	}
	if strings.TrimSpace(email) != "" {
		var ok bool
		if em, ok = cleanEmail(email); !ok {
			return PubUser{}, "", errors.New("that email does not look right")
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return PubUser{}, "", err
	}
	tok := hex.EncodeToString(b)
	t := now()
	res, err := s.DB.Exec(`INSERT INTO pub_users(token_hash, name, phone, email, created_at, last_seen) VALUES (?,?,?,?,?,?)`,
		hashToken(tok), name, ph, em, t, t)
	if err != nil {
		return PubUser{}, "", err
	}
	id, _ := res.LastInsertId()
	u := PubUser{ID: id, Name: name, Phone: ph, Email: em, Created: t, LastSeen: t}
	// the customer row exists from sign-in on, so the owner can block before the first message
	if c, err := s.Customer(u.Key(), name, strconv.FormatInt(id, 10)); err == nil {
		s.SetCustomerChannel(c.ID, pubChannel, pubAccount)
	}
	return u, tok, nil
}

func (s *Store) pubUserByToken(tok string) (PubUser, bool) {
	if tok == "" {
		return PubUser{}, false
	}
	var u PubUser
	err := s.DB.QueryRow(`SELECT id, name, phone, email, created_at, last_seen FROM pub_users WHERE token_hash=?`, hashToken(tok)).
		Scan(&u.ID, &u.Name, &u.Phone, &u.Email, &u.Created, &u.LastSeen)
	return u, err == nil
}

func (s *Store) touchPubUser(id int64) {
	s.DB.Exec(`UPDATE pub_users SET last_seen=? WHERE id=?`, now(), id)
}

// PubUsers lists the public web customers, newest first, with their block state.
func (s *Store) PubUsers() ([]PubUser, error) {
	rows, err := s.DB.Query(`SELECT u.id, u.name, u.phone, u.email, u.created_at, u.last_seen,
	  COALESCE((SELECT blocked FROM customers c WHERE c.phone='` + pubKeyPfx + `'||u.id), 0) FROM pub_users u ORDER BY u.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PubUser
	for rows.Next() {
		var u PubUser
		rows.Scan(&u.ID, &u.Name, &u.Phone, &u.Email, &u.Created, &u.LastSeen, &u.Blocked)
		out = append(out, u)
	}
	return out, rows.Err()
}

// pubPhones maps a customer key (webpub:<id>) to the phone and email the customer typed, for the owner pages.
func (s *Store) pubPhones() map[string]string {
	out := map[string]string{}
	rows, err := s.DB.Query(`SELECT id, phone, email FROM pub_users`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p, e string
		rows.Scan(&id, &p, &e)
		out[pubKeyPfx+strconv.FormatInt(id, 10)] = joinContact(p, e)
	}
	return out
}

func (s *Store) addPubMsg(user int64, dir, text, image string) int64 {
	res, err := s.DB.Exec(`INSERT INTO pub_messages(user_id, ts, direction, text, image) VALUES (?,?,?,?,?)`, user, now(), dir, text, image)
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

func (s *Store) pubMsgs(user, after int64) ([]WebMsg, error) {
	rows, err := s.DB.Query(`SELECT id, ts, direction, text, image FROM pub_messages WHERE user_id=? AND id>? ORDER BY id LIMIT 500`, user, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebMsg{}
	for rows.Next() {
		var m WebMsg
		rows.Scan(&m.ID, &m.TS, &m.Direction, &m.Text, &m.path)
		if m.path != "" {
			m.Image = "/api/image/" + strconv.FormatInt(m.ID, 10)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PubChat is the channel. SenderID is the pub_users id.
type PubChat struct {
	S    *Store
	Push func(userID int64, text string) // optional: a Web Push for a new shop reply

	mu sync.Mutex
	on func(Inbound)
}

func (p *PubChat) Name() string    { return pubChannel }
func (p *PubChat) Account() string { return pubAccount }
func (p *PubChat) Status() string  { return "customers on your public web chat link" }
func (p *PubChat) Start(_ context.Context, on func(Inbound)) error {
	p.mu.Lock()
	p.on = on
	p.mu.Unlock()
	return nil
}

func (p *PubChat) deliver(to, text, image string) error {
	id, err := strconv.ParseInt(to, 10, 64)
	if err != nil {
		return errors.New("bad web customer " + to)
	}
	p.S.addPubMsg(id, "out", text, image)
	if p.Push != nil {
		body := text
		if body == "" {
			body = "sent you a picture"
		}
		go p.Push(id, body)
	}
	return nil
}

func (p *PubChat) Send(to, text string) error               { return p.deliver(to, text, "") }
func (p *PubChat) SendImage(to, path, caption string) error { return p.deliver(to, caption, path) }

func (p *PubChat) Receive(u PubUser, text string) {
	in := Inbound{Channel: pubChannel, Account: pubAccount, SenderID: strconv.FormatInt(u.ID, 10), Phone: u.Key(),
		Name: u.Name, Text: strings.TrimSpace(text), Time: time.Now()}
	p.S.addPubMsg(u.ID, "in", in.Text, "")
	p.mu.Lock()
	on := p.on
	p.mu.Unlock()
	if on != nil {
		go on(in)
	}
}

// ---- rate limits ----

type limiter struct {
	mu   sync.Mutex
	n    int
	per  time.Duration
	m    map[string]*window
	now  func() time.Time
	seen int
}

type window struct {
	start time.Time
	count int
}

func newLimiter(n int, per time.Duration) *limiter {
	return &limiter{n: n, per: per, m: map[string]*window{}, now: time.Now}
}

func (l *limiter) allow(key string) bool {
	if key == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	if l.seen++; l.seen%1000 == 0 { // forget old windows now and then
		for k, w := range l.m {
			if t.Sub(w.start) > l.per {
				delete(l.m, k)
			}
		}
	}
	w := l.m[key]
	if w == nil || t.Sub(w.start) > l.per {
		w = &window{start: t}
		l.m[key] = w
	}
	if w.count >= l.n {
		return false
	}
	w.count++
	return true
}

// full says the key used up its window, without counting a try.
func (l *limiter) full(key string) bool {
	if key == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.m[key]
	return w != nil && l.now().Sub(w.start) <= l.per && w.count >= l.n
}

// ---- the public listener ----

type pubSite struct {
	a    *App
	host func() string // the only Host this listener answers to; "" while the link is down

	signinIP, signinBrowser, msgIP, msgBrowser *limiter
}

func newPubSite(a *App, host func() string) *pubSite {
	return &pubSite{a: a, host: host,
		signinIP: newLimiter(pubSigninPerIP, pubSigninWindow), signinBrowser: newLimiter(pubSigninPerBrow, pubSigninWindow),
		msgIP: newLimiter(pubMsgPerIP, pubMsgWindow), msgBrowser: newLimiter(pubMsgPerBrowser, pubMsgWindow)}
}

func (a *App) pubChat() *PubChat {
	if a.Hub != nil {
		if p, ok := a.Hub.Get(pubChannel, pubAccount).(*PubChat); ok {
			return p
		}
	}
	return nil
}

// clientIP is Cloudflare's CF-Connecting-IP (the listener is only reachable through cloudflared on
// this machine), else the socket address.
func clientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" && net.ParseIP(ip) != nil {
		return ip
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// guard: Host must be the current public host, a POST must come from that origin and carry JSON.
func (p *pubSite) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := p.host()
		if host == "" || !strings.EqualFold(r.Host, host) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			o := r.Header.Get("Origin")
			if u, err := url.Parse(o); o == "" || err != nil || !strings.EqualFold(u.Host, host) || u.Scheme != "https" {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "send Content-Type: application/json", http.StatusUnsupportedMediaType)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		}
		h(w, r)
	}
}

func (p *pubSite) user(r *http.Request) (PubUser, bool) {
	c, err := r.Cookie(pubCookie)
	if err != nil {
		return PubUser{}, false
	}
	return p.a.S.pubUserByToken(c.Value)
}

func (p *pubSite) auth(h func(http.ResponseWriter, *http.Request, PubUser)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := p.user(r)
		if !ok {
			jsonOut(w, 401, map[string]any{"error": "sign in first"})
			return
		}
		h(w, r, u)
	}
}

func setPubCookie(w http.ResponseWriter, tok string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: pubCookie, Value: tok, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

// Handler is everything the public link serves. Anything else is 404.
func (p *pubSite) Handler() http.Handler {
	m := http.NewServeMux()
	g := p.guard
	m.HandleFunc("GET /{$}", g(p.page))
	m.HandleFunc("GET /style.css", g(func(w http.ResponseWriter, r *http.Request) {
		b, _ := webFS.ReadFile("web/style.css")
		w.Header().Set("Content-Type", "text/css")
		w.Write(b)
	}))
	m.HandleFunc("GET /manifest.webmanifest", g(p.manifest))
	m.HandleFunc("GET /sw.js", g(p.serviceWorker))
	m.HandleFunc("GET /icon-192.png", g(p.icon))
	m.HandleFunc("GET /icon-512.png", g(p.icon))
	m.HandleFunc("GET /api/me", g(func(w http.ResponseWriter, r *http.Request) {
		u, ok := p.user(r)
		if !ok {
			jsonOut(w, 200, map[string]any{"signed_in": false})
			return
		}
		jsonOut(w, 200, map[string]any{"signed_in": true, "name": u.Name, "phone": u.Phone, "email": u.Email})
	}))
	m.HandleFunc("POST /api/signin", g(p.signin))
	m.HandleFunc("POST /api/signout", g(func(w http.ResponseWriter, r *http.Request) {
		setPubCookie(w, "", -1)
		jsonOut(w, 200, map[string]any{"ok": true})
	}))
	m.HandleFunc("GET /api/messages", g(p.auth(func(w http.ResponseWriter, r *http.Request, u PubUser) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		ms, err := p.a.S.pubMsgs(u.ID, after)
		if err != nil {
			jsonOut(w, 500, map[string]any{"error": "could not read the chat"})
			return
		}
		jsonOut(w, 200, map[string]any{"messages": ms})
	})))
	m.HandleFunc("POST /api/send", g(p.auth(p.send)))
	m.HandleFunc("GET /api/image/{id}", g(p.auth(func(w http.ResponseWriter, r *http.Request, u PubUser) {
		var path string
		p.a.S.DB.QueryRow(`SELECT image FROM pub_messages WHERE id=? AND user_id=? AND direction='out'`, r.PathValue("id"), u.ID).Scan(&path)
		if path == "" {
			http.NotFound(w, r)
			return
		}
		if _, err := os.Stat(path); err != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
	})))
	m.HandleFunc("GET /api/push/key", g(func(w http.ResponseWriter, r *http.Request) {
		pub, err := vapidPublic(p.a.S)
		if err != nil {
			jsonOut(w, 500, map[string]any{"error": "notifications are not available"})
			return
		}
		jsonOut(w, 200, map[string]any{"key": pub})
	}))
	m.HandleFunc("POST /api/push/subscribe", g(p.auth(p.subscribe)))
	m.HandleFunc("POST /api/push/unsubscribe", g(p.auth(func(w http.ResponseWriter, r *http.Request, u PubUser) {
		var body struct{ Endpoint string }
		json.NewDecoder(r.Body).Decode(&body)
		p.a.S.DB.Exec(`DELETE FROM push_subs WHERE endpoint=? AND user_id=?`, body.Endpoint, u.ID)
		jsonOut(w, 200, map[string]any{"ok": true})
	})))
	return m
}

func (p *pubSite) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
	p.a.page(w, "pubchat", map[string]any{"Theme": pubTheme, "PrivacyNote": p.a.S.Get("privacy_note")})
}

func (p *pubSite) signin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	brow := ""
	if c, err := r.Cookie(pubCookie); err == nil {
		brow = hashToken(c.Value)
	}
	if !p.signinIP.allow(ip) || !p.signinBrowser.allow(brow) {
		jsonOut(w, 429, map[string]any{"error": "Too many tries. Wait a few minutes and try again."})
		return
	}
	var body struct{ Name, Phone, Email string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonOut(w, 400, map[string]any{"error": "bad JSON"})
		return
	}
	u, tok, err := p.a.S.AddPubUser(body.Name, body.Phone, body.Email)
	if err != nil {
		jsonOut(w, 400, map[string]any{"error": err.Error()})
		return
	}
	setPubCookie(w, tok, pubCookieMaxAge)
	jsonOut(w, 200, map[string]any{"ok": true, "name": u.Name, "phone": u.Phone, "email": u.Email})
}

func (p *pubSite) send(w http.ResponseWriter, r *http.Request, u PubUser) {
	if !p.msgBrowser.allow(u.Key()) || !p.msgIP.allow(clientIP(r)) {
		jsonOut(w, 429, map[string]any{"error": "You are sending very fast. Wait a moment."})
		return
	}
	var body struct{ Text string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonOut(w, 400, map[string]any{"error": "bad JSON"})
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		jsonOut(w, 400, map[string]any{"error": "empty message"})
		return
	}
	if utf8.RuneCountInString(text) > pubMaxText {
		jsonOut(w, 400, map[string]any{"error": "That message is too long (up to 1000 characters)."})
		return
	}
	if p.a.S.isBlocked(u.Key()) {
		jsonOut(w, 403, map[string]any{"error": "This shop is not taking messages from this chat."})
		return
	}
	pc := p.a.pubChat()
	if pc == nil {
		jsonOut(w, 503, map[string]any{"error": "the shop is not answering right now"})
		return
	}
	p.a.S.touchPubUser(u.ID)
	pc.Receive(u, text)
	jsonOut(w, 200, map[string]any{"ok": true})
}

// listenPublic opens the public listener on 127.0.0.1, from port 2008 up.
func listenPublic(start int, avoid int) (net.Listener, int, error) {
	for p := start; p < start+50; p++ {
		if p == avoid {
			continue
		}
		ln, err := net.Listen("tcp", addrFor(p))
		if err == nil {
			return ln, p, nil
		}
	}
	return nil, 0, errors.New("no free port for the public web chat")
}

// startWebsite wires the Website channel into a running shop: channel, public listener, tunnel.
func (a *App) startWebsite(ownerPort int) func() {
	pc := &PubChat{S: a.S, Push: func(id int64, text string) { sendPush(a.S, nil, id, a.S.Get("shop_name"), text) }}
	if err := a.Hub.Add(pc); err != nil {
		log.Printf("website: %v", err)
	}
	ln, port, err := listenPublic(pubDefaultPort, ownerPort)
	if err != nil {
		log.Printf("website: %v", err)
		return func() {}
	}
	a.Tunnel = &Tunnel{S: a.S, Port: port}
	a.Pub = newPubSite(a, a.Tunnel.PublicHost)
	go http.Serve(ln, a.Pub.Handler())
	go a.Tunnel.Resume()
	return a.Tunnel.halt
}
