package main

import (
	"bytes"
	"context"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWebLogin(t *testing.T) {
	s, _ := setup(t)
	if err := s.AddWebUser("bob@example.com", "pw1", "Bob Test"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWebUser("bob@example.com", "x", ""); err == nil {
		t.Fatal("duplicate username accepted")
	}
	if err := s.AddWebUser("bad name", "x", ""); err == nil {
		t.Fatal("bad username accepted")
	}
	if u, err := s.WebLogin("bob@example.com", "pw1"); err != nil || u.DisplayName != "Bob Test" {
		t.Fatalf("login: %v %+v", err, u)
	}
	if _, err := s.WebLogin("bob@example.com", "nope"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err := s.WebLogin("ghost", "pw1"); err == nil {
		t.Fatal("unknown user accepted")
	}
	var hash string
	s.DB.QueryRow(`SELECT password_hash FROM web_users WHERE username='bob@example.com'`).Scan(&hash)
	if strings.Contains(hash, "pw1") || !strings.HasPrefix(hash, "$2") {
		t.Fatalf("password not hashed: %q", hash)
	}
	if err := s.DeleteWebUser("bob@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WebLogin("bob@example.com", "pw1"); err == nil {
		t.Fatal("deleted user can log in")
	}
}

var webModel = fakeModel{
	"do you have honey":     "call:search_product{query:<escape>honey<escape>}",
	"add 2 raw honey":       "call:add_to_order{product:<escape>raw honey<escape>,quantity:2}",
	"slot 1":                "call:pick_slot{slot:<escape>1<escape>}",
	"can you gift wrap it?": "call:faq{question:<escape>can you gift wrap it<escape>}",
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
	app := &App{S: s, Bot: b, Hub: hub, Dir: dir, File: NewShopFile(s, filepath.Join(dir, "products.xlsx"))}
	srv := httptest.NewServer(app.Routes())
	t.Cleanup(srv.Close)
	if err := s.AddWebUser("ana@example.com", "secret", "Ana Web"); err != nil {
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
	s.AddWebUser("ana@example.com", "secret", "Ana Web")
	u, _ := s.WebLogin("ana@example.com", "secret")
	wc.Receive(u, "do you have honey", 0, 0)
	<-done
	ms, _ := s.webMsgs("ana@example.com", 0)
	if len(ms) < 2 || ms[0].Direction != "in" || ms[1].Direction != "out" || !strings.Contains(ms[1].Text+ms[len(ms)-1].Text, "Raw honey") {
		t.Fatalf("messages %+v", ms)
	}
	c, err := s.Customer("web:ana@example.com", "", "")
	if err != nil || c.Channel != "web" || c.JID != "ana@example.com" || c.Name != "Ana Web" {
		t.Fatalf("customer %+v %v", c, err)
	}
	// an owner-side message goes to the same chat
	if err := b.Notify(c, Out{Text: "Hello from the owner"}); err != nil {
		t.Fatal(err)
	}
	ms, _ = s.webMsgs("ana@example.com", ms[len(ms)-1].ID)
	if len(ms) != 1 || ms[0].Text != "Hello from the owner" {
		t.Fatalf("owner message %+v", ms)
	}
}

func TestSayEndToEnd(t *testing.T) {
	s, srv := webShop(t)
	say := func(args ...string) string {
		var out bytes.Buffer
		a := append([]string{"--user", "ana@example.com", "--password", "secret", "--url", srv.URL, "--quiet", "300ms", "--timeout", "10s"}, args...)
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
	s.DB.QueryRow(`SELECT COUNT(*) FROM orders o JOIN customers c ON c.id=o.customer_id WHERE c.phone='web:ana@example.com' AND o.status='awaiting_payment'`).Scan(&n)
	if n != 1 {
		t.Fatalf("web order not on the Orders page: %d", n)
	}
	var out bytes.Buffer
	if err := sayCmd([]string{"--user", "ana@example.com", "--password", "wrong", "--url", srv.URL, "hi"}, &out); err == nil {
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
	req, _ = http.NewRequest("POST", srv.URL+"/webchat/login", strings.NewReader(`{"username":"ana@example.com","password":"secret"}`))
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

func TestWebLoginRegistersNewName(t *testing.T) {
	s, srv := webShop(t)
	login := func(user, pw string) (int, string) {
		req, _ := http.NewRequest("POST", srv.URL+"/webchat/login", strings.NewReader(`{"username":"`+user+`","password":"`+pw+`"}`))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		b.ReadFrom(res.Body)
		return res.StatusCode, b.String()
	}
	if code, body := login("plainname", "pw9"); code != 401 || !strings.Contains(body, "email") {
		t.Fatalf("a new plain name must be refused: %d %s", code, body)
	}
	if code, body := login("+1 555 000 7777", "pw9"); code != 200 || !strings.Contains(body, `"created":true`) {
		t.Fatalf("new name: %d %s", code, body)
	}
	if _, ok := s.webUser("+15550007777"); !ok {
		t.Fatal("new name not stored")
	}
	if code, body := login("+15550007777", "pw9"); code != 200 || !strings.Contains(body, `"created":false`) { // same number, any spacing
		t.Fatalf("same name again: %d %s", code, body)
	}
	if code, _ := login("ana@example.com", "wrong"); code != 401 {
		t.Fatalf("existing name, wrong password: %d", code)
	}
	if u, err := s.WebLogin("ana@example.com", "secret"); err != nil || u.DisplayName != "Ana Web" {
		t.Fatal("wrong password changed the existing user")
	}
	// failed logins are limited, like public sign-ins; then even the right password waits
	for i := 1; i < pubSigninPerIP; i++ {
		login("ana@example.com", "wrong")
	}
	if code, _ := login("ana@example.com", "secret"); code != 429 {
		t.Fatalf("after %d wrong passwords: %d, want 429", pubSigninPerIP, code)
	}
	// say registers on first use
	var out bytes.Buffer
	if err := sayCmd([]string{"--user", "cli@example.com", "--password", "pw", "--url", srv.URL, "--quiet", "300ms", "--timeout", "10s", "hi"}, &out); err != nil {
		t.Fatalf("say with a new name: %v", err)
	}
	if _, ok := s.webUser("cli@example.com"); !ok {
		t.Fatal("say did not register the new name")
	}
}

func TestOlderPlainUsernameStillLogsIn(t *testing.T) {
	s, _ := setup(t)
	h, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	s.DB.Exec(`INSERT INTO web_users(username, password_hash, display_name, created_at) VALUES ('priya', ?, 'Priya', ?)`, string(h), now())
	if u, err := s.WebLogin("priya", "pw"); err != nil || u.Username != "priya" {
		t.Fatalf("legacy login: %v", err)
	}
	if _, created, err := s.WebLoginOrRegister("priya", "pw"); err != nil || created {
		t.Fatalf("legacy login via register path: %v %v", created, err)
	}
	if err := s.AddWebUser("Ana@Example.com", "pw", ""); err != nil {
		t.Fatal(err)
	}
	if u, err := s.WebLogin("ana@example.com", "pw"); err != nil || u.DisplayName != "ana@example.com" {
		t.Fatalf("email not normalised or display default: %+v %v", u, err)
	}
}
