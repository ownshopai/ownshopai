package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startOverApp is a shop with products, one confirmed order, a chat line, a hand-off, an invoice file,
// a photo, a QR upload, a port and a Telegram token.
func startOverApp(t *testing.T) (*App, http.Handler) {
	t.Helper()
	s, dir := setup(t)
	app := &App{S: s, Bot: &Bot{S: s}, File: NewShopFile(s, filepath.Join(dir, "products.xlsx")), Dir: dir}
	ps, _ := s.Products()
	c, _ := s.Customer("+31600000077", "Tester", "chat-77")
	o, _ := s.EnsureCart(c.ID)
	s.AddItem(o.ID, ps[0], 1)
	o, _ = s.Cart(c.ID)
	if _, err := s.Confirm(o); err != nil {
		t.Fatal(err)
	}
	s.Log(c.Phone, "in", "hello", "", "")
	s.Handoff(c.Phone, "help", "test")
	s.DB.Exec(`INSERT INTO blocks(at) VALUES ('2026-12-01 10:00')`)
	for _, d := range []string{"invoices", "receipts", "images", "uploads"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
		os.WriteFile(filepath.Join(dir, d, "x.png"), []byte("x"), 0o644)
	}
	s.Set("port", "2011")
	s.Set("setup_done", "1")
	s.Set("telegram_token", "123:abc")
	s.Set("upi_id", "shop@ok")
	return app, app.Routes()
}

func soPost(h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rr, req)
	return rr
}

func count(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestClearTestDataKeepsShop(t *testing.T) {
	app, h := startOverApp(t)
	s := app.S
	nProd := count(t, s, "products")
	rr := soPost(h, "/start-over/clear", url.Values{"confirm": {"Test Shop"}})
	loc := rr.Header().Get("Location")
	if rr.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/orders?msg=") || !strings.Contains(loc, "backups") {
		t.Fatalf("redirect: %d %s", rr.Code, loc)
	}
	for _, tb := range []string{"orders", "order_items", "invoices", "customers", "chat_log", "handoffs", "blocks"} {
		if n := count(t, s, tb); n != 0 {
			t.Fatalf("%s still has %d rows", tb, n)
		}
	}
	if count(t, s, "products") != nProd || s.Get("upi_id") != "shop@ok" || s.Get("telegram_token") != "123:abc" || s.Get("setup_done") != "1" {
		t.Fatal("products or settings were touched")
	}
	if exists(filepath.Join(app.Dir, "invoices")) || exists(filepath.Join(app.Dir, "receipts")) || !exists(filepath.Join(app.Dir, "images", "x.png")) {
		t.Fatal("wrong files removed")
	}
	// order numbers start again at 1042
	c, _ := s.Customer("+31600000078", "Next", "chat-78")
	if o, _ := s.EnsureCart(c.ID); o.ID != 1042 {
		t.Fatalf("next order id %d, want 1042", o.ID)
	}
}

func TestResetShopEmptiesAllButPort(t *testing.T) {
	app, h := startOverApp(t)
	s := app.S
	rr := soPost(h, "/start-over/reset", url.Values{"confirm": {"CLEAR"}})
	loc := rr.Header().Get("Location")
	if rr.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/setup/1?msg=") {
		t.Fatalf("redirect: %d %s", rr.Code, loc)
	}
	for _, tb := range []string{"orders", "customers", "chat_log", "products", "faq", "slots"} {
		if n := count(t, s, tb); n != 0 {
			t.Fatalf("%s still has %d rows", tb, n)
		}
	}
	if s.Get("port") != "2011" || s.Get("upi_id") != "" || s.Get("shop_name") != "My Shop" {
		t.Fatalf("settings not reset: port=%q upi=%q shop=%q", s.Get("port"), s.Get("upi_id"), s.Get("shop_name"))
	}
	if s.Get("telegram_token") != "123:abc" {
		t.Fatal("channels must stay connected unless the box is ticked")
	}
	if exists(filepath.Join(app.Dir, "images")) || exists(filepath.Join(app.Dir, "uploads")) {
		t.Fatal("photos or QR left behind")
	}
	if st := app.File.Status(); !st.Exists || st.Products != 0 {
		t.Fatalf("products.xlsx not an empty template: %+v", st)
	}
	backups, _ := filepath.Glob(filepath.Join(app.Dir, "backups", "*", "ownshop.db"))
	xl, _ := filepath.Glob(filepath.Join(app.Dir, "backups", "*", "products.xlsx"))
	if len(backups) != 1 || len(xl) != 1 {
		t.Fatalf("backup missing: %v %v", backups, xl)
	}
	b, err := OpenStore(backups[0])
	if err != nil || count(t, b, "orders") != 1 {
		t.Fatal("backup does not hold the old orders")
	}
	// first-run setup shows again
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Header().Get("Location") != "/setup/1" {
		t.Fatalf("home goes to %q, want setup", rr.Header().Get("Location"))
	}
}

func TestResetDisconnect(t *testing.T) {
	app, h := startOverApp(t)
	app.S.Set("wa_accounts", "shop1")
	os.MkdirAll(filepath.Join(app.Dir, "wacli", "shop1"), 0o755)
	soPost(h, "/start-over/reset", url.Values{"confirm": {"CLEAR"}, "disconnect": {"on"}})
	if app.S.Get("telegram_token") != "" || app.S.Get("wa_accounts") != "" || exists(filepath.Join(app.Dir, "wacli", "shop1")) {
		t.Fatal("channels not disconnected")
	}
}

func TestStartOverWrongConfirmDoesNothing(t *testing.T) {
	app, h := startOverApp(t)
	for _, kind := range []string{"clear", "reset"} {
		rr := soPost(h, "/start-over/"+kind, url.Values{"confirm": {"yes"}})
		if !strings.HasPrefix(rr.Header().Get("Location"), "/start-over/"+kind+"?msg=") {
			t.Fatalf("%s: %s", kind, rr.Header().Get("Location"))
		}
	}
	if count(t, app.S, "orders") != 1 || exists(filepath.Join(app.Dir, "backups")) {
		t.Fatal("a wrong confirm changed something")
	}
}

func TestStartOverPages(t *testing.T) {
	_, h := startOverApp(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/start-over/reset", nil))
	body := rr.Body.String()
	if !strings.Contains(body, `name="disconnect" value="on">`) || strings.Contains(body, "checked") {
		t.Fatal("disconnect box must be there and unchecked")
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/settings", nil))
	if b := rr.Body.String(); !strings.Contains(b, "Delete this shop and start a new one") || !strings.Contains(b, `href="/settings#start-over"`) {
		t.Fatal("settings section or footer link missing")
	}
}
