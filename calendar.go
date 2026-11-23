package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The Calendar page: a week of booking slots on a real calendar grid (a CSS grid in 15-minute rows).
// The owner removes and restores single slots, closes and reopens whole days (date overrides), adds
// walk-in or phone bookings, and moves or cancels bookings. Plain HTML forms; on a phone one day shows.

const calUnit = 15 // minutes per grid row

// calItem is one block on the grid: an open range (background), a slot, a booking or the now line.
type calItem struct {
	Kind            string // open, free, blocked, past, booked, now
	Col, Row, Span  int
	Sel             bool // the day shown on a phone
	Time, At, Until string
	Busy            *Busy
	Week, Day       string
	Move            int64
	MoveTo          bool
}

type calDay struct {
	Label, Short, Date    string
	Col                   int
	Today, Past, Sel      bool
	Closed, Special, Open bool
	Bookings              int
}

type calLabel struct {
	Row  int
	Text string
}

func mondayOf(t time.Time) time.Time {
	d := startOfDay(t)
	for d.Weekday() != time.Monday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

func (a *App) now() time.Time { return a.Bot.now() }

func minutesOf(t time.Time) int { return t.Hour()*60 + t.Minute() }

func (a *App) calendar(w http.ResponseWriter, r *http.Request) {
	now := a.now()
	today := startOfDay(now)
	week := mondayOf(now)
	if v, err := time.ParseInLocation(dayFmt, r.URL.Query().Get("week"), time.Local); err == nil {
		week = mondayOf(v)
	}
	sel := week
	if !today.Before(week) && today.Before(week.AddDate(0, 0, 7)) {
		sel = today
	}
	if v, err := time.ParseInLocation(dayFmt, r.URL.Query().Get("day"), time.Local); err == nil && !v.Before(week) && v.Before(week.AddDate(0, 0, 7)) {
		sel = v
	}
	move, _ := strconv.ParseInt(r.URL.Query().Get("move"), 10, 64)
	sc := a.S.Schedule()
	end := week.AddDate(0, 0, 7)
	books := a.S.bookings(a.S.DB, week, end, 0)
	blocks := a.S.blocksIn(a.S.DB, week, end)
	isBlocked := map[string]bool{}
	for _, t := range blocks {
		isBlocked[t.Format(bookFmt)] = true
	}
	lenOf := func(min int) int { return sc.slotsFor(min) * sc.Step }

	// The axis runs from the week's earliest opening to its latest closing, stretched to
	// include any booking outside the hours, so none ever disappears.
	lo, hi := 24*60, 0
	widen := func(x, y int) {
		lo, hi = min(lo, x), max(hi, y)
	}
	for i := 0; i < 7; i++ {
		for _, rg := range sc.RangesOn(week.AddDate(0, 0, i)) {
			widen(rg[0], rg[1])
		}
	}
	for _, b := range books {
		widen(minutesOf(b.start), minutesOf(b.start)+lenOf(b.minutes))
	}
	if lo >= hi {
		lo, hi = 9*60, 17*60
	}
	lo, hi = lo/60*60, (hi+59)/60*60
	row := func(m int) int { return (m-lo)/calUnit + 1 }

	var days []calDay
	var items []calItem
	base := calItem{Week: week.Format(dayFmt), Day: sel.Format(dayFmt), Move: move}
	for i := 0; i < 7; i++ {
		d := week.AddDate(0, 0, i)
		date := d.Format(dayFmt)
		ov, special := sc.Over[date]
		cd := calDay{Label: d.Format("Mon 2 Jan"), Short: d.Format("Mon 2"), Date: date, Col: i + 2, Today: d.Equal(today),
			Past: d.Before(today), Sel: d.Equal(sel), Closed: special && len(ov) == 0, Special: special && len(ov) > 0}
		cd.Open = len(sc.RangesOn(d)) > 0
		mk := func(it calItem) calItem {
			it.Col, it.Sel, it.Week, it.Day, it.Move = i+2, cd.Sel, base.Week, base.Day, base.Move
			return it
		}
		var dayBooks []booking
		for _, b := range books {
			if startOfDay(b.start).Equal(d) {
				dayBooks = append(dayBooks, b)
			}
		}
		cd.Bookings = len(dayBooks)
		for _, rg := range sc.RangesOn(d) {
			items = append(items, mk(calItem{Kind: "open", Row: row(rg[0]), Span: (rg[1] - rg[0]) / calUnit}))
		}
		for _, rg := range sc.RangesOn(d) {
			for m := rg[0]; m+sc.Step <= rg[1]; m += sc.Step {
				t := d.Add(time.Duration(m) * time.Minute)
				covered := false
				for _, b := range dayBooks {
					bm := minutesOf(b.start)
					covered = covered || (m < bm+lenOf(b.minutes) && bm < m+sc.Step)
				}
				if covered {
					continue
				}
				it := mk(calItem{Kind: "free", Row: row(m), Span: sc.Step / calUnit, Time: ampm(m), At: t.Format(bookFmt), Until: ampm(m + sc.Step)})
				switch {
				case isBlocked[it.At]:
					it.Kind = "blocked"
				case !t.After(now):
					it.Kind = "past"
				}
				it.MoveTo = move > 0 && it.Kind == "free"
				items = append(items, it)
			}
		}
		for _, b := range dayBooks {
			bm := minutesOf(b.start)
			bb := b.b
			items = append(items, mk(calItem{Kind: "booked", Row: row(bm), Span: max(1, lenOf(b.minutes)/calUnit), Time: ampm(bm),
				Until: ampm(bm + lenOf(b.minutes)), At: b.b.At, Busy: &bb}))
		}
		if cd.Today && minutesOf(now) >= lo && minutesOf(now) < hi {
			items = append(items, mk(calItem{Kind: "now", Row: row(minutesOf(now)), Span: 1}))
		}
		days = append(days, cd)
	}
	var labels []calLabel
	for m := lo; m < hi; m += 60 {
		labels = append(labels, calLabel{row(m), ampm(m)})
	}
	var services []Product
	ps, _ := a.S.Products()
	for _, p := range ps {
		if p.IsService() {
			services = append(services, p)
		}
	}
	a.page(w, "calendar", map[string]any{"Days": days, "Items": items, "Labels": labels, "Rows": (hi - lo) / calUnit,
		"Week": week.Format(dayFmt), "Day": sel.Format(dayFmt), "Range": week.Format("2 Jan") + " – " + week.AddDate(0, 0, 6).Format("2 Jan 2006"),
		"Prev": week.AddDate(0, 0, -7).Format(dayFmt), "Next": week.AddDate(0, 0, 7).Format(dayFmt),
		"Move": move, "Services": services, "At": r.URL.Query().Get("at"), "Msg": r.URL.Query().Get("msg")})
}

func calBack(w http.ResponseWriter, r *http.Request, msg string) {
	q := url.Values{"week": {r.FormValue("week")}, "msg": {msg}}
	if d := r.FormValue("day"); d != "" {
		q.Set("day", d)
	}
	http.Redirect(w, r, "/calendar?"+q.Encode(), http.StatusSeeOther)
}

// calDay closes a whole date (an "Unavailable" date override) or reopens it (the override goes).
func (a *App) calDay(w http.ResponseWriter, r *http.Request) {
	date := r.FormValue("date")
	t, err := time.ParseInLocation(dayFmt, date, time.Local)
	if err != nil {
		calBack(w, r, "Pick a day.")
		return
	}
	label := t.Format("Mon 2 Jan")
	if r.FormValue("open") == "1" {
		if err := a.S.DelOverride(date); err != nil {
			calBack(w, r, err.Error())
			return
		}
		calBack(w, r, label+" is back to your usual hours.")
		return
	}
	if err := a.S.SetOverride(date, nil); err != nil {
		calBack(w, r, err.Error())
		return
	}
	msg := label + " is closed: customers cannot book it."
	if n := len(a.S.bookings(a.S.DB, t, t.AddDate(0, 0, 1), 0)); n > 0 {
		msg += fmt.Sprintf(" %d booking(s) on that day are still there: move or cancel them.", n)
	}
	calBack(w, r, msg)
}

func (a *App) calBlock(w http.ResponseWriter, r *http.Request) {
	at := r.FormValue("at")
	on := r.FormValue("on") == "1"
	if err := a.S.Block(at, on); err != nil {
		calBack(w, r, err.Error())
		return
	}
	if on {
		calBack(w, r, bookLabel(at)+" removed: customers cannot book it.")
	} else {
		calBack(w, r, bookLabel(at)+" restored.")
	}
}

func (a *App) calMove(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.FormValue("order"), 10, 64)
	if err := a.S.MoveBooking(id, r.FormValue("at")); err != nil {
		calBack(w, r, err.Error())
		return
	}
	a.notifyMoved(id)
	calBack(w, r, fmt.Sprintf("Booking %d moved to %s.", id, r.FormValue("at")))
}

