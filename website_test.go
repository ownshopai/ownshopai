package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testHost = "myshop.ownshopai.store"

// pubShop is a shop with the public chat channel behind the public handler, host fixed to testHost.
func pubShop(t *testing.T) (*Store, *App, http.Handler, chan struct{}) {
	s, dir := setup(t)
	hub := NewHub(nil)
	g := NewGuard(hub, s)
	b := &Bot{S: s, M: webModel, Send: g}
	done := make(chan struct{}, 16)
	hub.OnMessage = func(in Inbound) { b.Handle(context.Background(), in); done <- struct{}{} }
	hub.Add(&PubChat{S: s})
	a := &App{S: s, Bot: b, Hub: hub, Dir: dir}
	a.Pub = newPubSite(a, nil)
	return s, a, a.Pub.Handler(), done
}

type browser struct {
	t      *testing.T
	h      http.Handler
	cookie string
	ip     string
}

func (br *browser) do(method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "https://"+testHost+path, rd)
	req.Host = testHost
	if method == "POST" {
		req.Header.Set("Origin", "https://"+testHost)
		req.Header.Set("Content-Type", "application/json")
	}
	if br.cookie != "" {
		req.AddCookie(&http.Cookie{Name: pubCookie, Value: br.cookie})
	}
	if br.ip != "" {
		req.Header.Set("CF-Connecting-IP", br.ip)
	}
	rr := httptest.NewRecorder()
	br.h.ServeHTTP(rr, req)
	for _, c := range rr.Result().Cookies() {
		if c.Name == pubCookie {
			if !strings.HasPrefix(c.Name, "__Host-") || c.Domain != "" || c.Path != "/" || !c.Secure {
				br.t.Fatalf("cookie must be a __Host- cookie: %+v", c)
			}
			br.cookie = c.Value
			if c.MaxAge < 0 {
				br.cookie = ""
			}
		}
	}
	var out map[string]any
	json.Unmarshal(rr.Body.Bytes(), &out)
	return rr, out
}

