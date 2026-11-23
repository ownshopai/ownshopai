package main

import (
	"context"
	"encoding/csv"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// setCells edits the Products sheet the way the owner would in Excel.
func setCells(t *testing.T, path string, cells map[string]any) {
	t.Helper()
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for c, v := range cells {
		f.SetCellValue(sheetProducts, c, v)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func cell(t *testing.T, path, sheet, c string) string {
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, _ := f.GetCellValue(sheet, c)
	return v
}

func TestShopFileWatchStockBackOrdersCSVCancel(t *testing.T) {
	s, dir := setup(t)
	path := filepath.Join(dir, "products.xlsx")
	sf := NewShopFile(s, path)
	if err := sf.Load(); err != nil {
		t.Fatal(err)
	}
	if st := sf.Status(); st.Products != 15 || st.FAQ != 4 {
		t.Fatalf("status %+v", st)
	}
	csvPath := filepath.Join(dir, "orders.csv")
	s.AfterOrder = func() { sf.WriteStock(); WriteOrdersCSV(s, csvPath) }

	// the owner edits the file: the watcher picks it up
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sf.Watch(ctx, 50*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	setCells(t, path, map[string]any{"E2": 7}) // Raw honey 500g stock 20 -> 7
	os.Chtimes(path, time.Now().Add(time.Second), time.Now().Add(time.Second))
	deadline := time.Now().Add(3 * time.Second)
	var stock int
	for time.Now().Before(deadline) {
		s.DB.QueryRow(`SELECT stock FROM products WHERE name='Raw honey 500g'`).Scan(&stock)
		if stock == 7 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stock != 7 {
		t.Fatalf("file change not picked up: stock %d", stock)
	}
	cancel()

	// an order: stock goes down in the file too, and orders.csv gets a row
	c, _ := s.Customer("tg:77", "Emma", "77")
	s.SetCustomerChannel(c.ID, "telegram", "123")
	o, _ := s.EnsureCart(c.ID)
	ps, _ := s.Products()
	var honey Product
	for _, p := range ps {
		if p.Name == "Raw honey 500g" {
			honey = p
		}
	}
	s.AddItem(o.ID, honey, 2)
	o, _ = s.Order(o.ID)
	if _, err := s.Confirm(o); err != nil {
		t.Fatal(err)
	}
	if v := cell(t, path, sheetProducts, "E2"); v != "5" {
		t.Fatalf("stock not written back to the file: %q", v)
	}
	if v := cell(t, path, sheetFAQ, "A2"); !strings.Contains(v, "opening hours") {
		t.Fatalf("FAQ sheet damaged by the stock write: %q", v)
	}
	rows := readCSV(t, csvPath)
	if len(rows) != 2 || rows[1][0] != "1042" || rows[1][2] != "Emma tg:77" || rows[1][3] != "telegram" ||
		rows[1][4] != "2 x Raw honey 500g" || rows[1][5] != "19.00" || rows[1][6] != "Waiting for payment" || rows[1][7] != "INV-1042" {
		t.Fatalf("orders.csv: %v", rows)
	}

	// cancel: items back in stock, file and csv follow
	if _, err := s.Advance(o.ID, StCancel); err != nil {
		t.Fatal(err)
	}
	if v := cell(t, path, sheetProducts, "E2"); v != "7" {
		t.Fatalf("cancel did not restore stock in the file: %q", v)
	}
	if rows := readCSV(t, csvPath); rows[1][6] != "Cancelled" {
		t.Fatalf("csv after cancel: %v", rows[1])
	}
	if _, err := s.Advance(o.ID, StCancel); err == nil {
		t.Fatal("cancelled twice")
	}

	// a product removed from the file is no longer sold
	f, _ := excelize.OpenFile(path)
	f.RemoveRow(sheetProducts, 2)
	f.Save()
	f.Close()
	sf.Load()
	for _, p := range mustProducts(t, s) {
		if p.Name == "Raw honey 500g" {
			t.Fatal("removed product still sold")
		}
	}
}

func mustProducts(t *testing.T, s *Store) []Product {
	ps, err := s.Products()
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func readCSV(t *testing.T, p string) [][]string {
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestStockKeptWhenFileCannotBeWritten(t *testing.T) {
	s, dir := setup(t)
	path := filepath.Join(dir, "products.xlsx")
	sf := NewShopFile(s, path)
	sf.Load()
	s.DB.Exec(`UPDATE products SET stock=1 WHERE name='Raw honey 500g'`)
	os.Chmod(path, 0o444) // like a file locked by Excel
	defer os.Chmod(path, 0o644)
	if sf.WriteStock() == nil && os.Getuid() != 0 {
		t.Skip("cannot simulate a locked file here")
	}
	sf.Load() // the file still says 20; the shop must not oversell
	var stock int
	s.DB.QueryRow(`SELECT stock FROM products WHERE name='Raw honey 500g'`).Scan(&stock)
	if stock != 1 || !strings.Contains(sf.Status().Err, "stock not written") {
		t.Fatalf("stock %d, status %q", stock, sf.Status().Err)
	}
	os.Chmod(path, 0o644)
	sf.Load() // writable again: the pending stock goes into the file first
	if v := cell(t, path, sheetProducts, "E2"); v != "1" {
		t.Fatalf("pending stock not written: %q", v)
	}
}

func post(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rr, req)
	return rr
}

func TestWizard(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(filepath.Join(dir, "w.db"))
	hub := NewHub(nil)
	app := &App{S: s, Bot: &Bot{S: s, Send: hub}, Hub: hub, File: NewShopFile(s, filepath.Join(dir, "products.xlsx")), Dir: dir}
	h := app.Routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Header().Get("Location") != "/setup/1" {
		t.Fatalf("first run goes to %q", rr.Header().Get("Location"))
	}
	if loc := post(t, h, "/setup/1", url.Values{"shop_name": {"Green Leaf"}, "currency": {"eur"}}).Header().Get("Location"); loc != "/setup/2" {
		t.Fatalf("step 1 -> %q", loc)
	}
	if s.Get("shop_name") != "Green Leaf" || s.Get("currency") != "EUR" {
		t.Fatal("shop not saved")
	}

	// step 2: upload the example workbook
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/sample.xlsx", nil))
	var body strings.Builder
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "my products.xlsx")
	fw.Write(rr.Body.Bytes())
	mw.Close()
	req := httptest.NewRequest("POST", "/setup/2", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if loc := rr.Header().Get("Location"); !strings.HasPrefix(loc, "/setup/3") || !strings.Contains(loc, "15+products") {
		t.Fatalf("step 2 -> %q", loc)
	}
	if _, err := os.Stat(filepath.Join(dir, "products.xlsx")); err != nil {
		t.Fatal("products.xlsx not kept in the data folder")
	}

	// step 3: a bad link is refused, a good one saved
	if loc := post(t, h, "/setup/3", url.Values{"pay_link": {"javascript:alert(1)"}}).Header().Get("Location"); !strings.HasPrefix(loc, "/setup/3") {
		t.Fatalf("bad link accepted: %q", loc)
	}
	post(t, h, "/setup/3", url.Values{"pay_link": {"https://pay.example.test/x"}})
	if s.Get("pay_link") != "https://pay.example.test/x" {
		t.Fatal("link not saved")
	}

	// step 4: Telegram token is masked on the page; then finish
	post(t, h, "/channels/telegram?ret=setup", url.Values{"token": {fakeToken}})
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/setup/4", nil))
	if strings.Contains(rr.Body.String(), "FAKE-test-token") || !strings.Contains(rr.Body.String(), "123456:****") {
		t.Fatal("token not masked on the page")
	}
	if !strings.Contains(rr.Body.String(), "Connect WhatsApp") || !strings.Contains(rr.Body.String(), "@BotFather") {
		t.Fatal("step 4 missing a channel")
	}
	hub.Close()
	post(t, h, "/setup/done", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Header().Get("Location") != "/orders" {
		t.Fatalf("after setup / goes to %q", rr.Header().Get("Location"))
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/health", nil))
	if rr.Body.String() != appID {
		t.Fatal("health")
	}
}

func TestInstallLoginItemAndShortcut(t *testing.T) {
	for goos, want := range map[string][]string{
		"darwin":  {"Library/LaunchAgents/com.deemwar.ownshop.plist", "Desktop/OwnShop AI.webloc"},
		"windows": {"AppData/Roaming/Microsoft/Windows/Start Menu/Programs/Startup/ownshop.cmd", "Desktop/OwnShop AI.url"},
	} {
		home := t.TempDir()
		w, err := Install(home, "/Applications/ownshop.app/Contents/MacOS/ownshop", goos)
		if err != nil || len(w) != 2 {
			t.Fatalf("%s: %v %v", goos, w, err)
		}
		for _, p := range want {
			b, err := os.ReadFile(filepath.Join(home, p))
			if err != nil {
				t.Fatalf("%s: %s missing", goos, p)
			}
			if !strings.Contains(string(b), "--background") && !strings.Contains(string(b), appURL) {
				t.Fatalf("%s: %s: %s", goos, p, b)
			}
		}
		if w, _ := Install(home, "/Applications/ownshop.app/Contents/MacOS/ownshop", goos); len(w) != 0 {
			t.Fatalf("%s: second run rewrote %v", goos, w)
		}
	}
}

func TestSecondStartOnlyOpensTheBrowser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(appID)) }))
	defer srv.Close()
	if !running(srv.URL) {
		t.Fatal("a running ownshop was not detected")
	}
	if running("http://127.0.0.1:1") {
		t.Fatal("nothing listens there")
	}
}
