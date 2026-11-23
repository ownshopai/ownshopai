package main

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebLogin(t *testing.T) {
	s, _ := setup(t)
	if err := s.AddWebUser("bob", "pw1", "Bob Test"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWebUser("bob", "x", ""); err == nil {
		t.Fatal("duplicate username accepted")
	}
	if err := s.AddWebUser("bad name", "x", ""); err == nil {
		t.Fatal("bad username accepted")
	}
	if u, err := s.WebLogin("bob", "pw1"); err != nil || u.DisplayName != "Bob Test" {
		t.Fatalf("login: %v %+v", err, u)
	}
	if _, err := s.WebLogin("bob", "nope"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err := s.WebLogin("ghost", "pw1"); err == nil {
		t.Fatal("unknown user accepted")
	}
	var hash string
	s.DB.QueryRow(`SELECT password_hash FROM web_users WHERE username='bob'`).Scan(&hash)
	if strings.Contains(hash, "pw1") || !strings.HasPrefix(hash, "$2") {
		t.Fatalf("password not hashed: %q", hash)
	}
	if err := s.DeleteWebUser("bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WebLogin("bob", "pw1"); err == nil {
		t.Fatal("deleted user can log in")
	}
}

var webModel = fakeModel{
	"do you have honey": "call:search_product{query:<escape>honey<escape>}",
	"add 2 raw honey":   "call:add_to_order{product:<escape>raw honey<escape>,quantity:2}",
	"slot 1":            "call:pick_slot{slot:<escape>1<escape>}",
}

// webShop is a running shop with the web chat channel, the fake brain and no send pacing.
func webShop(t *testing.T) (*Store, *httptest.Server) {
	s, dir := setup(t)
	hub := NewHub(nil)
	b := &Bot{S: s, M: webModel, Send: hub}
	hub.OnMessage = func(in Inbound) { b.Handle(context.Background(), in) }
	if err := hub.Add(&WebChat{S: s}); err != nil {
		t.Fatal(err)
	}
	app := &App{S: s, Bot: b, Hub: hub, Dir: dir}
	srv := httptest.NewServer(app.Routes())
	t.Cleanup(srv.Close)
	if err := s.AddWebUser("ana", "secret", "Ana Web"); err != nil {
		t.Fatal(err)
	}
	return s, srv
}

func TestWebChatChannelRoundTrip(t *testing.T) {
	s, _ := setup(t)
	hub := NewHub(nil)
	b := &Bot{S: s, M: webModel, Send: hub}
	done := make(chan struct{}, 4)
	hub.OnMessage = func(in Inbound) { b.Handle(context.Background(), in); done <- struct{}{} }
	wc := &WebChat{S: s}
	hub.Add(wc)
	s.AddWebUser("ana", "secret", "Ana Web")
	u, _ := s.WebLogin("ana", "secret")
	wc.Receive(u, "do you have honey", 0, 0)
	<-done
	ms, _ := s.webMsgs("ana", 0)
	if len(ms) < 2 || ms[0].Direction != "in" || ms[1].Direction != "out" || !strings.Contains(ms[1].Text+ms[len(ms)-1].Text, "Raw honey") {
		t.Fatalf("messages %+v", ms)
	}
	c, err := s.Customer("web:ana", "", "")
	if err != nil || c.Channel != "web" || c.JID != "ana" || c.Name != "Ana Web" {
		t.Fatalf("customer %+v %v", c, err)
	}
	// an owner-side message goes to the same chat
	if err := b.Notify(c, Out{Text: "Hello from the owner"}); err != nil {
		t.Fatal(err)
	}
	ms, _ = s.webMsgs("ana", ms[len(ms)-1].ID)
	if len(ms) != 1 || ms[0].Text != "Hello from the owner" {
		t.Fatalf("owner message %+v", ms)
	}
}

func TestSayEndToEnd(t *testing.T) {
	s, srv := webShop(t)
	say := func(args ...string) string {
		var out bytes.Buffer
		a := append([]string{"--user", "ana", "--password", "secret", "--url", srv.URL, "--quiet", "300ms", "--timeout", "10s"}, args...)
		if err := sayCmd(a, &out); err != nil {
			t.Fatalf("say %v: %v", args, err)
		}
		return out.String()
	}
	expect := func(got, want string) {
		if !strings.Contains(got, want) {
			t.Fatalf("reply %q does not contain %q", got, want)
		}
	}
	expect(say("do you have honey"), "Raw honey 500g")
	expect(say("add 2 raw honey"), "Added 2 x Raw honey 500g")
	expect(say("slot 1"), "Reply YES to confirm order 1042")
	expect(say("yes"), "Where should we deliver?")
	got := say("--location", "12.9716,77.5946")
	expect(got, "Invoice INV-1042")
	expect(got, "shop> [image] "+srv.URL+"/webchat/image/")
	// the image is served to its own user
	i := strings.Index(got, srv.URL+"/webchat/image/")
	url := strings.Fields(got[i:])[0]
	if res, _ := http.Get(url); res.StatusCode != 401 {
		t.Fatalf("image without login: %d", res.StatusCode)
	}
	var n int
	s.DB.QueryRow(`SELECT COUNT(*) FROM orders o JOIN customers c ON c.id=o.customer_id WHERE c.phone='web:ana' AND o.status='awaiting_payment'`).Scan(&n)
	if n != 1 {
		t.Fatalf("web order not on the Orders page: %d", n)
	}
	var out bytes.Buffer
	if err := sayCmd([]string{"--user", "ana", "--password", "wrong", "--url", srv.URL, "hi"}, &out); err == nil {
		t.Fatal("wrong password: say succeeded")
	}
}

func TestWebChatIsLocalOnly(t *testing.T) {
	_, srv := webShop(t)
	req, _ := http.NewRequest("GET", srv.URL+"/webchat", nil)
	req.Host = "shop.example.com"
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 403 {
		t.Fatalf("other host: %d", res.StatusCode)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/webchat/login", strings.NewReader(`{"username":"ana","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 403 {
		t.Fatalf("other origin: %d", res.StatusCode)
	}
	if res, _ := http.Post(srv.URL+"/webchat/login", "application/x-www-form-urlencoded", strings.NewReader("username=ana&password=secret")); res.StatusCode != 415 {
		t.Fatalf("form post: %d", res.StatusCode)
	}
}

func TestBlockedCustomerGetsNoReply(t *testing.T) {
	s, _ := setup(t)
	cap := &capture{name: "whatsapp", account: "shop1"}
	hub := NewHub(nil)
	hub.Add(cap)
	b := &Bot{S: s, M: webModel, Send: hub}
	in := Inbound{Channel: "whatsapp", Account: "shop1", SenderID: "31699@s.whatsapp.net", Phone: "+31699", Name: "Blocky", Text: "do you have honey"}
	b.Handle(context.Background(), in)
	if len(cap.outs) == 0 {
		t.Fatal("no reply before blocking")
	}
	if err := s.SetBlocked("+31699", true); err != nil {
		t.Fatal(err)
	}
	n := len(cap.outs)
	if outs := b.Handle(context.Background(), in); len(outs) != 0 || len(cap.outs) != n {
		t.Fatalf("blocked customer got a reply: %+v", outs)
	}
	cs, _ := s.Customers()
	if len(cs) == 0 || !cs[0].Blocked {
		t.Fatalf("customers %+v", cs)
	}
	c, _ := s.Customer("+31699", "", "")
	if err := s.SetBlocked(strconvI(c.ID), false); err != nil {
		t.Fatal(err)
	}
	if b.Handle(context.Background(), in); len(cap.outs) == n {
		t.Fatal("no reply after unblocking")
	}
}

func strconvI(i int64) string { return strconv.FormatInt(i, 10) }