func TestPublicListenerServesOnlyCustomerRoutes(t *testing.T) {
	_, _, h, _ := pubShop(t)
	get := func(path, host string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = host
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	for _, p := range []string{"/orders", "/settings", "/users", "/products", "/chats", "/webchat", "/orders.csv", "/setup/1",
		"/payment-qr", "/.well-known/ucp", "/ucp/mcp", "/health", "/start-over/reset", "/channels/website/qr.png", "/website/linked", "/calendar"} {
		if rr := get(p, testHost); rr.Code != 404 {
			t.Errorf("%s on the public link: %d, want 404", p, rr.Code)
		}
	}
	for p, ct := range map[string]string{"/": "text/html", "/manifest.webmanifest": "application/manifest+json",
		"/sw.js": "text/javascript", "/icon-192.png": "image/png", "/icon-512.png": "image/png", "/style.css": "text/css"} {
		rr := get(p, testHost)
		if rr.Code != 200 || !strings.HasPrefix(rr.Header().Get("Content-Type"), ct) {
			t.Errorf("%s: %d %q, want 200 %s", p, rr.Code, rr.Header().Get("Content-Type"), ct)
		}
	}
	// this computer or the LAN by address gets nothing at all (see publicHost)
	for _, host := range []string{"localhost:2008", "127.0.0.1:2008", "192.168.1.5:2008", "[::1]:2008", "", "intranet"} {
		if rr := get("/", host); rr.Code != 404 {
			t.Errorf("host %s: %d, want 404", host, rr.Code)
		}
	}
	var m map[string]any
	json.Unmarshal(get("/manifest.webmanifest", testHost).Body.Bytes(), &m)
	if m["display"] != "standalone" || m["start_url"] != "/" || m["name"] == "" {
		t.Fatalf("manifest %v", m)
	}
}

func TestPublicPostNeedsOriginAndJSON(t *testing.T) {
	_, _, h, _ := pubShop(t)
	try := func(origin, ct string) int {
		req := httptest.NewRequest("POST", "/api/signin", strings.NewReader(`{"name":"A","phone":"+15551234567"}`))
		req.Host = testHost
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("Content-Type", ct)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	if c := try("", "application/json"); c != 403 {
		t.Errorf("no origin: %d", c)
	}
	if c := try("https://evil.example.com", "application/json"); c != 403 {
		t.Errorf("other origin: %d", c)
	}
	if c := try("http://"+testHost, "application/json"); c != 403 {
		t.Errorf("http origin: %d", c)
	}
	if c := try("https://"+testHost, "application/x-www-form-urlencoded"); c != 415 {
		t.Errorf("form post: %d", c)
	}
	if c := try("https://"+testHost, "application/json"); c != 200 {
		t.Errorf("good post: %d", c)
	}
}

func TestPublicSigninAndChat(t *testing.T) {
	s, _, h, done := pubShop(t)
	br := &browser{t: t, h: h, ip: "198.51.100.1"}
	if _, me := br.do("GET", "/api/me", nil); me["signed_in"] != false {
		t.Fatalf("me before sign-in %v", me)
	}
	if rr, _ := br.do("GET", "/api/messages", nil); rr.Code != 401 {
		t.Fatalf("messages before sign-in: %d", rr.Code)
	}
	for _, bad := range []map[string]string{{"name": "Ana"}, {"name": "Ana", "phone": "12"},
		{"name": "Ana", "phone": "call me"}, {"name": "Ana", "phone": "+1234567890123456"}} {
		if rr, _ := br.do("POST", "/api/signin", bad); rr.Code != 400 {
			t.Errorf("bad sign-in %v accepted: %d", bad, rr.Code)
		}
	}
	rr, _ := br.do("POST", "/api/signin", map[string]string{"name": "Ana Lee", "phone": "+1 (555) 123-4567"})
	if rr.Code != 200 {
		t.Fatalf("sign-in: %d %s", rr.Code, rr.Body)
	}
	ck := rr.Result().Cookies()[0]
	if !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteLaxMode || ck.MaxAge < 300*24*3600 {
		t.Fatalf("cookie %+v", ck)
	}
	var stored string
	s.DB.QueryRow(`SELECT token_hash FROM pub_users`).Scan(&stored)
	if stored == br.cookie || stored != hashToken(br.cookie) {
		t.Fatal("the browser token must be stored hashed")
	}
	if _, me := br.do("GET", "/api/me", nil); me["name"] != "Ana Lee" || me["phone"] != "+15551234567" {
		t.Fatalf("me %v", me)
	}
	if rr, _ := br.do("POST", "/api/send", map[string]string{"text": "do you have honey"}); rr.Code != 200 {
		t.Fatalf("send %d %s", rr.Code, rr.Body)
	}
	<-done
	_, j := br.do("GET", "/api/messages", nil)
	ms := j["messages"].([]any)
	if len(ms) < 2 || !strings.Contains(strings.ToLower(ms[1].(map[string]any)["text"].(string)), "honey") {
		t.Fatalf("messages %v", ms)
	}
	if rr, _ := br.do("POST", "/api/send", map[string]string{"text": strings.Repeat("x", pubMaxText+1)}); rr.Code != 400 {
		t.Fatalf("too long accepted: %d", rr.Code)
	}
	// the owner sees name and phone, as a web customer
	us, _ := s.PubUsers()
	if len(us) != 1 || us[0].Phone != "+15551234567" || us[0].Name != "Ana Lee" {
		t.Fatalf("pub users %+v", us)
	}
	c, _ := s.Customer(us[0].Key(), "", "")
	if c.Channel != pubChannel || c.Name != "Ana Lee" {
		t.Fatalf("customer %+v", c)
	}
	// Not you? Start again
	br.do("POST", "/api/signout", map[string]string{})
	if _, me := br.do("GET", "/api/me", nil); me["signed_in"] != false {
		t.Fatal("still signed in after Start again")
	}
}

func TestPublicBrowsersAreIsolated(t *testing.T) {
	s, _, h, done := pubShop(t)
	a := &browser{t: t, h: h, ip: "198.51.100.2"}
	b := &browser{t: t, h: h, ip: "198.51.100.3"}
	a.do("POST", "/api/signin", map[string]string{"name": "Sam", "phone": "+15550001111"})
	b.do("POST", "/api/signin", map[string]string{"name": "Sam", "phone": "+15550001111"})
	if a.cookie == "" || a.cookie == b.cookie {
		t.Fatal("two browsers must get two tokens")
	}
	a.do("POST", "/api/send", map[string]string{"text": "do you have honey"})
	<-done
	_, j := b.do("GET", "/api/messages", nil)
	if ms := j["messages"].([]any); len(ms) != 0 {
		t.Fatalf("browser B sees browser A's chat: %v", ms)
	}
	var aMsg string
	s.DB.QueryRow(`SELECT id FROM pub_messages WHERE direction='out' LIMIT 1`).Scan(&aMsg)
	s.DB.Exec(`UPDATE pub_messages SET image='/etc/hosts' WHERE id=?`, aMsg)
	if rr, _ := b.do("GET", "/api/image/"+aMsg, nil); rr.Code != 404 {
		t.Fatalf("browser B reads browser A's image: %d", rr.Code)
	}
	us, _ := s.PubUsers()
	if len(us) != 2 || us[0].Key() == us[1].Key() {
		t.Fatalf("same phone was merged: %+v", us)
	}
	// never merged with a WhatsApp customer with the same number
	s.Customer("+15550001111", "Sam WA", "15550001111@s.whatsapp.net")
	_, j = a.do("GET", "/api/messages", nil)
	if c, _ := s.Customer(us[0].Key(), "", ""); c.Phone == "+15550001111" {
		t.Fatal("merged with the WhatsApp customer")
	}
}

func TestPublicRateLimitsAndBlock(t *testing.T) {
	s, a, h, done := pubShop(t)
	_ = a
	// sign-in per IP
	for i := 0; i < pubSigninPerIP; i++ {
		br := &browser{t: t, h: h, ip: "203.0.113.9"}
		if rr, _ := br.do("POST", "/api/signin", map[string]string{"name": "X", "phone": "+15550000000"}); rr.Code != 200 {
			t.Fatalf("sign-in %d: %d", i, rr.Code)
		}
	}
	br := &browser{t: t, h: h, ip: "203.0.113.9"}
	if rr, _ := br.do("POST", "/api/signin", map[string]string{"name": "X", "phone": "+15550000000"}); rr.Code != 429 {
		t.Fatalf("sign-in over the IP limit: %d", rr.Code)
	}
	// messages per browser
	m := &browser{t: t, h: h, ip: "203.0.113.10"}
	m.do("POST", "/api/signin", map[string]string{"name": "Fast", "phone": "+15550000001"})
	got429 := false
	for i := 0; i < pubMsgPerBrowser+1; i++ {
		rr, _ := m.do("POST", "/api/send", map[string]string{"text": "hello"})
		if rr.Code == 429 {
			got429 = true
			if i < pubMsgPerBrowser {
				t.Fatalf("limited too early at %d", i)
			}
		}
	}
	if !got429 {
		t.Fatal("no message rate limit")
	}
	for len(done) > 0 {
		<-done
	}
	// block applies
	bl := &browser{t: t, h: h, ip: "203.0.113.11"}
	bl.do("POST", "/api/signin", map[string]string{"name": "Spam", "phone": "+15550000002"})
	us, _ := s.PubUsers()
	var key string
	for _, u := range us {
		if u.Name == "Spam" {
			key = u.Key()
		}
	}
	if err := s.SetBlocked(key, true); err != nil {
		t.Fatal(err)
	}
	if rr, _ := bl.do("POST", "/api/send", map[string]string{"text": "hello"}); rr.Code != 403 {
		t.Fatalf("blocked customer could send: %d", rr.Code)
	}
	us, _ = s.PubUsers()
	for _, u := range us {
		if u.Key() == key && !u.Blocked {
			t.Fatal("Users page does not show the block")
		}
	}
}

func TestLimiterWindow(t *testing.T) {
	l := newLimiter(2, time.Minute)
	t0 := time.Now()
	l.now = func() time.Time { return t0 }
	if !l.allow("k") || !l.allow("k") || l.allow("k") {
		t.Fatal("limit of 2 not applied")
	}
	l.now = func() time.Time { return t0.Add(2 * time.Minute) }
	if !l.allow("k") {
		t.Fatal("window did not reset")
	}
}

func TestCleanPhone(t *testing.T) {
	for in, want := range map[string]bool{"+31 6 1234 5678": true, "0612345678": true, "123456": false, "+1-555-123-4567": true,
		"1234567890123456": false, "12a4567890": false, "++15551234567": false, "": false} {
		if _, ok := cleanPhone(in); ok != want {
			t.Errorf("cleanPhone(%q) = %v", in, ok)
		}
	}
}

func TestPushSubscriptionStorageAndCleanup(t *testing.T) {
	s, _, h, _ := pubShop(t)
	br := &browser{t: t, h: h, ip: "198.51.100.20"}
	br.do("POST", "/api/signin", map[string]string{"name": "Pia", "phone": "+15550002222"})
	_, k := br.do("GET", "/api/push/key", nil)
	if k["key"] == "" || s.Get(setVapidPrivate) == "" {
		t.Fatal("no VAPID keys")
	}
	if strings.Contains(br.lastBody(h), s.Get(setVapidPrivate)) {
		t.Fatal("private key served")
	}
	priv, pub, _ := vapidKeys(s)
	_ = priv
	// a real-looking P-256 key from a test subscription
	sub := map[string]any{"endpoint": "https://push.example.com/send/abc", "keys": map[string]string{
		"p256dh": "BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM", "auth": "tBHItJI5svbpez7KI4CCXg"}} // gitleaks:allow (public example key)
	if rr, _ := br.do("POST", "/api/push/subscribe", sub); rr.Code != 200 {
		t.Fatalf("subscribe %d %s", rr.Code, rr.Body)
	}
	if rr, _ := br.do("POST", "/api/push/subscribe", map[string]any{"endpoint": "http://x", "keys": map[string]string{}}); rr.Code != 400 {
		t.Fatal("bad subscription accepted")
	}
	us, _ := s.PubUsers()
	if n := len(s.pushSubs(us[0].ID)); n != 1 {
		t.Fatalf("%d subscriptions", n)
	}
	// a push service that says the subscription is gone: it is deleted, no network used
	ft := &fakePush{code: 410}
	sendPush(s, ft, us[0].ID, "Shop", "Your order is packed")
	if ft.n != 1 || !strings.HasPrefix(ft.auth, "vapid t=") || !strings.Contains(ft.auth, pub) {
		t.Fatalf("push request %d %q", ft.n, ft.auth)
	}
	if n := len(s.pushSubs(us[0].ID)); n != 0 {
		t.Fatal("dead subscription kept")
	}
}

type fakePush struct {
	mu   sync.Mutex
	code int
	n    int
	auth string
}

func (f *fakePush) Do(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	f.auth = r.Header.Get("Authorization")
	return &http.Response{StatusCode: f.code, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func (br *browser) lastBody(h http.Handler) string {
	req := httptest.NewRequest("GET", "/api/push/key", nil)
	req.Host = testHost
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Body.String()
}

// ---- free fixed link (link service fake) ----

type fakeLinks struct {
	down                                         bool
	exchanged, claimed, released, revoked, beats int
	lastKey, lastVerifier, lastChallenge         string
	info                                         LinkInfo
}

func (f *fakeLinks) Up(context.Context) bool { return !f.down }

func (f *fakeLinks) LoginURL(state, challenge, ret string) string {
	f.lastChallenge = challenge
	return "https://links.example/login?state=" + state + "&challenge=" + challenge + "&return=" + ret
}
func (f *fakeLinks) Exchange(_ context.Context, code, verifier string) (string, error) {
	f.lastVerifier = verifier
	sum := sha256.Sum256([]byte(verifier))
	if code != "good-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.lastChallenge {
		return "", &LinkError{Status: 400, Code: "invalid_code"}
	}
	f.exchanged++
	return "osk_appkey-secret-123", nil
}
func (f *fakeLinks) Revoke(_ context.Context, key string) error {
	f.revoked++
	f.lastKey = key
	return nil
}
func (f *fakeLinks) Available(_ context.Context, n string) (bool, string, error) {
	if n == "taken" {
		return false, "name_taken", nil
	}
	return true, "", nil
}
func (f *fakeLinks) Claim(_ context.Context, key, name string) (LinkClaim, error) {
	f.claimed++
	f.lastKey = key
	if name == "taken" {
		return LinkClaim{}, &LinkError{Status: 409, Code: "name_taken", Message: "x"}
	}
	return LinkClaim{Name: name, Hostname: name + ".ownshopai.store", URL: "https://" + name + ".ownshopai.store"}, nil
}
func (f *fakeLinks) Heartbeat(_ context.Context, key, name string) (LinkInfo, error) {
	f.beats++
	return f.info, nil
}
func (f *fakeLinks) Release(_ context.Context, key, name string) error { f.released++; return nil }

func TestFreeLinkSignInClaimRelease(t *testing.T) {
	s, _ := setup(t)
	fl := &fakeLinks{}
	ctx := context.Background()
	if _, err := ClaimLink(ctx, s, fl, "myshop"); err == nil {
		t.Fatal("claimed without signing in")
	}
	u := StartLinkLogin(s, fl, "http://127.0.0.1:2007/website/linked")
	st := s.Get(setLinkState)
	if st == "" || !strings.Contains(u, st) {
		t.Fatalf("login url %q", u)
	}
	if err := FinishLinkLogin(ctx, s, fl, "good-code", "forged"); err == nil {
		t.Fatal("wrong state accepted")
	}
	StartLinkLogin(s, fl, "x")
	if err := FinishLinkLogin(ctx, s, fl, "good-code", s.Get(setLinkState)); err != nil {
		t.Fatal(err)
	}
	if s.Get(setLinkAppKey) != "osk_appkey-secret-123" || s.Get(setLinkState) != "" || s.Get(setLinkVerifier) != "" {
		t.Fatal("app key not kept or state/verifier reusable")
	}
	if len(fl.lastVerifier) < 43 || len(fl.lastChallenge) != 43 {
		t.Fatalf("PKCE verifier %d, challenge %d chars", len(fl.lastVerifier), len(fl.lastChallenge))
	}
	if _, err := ClaimLink(ctx, s, fl, "taken"); err == nil || !strings.Contains(err.Error(), "taken") {
		t.Fatalf("taken name: %v", err)
	}
	if _, err := ClaimLink(ctx, s, fl, "Bad Name!"); err == nil {
		t.Fatal("bad name accepted")
	}
	host, err := ClaimLink(ctx, s, fl, "myshop")
	if err != nil || host != "myshop.ownshopai.store" || fl.lastKey != "osk_appkey-secret-123" {
		t.Fatalf("claim %q %v", host, err)
	}
	if s.Get(setLinkName) != "myshop" || s.Get(setLinkHost) != "myshop.ownshopai.store" {
		t.Fatal("claim not kept")
	}
	// heartbeat once a day
	fl.info = LinkInfo{Name: "myshop", Status: "active"}
	beatOnce(ctx, s, fl)
	beatOnce(ctx, s, fl)
	if fl.beats != 1 {
		t.Fatalf("second heartbeat the same day: %d", fl.beats)
	}
	s.Set(setLinkBeatDate, "")
	fl.info = LinkInfo{Name: "myshop", Status: "disabled", DisabledReason: "inactive"}
	beatOnce(ctx, s, fl)
	if s.Get(setLinkStatus) != "disabled:inactive" {
		t.Fatal("disabled link not noted")
	}
	// the owner page shows the link but no secret
	app := &App{S: s, Hub: NewHub(nil), Link: &WebLink{S: s, Port: 2008}, Links: fl, Dir: t.TempDir()}
	rr := httptest.NewRecorder()
	app.Routes().ServeHTTP(rr, httptest.NewRequest("GET", "/settings", nil))
	body := rr.Body.String()
	for _, secret := range []string{"osk_appkey-secret-123"} {
		if strings.Contains(body, secret) {
			t.Fatalf("settings page shows %s", secret)
		}
	}
	if !strings.Contains(body, "myshop.ownshopai.store") || !strings.Contains(body, "This link is off") {
		t.Fatal("link or its off state not shown")
	}
	// /website/linked is on the owner listener only
	if err := ReleaseLink(ctx, s, fl); err != nil || fl.released != 1 || s.Get(setLinkName) != "" || s.Get(setLinkHost) != "" {
		t.Fatal("release did not forget the link")
	}
	if err := SignOutLink(ctx, s, fl); err != nil || fl.revoked != 1 || fl.lastKey != "osk_appkey-secret-123" || s.Get(setLinkAppKey) != "" {
		t.Fatal("sign out did not revoke")
	}
}

func TestLinkedCallbackOnOwnerPageOnly(t *testing.T) {
	s, _ := setup(t)
	fl := &fakeLinks{}
	app := &App{S: s, Hub: NewHub(nil), Link: &WebLink{S: s, Port: 2008}, Links: fl, Dir: t.TempDir()}
	StartLinkLogin(s, fl, "x")
	st := s.Get(setLinkState)
	req := httptest.NewRequest("GET", "/website/linked?code=good-code&state="+st, nil)
	req.Host = "evil.example.com"
	rr := httptest.NewRecorder()
	app.Routes().ServeHTTP(rr, req)
	if (rr.Code != 403 && rr.Code != 404) || s.Get(setLinkAppKey) != "" {
		t.Fatalf("callback under another host: %d", rr.Code)
	}
	req = httptest.NewRequest("GET", "/website/linked?code=good-code&state="+st, nil)
	req.Host = "127.0.0.1:2007"
	rr = httptest.NewRecorder()
	app.Routes().ServeHTTP(rr, req)
	if rr.Code != 303 || s.Get(setLinkAppKey) == "" {
		t.Fatalf("callback: %d", rr.Code)
	}
}

func captureLog(t *testing.T, w io.Writer) {
	old := stdlog.Writer()
	stdlog.SetOutput(io.MultiWriter(w, old))
	t.Cleanup(func() { stdlog.SetOutput(old) })
}

func TestOwnerPortNeverTakesPublicPort(t *testing.T) {
	if p, _, _ := pickPort(2007, func(p int) bool { return p != 2007 }, func(int) bool { return false }); p == pubDefaultPort {
		t.Fatal("owner page took the public port")
	}
}

func TestWebsiteOwnerRoutesRefuseOtherSites(t *testing.T) {
	s, _ := setup(t)
	app := &App{S: s, Hub: NewHub(nil), Link: &WebLink{S: s, Port: 2008}, Links: &fakeLinks{}, Dir: t.TempDir()}
	h := app.Routes()
	post := func(host, origin string) int {
		req := httptest.NewRequest("POST", "/channels/website/start", strings.NewReader(""))
		req.Host = host
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	for _, c := range [][2]string{{"127.0.0.1:2007", "https://evil.example.com"}, {"127.0.0.1:2007", "null"}, {"evil.example.com", ""}, {"name.ownshopai.store", "https://name.ownshopai.store"}} {
		if code := post(c[0], c[1]); code != 403 {
			t.Errorf("host %s origin %s: %d", c[0], c[1], code)
		}
	}
	if s.Get(setWebPublic) != "" {
		t.Fatal("a foreign page turned the public link on")
	}
	s.Set(setLinkName, "name")
	s.Set(setLinkHost, "name.ownshopai.store")
	s.Set(setLinkAppKey, "osk_k")
	app.Link.RelayURL = "ws://127.0.0.1:1/v1/relay" // nothing listens: it keeps retrying, never reaches the internet
	defer app.Link.halt()
	if code := post("127.0.0.1:2007", "http://127.0.0.1:2007"); code != 303 || s.Get(setWebPublic) != "on" {
		t.Fatalf("owner post: %d", code)
	}
}

func TestHTTPLinkServiceWire(t *testing.T) {
	type hit struct{ path, auth, body string }
	var hits []hit
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hits = append(hits, hit{r.URL.Path, r.Header.Get("Authorization"), string(b)})
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/session/exchange":
			io.WriteString(w, `{"app_key":"osk_k","key_id":"u"}`)
		case "/v1/claim":
			w.WriteHeader(409)
			io.WriteString(w, `{"error":{"code":"name_taken","message":"That name is taken.","request_id":"r1"}}`)
		case "/v1/heartbeat":
			io.WriteString(w, `{"name":"a","hostname":"a.ownshopai.store","status":"active"}`)
		default:
			io.WriteString(w, `{"ok":true}`)
		}
	}))
	defer srv.Close()
	ls := &httpLinkService{UI: "https://ui.example", API: srv.URL, HC: srv.Client()}
	ctx := context.Background()
	u := ls.LoginURL("st", "ch", "http://127.0.0.1:2007/website/linked")
	if !strings.HasPrefix(u, "https://ui.example/login?state=st&challenge=ch&return=http%3A%2F%2F127.0.0.1%3A2007%2Fwebsite%2Flinked") {
		t.Fatalf("login url %s", u)
	}
	if k, err := ls.Exchange(ctx, "lsc_x", "verif"); err != nil || k != "osk_k" {
		t.Fatal(k, err)
	}
	_, err := ls.Claim(ctx, "osk_k", "a")
	var le *LinkError
	if !errors.As(err, &le) || le.Code != "name_taken" || le.Status != 409 || le.RequestID != "r1" {
		t.Fatalf("error %v", err)
	}
	info, _ := ls.Heartbeat(ctx, "osk_k", "a")
	ls.Release(ctx, "osk_k", "a")
	ls.Revoke(ctx, "osk_k")
	if info.Status != "active" {
		t.Fatal(info)
	}
	for _, h := range hits {
		if h.path == "/v1/token" || h.path == "/v1/rotate" {
			t.Errorf("%s is gone from the contract but was called", h.path)
		}
	}
	for _, h := range hits {
		if strings.Contains(h.body, "osk_") {
			t.Errorf("%s: app key in the body: %s", h.path, h.body)
		}
		if h.path == "/v1/session/exchange" {
			if h.auth != "" || !strings.Contains(h.body, `"code_verifier":"verif"`) || !strings.Contains(h.body, `"client":"OwnShop AI `) {
				t.Errorf("exchange %+v", h)
			}
			continue
		}
		if h.auth != "Bearer osk_k" {
			t.Errorf("%s auth %q", h.path, h.auth)
		}
		if h.path == "/v1/session/revoke" && h.body != "{}" {
			t.Errorf("revoke body %q", h.body)
		}
		if h.path != "/v1/session/revoke" && h.body != `{"name":"a"}` {
			t.Errorf("%s body %q", h.path, h.body)
		}
	}
}

