package main

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Services are booked on a date-time grid ownshop splits from Settings: working days, opening
// hours (one or more ranges, for a lunch break), slot length and how many days ahead.
// A confirmed booking, or a slot the owner blocked, holds the slot (capacity 1).

const bookFmt = "2006-01-02 15:04"
const dayFmt = "2006-01-02"

var errTaken = errors.New("that time was just booked by someone else")

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// Schedule is the booking grid from Settings: the availability plus slot length and days ahead.
// It is read once, up front: the store has one connection, so nothing below may query settings.
type Schedule struct {
	Avail
	Step  int // slot length in minutes
	Ahead int // days offered after today
}

func (s *Store) Schedule() Schedule {
	return Schedule{Avail: s.Avail(), Step: atoiOr(s.Get("slot_minutes"), 30), Ahead: atoiOr(s.Get("days_ahead"), 7)}
}

func atoiOr(v string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return n
	}
	return def
}

func dayIndex(w string) int {
	w = strings.ToLower(strings.TrimSpace(w))
	for i, d := range weekdays {
		if len(w) >= 3 && strings.HasPrefix(w, d) {
			return i
		}
	}
	return -1
}

// parseDays reads "Mon-Sat", "Tue-Sun" or "Mon,Wed,Fri". Empty means Mon-Sat.
func parseDays(v string) [7]bool {
	var d [7]bool
	if strings.TrimSpace(v) == "" {
		v = "Mon-Sat"
	}
	for _, part := range strings.Split(v, ",") {
		a, b, isRange := strings.Cut(part, "-")
		i, j := dayIndex(a), dayIndex(b)
		if !isRange {
			j = i
		}
		if i < 0 || j < 0 {
			continue
		}
		for k := i; ; k = (k + 1) % 7 {
			d[k] = true
			if k == j {
				break
			}
		}
	}
	return d
}

func clock(v string) (int, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(v))
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// parseHours reads "09:00-13:00, 17:00-20:00". Empty means 10:00-19:00.
func parseHours(v string) [][2]int {
	var out [][2]int
	for _, part := range strings.Split(v, ",") {
		a, b, _ := strings.Cut(part, "-")
		x, ok1 := clock(a)
		y, ok2 := clock(b)
		if ok1 && ok2 && y > x {
			out = append(out, [2]int{x, y})
		}
	}
	if len(out) == 0 {
		out = [][2]int{{600, 1140}}
	}
	return out
}

// RangesOn are the open ranges of one date: its override if it has one, else its weekday's.
func (sc Schedule) RangesOn(day time.Time) [][2]int {
	if rs, ok := sc.Over[day.Format(dayFmt)]; ok {
		return rs
	}
	return sc.Week[day.Weekday()]
}

// Starts are the slot start times of one date ("10:00", "10:30", ...), nil when it is closed.
func (sc Schedule) Starts(day time.Time) []string {
	var out []string
	for _, r := range sc.RangesOn(day) {
		for m := r[0]; m+sc.Step <= r[1]; m += sc.Step {
			out = append(out, hhmm(m))
		}
	}
	return out
}

// slotsFor is how many grid slots a booking of min minutes takes.
func (sc Schedule) slotsFor(min int) int {
	if min <= sc.Step {
		return 1
	}
	return (min + sc.Step - 1) / sc.Step
}

// Busy is what holds one slot: a booking (order) or the owner's block.
type Busy struct {
	OrderID int64
	Name    string
	Service string
	At      string // when the booking starts
	Blocked bool
}

type querier interface {
	Query(string, ...any) (*sql.Rows, error)
}

type booking struct {
	b       Busy
	start   time.Time
	minutes int
}

// bookings are the confirmed bookings starting in [from, to).
func (s *Store) bookings(q querier, from, to time.Time, skipOrder int64) []booking {
	rows, err := q.Query(`SELECT o.id, o.book_at, o.book_min, COALESCE(c.name,''),
	  COALESCE((SELECT GROUP_CONCAT(oi.name, ', ') FROM order_items oi JOIN products p ON p.id=oi.product_id WHERE oi.order_id=o.id AND p.kind='service'),'')
	  FROM orders o LEFT JOIN customers c ON c.id=o.customer_id
	  WHERE o.book_at<>'' AND o.status NOT IN ('cart','cancelled') AND o.id<>? AND o.book_at>=? AND o.book_at<?`,
		skipOrder, from.Format(bookFmt), to.Format(bookFmt))
	if err != nil {
		return nil
	}
	var list []booking
	for rows.Next() {
		var x booking
		var at string
		rows.Scan(&x.b.OrderID, &at, &x.minutes, &x.b.Name, &x.b.Service)
		t, err := time.ParseInLocation(bookFmt, at, time.Local)
		if err != nil {
			continue
		}
		x.start, x.b.At = t, at
		list = append(list, x)
	}
	rows.Close()
	return list
}

