package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// bookingForm is the booking step with the given days on, each 09:00-20:00, and two services.
func bookingForm(days ...string) url.Values {
	v := url.Values{"hours_form": {"1"}, "day_on": days, "slot_minutes": {"30"}, "days_ahead": {"14"},
		"min_notice": {"0"}, "buffer_minutes": {"0"}, "max_per_day": {"0"}, "ov_idx": {"__o__"}, "ov_date___o__": {""},
		"svc_idx": {"0", "n1"}, "svc_name_0": {"Consultation"}, "svc_min_0": {"15"}, "svc_price_0": {"500"},
		"svc_name_n1": {"Hot stone massage"}, "svc_min_n1": {"90"}, "svc_price_n1": {"1200.50"}}
	for _, d := range weekOrder {
		v.Set("from_"+d, "09:00")
		v.Set("to_"+d, "20:00")
	}
	return v
}

func bookingApp(t *testing.T) (*App, http.Handler) {
	s, dir := setup(t)
	app := &App{S: s, Bot: &Bot{S: s}, File: NewShopFile(s, filepath.Join(dir, "products.xlsx")), Dir: dir}
	if err := ApplyPreset(s, app.File, "clinic"); err != nil {
		t.Fatal(err)
	}
	return app, app.Routes()
}

func TestBookingFormStores(t *testing.T) {
	app, h := bookingApp(t)
	f := bookingForm("Mon", "Tue", "Wed", "Thu", "Fri", "Sat")
	f["from_Mon"], f["to_Mon"] = []string{"14:30", "09:00"}, []string{"20:00", "13:00"} // two ranges, out of order
	f["from_Sat"], f["to_Sat"] = []string{"10:00"}, []string{"14:00"}
	f.Set("min_notice", "120")
	f.Set("buffer_minutes", "10")
	f.Set("max_per_day", "8")
	f["ov_idx"] = []string{"0", "1", "__o__"}
	f.Set("ov_date_0", "2026-12-25")
	f.Set("ov_off_0", "1")
	f.Set("ov_date_1", "2026-12-24")
	f["ov_from_1"], f["ov_to_1"] = []string{"09:00"}, []string{"12:00"}
	rr := soPost(h, "/setup/hours", f)
	if rr.Header().Get("Location") != "/setup/3" {
		t.Fatalf("redirect %s", rr.Header().Get("Location"))
	}
	s := app.S
	sc := s.Schedule()
	if rangesText(sc.Week[1]) != "9:00 AM–1:00 PM, 2:30 PM–8:00 PM" || rangesText(sc.Week[6]) != "10:00 AM–2:00 PM" || len(sc.Week[0]) != 0 {
		t.Fatalf("week %v", sc.Week)
	}
	if sc.Notice != 120 || sc.Buffer != 10 || sc.MaxDay != 8 || sc.Step != 30 || sc.Ahead != 14 {
		t.Fatalf("rules %+v", sc)
	}
	if rs, ok := sc.Over["2026-12-25"]; !ok || len(rs) != 0 || rangesText(sc.Over["2026-12-24"]) != "9:00 AM–12:00 PM" {
		t.Fatalf("overrides %v", sc.Over)
	}
	eq(t, "summary", sc.Avail.Summary(sc.Step, sc.Ahead), "Mon 9:00 AM–1:00 PM, 2:30 PM–8:00 PM; Tue–Fri 9:00 AM–8:00 PM; Sat 10:00 AM–2:00 PM · every 30 min, up to 2 weeks ahead · 2 hours notice · 10 min between bookings · at most 8 a day · 2 date overrides")
	ps, _ := s.Products()
	got := map[string]Product{}
	for _, p := range ps {
		if p.IsService() {
			got[p.Name] = p
		}
	}
	if len(got) != 2 || got["Consultation"].Price != 500 || got["Consultation"].Minutes != 15 || got["Hot stone massage"].Minutes != 90 || got["Hot stone massage"].SKU != "SVC-HOTSTONEMA" {
		t.Fatalf("services %+v", got) // Follow-up and Health check were removed, the massage added
	}
}