func TestDeleteShopReleasesFreeLink(t *testing.T) {
	s, dir := setup(t)
	fl := &fakeLinks{}
	s.Set(setLinkAppKey, "osk_k")
	s.Set(setLinkName, "myshop")
	s.Set(setLinkHost, "myshop.ownshopai.store")
	app := &App{S: s, Hub: NewHub(nil), Link: &WebLink{S: s, Port: 2008}, Links: fl, Dir: dir}
	if _, err := app.ResetShop(false); err != nil {
		t.Fatal(err)
	}
	if fl.released != 1 || fl.revoked != 1 || s.Get(setLinkHost) != "" || s.Get(setLinkAppKey) != "" {
		t.Fatalf("released %d revoked %d", fl.released, fl.revoked)
	}
}

func TestPublicSigninWithEmail(t *testing.T) {
	s, _, h, _ := pubShop(t)
	bad := &browser{t: t, h: h, ip: "198.51.100.6"}
	for _, m := range []map[string]string{{"name": "Ana"}, {"name": "Ana", "phone": " ", "email": ""}, {"name": "Ana", "email": "ana"},
		{"name": "Ana", "email": "ana@nodot"}, {"name": "Ana", "email": "a b@x.com"}, {"name": "Ana", "email": "a@@x.com"}} {
		if rr, _ := bad.do("POST", "/api/signin", m); rr.Code != 400 {
			t.Errorf("bad sign-in %v accepted: %d", m, rr.Code)
		}
	}
	a := &browser{t: t, h: h, ip: "198.51.100.7"}
	if rr, _ := a.do("POST", "/api/signin", map[string]string{"name": "Mia", "email": " mia@example.com "}); rr.Code != 200 {
		t.Fatalf("email only: %d %s", rr.Code, rr.Body)
	}
	if _, me := a.do("GET", "/api/me", nil); me["email"] != "mia@example.com" || me["phone"] != "" {
		t.Fatalf("me %v", me)
	}
	b := &browser{t: t, h: h, ip: "198.51.100.8"}
	if rr, _ := b.do("POST", "/api/signin", map[string]string{"name": "Bo", "phone": "+15550003333", "email": "bo@example.org"}); rr.Code != 200 {
		t.Fatalf("both: %d", rr.Code)
	}
	us, _ := s.PubUsers()
	if len(us) != 2 || us[0].Contact() != "+15550003333 · bo@example.org" || us[1].Contact() != "mia@example.com" {
		t.Fatalf("pub users %+v", us)
	}
	if got := s.pubPhones()[us[1].Key()]; got != "mia@example.com" {
		t.Fatalf("owner pages see %q", got)
	}
}