func (a *App) calCancel(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.FormValue("order"), 10, 64)
	o, err := a.S.Advance(id, StCancel)
	if err != nil {
		calBack(w, r, err.Error())
		return
	}
	msg := fmt.Sprintf("Booking %d cancelled.", id)
	if !strings.HasPrefix(o.Customer.Phone, "walkin:") && o.Customer.JID != "" { // tell them only if there is a chat
		if err := a.Bot.Notify(o.Customer, a.Bot.StatusMessage(o, a.Dir)); err != nil {
			msg += " The customer was NOT told: " + err.Error()
		}
	}
	calBack(w, r, msg)
}

// notifyMoved tells a chat customer their new time; walk-ins have no chat.
func (a *App) notifyMoved(id int64) {
	o, _ := a.S.Order(id)
	if o == nil || strings.HasPrefix(o.Customer.Phone, "walkin:") || o.Customer.JID == "" {
		return
	}
	a.Bot.Notify(o.Customer, Out{Text: a.Bot.renderW("status", map[string]any{"Order": o})})
}

// calBook adds a walk-in or phone booking at a free time.
func (a *App) calBook(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	at := strings.Replace(strings.TrimSpace(r.FormValue("at")), "T", " ", 1)
	sku := r.FormValue("service")
	if name == "" || at == "" {
		calBack(w, r, "Give a name and a time.")
		return
	}
	id, err := a.S.ManualBooking(name, strings.TrimSpace(r.FormValue("phone")), sku, at)
	if err != nil {
		calBack(w, r, "Not booked: "+err.Error())
		return
	}
	calBack(w, r, fmt.Sprintf("Booked %s at %s (booking %d).", name, at, id))
}

