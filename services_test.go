package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Friday 20 Nov 2026, 12:10 local.
var fri = time.Date(2026, 11, 20, 12, 10, 0, 0, time.Local)

// svcHarness: a shop that sells services, open Mon-Sat 10-12 and 14-16 (a lunch break), 30-min slots.
func svcHarness(t *testing.T, sells string) *harness {
	h := newHarness(t)
	h.b.Now = func() time.Time { return fri }
	h.s.Set("sells", sells)
	h.s.Set("work_days", "Mon-Sat")
	h.s.Set("open_hours", "10:00-12:00, 14:00-16:00")
	h.s.Set("slot_minutes", "30")
	h.s.Set("days_ahead", "7")
	h.s.UpsertProduct(Product{SKU: "SVC-CUT", Name: "Haircut", Price: 300, Stock: 0, Kind: KindService, Minutes: 30, Keywords: "haircut, hair"})
	h.s.UpsertProduct(Product{SKU: "SVC-COL", Name: "Hair colour", Price: 900, Stock: 0, Kind: KindService, Minutes: 60, Keywords: "colour, color"})
	return h
}

func (h *harness) from(phone, raw, text string) string {
	h.b.M = stub(raw)
	return h.as(phone, Inbound{Text: text})
}

func addCut(h *harness, phone string) string {
	return h.from(phone, call("add_to_order", "product:"+e("haircut")+",quantity:1"), "1 haircut")
}

func TestScheduleSplitAndLunchBreak(t *testing.T) {
	sc := Schedule{Avail: legacyAvail("Mon-Sat", "09:00-13:00, 17:00-20:00"), Step: 60}
	eq(t, "starts", strings.Join(sc.Starts(fri), " "), "09:00 10:00 11:00 12:00 17:00 18:00 19:00")
	if sc.Starts(fri.AddDate(0, 0, 2)) != nil { // Sunday
		t.Fatal("Sunday is closed")
	}
	if d := parseDays("Tue-Sun"); d[1] || !d[0] || !d[2] {
		t.Fatalf("Tue-Sun: %v", d)
	}
	sc.Step = 30
	if got := sc.Starts(fri); len(got) != 14 || got[7] != "12:30" || got[8] != "17:00" {
		t.Fatalf("30-min split %v", got)
	}
}

func TestPastSlotsExcluded(t *testing.T) {
	h := svcHarness(t, "services")
	eq(t, "today", strings.Join(h.s.FreeTimes(fri, fri, 30, 0), " "), "14:00 14:30 15:00 15:30")
	// A 60-min service never starts at 11:30 (runs into lunch) or 15:30 (closing).
	eq(t, "60 min tomorrow", strings.Join(h.s.FreeTimes(fri.AddDate(0, 0, 1), fri, 60, 0), " "), "10:00 10:30 11:00 14:00 14:30 15:00")
}

func TestServiceStockZeroAddsAndWording(t *testing.T) {
	h := svcHarness(t, "services")
	if got := h.say(call("search_product", "query:"+e("haircut")), "haircut?"); !strings.Contains(got, "Haircut, 30 min, INR 300.00\nTo book, reply for example: book Haircut") || strings.Contains(got, "stock") {
		t.Fatalf("search %q", got)
	}
	got := h.say(call("add_to_order", "product:"+e("haircut")+",quantity:1"), "1 haircut")
	want := "Added 1 x Haircut.\nYour booking 1042:\n- 1 x Haircut = INR 300.00\nTotal: INR 300.00\nPick a time:\n1. Today (Fri 20)\n2. Tomorrow (Sat 21)\n3. Mon 23\nReply with a day number, or for example: tomorrow 16:00"
	eq(t, "cart", got, want)
	// A services shop ignores product rows: honey cannot be added.
	if got := h.say(call("add_to_order", "product:"+e("raw honey")+",quantity:1"), "1 raw honey"); strings.Contains(got, "honey") {
		t.Fatalf("product added in a services shop: %q", got)
	}
}

