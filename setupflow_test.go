package main

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestServicesSetupSkipsProducts(t *testing.T) {
	app, h := bookingApp(t) // clinic preset: sells=services
	get := func(p string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", p, nil))
		return rr
	}
	if loc := get("/setup/2").Header().Get("Location"); loc != "/setup/type?picked=1" {
		t.Fatalf("/setup/2 for services: %q", loc)
	}
	b := get("/setup/type?picked=1").Body.String()
	if !strings.Contains(b, `<span>1. Shop</span>`) && !strings.Contains(b, `<span >1. Shop</span>`) {
		t.Fatalf("steps: %s", b[strings.Index(b, `class="steps"`):strings.Index(b, `class="steps"`)+300])
	}
	for _, want := range []string{`<span class="on">2. Services</span>`, "3. Payment", "4. Chat channels"} {
		if !strings.Contains(b, want) {
			t.Fatalf("booking step lacks %q", want)
		}
	}
	if b := get("/setup/3").Body.String(); !strings.Contains(b, `<a href="/setup/type?picked=1">2. Services</a>`) || strings.Contains(b, "Products</span>") {
		t.Fatal("payment step: 2. Services should link back")
	}
	// Setup completes with no products upload.
	soPost(h, "/setup/3", url.Values{"pay_method": {"upi"}, "upi_id": {"clinic@okicici"}})
	soPost(h, "/setup/done", url.Values{})
	if !app.setupDone() || get("/").Header().Get("Location") != "/orders" {
		t.Fatal("setup not done without an upload")
	}
	if !strings.Contains(get("/orders").Body.String(), `<a href="/products" >Services</a>`) {
		t.Fatal("nav should say Services")
	}
}

func TestStepLabelsAndPlurals(t *testing.T) {
	app, h := bookingApp(t)
	get := func(p string) string {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", p, nil))
		return rr.Body.String()
	}
	app.S.Set("sells", "products")
	if b := get("/setup/2"); !strings.Contains(b, "2. Products") || strings.Contains(b, "already set") {
		t.Fatal("products mode changed")
	}
	if !strings.Contains(get("/orders"), `<a href="/products" >Products</a>`) {
		t.Fatal("nav for products")
	}
	eq(t, "1", plural(1, "service"), "1 service")
	eq(t, "0", plural(0, "product"), "0 products")
	app.S.DB.Exec(`UPDATE products SET active=0 WHERE kind<>'service' AND id<>(SELECT MIN(id) FROM products WHERE kind<>'service')`)
	if s := app.haveText(); s != "1 product" {
		t.Fatalf("haveText %q", s)
	}
}

func TestPaymentChooseOne(t *testing.T) {
	app, h := bookingApp(t)
	app.S.Set("pay_link", "https://pay.example/old")
	app.S.Set("pay_note", "Bank X")
	app.S.Set("qr_path", "")
	// Every field is posted (no JS hides them); only the chosen method is saved.
	soPost(h, "/setup/3", url.Values{"pay_method": {"upi"}, "upi_id": {"shop@okhdfc"}, "upi_name": {""},
		"pay_link": {"https://changed.example"}, "pay_note": {"changed"}})
	if app.S.Get("upi_id") != "shop@okhdfc" || app.S.Get("pay_link") != "https://pay.example/old" || app.S.Get("pay_note") != "Bank X" {
		t.Fatalf("saved %q %q %q", app.S.Get("upi_id"), app.S.Get("pay_link"), app.S.Get("pay_note"))
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/setup/3", nil))
	if !strings.Contains(rr.Body.String(), `name="pay_method" value="upi" checked`) {
		t.Fatal("UPI should be the default for INR")
	}
	app.S.Set("currency", "EUR")
	if f := app.payForm("", true); f.Method != "link" {
		t.Fatalf("default for EUR: %s", f.Method)
	}
	// Settings lists what is set, with Edit and Remove.
	app.S.Set("sells", "services")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/settings", nil))
	for _, want := range []string{`<strong>UPI</strong><span class="muted">shop@okhdfc</span>`, `href="/settings?pay=bank#payment">Edit</a>`, `name="pay_remove" value="link"`, "+ Add another way to pay"} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("settings lacks %q", want)
		}
	}
	soPost(h, "/settings", url.Values{"pay_remove": {"link"}})
	if app.S.Get("pay_link") != "" || app.S.Get("upi_id") == "" {
		t.Fatal("remove")
	}
}

func TestMigrateBothAndIgnoreOtherKind(t *testing.T) {
	s, dir := setup(t) // the sample: 15 products
	path := filepath.Join(dir, "t.db")
	s.Set("sells", "both")
	s.DB.Close()
	s, _ = OpenStore(path)
	if s.Get("sells") != "products" {
		t.Fatalf("both with products -> %s", s.Get("sells"))
	}
	s.UpsertProduct(Product{SKU: "SVC-X", Name: "Massage", Kind: KindService, Minutes: 60})
	s.DB.Exec(`UPDATE products SET active=0 WHERE kind<>'service'`)
	s.Set("sells", "both")
	s.DB.Close()
	s, _ = OpenStore(path)
	defer s.DB.Close()
	if s.Get("sells") != "services" {
		t.Fatalf("both with only services -> %s", s.Get("sells"))
	}

	// A products shop ignores service rows everywhere, and the page says so.
	app, h := bookingApp(t) // clinic: one service row next to the 15 sample products
	app.S.Set("sells", "products")
	app.Bot.S = app.S
	if ps, _ := app.S.Sellable(); len(ps) != 15 {
		t.Fatalf("sellable %d", len(ps))
	}
	if app.Bot.matchProducts("consultation") != nil {
		t.Fatal("search finds a service in a products shop")
	}
	if app.findProduct("SVC-CON") != nil {
		t.Fatal("UCP finds a service in a products shop")
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/products", nil))
	if b := rr.Body.String(); !strings.Contains(b, "1 service row in your Excel is ignored in a products shop.") || strings.Contains(b, ">Consultation<") {
		t.Fatal("products page: ignored note or hidden row")
	}
	app.S.Set("sells", "services")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/products", nil))
	if !strings.Contains(rr.Body.String(), "15 product rows in your Excel are ignored in a services shop.") {
		t.Fatal("services page note")
	}
}

func TestCatalogShowsTwentyThenMore(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 10; i++ {
		h.s.UpsertProduct(Product{SKU: fmt.Sprintf("X%d", i), Name: fmt.Sprintf("Zz item %d", i), Price: 1, Stock: 3})
	}
	got := h.say(bad, "what products do you have")
	if !strings.Contains(got, "\n20. ") || strings.Contains(got, "\n21. ") || !strings.Contains(got, "and 5 more, ask for an item by name.") {
		t.Fatalf("catalog %q", got)
	}
}
