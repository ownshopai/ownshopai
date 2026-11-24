package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSupportFlagSetOnHandoffAndResolved(t *testing.T) {
	s, srv := webShop(t)
	c, err := s.Customer("+31600000009", "Emma", "")
	if err != nil {
		t.Fatal(err)
	}
	s.SetCustomerChannel(c.ID, "whatsapp", "shop1")
	if q := s.SupportQueue(); len(q) != 0 {
		t.Fatalf("queue before hand-off: %+v", q)
	}
	s.Handoff(c.Phone, "can I pay in cash?", "not sure")
	q := s.SupportQueue()
	if len(q) != 1 || q[0].Key != c.Phone || q[0].Status() != "Needs support" {
		t.Fatalf("queue after hand-off: %+v", q)
	}
	// the Chats page lists them on top with the badge
	res, _ := http.Get(srv.URL + "/chats")
	body := readAll(res)
	if i, j := strings.Index(body, "Needs support"), strings.Index(body, "Waiting for you"); i < 0 || i > j || !strings.Contains(body, "Emma") {
		t.Fatal("Chats page does not show the needs-support customer on top")
	}
	// Resolved clears it
	if res, _ := http.PostForm(srv.URL+"/users/"+strconvI(c.ID)+"/resolved", url.Values{"back": {"chats"}}); res.StatusCode != 200 {
		t.Fatalf("resolved: %d", res.StatusCode)
	}
	if q := s.SupportQueue(); len(q) != 0 {
		t.Fatalf("queue after Resolved: %+v", q)
	}
	// and the owner can set it by hand
	http.PostForm(srv.URL+"/users/"+strconvI(c.ID)+"/support", nil)
	if q := s.SupportQueue(); len(q) != 1 {
		t.Fatal("Mark needs support did not set it")
	}
}

func TestUsersTableAndFilters(t *testing.T) {
	s, srv := webShop(t) // has web test user ana
	wa, _ := s.Customer("+31600000001", "Emma", "")
	s.SetCustomerChannel(wa.ID, "whatsapp", "shop1")
	tg, _ := s.Customer("tg:77", "Tom", "")
	s.SetCustomerChannel(tg.ID, "telegram", "123")
	s.SetBlocked("tg:77", true)
	p, _, err := s.AddPubUser("Mia", "", "mia@example.com")
	if err != nil {
		t.Fatal(err)
	}
	s.Handoff(p.Key(), "help", "x")
	all, _ := s.UserRows()
	if len(all) != 5 { // setup adds one WhatsApp customer too
		t.Fatalf("rows %+v", all)
	}
	if all[0].Name != "Mia" || all[0].Email != "mia@example.com" || all[0].Provider != provWebPub {
		t.Fatalf("needs-support first: %+v", all[0])
	}
	pw := map[string]bool{}
	for _, u := range all {
		pw[u.Provider] = u.PasswordSet
	}
	if !pw[provWebTest] || pw[provWhatsApp] || pw[provWebPub] {
		t.Fatalf("password column %v", pw)
	}
	check := func(provider, status, q string, want ...string) {
		t.Helper()
		var got []string
		for _, u := range FilterUsers(all, provider, status, q) {
			got = append(got, u.Key)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("filter %q %q %q = %v, want %v", provider, status, q, got, want)
		}
	}
	check(provTelegram, "", "", "tg:77")
	check("", "banned", "", "tg:77")
	check("", "support", "", p.Key())
	check(provWebTest, "active", "", "web:ana@example.com")
	check("", "", "MIA@", p.Key())
	check("", "", "emma", "+31600000001")
	check(provWhatsApp, "banned", "")
	res, _ := http.Get(srv.URL + "/users?provider=telegram")
	body := readAll(res)
	if !strings.Contains(body, "Tom") || strings.Contains(body, "Emma") || !strings.Contains(body, "Banned") {
		t.Fatal("users page filter by provider")
	}
	res, _ = http.Get(srv.URL + "/users")
	body = readAll(res)
	for _, w := range []string{"Web test (password)", "Web public (link)", "WhatsApp", "Telegram", "mia@example.com", ">set<", ">none<"} {
		if !strings.Contains(body, w) {
			t.Errorf("users page lacks %q", w)
		}
	}
}

func readAll(res *http.Response) string {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	res.Body.Close()
	return b.String()
}