func TestPickDayThenTimeServicesNoAddress(t *testing.T) {
	h := svcHarness(t, "services")
	p := "+31622222221" // no saved address
	addCut(h, p)
	got := h.from(p, call("faq", ""), "2")
	if !strings.Contains(got, "Tomorrow (Sat 21), free times:\n1. 10:00  2. 10:30  3. 11:00  4. 11:30\n5. 14:00") {
		t.Fatalf("times %q", got)
	}
	got = h.from(p, call("faq", ""), "3")
	if !strings.Contains(got, "Time: Sat 21 Nov 11:00\nReply YES to confirm booking 1042.") {
		t.Fatalf("picked %q", got)
	}
	got = h.from(p, call("confirm_order", ""), "yes")
	if !strings.Contains(got, "Booking 1042 confirmed") || strings.Contains(got, "Where should we deliver") {
		t.Fatalf("services-only cart must confirm without an address: %q", got)
	}
	if h.s.FreeTimes(fri.AddDate(0, 0, 1), fri, 30, 0)[2] == "11:00" {
		t.Fatal("booked slot still offered")
	}
}

func TestDirectTimeReply(t *testing.T) {
	h := svcHarness(t, "services")
	p := "+31622222222"
	addCut(h, p)
	if got := h.from(p, call("faq", ""), "tomorrow 2pm"); !strings.Contains(got, "Time: Sat 21 Nov 14:00") {
		t.Fatalf("tomorrow 2pm: %q", got)
	}
	day, hm, ok := parseWhen("sat 16:00", fri)
	if !ok || hm != "16:00" || day.Day() != 21 {
		t.Fatalf("sat 16:00: %v %s %v", day, hm, ok)
	}
	if _, _, ok := parseWhen("add 2 haircut", fri); ok {
		t.Fatal("an order is not a time")
	}
}

func TestBookingConflictTwoCustomers(t *testing.T) {
	h := svcHarness(t, "services")
	a, b := "+31622222223", "+31622222224"
	addCut(h, a)
	addCut(h, b)
	h.from(a, call("faq", ""), "tomorrow 10:00")
	h.from(b, call("faq", ""), "tomorrow 10:00")
	if got := h.from(a, call("confirm_order", ""), "yes"); !strings.Contains(got, "confirmed") {
		t.Fatalf("a: %q", got)
	}
	got := h.from(b, call("confirm_order", ""), "yes")
	if !strings.Contains(got, "just booked by someone else") || strings.Contains(got, "confirmed") || !strings.Contains(got, "Pick a time") {
		t.Fatalf("b: %q", got)
	}
	// Cancelling A frees 10:00 again.
	h.s.Advance(1042, StCancel)
	if h.s.FreeTimes(fri.AddDate(0, 0, 1), fri, 30, 0)[0] != "10:00" {
		t.Fatal("cancel did not free the slot")
	}
}

func TestMultiSlotBlockMoveManual(t *testing.T) {
	h := svcHarness(t, "services")
	sat := fri.AddDate(0, 0, 1)
	id, err := h.s.ManualBooking("Walk In", "", "SVC-COL", "2026-11-21 10:00") // 60 min = 2 slots
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "after 60-min", strings.Join(h.s.FreeTimes(sat, fri, 30, 0), " "), "11:00 11:30 14:00 14:30 15:00 15:30")
	if _, err := h.s.ManualBooking("Other", "+3100", "Haircut", "2026-11-21 10:30"); err == nil {
		t.Fatal("double booking allowed")
	}
	h.s.Block("2026-11-21 14:00", true)
	eq(t, "blocked", strings.Join(h.s.FreeTimes(sat, fri, 30, 0), " "), "11:00 11:30 14:30 15:00 15:30")
	h.s.Block("2026-11-21 14:00", false)
	if len(h.s.FreeTimes(sat, fri, 30, 0)) != 6 {
		t.Fatal("unblock")
	}
	if err := h.s.MoveBooking(id, "2026-11-21 11:30"); err == nil {
		t.Fatal("60 min at 11:30 runs into lunch")
	}
	if err := h.s.MoveBooking(id, "2026-11-21 14:00"); err != nil {
		t.Fatal(err)
	}
	eq(t, "after move", strings.Join(h.s.FreeTimes(sat, fri, 30, 0), " "), "10:00 10:30 11:00 11:30 15:00 15:30")
	o, _ := h.s.Order(id)
	if o.Status != StAwaiting || o.Customer.Name != "Walk In" || o.BookMin != 60 {
		t.Fatalf("manual booking %+v", o)
	}
}

