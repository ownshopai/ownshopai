package main

import "strings"

// Preset is a business type the owner picks in setup: its services, slot length and hours.
// The owner accepts it with one OK and can edit any of it later.
type Preset struct {
	Key, Label string
	Services   []Product
	Slot       string
	Days       string
	Hours      string
}

func svc(sku, name string, min int) Product {
	return Product{SKU: sku, Name: name, Kind: KindService, Minutes: min, Keywords: strings.ToLower(name)}
}

// Each type starts simple: one main service, Mon-Sat, one range, no lunch break.
var presets = []Preset{
	{"clinic", "Clinic / Doctor", []Product{svc("SVC-CON", "Consultation", 30)}, "30", "Mon-Sat", "09:00-18:00"},
	{"salon", "Salon / Beauty", []Product{svc("SVC-CUT", "Haircut", 30)}, "30", "Mon-Sat", "09:00-18:00"},
	{"tuition", "Tuition / Classes", []Product{svc("SVC-CLS", "Class", 60)}, "30", "Mon-Sat", "16:00-20:00"},
	{"repair", "Repair / Home service", []Product{svc("SVC-VIS", "Visit", 60)}, "30", "Mon-Sat", "09:00-18:00"},
	{"consultant", "Consultant", []Product{svc("SVC-CALL", "Call", 30)}, "30", "Mon-Sat", "09:00-18:00"},
	{"other", "Other", []Product{svc("SVC-APT", "Appointment", 30)}, "30", "Mon-Sat", "09:00-18:00"},
}

// presetSKUs are every service a preset ever added, older versions included, so switching the
// business type can take them away again.
var presetSKUs = map[string]bool{"SVC-CON": true, "SVC-FUP": true, "SVC-CHK": true, "SVC-CUT": true, "SVC-COL": true,
	"SVC-FAC": true, "SVC-BRD": true, "SVC-CLS": true, "SVC-TRL": true, "SVC-VIS": true, "SVC-REP": true,
	"SVC-30M": true, "SVC-60M": true, "SVC-CALL": true, "SVC-APT": true}

func presetByKey(k string) *Preset {
	for i := range presets {
		if presets[i].Key == k {
			return &presets[i]
		}
	}
	return nil
}

// ApplyPreset fills the booking settings and puts in the preset's services (price 0, for the owner
// to edit), replacing services an earlier preset added that the owner never priced. With a products
// file they are written into it, so it stays the one list.
func ApplyPreset(s *Store, sf *ShopFile, key string) error {
	p := presetByKey(key)
	if p == nil {
		p = presetByKey("other")
	}
	for k, v := range map[string]string{"biz_type": p.Key, "slot_minutes": p.Slot, "work_days": p.Days, "open_hours": p.Hours, "days_ahead": "14"} {
		s.Set(k, v)
	}
	if s.Get("sells") == "products" || s.Get("sells") == "" {
		s.Set("sells", "services")
	}
	keep := map[string]bool{}
	for _, sv := range p.Services {
		keep[sv.SKU] = true
	}
	var stale []string
	ps, _ := s.Products()
	for _, x := range ps {
		if x.IsService() && presetSKUs[x.SKU] && !keep[x.SKU] && x.Price == 0 {
			stale = append(stale, x.Name)
		}
	}
	if sf != nil {
		if len(stale) > 0 {
			if err := sf.RemoveRows(stale); err != nil {
				return err
			}
		}
		return sf.PutRows(p.Services)
	}
	for _, n := range stale {
		s.DB.Exec(`UPDATE products SET active=0 WHERE name=?`, n)
	}
	for _, sv := range p.Services {
		if err := s.UpsertProduct(sv); err != nil {
			return err
		}
	}
	return nil
}
