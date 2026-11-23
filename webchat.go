package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Web chat: a test customer channel in the browser at /webchat, on this computer only. A test
// customer logs in with a username and password the owner made on the Users page; messages go
// through the same Hub and Bot as WhatsApp and Telegram, and replies land in that user's chat.

const (
	webChannel = "web"
	webAccount = "local"
	webCookie  = "ownshop_webchat"
)

const webSchema = `
CREATE TABLE IF NOT EXISTS web_users (
  id INTEGER PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS web_messages (
  id INTEGER PRIMARY KEY, username TEXT NOT NULL, ts TEXT NOT NULL, direction TEXT NOT NULL,
  text TEXT NOT NULL DEFAULT '', image TEXT NOT NULL DEFAULT '');
`

type WebUser struct {
	ID                             int64
	Username, DisplayName, Created string
}

func validWebUsername(u string) bool {
	if u == "" || len(u) > 40 {
		return false
	}
	for _, r := range u {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func (s *Store) AddWebUser(username, password, display string) error {
	username = strings.TrimSpace(username)
	if !validWebUsername(username) {
		return errors.New("username: letters, digits, dot, dash or underscore, up to 40")
	}
	if password == "" {
		return errors.New("password is empty")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if strings.TrimSpace(display) == "" {
		display = username
	}
	_, err = s.DB.Exec(`INSERT INTO web_users(username, password_hash, display_name, created_at) VALUES (?,?,?,?)`,
		username, string(h), strings.TrimSpace(display), now())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return errors.New("username " + username + " already exists")
	}
	return err
}

func (s *Store) WebUsers() ([]WebUser, error) {
	rows, err := s.DB.Query(`SELECT id, username, display_name, created_at FROM web_users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebUser
	for rows.Next() {
		var u WebUser
		rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Created)
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteWebUser removes a test customer by id or username, with their web chat messages.
func (s *Store) DeleteWebUser(idOrName string) error {
	res, err := s.DB.Exec(`DELETE FROM web_users WHERE username=? OR CAST(id AS TEXT)=?`, idOrName, idOrName)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no web user " + idOrName)
	}
	s.DB.Exec(`DELETE FROM web_messages WHERE username NOT IN (SELECT username FROM web_users)`)
	return nil
}

// WebLogin checks a username and password and returns the user.
func (s *Store) WebLogin(username, password string) (WebUser, error) {
	var u WebUser
	var hash string
	err := s.DB.QueryRow(`SELECT id, username, display_name, created_at, password_hash FROM web_users WHERE username=?`,
		strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Created, &hash)
	if err == nil {
		err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	}
	if err != nil {
		return WebUser{}, errors.New("wrong username or password")
	}
	return u, nil
}

func (s *Store) webUser(username string) (WebUser, bool) {
	var u WebUser
	err := s.DB.QueryRow(`SELECT id, username, display_name, created_at FROM web_users WHERE username=?`, username).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Created)
	return u, err == nil
}

type WebMsg struct {
	ID        int64  `json:"id"`
	TS        string `json:"ts"`
	Direction string `json:"direction"` // "in" from the customer, "out" from the shop
	Text      string `json:"text"`
	Image     string `json:"image,omitempty"` // URL of the image, when there is one
	path      string
}

func (s *Store) addWebMsg(user, dir, text, image string) int64 {
	res, err := s.DB.Exec(`INSERT INTO web_messages(username, ts, direction, text, image) VALUES (?,?,?,?,?)`,
		user, now(), dir, text, image)
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

func (s *Store) webMsgs(user string, after int64) ([]WebMsg, error) {
	rows, err := s.DB.Query(`SELECT id, ts, direction, text, image FROM web_messages WHERE username=? AND id>? ORDER BY id LIMIT 500`, user, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebMsg{}
	for rows.Next() {
		var m WebMsg
		rows.Scan(&m.ID, &m.TS, &m.Direction, &m.Text, &m.path)
		if m.path != "" {
			m.Image = "/webchat/image/" + strconv.FormatInt(m.ID, 10)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// WebChat is the channel. SenderID is the web username; the customer key is "web:<username>".
type WebChat struct {
	S  *Store
	mu sync.Mutex
	on func(Inbound)
}

func (w *WebChat) Name() string    { return webChannel }
func (w *WebChat) Account() string { return webAccount }
func (w *WebChat) Status() string  { return "test customers at /webchat (this computer only)" }
func (w *WebChat) Start(_ context.Context, on func(Inbound)) error {
	w.mu.Lock()
	w.on = on
	w.mu.Unlock()
	return nil
}
func (w *WebChat) Send(to, text string) error {
	w.S.addWebMsg(to, "out", text, "")
	return nil
}
func (w *WebChat) SendImage(to, path, caption string) error {
	w.S.addWebMsg(to, "out", caption, path)
	return nil
}

// Receive stores what the customer typed and hands it to the bot (in the background, like a real channel).
func (w *WebChat) Receive(u WebUser, text string, lat, lng float64) {
	in := Inbound{Channel: webChannel, Account: webAccount, SenderID: u.Username, Phone: "web:" + u.Username,
		Name: u.DisplayName, Text: strings.TrimSpace(text), Lat: lat, Lng: lng, Time: time.Now()}
	shown := in.Text
	if in.HasLocation() {
		shown = "[location] " + in.LocationText()
	}
	w.S.addWebMsg(u.Username, "in", shown, "")
	w.mu.Lock()
	on := w.on
	w.mu.Unlock()
	if on != nil {
		go on(in)
	}
}

// ---- HTTP: /webchat and its API, localhost only ----

type webSessions struct {
	mu sync.Mutex
	m  map[string]string // token -> username
}

func (a *App) webChat() *WebChat {
	if a.Hub != nil {
		if w, ok := a.Hub.Get(webChannel, webAccount).(*WebChat); ok {
			return w
		}
	}
	return nil
}

// localOnly refuses requests that arrived under another host name or from a page on another site.
func localOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !localHost(r.Host) {
			http.Error(w, "web chat is served on this computer only", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || !localHost(u.Host) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		if r.Method == http.MethodPost {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "send Content-Type: application/json", http.StatusUnsupportedMediaType)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		h(w, r)
	}
}

func (a *App) webchatRoutes(m *http.ServeMux) {
	a.webSess = &webSessions{m: map[string]string{}}
	m.HandleFunc("GET /webchat", localOnly(func(w http.ResponseWriter, r *http.Request) {
		u, ok := a.webSession(r)
		a.page(w, "webchat", map[string]any{"User": u, "In": ok})
	}))
	m.HandleFunc("POST /webchat/login", localOnly(a.webLogin))
	m.HandleFunc("POST /webchat/logout", localOnly(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(webCookie); err == nil {
			a.webSess.mu.Lock()
			delete(a.webSess.m, c.Value)
			a.webSess.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: webCookie, Value: "", Path: "/webchat", MaxAge: -1})
		jsonOut(w, 200, map[string]any{"ok": true})
	}))
	m.HandleFunc("GET /webchat/messages", localOnly(a.webAuth(func(w http.ResponseWriter, r *http.Request, u WebUser) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		ms, err := a.S.webMsgs(u.Username, after)
		if err != nil {
			jsonOut(w, 500, map[string]any{"error": err.Error()})
			return
		}
		jsonOut(w, 200, map[string]any{"messages": ms})
	})))
	m.HandleFunc("POST /webchat/send", localOnly(a.webAuth(func(w http.ResponseWriter, r *http.Request, u WebUser) {
		var body struct {
			Text string  `json:"text"`
			Lat  float64 `json:"lat"`
			Lng  float64 `json:"lng"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonOut(w, 400, map[string]any{"error": "bad JSON"})
			return
		}
		if strings.TrimSpace(body.Text) == "" && body.Lat == 0 && body.Lng == 0 {
			jsonOut(w, 400, map[string]any{"error": "empty message"})
			return
		}
		wc := a.webChat()
		if wc == nil {
			jsonOut(w, 503, map[string]any{"error": "web chat channel is not running"})
			return
		}
		wc.Receive(u, body.Text, body.Lat, body.Lng)
		jsonOut(w, 200, map[string]any{"ok": true})
	})))
	m.HandleFunc("GET /webchat/image/{id}", localOnly(a.webAuth(func(w http.ResponseWriter, r *http.Request, u WebUser) {
		var path string
		a.S.DB.QueryRow(`SELECT image FROM web_messages WHERE id=? AND username=?`, r.PathValue("id"), u.Username).Scan(&path)
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
}

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (a *App) webLogin(w http.ResponseWriter, r *http.Request) {
	var body struct{ Username, Password string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonOut(w, 400, map[string]any{"error": "bad JSON"})
		return
	}
	u, err := a.S.WebLogin(body.Username, body.Password)
	if err != nil {
		jsonOut(w, 401, map[string]any{"error": err.Error()})
		return
	}
	b := make([]byte, 24)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	a.webSess.mu.Lock()
	a.webSess.m[tok] = u.Username
	a.webSess.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: webCookie, Value: tok, Path: "/webchat", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	jsonOut(w, 200, map[string]any{"ok": true, "username": u.Username, "display_name": u.DisplayName})
}

// webSession is the logged-in user; a deleted user's session no longer counts.
func (a *App) webSession(r *http.Request) (WebUser, bool) {
	c, err := r.Cookie(webCookie)
	if err != nil {
		return WebUser{}, false
	}
	a.webSess.mu.Lock()
	name, ok := a.webSess.m[c.Value]
	a.webSess.mu.Unlock()
	if !ok {
		return WebUser{}, false
	}
	return a.S.webUser(name)
}

func (a *App) webAuth(h func(http.ResponseWriter, *http.Request, WebUser)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := a.webSession(r)
		if !ok {
			jsonOut(w, 401, map[string]any{"error": "log in first"})
			return
		}
		h(w, r, u)
	}
}

// ---- owner side: the Users page ----

func (a *App) usersPage(w http.ResponseWriter, r *http.Request) {
	wu, _ := a.S.WebUsers()
	cs, _ := a.S.Customers()
	a.page(w, "users", map[string]any{"WebUsers": wu, "Customers": cs, "Msg": r.URL.Query().Get("msg")})
}

func (a *App) usersRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /users", a.usersPage)
	m.HandleFunc("POST /users/web", func(w http.ResponseWriter, r *http.Request) {
		if err := a.S.AddWebUser(r.FormValue("username"), r.FormValue("password"), r.FormValue("display_name")); err != nil {
			back(w, r, "/users", "Not added: "+err.Error())
			return
		}
		back(w, r, "/users", "Test customer added. They log in at /webchat on this computer.")
	})
	m.HandleFunc("POST /users/web/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		if err := a.S.DeleteWebUser(r.PathValue("id")); err != nil {
			back(w, r, "/users", err.Error())
			return
		}
		back(w, r, "/users", "Test customer deleted.")
	})
	m.HandleFunc("POST /users/{id}/block", func(w http.ResponseWriter, r *http.Request) {
		a.blockRoute(w, r, true)
	})
	m.HandleFunc("POST /users/{id}/unblock", func(w http.ResponseWriter, r *http.Request) {
		a.blockRoute(w, r, false)
	})
}