// ManualBooking makes a confirmed booking for a customer who did not write in (walk-in, phone).
func (s *Store) ManualBooking(name, phone, sku, at string) (int64, error) {
	var p *Product
	ps, _ := s.Products()
	for i := range ps {
		if ps[i].IsService() && (ps[i].SKU == sku || ps[i].Name == sku) {
			p = &ps[i]
		}
	}
	if p == nil {
		return 0, fmt.Errorf("pick a service")
	}
	key := strings.TrimSpace(phone)
	if key == "" {
		key = "walkin:" + name
	}
	c, err := s.Customer(key, name, "")
	if err != nil {
		return 0, err
	}
	sc := s.Schedule()
	min := p.Minutes
	if min <= 0 {
		min = sc.Step
	}
	if !s.timeFree(s.DB, sc, at, min, 0) {
		return 0, fmt.Errorf("%s is not a free time", at)
	}
	res, err := s.DB.Exec(`INSERT INTO orders(customer_id, status, created, updated, channel, account) VALUES (?, 'cart', ?, ?, 'walk-in', '')`, c.ID, now(), now())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if err := s.AddItem(id, *p, 1); err != nil {
		return 0, err
	}
	s.SetBookAt(id, at, min)
	o, _ := s.Order(id)
	if _, err := s.Confirm(o); err != nil {
		s.DB.Exec(`DELETE FROM order_items WHERE order_id=?`, id)
		s.DB.Exec(`DELETE FROM orders WHERE id=?`, id)
		return 0, err
	}
	return id, nil
}
