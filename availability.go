package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Availability, like Cal.com: a weekly schedule with one or more time ranges per day, date
// overrides (closed all day, or special hours) and booking rules (minimum notice, a buffer between
// bookings, a cap per day). It is stored as one settings value, "availability" (JSON). Old shops
// only have work_days and open_hours; they are converted on startup, and writing either of those
// keys (presets do) refills the weekly part.

// availJSON is the stored form; times are "09:00", days "Mon".."Sun", overrides by date.
type availJSON struct {
	Days      map[string][][2]string `json:"days"`
	Overrides map[string][][2]string `json:"overrides,omitempty"`
	Notice    int                    `json:"min_notice,omitempty"`  // minutes
	Buffer    int                    `json:"buffer,omitempty"`      // minutes
	MaxDay    int                    `json:"max_per_day,omitempty"` // 0 = no limit
}

// Avail is the parsed availability.
type Avail struct {
	Week                   [7][][2]int         // by time.Weekday; empty = unavailable
	Over                   map[string][][2]int // by date; empty = unavailable all day
	Notice, Buffer, MaxDay int
}

// legacyAvail turns the old work_days + open_hours into a weekly schedule.
func legacyAvail(workDays, openHours string) Avail {
	var a Avail
	days, hours := parseDays(workDays), parseHours(openHours)
	for i := range a.Week {
		if days[i] {
			a.Week[i] = append([][2]int(nil), hours...)
		}
	}
	return a
}

func toPairs(rs [][2]int) [][2]string {
	out := [][2]string{}
	for _, r := range rs {
		out = append(out, [2]string{hhmm(r[0]), hhmm(r[1])})
	}
	return out
}

func fromPairs(ps [][2]string) [][2]int {
	var out [][2]int
	for _, p := range ps {
		x, ok1 := clock(p[0])
		y, ok2 := clock(p[1])
		if ok1 && ok2 && y > x {
			out = append(out, [2]int{x, y})
		}
	}
	return out
}

func (a Avail) encode() string {
	j := availJSON{Days: map[string][][2]string{}, Notice: a.Notice, Buffer: a.Buffer, MaxDay: a.MaxDay}
	for i, d := range weekOrder {
		j.Days[d] = toPairs(a.Week[(i+1)%7])
	}
	if len(a.Over) > 0 {
		j.Overrides = map[string][][2]string{}
		for d, rs := range a.Over {
			j.Overrides[d] = toPairs(rs)
		}
	}
	b, _ := json.Marshal(j)
	return string(b)
}

func decodeAvail(v string) (Avail, bool) {
	var j availJSON
	if strings.TrimSpace(v) == "" || json.Unmarshal([]byte(v), &j) != nil || j.Days == nil {
		return Avail{}, false
	}
	a := Avail{Notice: j.Notice, Buffer: j.Buffer, MaxDay: j.MaxDay, Over: map[string][][2]int{}}
	for i, d := range weekOrder {
		a.Week[(i+1)%7] = fromPairs(j.Days[d])
	}
	for d, ps := range j.Overrides {
		if _, err := time.ParseInLocation(dayFmt, d, time.Local); err == nil {
			a.Over[d] = fromPairs(ps)
		}
	}
	return a, true
}

// Avail is the shop's availability: the stored JSON, else the old settings.
func (s *Store) Avail() Avail {
	if a, ok := decodeAvail(s.Get("availability")); ok {
		return a
	}
	a := legacyAvail(s.Get("work_days"), s.Get("open_hours"))
	a.Over = map[string][][2]int{}
	return a
}

func (s *Store) SetAvail(a Avail) error { return s.Set("availability", a.encode()) }

// migrateAvail converts an old shop's work_days and open_hours once, on startup.
func (s *Store) migrateAvail() {
	if s.Get("availability") == "" {
		s.SetAvail(s.Avail())
	}
}

// legacyChanged refills the weekly schedule after work_days or open_hours was written (presets,
// older code); overrides and rules stay.
func (s *Store) legacyChanged() {
	a := s.Avail()
	a.Week = legacyAvail(s.Get("work_days"), s.Get("open_hours")).Week
	s.SetAvail(a)
}

// SetOverride closes a date (no ranges) or gives it its own hours; DelOverride brings back the usual week.
func (s *Store) SetOverride(date string, ranges [][2]int) error {
	if _, err := time.ParseInLocation(dayFmt, date, time.Local); err != nil {
		return fmt.Errorf("bad date %q", date)
	}
	a := s.Avail()
	a.Over[date] = ranges
	return s.SetAvail(a)
}

func (s *Store) DelOverride(date string) error {
	a := s.Avail()
	delete(a.Over, date)
	return s.SetAvail(a)
}

// checkRanges sorts a day's ranges and refuses overlaps.
func checkRanges(rs [][2]int) ([][2]int, error) {
	sort.Slice(rs, func(i, j int) bool { return rs[i][0] < rs[j][0] })
	for i, r := range rs {
		if r[1] <= r[0] {
			return nil, fmt.Errorf("each time range must end after it starts")
		}
		if i > 0 && r[0] < rs[i-1][1] {
			return nil, fmt.Errorf("the time ranges overlap")
		}
	}
	return rs, nil
}

func rangesText(rs [][2]int) string {
	var p []string
	for _, r := range rs {
		p = append(p, ampm(r[0])+"–"+ampm(r[1]))
	}
	return strings.Join(p, ", ")
}

// Summary is the one-line preview: "Mon–Fri 9:00 AM–5:00 PM; Sat 10:00 AM–2:00 PM · every 30 min, up to 1 week ahead".
func (a Avail) Summary(step, ahead int) string {
	var groups []string
	for i := 0; i < 7; i++ {
		rs := a.Week[(i+1)%7]
		if len(rs) == 0 {
			continue
		}
		j := i
		for j+1 < 7 && rangesText(a.Week[(j+2)%7]) == rangesText(rs) {
			j++
		}
		name := weekOrder[i]
		switch {
		case j-i >= 2:
			name += "–" + weekOrder[j]
		case j > i:
			name += ", " + weekOrder[j]
		}
		groups = append(groups, name+" "+rangesText(rs))
		i = j
	}
	if len(groups) == 0 {
		return "Every day is unavailable: switch on at least one day."
	}
	aheadLabel := fmt.Sprintf("%d days", ahead)
	for _, c := range aheadChoices {
		if c[0] == fmt.Sprint(ahead) {
			aheadLabel = c[1]
		}
	}
	out := strings.Join(groups, "; ") + fmt.Sprintf(" · every %d min, up to %s ahead", step, aheadLabel)
	for _, c := range noticeChoices {
		if c[0] == fmt.Sprint(a.Notice) && a.Notice > 0 {
			out += " · " + c[1] + " notice"
		}
	}
	if a.Buffer > 0 {
		out += fmt.Sprintf(" · %d min between bookings", a.Buffer)
	}
	if a.MaxDay > 0 {
		out += fmt.Sprintf(" · at most %d a day", a.MaxDay)
	}
	if n := len(a.Over); n == 1 {
		out += " · 1 date override"
	} else if n > 1 {
		out += fmt.Sprintf(" · %d date overrides", n)
	}
	return out
}
