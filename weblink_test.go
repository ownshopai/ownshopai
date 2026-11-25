package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ownshopai/ownshopai/relay/relaytest"
)

// relayShop is a shop whose Website link runs through a fake link-service relay endpoint.
func relayShop(t *testing.T) (*Store, *App, *relaytest.Server, chan struct{}) {
	s, a, h, done := pubShop(t)
	srv := relaytest.New("osk_relay-key", "myshop")
	t.Cleanup(srv.Close)
	s.Set(setLinkName, "myshop")
	s.Set(setLinkHost, testHost)
	s.Set(setLinkAppKey, "osk_relay-key")
	a.Link = &WebLink{S: s, Port: pubDefaultPort, Public: h, RelayURL: srv.RelayURL()}
	t.Cleanup(a.Link.halt)
	return s, a, srv, done
}

// visit sends one request the way the link service does: Host is the claimed name, the visitor
// address in CF-Connecting-IP.
func visit(t *testing.T, srv *relaytest.Server, method, path, cookie string, body string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, "http://"+testHost+path, rd)
	req.Header.Set("CF-Connecting-IP", "203.0.113.9")
	req.Header.Set("X-Forwarded-Proto", "https")
	if method == "POST" {
		req.Header.Set("Origin", "https://"+testHost)
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: pubCookie, Value: cookie})
	}
	res, err := srv.Do(ctx, req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}

func waitLink(t *testing.T, a *App, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a.Link.View().State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("link state %q (%s), want %q", a.Link.View().State, a.Link.View().Err, want)
}

func TestRelayCarriesTheCustomerChatOnly(t *testing.T) {
	s, a, srv, done := relayShop(t)
	if err := a.Link.Start(); err != nil {
		t.Fatal(err)
	}
	waitLink(t, a, "on")
	if a.Link.URL() != "https://"+testHost {
		t.Fatalf("url %q", a.Link.URL())
	}
	res := visit(t, srv, "GET", "/", "", "")
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !bytes.Contains(b, []byte("Chat with us")) {
		t.Fatalf("chat page through the relay: %d", res.StatusCode)
	}
	// sign in and send a message end to end
	res = visit(t, srv, "POST", "/api/signin", "", `{"name":"Ana","phone":"+1 555 123 4567"}`)
	var cookie string
	for _, c := range res.Cookies() {
		if c.Name == pubCookie {
			cookie = c.Value
		}
	}
	if res.StatusCode != 200 || cookie == "" {
		t.Fatalf("sign-in through the relay: %d", res.StatusCode)
	}
	if res = visit(t, srv, "POST", "/api/send", cookie, `{"text":"do you have honey"}`); res.StatusCode != 200 {
		t.Fatalf("send through the relay: %d", res.StatusCode)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the shop never handled the message")
	}
	var n int
	s.DB.QueryRow(`SELECT COUNT(*) FROM pub_messages WHERE direction='in' AND text='do you have honey'`).Scan(&n)
	if n != 1 {
		t.Fatalf("message not stored: %d", n)
	}
	// owner routes do not exist through the relay
	for _, p := range []string{"/orders", "/settings", "/users", "/faq", "/products", "/chats", "/webchat", "/relay/status",
		"/channels/website/qr.png", "/website/linked", "/start-over/reset"} {
		if res := visit(t, srv, "GET", p, "", ""); res.StatusCode != 404 {
			t.Errorf("%s through the relay: %d, want 404", p, res.StatusCode)
		}
	}
}

func TestRelayReconnectsAndStops(t *testing.T) {
	_, a, srv, _ := relayShop(t)
	a.Link.Start()
	waitLink(t, a, "on")
	srv.Drop()
	if res := visit(t, srv, "GET", "/style.css", "", ""); res.StatusCode != 200 {
		t.Fatalf("after the service dropped the connection: %d", res.StatusCode)
	}
	if srv.Accepts() < 2 {
		t.Fatalf("no reconnect: %d accepts", srv.Accepts())
	}
	a.Link.Stop()
	if v := a.Link.View(); v.State != "off" || v.URL != "" || v.Connected {
		t.Fatalf("after stop: %+v", v)
	}
	if a.S.Get(setWebPublic) != "" {
		t.Fatal("stop did not remember the owner's choice")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	time.Sleep(50 * time.Millisecond)
	if _, err := srv.Session(ctx); err == nil {
		t.Fatal("relay still connected after stop")
	}
}

func TestRelayRefusedShowsOwner(t *testing.T) {
	_, a, srv, _ := relayShop(t)
	srv.SetStatus(404)
	a.Link.Start()
	waitLink(t, a, "error")
	if w := linkErrWords(a.Link.View().Err); !strings.Contains(w, "does not know this link") {
		t.Fatalf("owner words %q", w)
	}
}

func TestRelayStatusEndpoint(t *testing.T) {
	_, a, _, _ := relayShop(t)
	a.Link.Start()
	waitLink(t, a, "on")
	req, _ := http.NewRequest("GET", "/relay/status", nil)
	req.Host = "127.0.0.1:2007"
	rr := httptest.NewRecorder()
	a.Routes().ServeHTTP(rr, req)
	body := rr.Body.String()
	if rr.Code != 200 || !strings.Contains(body, `"connected":true`) || !strings.Contains(body, `"url":"https://`+testHost) ||
		strings.Contains(body, "osk_") {
		t.Fatalf("status %d %s", rr.Code, body)
	}
	req.Host = testHost
	rr = httptest.NewRecorder()
	a.Routes().ServeHTTP(rr, req)
	if rr.Code == 200 {
		t.Fatal("relay status served under a public host name")
	}
}

// The real public listener on a TCP port: owner routes 404, the chat page is served.
func TestPublicListenerOnPort(t *testing.T) {
	_, a, _, _ := pubShop(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, a.Pub.Handler())
	defer ln.Close()
	get := func(path string) int {
		req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+path, nil)
		req.Host = testHost // as a tunnel the owner runs would send it
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	for _, p := range []string{"/orders", "/settings", "/users", "/faq", "/products", "/chats", "/webchat", "/relay/status"} {
		if c := get(p); c != 404 {
			t.Errorf("%s on the public port: %d", p, c)
		}
	}
	if c := get("/"); c != 200 {
		t.Fatalf("chat page on the public port: %d", c)
	}
}

func TestPublicHostRule(t *testing.T) {
	for h, want := range map[string]bool{"myshop.ownshopai.store": true, "abc.ngrok-free.app": true, "x.trycloudflare.com:443": true,
		"localhost:2008": false, "127.0.0.1:2008": false, "[::1]:2008": false, "10.0.0.2": false, "": false, "intranet": false, "a.localhost": false} {
		if publicHost(h) != want {
			t.Errorf("publicHost(%q) = %v", h, !want)
		}
	}
}