func TestPublicSigninNameOptionalIdentifierRequired(t *testing.T) {
	s, _, h, _ := pubShop(t)
	br := &browser{t: t, h: h, ip: "198.51.100.40"}
	if rr, _ := br.do("POST", "/api/signin", map[string]string{"name": "Only A Name"}); rr.Code != 400 {
		t.Fatalf("name without phone or email: %d", rr.Code)
	}
	if rr, _ := br.do("POST", "/api/signin", map[string]string{"email": "nn@example.com"}); rr.Code != 200 {
		t.Fatalf("email without name: %d %s", rr.Code, rr.Body)
	}
	us, _ := s.PubUsers()
	if len(us) != 1 || us[0].Name != "nn@example.com" {
		t.Fatalf("no-name user %+v", us)
	}
}

func TestWebsiteCardComingSoonAndNoOldModes(t *testing.T) {
	s, _ := setup(t)
	fl := &fakeLinks{down: true}
	app := &App{S: s, Hub: NewHub(nil), Link: &WebLink{S: s, Port: 2008}, Links: fl, Dir: t.TempDir()}
	page := func() string {
		linkUpCache.Lock()
		linkUpCache.at = time.Time{}
		linkUpCache.Unlock()
		rr := httptest.NewRecorder()
		app.Routes().ServeHTTP(rr, httptest.NewRequest("GET", "/settings", nil))
		return rr.Body.String()
	}
	b := page()
	if !strings.Contains(b, "Coming soon") || !strings.Contains(b, "<button disabled>Sign in with Google</button>") {
		t.Fatal("no Coming soon while the service is down")
	}
	for _, gone := range []string{"Quick link", "trycloudflare", "own domain", "Tunnel token", "(recommended)", "cloudflared", "tunnel program", "New tunnel key"} {
		if strings.Contains(b, gone) {
			t.Errorf("card still offers %q", gone)
		}
	}
	fl.down = false
	b = page()
	if strings.Contains(b, "Coming soon") || !strings.Contains(b, `action="/channels/website/signin`) {
		t.Fatal("card does not work once the service answers")
	}
	for _, p := range []string{"/channels/website/token", "/channels/website/rotate", "/channels/website/check"} {
		req := httptest.NewRequest("POST", p, nil)
		req.Host = "127.0.0.1:2007"
		rr := httptest.NewRecorder()
		app.Routes().ServeHTTP(rr, req)
		if rr.Code != 404 && rr.Code != 405 {
			t.Errorf("%s still served: %d", p, rr.Code)
		}
	}
}

func TestHTTPLinkServiceUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/up" {
			io.WriteString(w, "ok")
			return
		}
		http.NotFound(w, r)
	}))
	ls := &httpLinkService{API: srv.URL, HC: srv.Client()}
	if !ls.Up(context.Background()) {
		t.Fatal("up not seen")
	}
	srv.Close()
	if ls.Up(context.Background()) {
		t.Fatal("closed service seen as up")
	}
}
