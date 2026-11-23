package main

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/xuri/excelize/v2"
)

// The booking controls (setup "Your bookings" and Settings), like Cal.com's availability: a weekly
// schedule (each day on or off, one or more time ranges), date overrides (closed all day or special
// hours) and booking rules. Everything is picked, nothing is typed. Stored as the "availability"
// setting (see availability.go) plus slot_minutes and days_ahead.

var weekOrder = []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

var (
	slotChoices     = []string{"15", "30", "45", "60"}
	aheadChoices    = [][2]string{{"3", "3 days"}, {"7", "1 week"}, {"14", "2 weeks"}, {"30", "1 month"}}
	noticeChoices   = [][2]string{{"0", "None"}, {"60", "1 hour"}, {"120", "2 hours"}, {"1440", "1 day"}}
	bufferChoices   = [][2]string{{"0", "0"}, {"5", "5 min"}, {"10", "10 min"}, {"15", "15 min"}}
	maxDayChoices   = [][2]string{{"0", "No limit"}, {"4", "4"}, {"8", "8"}, {"12", "12"}, {"20", "20"}}
	durationChoices = []int{15, 30, 45, 60, 90, 120}
	defaultRange    = [2]int{9 * 60, 17 * 60}
)

type option struct{ Val, Label string }

type dayChip struct {
	Name string
	On   bool
}

func options(cs [][2]string) []option {
	var out []option
	for _, c := range cs {
		out = append(out, option{c[0], c[1]})
	}
	return out
}

type timeRange struct{ From, To string }

// dayRow is one weekday of the weekly schedule; overrideRow one date override.
type dayRow struct {
	Name   string
	On     bool
	Ranges []timeRange
}

type overrideRow struct {
	Idx, Date, Label string
	Off              bool
	Ranges           []timeRange
}

// HoursForm is what the booking controls show.
type HoursForm struct {
	Week                                []dayRow
	Overrides                           []overrideRow
	Blank                               overrideRow // the row "+ Add a date override" copies
	Times                               []option    // 06:00 to 23:30 in 30-minute steps
	Slot, Ahead, Notice, Buffer, MaxDay string
	Slots                               []string
	Aheads, Notices, Buffers, MaxDays   []option
	Durations                           []int
	Summary                             string
	// The simple view: one range for every chosen day. Advanced is open (and the simple
	// fields disabled) when the stored schedule needs more than that.
	Days          []dayChip
	Opens, Closes string
	Advanced      bool
}

func hhmm(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

// ampm shows 13:30 as "1:30 PM".
func ampm(m int) string {
	h, suf := m/60, "AM"
	if h >= 12 {
		suf = "PM"
	}
	if h = h % 12; h == 0 {
		h = 12
	}
	return fmt.Sprintf("%d:%02d %s", h, m%60, suf)
}

func timeOptions() []option {
	var out []option
	for m := 6 * 60; m <= 23*60+30; m += 30 {
		out = append(out, option{hhmm(m), ampm(m)})
	}
	return out
}

func rangeRows(rs [][2]int) []timeRange {
	if len(rs) == 0 {
		rs = [][2]int{defaultRange}
	}
	var out []timeRange
	for _, r := range rs {
		out = append(out, timeRange{hhmm(r[0]), hhmm(r[1])})
	}
	return out
}

func (a *App) hoursForm() HoursForm {
	sc := a.S.Schedule()
	f := HoursForm{Times: timeOptions(), Slot: strconv.Itoa(sc.Step), Ahead: strconv.Itoa(sc.Ahead),
		Notice: strconv.Itoa(sc.Notice), Buffer: strconv.Itoa(sc.Buffer), MaxDay: strconv.Itoa(sc.MaxDay),
		Slots: slotChoices, Aheads: options(aheadChoices), Notices: options(noticeChoices), Buffers: options(bufferChoices),
		MaxDays: options(maxDayChoices), Durations: durationChoices, Summary: sc.Avail.Summary(sc.Step, sc.Ahead),
		Blank: overrideRow{Idx: "__o__", Off: true, Ranges: rangeRows(nil)}}
	for i, d := range weekOrder {
		rs := sc.Week[(i+1)%7]
		f.Week = append(f.Week, dayRow{d, len(rs) > 0, rangeRows(rs)})
	}
	simple, first := len(sc.Over) == 0, ""
	for i, d := range weekOrder {
		rs := sc.Week[(i+1)%7]
		f.Days = append(f.Days, dayChip{d, len(rs) > 0})
		if len(rs) == 0 {
			continue
		}
		if first == "" {
			first = rangesText(rs)
			f.Opens, f.Closes = hhmm(rs[0][0]), hhmm(rs[0][1])
		}
		simple = simple && len(rs) == 1 && rangesText(rs) == first
	}
	if f.Opens == "" {
		f.Opens, f.Closes = hhmm(9*60), hhmm(18*60)
	}
	f.Advanced = !simple
	var dates []string
	for d := range sc.Over {
		dates = append(dates, d)
	}
	slices.Sort(dates)
	for i, d := range dates {
		t, _ := time.ParseInLocation(dayFmt, d, time.Local)
		f.Overrides = append(f.Overrides, overrideRow{Idx: strconv.Itoa(i), Date: d, Label: t.Format("Mon 2 Jan 2006"),
			Off: len(sc.Over[d]) == 0, Ranges: rangeRows(sc.Over[d])})
	}
	return f
}

// formRanges reads the paired from/to selects named prefix_from / prefix_to.
func formRanges(r *http.Request, from, to string) ([][2]int, error) {
	fs, ts := r.Form[from], r.Form[to]
	var out [][2]int
	for i := range fs {
		if i >= len(ts) {
			break
		}
		x, ok1 := clock(fs[i])
		y, ok2 := clock(ts[i])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("pick both ends of every time range")
		}
		out = append(out, [2]int{x, y})
	}
	return checkRanges(out)
}

