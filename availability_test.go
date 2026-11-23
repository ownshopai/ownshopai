package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func setAvail(t *testing.T, h *harness, f func(*Avail)) {
	a := h.s.Avail()
	f(&a)
	if err := h.s.SetAvail(a); err != nil {
		t.Fatal(err)
	}
}

func TestPerDayRanges(t *testing.T) {
	h := svcHarness(t, "services")
	setAvail(t, h, func(a *Avail) {
		a.Week[6] = [][2]int{{9 * 60, 10 * 60}, {13 * 60, 14*60 + 30}} // Sat: two ranges
		a.Week[1] = [][2]int{{17 * 60, 18 * 60}}                       // Mon: evening only
		a.Week[0] = [][2]int{{11 * 60, 12 * 60}}                       // Sun: now open
	})
	sat, sun, mon := fri.AddDate(0, 0, 1), fri.AddDate(0, 0, 2), fri.AddDate(0, 0, 3)
	eq(t, "sat", strings.Join(h.s.FreeTimes(sat, fri, 30, 0), " "), "09:00 09:30 13:00 13:30 14:00")
	eq(t, "sun", strings.Join(h.s.FreeTimes(sun, fri, 30, 0), " "), "11:00 11:30")
	eq(t, "mon", strings.Join(h.s.FreeTimes(mon, fri, 30, 0), " "), "17:00 17:30")
	// A 60-minute service needs an hour inside one range.
	eq(t, "sat 60", strings.Join(h.s.FreeTimes(sat, fri, 60, 0), " "), "09:00 13:00 13:30")
	// Ranges that touch are still separate: a 60-minute service does not straddle them.
	setAvail(t, h, func(a *Avail) { a.Week[6] = [][2]int{{9 * 60, 10 * 60}, {10 * 60, 11 * 60}} })
	eq(t, "touching", strings.Join(h.s.FreeTimes(sat, fri, 60, 0), " "), "09:00 10:00")
}

func TestOverrideUnavailableAndCustom(t *testing.T) {
	h := svcHarness(t, "services")
	sat, mon := fri.AddDate(0, 0, 1), fri.AddDate(0, 0, 3)
	h.s.SetOverride("2026-11-21", nil)
	if got := h.s.FreeTimes(sat, fri, 30, 0); len(got) != 0 {
		t.Fatalf("closed Saturday offers %v", got)
	}
	for _, d := range h.s.OfferDays(fri, 30, 5) {
		if d.Date == "2026-11-21" {
			t.Fatal("a closed date is offered")
		}
	}
	h.s.SetOverride("2026-11-23", [][2]int{{18 * 60, 19 * 60}}) // special hours on Monday
	eq(t, "special", strings.Join(h.s.FreeTimes(mon, fri, 30, 0), " "), "18:00 18:30")
	h.s.SetOverride("2026-11-22", [][2]int{{10 * 60, 11 * 60}}) // a Sunday opened for once
	eq(t, "sunday", strings.Join(h.s.FreeTimes(fri.AddDate(0, 0, 2), fri, 30, 0), " "), "10:00 10:30")
	h.s.DelOverride("2026-11-21")
	if len(h.s.FreeTimes(sat, fri, 30, 0)) != 8 {
		t.Fatal("deleting the override did not bring back the usual hours")
	}
}

func TestMinimumNotice(t *testing.T) {
	h := svcHarness(t, "services")
	setAvail(t, h, func(a *Avail) { a.Notice = 120 }) // now is 12:10
	eq(t, "2h notice", strings.Join(h.s.FreeTimes(fri, fri, 30, 0), " "), "14:30 15:00 15:30")
	setAvail(t, h, func(a *Avail) { a.Notice = 1440 })
	eq(t, "1 day notice today", strings.Join(h.s.FreeTimes(fri, fri, 30, 0), " "), "")
	eq(t, "1 day notice tomorrow", strings.Join(h.s.FreeTimes(fri.AddDate(0, 0, 1), fri, 30, 0), " "), "14:00 14:30 15:00 15:30")
	// An agent over UCP gets the same rule.
	app := &App{S: h.s, Bot: h.b}
	if app.bookable(h.s.Schedule(), "2026-11-21 10:00", 30) || !app.bookable(h.s.Schedule(), "2026-11-21 14:00", 30) {
		t.Fatal("UCP ignores the minimum notice")
	}
}

func TestBufferBetweenBookings(t *testing.T) {
	h := svcHarness(t, "services")
	setAvail(t, h, func(a *Avail) { a.Buffer = 10 })
	if _, err := h.s.ManualBooking("Asha", "", "SVC-CUT", "2026-11-21 10:30"); err != nil {
		t.Fatal(err)
	}
	// 10:00 would end at 10:30, touching Asha with no gap; 11:00 starts right after her.
	eq(t, "buffer", strings.Join(h.s.FreeTimes(fri.AddDate(0, 0, 1), fri, 30, 0), " "), "11:30 14:00 14:30 15:00 15:30")
	if _, err := h.s.ManualBooking("Ravi", "", "SVC-CUT", "2026-11-21 11:00"); err == nil {
		t.Fatal("a booking inside the buffer was allowed")
	}
	setAvail(t, h, func(a *Avail) { a.Buffer = 0 })
	eq(t, "no buffer", strings.Join(h.s.FreeTimes(fri.AddDate(0, 0, 1), fri, 30, 0), " "), "10:00 11:00 11:30 14:00 14:30 15:00 15:30")
}

