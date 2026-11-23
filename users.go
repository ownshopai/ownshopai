package main

import (
	"errors"
	"net/http"
	"sort"
	"strings"
)

// The Users page: one table of every customer on every channel, with ban and needs-support.

const (
	provWhatsApp = "whatsapp"
	provTelegram = "telegram"
	provWebTest  = "webtest"
	provWebPub   = "webpub"
)

var providerLabels = map[string]string{provWhatsApp: "WhatsApp", provTelegram: "Telegram",
	provWebTest: "Web test (password)", provWebPub: "Web public (link)"}

type UserRow struct {
	Key, Name, Username, Phone, Email, Provider string
	PasswordSet                                 bool
	FirstSeen, LastSeen                         string
	Orders                                      int
	Blocked, Support                            bool
	CustomerID, WebUserID                       int64
}

func (u UserRow) ProviderLabel() string {
	if l, ok := providerLabels[u.Provider]; ok {
		return l
	}
	return u.Provider
}

func (u UserRow) Status() string {
	switch {
	case u.Blocked:
		return "Banned"
	case u.Support:
		return "Needs support"
	}
	return "Active"
}

func providerOf(channel, key string) string {
	switch {
	case channel == webChannel || strings.HasPrefix(key, "web:"):
		return provWebTest
	case channel == pubChannel || strings.HasPrefix(key, pubKeyPfx):
		return provWebPub
	case channel == "telegram" || strings.HasPrefix(key, "tg:"):
		return provTelegram
	}
	return provWhatsApp
}

// UserRows is every customer, plus web test logins that have not written yet; needs-support first,
// then the most recently seen.
func (s *Store) UserRows() ([]UserRow, error) {
	rows, err := s.DB.Query(`SELECT c.id, c.phone, c.name, c.channel, c.created, c.blocked, c.needs_support,
	  (SELECT COUNT(*) FROM orders o WHERE o.customer_id=c.id AND o.status<>'cart'),
	  COALESCE((SELECT MIN(ts) FROM chat_log l WHERE l.phone=c.phone), ''),
	  COALESCE((SELECT MAX(ts) FROM chat_log l WHERE l.phone=c.phone), '')
	  FROM customers c`)
	if err != nil {
		return nil, err
	}
	var out []UserRow
	seen := map[string]bool{}
	for rows.Next() {
		var u UserRow
		var channel, created string
		rows.Scan(&u.CustomerID, &u.Key, &u.Name, &channel, &created, &u.Blocked, &u.Support, &u.Orders, &u.FirstSeen, &u.LastSeen)
		if u.FirstSeen == "" {
			u.FirstSeen = created
		}
		u.Provider = providerOf(channel, u.Key)
		switch u.Provider {
		case provWhatsApp:
			u.Phone = u.Key
		case provWebTest:
			u.Username = strings.TrimPrefix(u.Key, "web:")
		}
		seen[u.Key] = true
		out = append(out, u)
	}
	rows.Close()
	pub, _ := s.PubUsers()
	byKey := map[string]PubUser{}
	for _, p := range pub {
		byKey[p.Key()] = p
	}
	web, _ := s.WebUsers()
	webBy := map[string]WebUser{}
	for _, w := range web {
		webBy["web:"+w.Username] = w
	}
	for i := range out {
		u := &out[i]
		if p, ok := byKey[u.Key]; ok {
			u.Phone, u.Email = p.Phone, p.Email
			if p.LastSeen > u.LastSeen {
				u.LastSeen = p.LastSeen
			}
			if u.FirstSeen == "" || p.Created < u.FirstSeen {
				u.FirstSeen = p.Created
			}
		}
		if w, ok := webBy[u.Key]; ok {
			u.PasswordSet, u.WebUserID = true, w.ID
			if u.Name == "" {
				u.Name = w.DisplayName
			}
		}
	}
	for _, w := range web {
		if !seen["web:"+w.Username] {
			out = append(out, UserRow{Key: "web:" + w.Username, Name: w.DisplayName, Username: w.Username,
				Provider: provWebTest, PasswordSet: true, FirstSeen: w.Created, WebUserID: w.ID})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Support != out[j].Support {
			return out[i].Support
		}
		return max(out[i].LastSeen, out[i].FirstSeen) > max(out[j].LastSeen, out[j].FirstSeen)
	})
	return out, nil
}

// FilterUsers keeps rows of one provider ("" any), one status ("active", "banned", "support"; "" any)
// and matching q in name, username, phone or email.
func FilterUsers(in []UserRow, provider, status, q string) []UserRow {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []UserRow
	for _, u := range in {
		if provider != "" && u.Provider != provider {
			continue
		}
		switch status {
		case "active":
			if u.Blocked || u.Support {
				continue
			}
		case "banned":
			if !u.Blocked {
				continue
			}
		case "support":
			if !u.Support || u.Blocked {
				continue
			}
		}
		if q != "" && !strings.Contains(strings.ToLower(u.Name+" "+u.Username+" "+u.Phone+" "+u.Email+" "+u.Key), q) {
			continue
		}
		out = append(out, u)
	}
	return out
}

// SetSupport marks or clears "needs support" by customer id or key.
func (s *Store) SetSupport(idOrPhone string, on bool) error {
	res, err := s.DB.Exec(`UPDATE customers SET needs_support=? WHERE phone=? OR CAST(id AS TEXT)=?`, boolInt(on), idOrPhone, idOrPhone)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("no customer " + idOrPhone)
	}
	return nil
}

// SupportQueue is the customers marked "needs support", for the top of the Chats page.
func (s *Store) SupportQueue() []UserRow {
	all, _ := s.UserRows()
	return FilterUsers(all, "", "support", "")
}

func (a *App) supportRoute(w http.ResponseWriter, r *http.Request, on bool) {
	to := "/users"
	if r.FormValue("back") == "chats" {
		to = "/chats"
	}
	if err := a.S.SetSupport(r.PathValue("id"), on); err != nil {
		back(w, r, to, err.Error())
		return
	}
	if on {
		back(w, r, to, "Marked: needs support. It is at the top of the Chats page.")
		return
	}
	back(w, r, to, "Resolved.")
}