func pick(r *http.Request, name string, cs [][2]string, msg string) (int, error) {
	v := r.FormValue(name)
	if v == "" {
		v = "0"
	}
	if !slices.ContainsFunc(cs, func(c [2]string) bool { return c[0] == v }) {
		return 0, fmt.Errorf("%s", msg)
	}
	n, _ := strconv.Atoi(v)
	return n, nil
}

// parseHoursForm checks the booking controls and returns the settings to store. A copy_from=<day>
// button copies that day's switch and ranges to every other day first.
func parseHoursForm(r *http.Request) (map[string]string, error) {
	var av Avail
	av.Over = map[string][][2]int{}
	on := map[string]bool{}
	for _, d := range r.Form["day_on"] {
		on[d] = true
	}
	var week [7][][2]int
	_, simple := r.Form["opens"]
	for i, d := range weekOrder {
		if simple || !on[d] {
			continue
		}
		rs, err := formRanges(r, "from_"+d, "to_"+d)
		if err != nil {
			return nil, fmt.Errorf("%s: %s.", d, err.Error())
		}
		if len(rs) == 0 {
			return nil, fmt.Errorf("Add hours for %s, or switch it off.", d)
		}
		week[i] = rs
	}
	if simple { // the simple view: one range for every chosen day
		opens, ok1 := clock(r.FormValue("opens"))
		closes, ok2 := clock(r.FormValue("closes"))
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("Pick when you open and close.")
		}
		if closes <= opens {
			return nil, fmt.Errorf("Closing time must be after opening time.")
		}
		chosen := r.Form["day"]
		for i, d := range weekOrder {
			week[i] = nil
			if slices.Contains(chosen, d) {
				week[i] = [][2]int{{opens, closes}}
			}
		}
	}
	if src := slices.Index(weekOrder, r.FormValue("copy_from")); src >= 0 {
		for i := range week {
			week[i] = append([][2]int(nil), week[src]...)
		}
	}
	any := false
	for i := range week {
		av.Week[(i+1)%7] = week[i]
		any = any || len(week[i]) > 0
	}
	if !any {
		return nil, fmt.Errorf("Switch on at least one day.")
	}
	for _, i := range r.Form["ov_idx"] {
		date := strings.TrimSpace(r.FormValue("ov_date_" + i))
		if date == "" {
			continue // the blank row
		}
		t, err := time.ParseInLocation(dayFmt, date, time.Local)
		if err != nil {
			return nil, fmt.Errorf("Pick a date for each date override.")
		}
		label := t.Format("Mon 2 Jan")
		if _, dup := av.Over[date]; dup {
			return nil, fmt.Errorf("%s has two date overrides.", label)
		}
		if r.FormValue("ov_off_"+i) != "" {
			av.Over[date] = nil
			continue
		}
		rs, err := formRanges(r, "ov_from_"+i, "ov_to_"+i)
		if err != nil {
			return nil, fmt.Errorf("%s: %s.", label, err.Error())
		}
		if len(rs) == 0 {
			return nil, fmt.Errorf("Give %s its hours, or mark it unavailable all day.", label)
		}
		av.Over[date] = rs
	}
	var err error
	if av.Notice, err = pick(r, "min_notice", noticeChoices, "Pick a minimum notice."); err != nil {
		return nil, err
	}
	if av.Buffer, err = pick(r, "buffer_minutes", bufferChoices, "Pick the time between bookings."); err != nil {
		return nil, err
	}
	if av.MaxDay, err = pick(r, "max_per_day", maxDayChoices, "Pick the most bookings a day."); err != nil {
		return nil, err
	}
	slot := r.FormValue("slot_minutes")
	if !slices.Contains(slotChoices, slot) {
		return nil, fmt.Errorf("Pick a slot length.")
	}
	ahead := r.FormValue("days_ahead")
	if !slices.ContainsFunc(aheadChoices, func(c [2]string) bool { return c[0] == ahead }) {
		return nil, fmt.Errorf("Pick how far ahead customers can book.")
	}
	return map[string]string{"availability": av.encode(), "slot_minutes": slot, "days_ahead": ahead}, nil
}