// blocksIn are the slots the owner removed in [from, to).
func (s *Store) blocksIn(q querier, from, to time.Time) []time.Time {
	rows, err := q.Query(`SELECT at FROM blocks WHERE at>=? AND at<?`, from.Format(bookFmt), to.Format(bookFmt))
	if err != nil {
		return nil
	}
	var out []time.Time
	for rows.Next() {
		var at string
		rows.Scan(&at)
		if t, err := time.ParseInLocation(bookFmt, at, time.Local); err == nil {
			out = append(out, t)
		}
	}
	rows.Close()
	return out
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// FreeTimes are the start times on day where a booking of min minutes fits: every slot it needs
// is in the grid (so it never runs over a lunch break or closing), free, and not in the past.
func (s *Store) FreeTimes(day, now time.Time, min int, skipOrder int64) []string {
	return s.freeTimes(s.DB, s.Schedule(), day, now, min, skipOrder)
}

func (s *Store) freeTimes(q querier, sc Schedule, day, now time.Time, min int, skipOrder int64) []string {
	day = startOfDay(day)
	books := s.bookings(q, day.AddDate(0, 0, -1), day.AddDate(0, 0, 2), skipOrder)
	if sc.MaxDay > 0 {
		n := 0
		for _, b := range books {
			if startOfDay(b.start).Equal(day) {
				n++
			}
		}
		if n >= sc.MaxDay {
			return nil
		}
	}
	blocked := s.blocksIn(q, day, day.AddDate(0, 0, 1))
	earliest := now
	if !now.IsZero() {
		earliest = now.Add(time.Duration(sc.Notice) * time.Minute)
	}
	need := time.Duration(sc.slotsFor(min)*sc.Step) * time.Minute
	buf := time.Duration(sc.Buffer) * time.Minute
	step := time.Duration(sc.Step) * time.Minute
	var out []string
	for _, r := range sc.RangesOn(day) {
		for m := r[0]; m+sc.Step <= r[1]; m += sc.Step {
			t := day.Add(time.Duration(m) * time.Minute)
			if !t.After(now) || t.Before(earliest) || m+sc.slotsFor(min)*sc.Step > r[1] {
				continue // past, too soon, or would run past the end of this range
			}
			ok := true
			for _, b := range books {
				end := b.start.Add(time.Duration(sc.slotsFor(b.minutes)*sc.Step) * time.Minute)
				ok = ok && !(t.Before(end.Add(buf)) && b.start.Before(t.Add(need).Add(buf)))
			}
			for _, at := range blocked {
				ok = ok && !(t.Before(at.Add(step)) && at.Before(t.Add(need)))
			}
			if ok {
				out = append(out, hhmm(m))
			}
		}
	}
	return out
}

// timeFree: a booking at `at` for min minutes fits the grid and nothing else holds it.
func (s *Store) timeFree(q querier, sc Schedule, at string, min int, skipOrder int64) bool {
	t, err := time.ParseInLocation(bookFmt, at, time.Local)
	if err != nil {
		return false
	}
	for _, st := range s.freeTimes(q, sc, t, time.Time{}, min, skipOrder) {
		if st == t.Format("15:04") {
			return true
		}
	}
	return false
}

// BookDay is one day offered to the customer.
type BookDay struct {
	Date  string // 2006-01-02
	Label string // "Tomorrow (Sun 23)"
}

func dayLabel(d, now time.Time) string {
	short := d.Format("Mon 2")
	switch startOfDay(d).Sub(startOfDay(now)).Hours() / 24 {
	case 0:
		return "Today (" + short + ")"
	case 1:
		return "Tomorrow (" + short + ")"
	}
	return short
}

func bookLabel(at string) string {
	t, err := time.ParseInLocation(bookFmt, at, time.Local)
	if err != nil {
		return at
	}
	return t.Format("Mon 2 Jan 15:04")
}

// OfferDays are the next n days (today first) with a free time for min minutes.
func (s *Store) OfferDays(now time.Time, min, n int) []BookDay {
	sc := s.Schedule()
	var out []BookDay
	for i := 0; i <= sc.Ahead && len(out) < n; i++ {
		d := startOfDay(now).AddDate(0, 0, i)
		if len(s.FreeTimes(d, now, min, 0)) > 0 {
			out = append(out, BookDay{Date: d.Format(dayFmt), Label: dayLabel(d, now)})
		}
	}
	return out
}

func (s *Store) SetBookDay(orderID int64, day string) {
	s.DB.Exec(`UPDATE orders SET book_day=?, book_at='', updated=? WHERE id=?`, day, now(), orderID)
}

func (s *Store) SetBookAt(orderID int64, at string, min int) {
	s.DB.Exec(`UPDATE orders SET book_at=?, book_min=?, updated=? WHERE id=?`, at, min, now(), orderID)
}

// MoveBooking is the owner moving a booking; the new time must be free.
func (s *Store) MoveBooking(orderID int64, at string) error {
	o, err := s.Order(orderID)
	if err != nil || o == nil {
		return fmt.Errorf("order %d not found", orderID)
	}
	sc := s.Schedule()
	min := o.BookMin
	if min == 0 {
		min = o.ServiceMinutes(sc.Step)
	}
	if !s.timeFree(s.DB, sc, at, min, orderID) {
		return fmt.Errorf("%s is not a free time", at)
	}
	s.SetBookAt(orderID, at, min)
	s.afterOrder()
	return nil
}

// Block / Unblock is the owner closing one slot (break, leave) or opening it again.
func (s *Store) Block(at string, on bool) error {
	if _, err := time.ParseInLocation(bookFmt, at, time.Local); err != nil {
		return fmt.Errorf("bad time %q", at)
	}
	var err error
	if on {
		_, err = s.DB.Exec(`INSERT OR IGNORE INTO blocks(at) VALUES (?)`, at)
	} else {
		_, err = s.DB.Exec(`DELETE FROM blocks WHERE at=?`, at)
	}
	return err
}

// HasService / HasProduct tell what kind of cart this is.
func (o Order) HasService() bool {
	for _, it := range o.Items {
		if it.Service {
			return true
		}
	}
	return false
}

func (o Order) HasProduct() bool {
	for _, it := range o.Items {
		if !it.Service {
			return true
		}
	}
	return false
}

// ServiceMinutes is how long the booking takes: each service's minutes (one slot when unset) times its quantity.
func (o Order) ServiceMinutes(step int) int {
	n := 0
	for _, it := range o.Items {
		if it.Service {
			m := it.Minutes
			if m <= 0 {
				m = step
			}
			n += m * it.Qty
		}
	}
	return n
}

// Ready: the cart has items and a time (services) or a delivery slot (products only).
func (o Order) Ready() bool {
	if len(o.Items) == 0 {
		return false
	}
	if o.HasService() {
		return o.BookAt != ""
	}
	return o.SlotID.Valid
}

// BookInput is the datetime-local value of the booking, for the owner's edit form.
func (o Order) BookInput() string { return strings.Replace(o.BookAt, " ", "T", 1) }

var (
	pickNumRe  = regexp.MustCompile(`^(?:slot|day|time|no|number)?\s*#?\s*(\d{1,2})\.?$`)
	timeTokRe  = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?(am|pm)?$`)
	fillerWord = map[string]bool{"at": true, "on": true, "for": true, "please": true, "the": true, "am": true, "pm": true, "slot": true}
)

// parseWhen reads "tomorrow 4pm", "sat 16:00", "today 10:30" or just "16:00".
// day is "" when no day word was given; ok is false unless every word was understood.
func parseWhen(text string, now time.Time) (day time.Time, hhmm string, ok bool) {
	ws := strings.Fields(strings.ToLower(strings.NewReplacer(",", " ", ".", " ").Replace(text)))
	for i, w := range ws {
		switch {
		case w == "today":
			day = startOfDay(now)
		case w == "tomorrow" || w == "tmrw":
			day = startOfDay(now).AddDate(0, 0, 1)
		case dayIndex(w) >= 0 && len(w) <= 9 && !strings.ContainsAny(w, "0123456789"):
			want := dayIndex(w)
			d := startOfDay(now)
			for d.Weekday() != time.Weekday(want) {
				d = d.AddDate(0, 0, 1)
			}
			day = d
		case timeTokRe.MatchString(w):
			m := timeTokRe.FindStringSubmatch(w)
			h, _ := strconv.Atoi(m[1])
			min, _ := strconv.Atoi(m[2])
			ap := m[3]
			if ap == "" && i+1 < len(ws) && (ws[i+1] == "am" || ws[i+1] == "pm") {
				ap = ws[i+1]
			}
			if ap == "" && m[2] == "" {
				return day, "", false // a bare number is a list pick, not a time
			}
			if ap == "pm" && h < 12 {
				h += 12
			} else if ap == "am" && h == 12 {
				h = 0
			}
			if h > 23 || min > 59 {
				return day, "", false
			}
			hhmm = fmt.Sprintf("%02d:%02d", h, min)
		case fillerWord[w]:
		default:
			return day, "", false
		}
	}
	return day, hhmm, !day.IsZero() || hhmm != ""
}