func TestMaxPerDay(t *testing.T) {
	h := svcHarness(t, "services")
	setAvail(t, h, func(a *Avail) { a.MaxDay = 2 })
	sat := fri.AddDate(0, 0, 1)
	h.s.ManualBooking("A", "", "SVC-CUT", "2026-11-21 10:00")
	if len(h.s.FreeTimes(sat, fri, 30, 0)) == 0 {
		t.Fatal("one booking already fills the day")
	}
	h.s.ManualBooking("B", "", "SVC-CUT", "2026-11-21 14:00")
	if got := h.s.FreeTimes(sat, fri, 30, 0); len(got) != 0 {
		t.Fatalf("day full but offers %v", got)
	}
	if _, err := h.s.ManualBooking("C", "", "SVC-CUT", "2026-11-21 15:00"); err == nil {
		t.Fatal("a third booking was allowed")
	}
	if len(h.s.FreeTimes(fri.AddDate(0, 0, 3), fri, 30, 0)) != 8 {
		t.Fatal("the cap leaked into Monday")
	}
	// Moving one of the day's own bookings is still possible.
	id, _ := h.s.ManualBooking("D", "", "SVC-CUT", "2026-11-23 10:00")
	if err := h.s.MoveBooking(id, "2026-11-23 11:00"); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateOldHours(t *testing.T) {
	s, dir := setup(t)
	path := filepath.Join(dir, "t.db")
	// An old shop: only work_days and open_hours, no availability.
	s.DB.Exec(`DELETE FROM settings WHERE key='availability'`)
	s.DB.Exec(`UPDATE settings SET value='Tue-Sat' WHERE key='work_days'`)
	s.DB.Exec(`UPDATE settings SET value='09:00-13:00,15:00-19:00' WHERE key='open_hours'`)
	s.DB.Close()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if !strings.Contains(s.Get("availability"), `"Tue":[["09:00","13:00"],["15:00","19:00"]]`) {
		t.Fatalf("not migrated: %s", s.Get("availability"))
	}
	sc := s.Schedule()
	if len(sc.Week[1]) != 0 || len(sc.Week[0]) != 0 || rangesText(sc.Week[6]) != "9:00 AM–1:00 PM, 3:00 PM–7:00 PM" {
		t.Fatalf("week %v", sc.Week)
	}
	// Presets (and anything else) writing the old keys refill the week, keeping overrides.
	s.SetOverride("2026-12-25", nil)
	s.Set("work_days", "Mon-Fri")
	s.Set("open_hours", "08:00-12:00")
	sc = s.Schedule()
	if rangesText(sc.Week[1]) != "8:00 AM–12:00 PM" || len(sc.Week[6]) != 0 {
		t.Fatalf("old keys not applied: %v", sc.Week)
	}
	if _, ok := sc.Over["2026-12-25"]; !ok {
		t.Fatal("override lost")
	}
}

// calApp serves the calendar for the svcHarness shop.
func calApp(h *harness) *httptest.Server {
	app := &App{S: h.s, Bot: h.b, File: NewShopFile(h.s, filepath.Join(h.dir, "products.xlsx")), Dir: h.dir}
	return httptest.NewServer(app.Routes())
}

func getPage(t *testing.T, u string) string {
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("%s: %d %s", u, res.StatusCode, b)
	}
	return string(b)
}

func TestCalendarRemoveRestoreSlot(t *testing.T) {
	h := svcHarness(t, "services")
	srv := calApp(h)
	defer srv.Close()
	sat := fri.AddDate(0, 0, 1)
	form := url.Values{"at": {"2026-11-21 10:30"}, "on": {"1"}, "week": {"2026-11-16"}, "day": {"2026-11-21"}}
	res, _ := http.PostForm(srv.URL+"/calendar/block", form)
	if !strings.Contains(res.Request.URL.RawQuery, "day=2026-11-21") {
		t.Fatalf("back to %s", res.Request.URL)
	}
	eq(t, "removed", strings.Join(h.s.FreeTimes(sat, fri, 30, 0), " "), "10:00 11:00 11:30 14:00 14:30 15:00 15:30")
	page := getPage(t, srv.URL+"/calendar?week=2026-11-16")
	if !strings.Contains(page, `<div class="it blocked c7" style="grid-row:3 / span 2"><span class="t">10:30 AM</span> <span class="tag">Removed</span>`) {
		t.Fatal("removed slot not drawn hatched with Restore")
	}
	if len(h.s.FreeTimes(sat.AddDate(0, 0, 7), fri, 30, 0)) != 8 {
		t.Fatal("removing a slot touched the next week")
	}
	form.Set("on", "0")
	http.PostForm(srv.URL+"/calendar/block", form)
	if len(h.s.FreeTimes(sat, fri, 30, 0)) != 8 {
		t.Fatal("restore")
	}
}