func TestPresetsFill(t *testing.T) {
	s, dir := setup(t)
	sf := NewShopFile(s, filepath.Join(dir, "products.xlsx"))
	if err := ApplyPreset(s, sf, "clinic"); err != nil {
		t.Fatal(err)
	}
	if s.Get("open_hours") != "09:00-18:00" || s.Get("slot_minutes") != "30" || s.Get("sells") != "services" {
		t.Fatalf("settings %s %s %s", s.Get("open_hours"), s.Get("slot_minutes"), s.Get("sells"))
	}
	if err := sf.Load(); err != nil { // the services live in the file, so a re-read keeps them
		t.Fatal(err)
	}
	ps, _ := s.Products()
	var n int
	for _, p := range ps {
		if p.IsService() {
			n++
		}
	}
	if n != 1 || len(ps) != 16 {
		t.Fatalf("services %d of %d", n, len(ps))
	}
}

func TestXlsxTypeColumn(t *testing.T) {
	s, dir := setup(t)
	path := filepath.Join(dir, "old.xlsx")
	writeWorkbook(path, []sheet{{sheetProducts, [][]any{{"sku", "name", "price", "stock"}, {"A1", "Apple", 1.5, 3}}}})
	if err := NewShopFile(s, path).Load(); err != nil {
		t.Fatal(err)
	}
	ps, _ := s.Products()
	if len(ps) != 1 || ps[0].IsService() {
		t.Fatalf("no Type column: %+v", ps)
	}
	writeWorkbook(path, []sheet{{sheetProducts, [][]any{{"sku", "name", "price", "stock", "Type", "Minutes"}, {"A1", "Apple", 1.5, 3, "", ""}, {"S1", "Massage", 40, 0, "Service", 45}}}})
	if err := NewShopFile(s, path).Load(); err != nil {
		t.Fatal(err)
	}
	ps, _ = s.Products()
	if len(ps) != 2 || ps[0].IsService() || !ps[1].IsService() || ps[1].Minutes != 45 {
		t.Fatalf("with Type column: %+v", ps)
	}
}

func TestCalendarPage(t *testing.T) {
	h := svcHarness(t, "services")
	h.s.ManualBooking("Asha", "", "SVC-CUT", "2026-11-21 10:00")
	h.s.Block("2026-11-21 14:00", true)
	app := &App{S: h.s, Bot: h.b, File: NewShopFile(h.s, filepath.Join(h.dir, "products.xlsx")), Dir: h.dir}
	srv := httptest.NewServer(app.Routes())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/calendar?week=2026-11-16")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	page := string(body)
	for _, want := range []string{"Sat 21 Nov", `<a class="who" href="/orders#order-1042">Asha</a>`, `<span class="what">Haircut · 10:00 AM</span>`, "Removed", "Restore", "2:00 PM", `>Services</a>`, `name="at" value="2026-11-21 15:00"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("calendar status %d lacks %q", res.StatusCode, want)
		}
	}
	if res.StatusCode != 200 || strings.Contains(page, `value="2026-11-21 12:00"`) {
		t.Fatalf("status %d or lunch slot shown", res.StatusCode)
	}
	// Unblock through the page.
	http.PostForm(srv.URL+"/calendar/block", url.Values{"at": {"2026-11-21 14:00"}, "on": {"0"}, "week": {"2026-11-16"}})
	if len(h.s.FreeTimes(fri.AddDate(0, 0, 1), fri, 30, 0)) != 7 {
		t.Fatal("unblock via page")
	}
}