// copiedMsg is the note after a "Copy to all days" button.
func copiedMsg(r *http.Request) string {
	if d := r.FormValue("copy_from"); slices.Contains(weekOrder, d) {
		return "Copied " + d + " to all days. Saved."
	}
	return ""
}

// svcRow is one row of the services list.
type svcRow struct {
	Name    string
	Minutes int
	Price   float64
}

// parseServices reads the services list: rows svc_name_<i>, svc_min_<i>, svc_price_<i> for each svc_idx.
func parseServices(r *http.Request) ([]svcRow, error) {
	var out []svcRow
	seen := map[string]bool{}
	for _, i := range r.Form["svc_idx"] {
		name := strings.TrimSpace(r.FormValue("svc_name_" + i))
		if name == "" {
			continue
		}
		if seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("%s is in the list twice.", name)
		}
		seen[strings.ToLower(name)] = true
		min, _ := strconv.Atoi(r.FormValue("svc_min_" + i))
		if !slices.Contains(durationChoices, min) {
			return nil, fmt.Errorf("Pick how long %s takes.", name)
		}
		price := 0.0
		if p := strings.TrimSpace(r.FormValue("svc_price_" + i)); p != "" {
			v, err := strconv.ParseFloat(strings.ReplaceAll(p, ",", "."), 64)
			if err != nil || v < 0 {
				return nil, fmt.Errorf("The price of %s must be a number.", name)
			}
			price = v
		}
		out = append(out, svcRow{name, min, price})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Add at least one service.")
	}
	return out, nil
}

// skuFor makes an ID for a new service from its name: "Hot stone massage" -> "SVC-HOTSTONEMAS".
func skuFor(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) && b.Len() < 10 {
			b.WriteRune(r)
		}
	}
	return "SVC-" + b.String()
}

// saveServices makes the services match the list: new and changed rows are written, missing ones removed.
func (a *App) saveServices(rows []svcRow) error {
	ps, _ := a.S.Products()
	have := map[string]Product{}
	skus := map[string]bool{}
	for _, p := range ps {
		skus[p.SKU] = true
		if p.IsService() {
			have[p.Name] = p
		}
	}
	var put []Product
	keep := map[string]bool{}
	for _, r := range rows {
		keep[r.Name] = true
		p, ok := have[r.Name]
		if !ok {
			sku := skuFor(r.Name)
			for n := 2; skus[sku]; n++ {
				sku = skuFor(r.Name) + strconv.Itoa(n)
			}
			skus[sku] = true
			p = Product{SKU: sku, Name: r.Name, Keywords: strings.ToLower(r.Name), Kind: KindService}
		}
		p.Minutes, p.Price = r.Minutes, r.Price
		put = append(put, p)
	}
	var gone []string
	for n := range have {
		if !keep[n] {
			gone = append(gone, n)
		}
	}
	if a.File == nil {
		for _, p := range put {
			if err := a.S.UpsertProduct(p); err != nil {
				return err
			}
		}
		for _, n := range gone {
			a.S.DB.Exec(`UPDATE products SET active=0 WHERE name=?`, n)
		}
		return nil
	}
	if len(gone) > 0 {
		if err := a.File.RemoveRows(gone); err != nil {
			return err
		}
	}
	return a.File.PutRows(put)
}

// RemoveRows deletes the named rows from the Products sheet.
func (sf *ShopFile) RemoveRows(names []string) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	f, err := excelize.OpenFile(sf.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	sh := ""
	for _, n := range f.GetSheetList() {
		if strings.EqualFold(strings.TrimSpace(n), sheetProducts) {
			sh = n
		}
	}
	if sh == "" {
		sh = f.GetSheetName(0)
	}
	rows, err := f.GetRows(sh)
	if err != nil || len(rows) == 0 {
		return fmt.Errorf("no %s sheet", sheetProducts)
	}
	col := -1
	for i, h := range rows[0] {
		if strings.EqualFold(strings.TrimSpace(h), "name") {
			col = i
		}
	}
	if col < 0 {
		return nil
	}
	for r := len(rows) - 1; r >= 1; r-- { // bottom up, so row numbers stay valid
		if col < len(rows[r]) && slices.Contains(names, strings.TrimSpace(rows[r][col])) {
			if err := f.RemoveRow(sh, r+1); err != nil {
				return err
			}
		}
	}
	return f.Save()
}