func TestCopyToAllDays(t *testing.T) {
	app, h := bookingApp(t)
	f := bookingForm("Mon")
	f["from_Mon"], f["to_Mon"] = []string{"09:00", "14:00"}, []string{"12:00", "18:00"}
	f.Set("copy_from", "Mon")
	loc, _ := url.QueryUnescape(soPost(h, "/setup/hours", f).Header().Get("Location"))
	if !strings.HasPrefix(loc, "/setup/type?picked=1&msg=Copied Mon to all days") {
		t.Fatalf("copy should stay on the step: %s", loc)
	}
	sc := app.S.Schedule()
	for d := 0; d < 7; d++ {
		if rangesText(sc.Week[d]) != "9:00 AM–12:00 PM, 2:00 PM–6:00 PM" {
			t.Fatalf("day %d: %v", d, sc.Week[d])
		}
	}
	// Settings: same button, back to Settings
	app.S.Set("sells", "services")
	f = bookingForm("Tue")
	f.Set("copy_from", "Tue")
	if loc := soPost(h, "/settings", f).Header().Get("Location"); !strings.Contains(loc, "Copied+Tue") {
		t.Fatalf("settings copy: %s", loc)
	}
	if rangesText(app.S.Schedule().Week[0]) != "9:00 AM–8:00 PM" {
		t.Fatal("settings copy did not fill Sunday")
	}
}

func TestBookingFormErrors(t *testing.T) {
	app, h := bookingApp(t)
	before := app.S.Get("availability")
	for name, c := range map[string]struct {
		f    func(url.Values)
		want string
	}{
		"no day":     {func(v url.Values) { v.Del("day_on") }, "at least one day"},
		"ends early": {func(v url.Values) { v.Set("to_Mon", "08:00") }, "end after it starts"},
		"overlap": {func(v url.Values) {
			v["from_Mon"], v["to_Mon"] = []string{"09:00", "11:00"}, []string{"12:00", "13:00"}
		}, "overlap"},
		"no ranges":    {func(v url.Values) { v.Del("from_Mon"); v.Del("to_Mon") }, "Add hours for Mon"},
		"bad slot":     {func(v url.Values) { v.Set("slot_minutes", "7") }, "slot length"},
		"bad notice":   {func(v url.Values) { v.Set("min_notice", "33") }, "minimum notice"},
		"bad buffer":   {func(v url.Values) { v.Set("buffer_minutes", "7") }, "between bookings"},
		"bad max":      {func(v url.Values) { v.Set("max_per_day", "3") }, "bookings a day"},
		"override bad": {func(v url.Values) { v.Set("ov_date___o__", "christmas") }, "Pick a date"},
		"override empty": {func(v url.Values) {
			v.Set("ov_date___o__", "2026-12-24")
			v.Del("ov_from___o__")
		}, "Give Thu 24 Dec its hours"},
	} {
		f := bookingForm("Mon")
		c.f(f)
		loc, _ := url.QueryUnescape(soPost(h, "/setup/hours", f).Header().Get("Location"))
		if !strings.HasPrefix(loc, "/setup/type?picked=1&msg=") || !strings.Contains(loc, c.want) {
			t.Fatalf("%s: %s", name, loc)
		}
	}
	if app.S.Get("availability") != before {
		t.Fatal("a rejected form changed the settings")
	}
	// Settings uses the same controls and checks
	app.S.Set("sells", "services")
	f := bookingForm("Sat", "Sun")
	f.Set("to_Sat", "09:00")
	if loc := soPost(h, "/settings", f).Header().Get("Location"); !strings.Contains(loc, "end+after") {
		t.Fatalf("settings: %s", loc)
	}
	f.Set("to_Sat", "18:00")
	soPost(h, "/settings", f)
	sc := app.S.Schedule()
	if rangesText(sc.Week[6]) != "9:00 AM–6:00 PM" || len(sc.Week[1]) != 0 {
		t.Fatalf("settings stored %v", sc.Week)
	}
}