func TestCalendarCloseAndReopenDay(t *testing.T) {
	h := svcHarness(t, "services")
	srv := calApp(h)
	defer srv.Close()
	sat := fri.AddDate(0, 0, 1)
	page := getPage(t, srv.URL+"/calendar?week=2026-11-16")
	if strings.Count(page, ">Close this day<") != 2 { // Fri and Sat; Mon-Thu are past, Sun is off
		t.Fatalf("close buttons: %d", strings.Count(page, ">Close this day<"))
	}
	http.PostForm(srv.URL+"/calendar/day", url.Values{"date": {"2026-11-21"}, "week": {"2026-11-16"}})
	if len(h.s.FreeTimes(sat, fri, 30, 0)) != 0 {
		t.Fatal("closed day still bookable")
	}
	if rs, ok := h.s.Avail().Over["2026-11-21"]; !ok || len(rs) != 0 {
		t.Fatal("no unavailable override")
	}
	page = getPage(t, srv.URL+"/calendar?week=2026-11-16")
	if !strings.Contains(page, `<span class="tag">Unavailable</span> <button class="lnk" name="open" value="1">Reopen</button>`) {
		t.Fatal("no Reopen")
	}
	http.PostForm(srv.URL+"/calendar/day", url.Values{"date": {"2026-11-21"}, "open": {"1"}, "week": {"2026-11-16"}})
	if len(h.s.FreeTimes(sat, fri, 30, 0)) != 8 {
		t.Fatal("reopen")
	}
	if _, ok := h.s.Avail().Over["2026-11-21"]; ok {
		t.Fatal("override left behind")
	}
}

func TestCalendarCancelFreesSlot(t *testing.T) {
	h := svcHarness(t, "services")
	srv := calApp(h)
	defer srv.Close()
	sat := fri.AddDate(0, 0, 1)
	id, err := h.s.ManualBooking("Asha", "", "SVC-COL", "2026-11-21 10:00")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.s.FreeTimes(sat, fri, 30, 0)) != 6 {
		t.Fatal("booking did not hold two slots")
	}
	res, _ := http.PostForm(srv.URL+"/calendar/cancel", url.Values{"order": {"1042"}, "week": {"2026-11-16"}})
	if msg := res.Request.URL.Query().Get("msg"); msg != "Booking 1042 cancelled." {
		t.Fatalf("msg %q", msg)
	}
	if len(h.s.FreeTimes(sat, fri, 30, 0)) != 8 {
		t.Fatal("cancel did not free the slots")
	}
	if o, _ := h.s.Order(id); o.Status != StCancel {
		t.Fatalf("status %s", o.Status)
	}
}

func TestCalendarRendersBlocks(t *testing.T) {
	h := svcHarness(t, "services")
	srv := calApp(h)
	defer srv.Close()
	h.s.ManualBooking("Asha", "", "SVC-COL", "2026-11-21 14:00") // 60 min
	h.s.SetOverride("2026-11-20", [][2]int{{13 * 60, 15 * 60}})  // Friday special hours
	page := getPage(t, srv.URL+"/calendar?week=2026-11-16")
	for _, want := range []string{
		`<span class="range">16 Nov – 22 Nov 2026</span>`,
		`<a class="btn" href="/calendar">Today</a>`,
		`<div class="dhead c6 sel today">`, // Friday is today, and the phone day
		`<span class="tag">Special hours</span>`,
		`<div class="it booked c7" style="grid-row:17 / span 4"><a class="who" href="/orders#order-1042">Asha</a>`, // 2 PM, an hour tall
		"Hair colour · 2:00 PM&ndash;3:00 PM",
		"Cancel booking",
		`<div class="it free c7" style="grid-row:1 / span 2"><span class="t">10:00 AM</span>`,
		`<div class="it past c2" style="grid-row:1 / span 2">`,
		`<div class="dcol c8 off"`, // Sunday
		`<div class="it now c6 sel"`,
		`<div class="hl" style="grid-row:1 / span 4">10:00 AM</div>`,
		`href="/calendar?week=2026-11-16&day=2026-11-21"`, // phone day tabs
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("calendar lacks %q", want)
		}
	}
	if strings.Contains(page, `value="2026-11-21 14:30"`) {
		t.Fatal("a slot is drawn under the booking")
	}
	// Moving: free future slots offer Move here.
	page = getPage(t, srv.URL+"/calendar?week=2026-11-16&move=1042")
	if !strings.Contains(page, "Move here") || !strings.Contains(page, "Moving booking 1042") {
		t.Fatal("move mode")
	}
}