func (a *App) blockRoute(w http.ResponseWriter, r *http.Request, on bool) {
	if err := a.S.SetBlocked(r.PathValue("id"), on); err != nil {
		back(w, r, "/users", err.Error())
		return
	}
	if on {
		back(w, r, "/users", "Blocked: the shop will not reply to them on any channel.")
		return
	}
	back(w, r, "/users", "Unblocked.")
}

// ---- blocking customers ----

type CustomerRow struct {
	Customer
	Blocked bool
	Orders  int
}

// Customers lists every customer on every channel, newest first.
func (s *Store) Customers() ([]CustomerRow, error) {
	rows, err := s.DB.Query(`SELECT ` + custCols + `, blocked, (SELECT COUNT(*) FROM orders o WHERE o.customer_id=customers.id AND o.status<>'cart')
	  FROM customers ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CustomerRow
	for rows.Next() {
		var c CustomerRow
		rows.Scan(&c.ID, &c.Phone, &c.Name, &c.JID, &c.Channel, &c.Account, &c.Address, &c.WaitAddr, &c.Blocked, &c.Orders)
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetBlocked blocks or unblocks a customer by id or phone (customer key, e.g. +31..., tg:7, web:bob).
func (s *Store) SetBlocked(idOrPhone string, on bool) error {
	res, err := s.DB.Exec(`UPDATE customers SET blocked=? WHERE phone=? OR CAST(id AS TEXT)=?`, boolInt(on), idOrPhone, idOrPhone)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no customer " + idOrPhone)
	}
	return nil
}

func (s *Store) isBlocked(phone string) bool {
	var b bool
	s.DB.QueryRow(`SELECT blocked FROM customers WHERE phone=?`, phone).Scan(&b)
	return b
}