func TestBookingPageControls(t *testing.T) {
	app, h := bookingApp(t)
	get := func(path string) string {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		return rr.Body.String()
	}
	// A preset is simple: one service, Opens/Closes and day chips, Advanced closed.
	for _, path := range []string{"/setup/type?picked=1", "/settings"} {
		b := get(path)
		for _, want := range []string{`<select name="opens"><option`, `<option value="09:00" selected>9:00 AM</option>`, `<option value="18:00" selected>6:00 PM</option>`,
			`name="day" value="Mon" checked`, `name="day" value="Sun" >`, `<details class="adv" id="adv"><summary>Advanced</summary>`,
			`name="day_on" value="Mon" checked`, `name="slot_minutes" value="30" checked`, `name="days_ahead" value="14" checked`,
			`name="min_notice" value="0" checked`, `name="buffer_minutes" value="0" checked`, `name="max_per_day" value="0" checked`,
			"Copy to all days", "+ Add a date override", `id="ovtpl"`, "Mon–Sat 9:00 AM–6:00 PM · every 30 min, up to 2 weeks ahead"} {
			if !strings.Contains(b, want) {
				t.Fatalf("%s lacks %q", path, want)
			}
		}
	}
	b := get("/setup/type?picked=1")
	for _, want := range []string{"Looks good, continue", "Choose a different business type", "+ Add service", `name="svc_name_0" value="Consultation"`, `name="svc_min_0" value="30" checked`, `id="svctpl"`} {
		if !strings.Contains(b, want) {
			t.Fatalf("setup lacks %q", want)
		}
	}
	// A schedule with a break, or an override, opens Advanced and locks the simple fields.
	app.S.Set("sells", "services")
	app.S.SetOverride("2026-12-25", nil)
	b = get("/settings")
	for _, want := range []string{`<div class="simple" hidden>`, `<select name="opens" disabled>`, `<details class="adv" id="adv" open>`, `name="ov_date_0" value="2026-12-25"`, `name="ov_off_0" value="1" checked`} {
		if !strings.Contains(b, want) {
			t.Fatalf("complex settings lacks %q", want)
		}
	}
}

func TestSimpleBookingPost(t *testing.T) {
	app, h := bookingApp(t)
	f := url.Values{"hours_form": {"1"}, "opens": {"10:00"}, "closes": {"17:30"}, "day": {"Mon", "Wed", "Sat"},
		"slot_minutes": {"30"}, "days_ahead": {"14"}, "svc_idx": {"0"}, "svc_name_0": {"Consultation"}, "svc_min_0": {"30"}, "svc_price_0": {"400"},
		"day_on": {"Tue"}, "from_Tue": {"08:00"}, "to_Tue": {"09:00"}} // the hidden Advanced rows do not count
	if loc := soPost(h, "/setup/hours", f).Header().Get("Location"); loc != "/setup/3" {
		t.Fatalf("redirect %s", loc)
	}
	sc := app.S.Schedule()
	for d, want := range map[int]string{1: "10:00 AM–5:30 PM", 3: "10:00 AM–5:30 PM", 6: "10:00 AM–5:30 PM", 2: "", 0: ""} {
		if rangesText(sc.Week[d]) != want {
			t.Fatalf("day %d: %v", d, sc.Week[d])
		}
	}
	f.Set("closes", "09:00")
	loc, _ := url.QueryUnescape(soPost(h, "/setup/hours", f).Header().Get("Location"))
	if !strings.Contains(loc, "Closing time must be after opening time") {
		t.Fatalf("simple error: %s", loc)
	}
}

func TestSwitchPresetReplacesServices(t *testing.T) {
	app, h := bookingApp(t) // clinic
	soPost(h, "/setup/type", url.Values{"preset": {"other"}})
	soPost(h, "/setup/type", url.Values{"preset": {"salon"}})
	names := func() []string {
		ps, _ := app.S.Products()
		var out []string
		for _, p := range ps {
			if p.IsService() {
				out = append(out, p.Name)
			}
		}
		return out
	}
	if got := strings.Join(names(), ","); got != "Haircut" {
		t.Fatalf("services after switching: %s", got)
	}
	if err := app.File.Load(); err != nil { // products.xlsx holds no stale rows either
		t.Fatal(err)
	}
	if got := strings.Join(names(), ","); got != "Haircut" {
		t.Fatalf("services in products.xlsx: %s", got)
	}
	// A service the owner priced stays.
	app.S.DB.Exec(`UPDATE products SET price=250 WHERE sku='SVC-CUT'`)
	app.File.PutRows([]Product{{SKU: "SVC-CUT", Name: "Haircut", Kind: KindService, Minutes: 30, Price: 250, Keywords: "haircut"}})
	soPost(h, "/setup/type", url.Values{"preset": {"clinic"}})
	if got := strings.Join(names(), ","); got != "Consultation,Haircut" && got != "Haircut,Consultation" {
		t.Fatalf("priced service lost: %s", got)
	}
}
