package main

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed web/*
var webFS embed.FS

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"money":  func(v float64) string { return fmt.Sprintf("%.2f", v) },
	"label":  func(s string) string { return statusLabel[s] },
	"steps":  func() []string { return []string{StPaid, StPacked, StShipped, StDone} },
	"split":  strings.Fields,
	"inc":    func(i int) int { return i + 1 },
	"dash":   func(s string) string { return strings.ReplaceAll(s, "-", " ") },
	"title":  func(s string) string { return strings.ToUpper(s[:1]) + s[1:] },
	"mulf":   func(q int, p float64) float64 { return float64(q) * p },
	"plural": plural,
	"list":   func(v ...string) []string { return v },
	// svcrow is one row of the services list; i < 0 is the blank row "+ Add service" copies.
	"svcrow": func(i int, name string, min int, price float64, cur string, durs []int) map[string]any {
		id := "__i__"
		if i >= 0 {
			id = strconv.Itoa(i)
		}
		return map[string]any{"I": id, "Name": name, "Min": min, "Price": price, "Cur": cur, "Durs": durs}
	},
	// rng is one time range of the booking controls: two time dropdowns and a remove button.
	"rng": func(from, to, f, t string, times []option) map[string]any {
		return map[string]any{"F": from, "T": to, "From": f, "To": t, "Times": times}
	},
	"ovrow": func(r overrideRow, times []option) map[string]any { return map[string]any{"R": r, "Times": times} },
}).ParseFS(webFS, "web/*.html"))

// App is the owner's web page. Everything the owner does happens here.
type App struct {
	S    *Store
	Bot  *Bot
	Hub  *Hub
	File *ShopFile // products.xlsx
	Dir  string    // data folder: products.xlsx, orders.csv, receipts, uploads
	UCP  *ucpState // Universal Commerce Protocol checkouts (ucp.go); off unless ucp_enabled

	webSess *webSessions // web chat logins (webchat.go)
	Tunnel  *Tunnel      // the Website channel's public link (tunnel.go); nil when not running
	Pub     *pubSite     // the public listener (website.go)
	Links   LinkService  // the free fixed link service (linkservice_client.go)
}

func (a *App) ordersCSV() string { return filepath.Join(a.Dir, "orders.csv") }

func (a *App) setupDone() bool { return a.S.Get("setup_done") == "1" }

func (a *App) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if !a.setupDone() {
			http.Redirect(w, r, "/setup/1", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/orders", http.StatusFound)
	})
	m.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, appID) })
	m.HandleFunc("GET /style.css", func(w http.ResponseWriter, r *http.Request) {
		b, _ := webFS.ReadFile("web/style.css")
		w.Header().Set("Content-Type", "text/css")
		w.Write(b)
	})
	m.HandleFunc("GET /setup/{step}", a.setup)
	m.HandleFunc("POST /setup/1", a.setupShop)
	m.HandleFunc("POST /setup/2", a.uploadProducts("/setup/3", "/setup/2"))
	m.HandleFunc("POST /setup/3", a.setupPayment)
	m.HandleFunc("POST /setup/done", func(w http.ResponseWriter, r *http.Request) {
		a.S.Set("setup_done", "1")
		back(w, r, "/orders", "Your shop is ready. New orders show up here.")
	})
	m.HandleFunc("GET /sample.xlsx", a.sampleFile)
	m.HandleFunc("GET /settings", a.settings)
	m.HandleFunc("POST /settings", a.saveSettings)
	m.HandleFunc("GET /payment-qr", a.serveSetting("qr_path"))
	m.HandleFunc("GET /products", a.products)
	a.faqRoutes(m)
	m.HandleFunc("POST /products/reload", a.reloadProducts)
	m.HandleFunc("POST /products/upload", a.uploadProducts("/products", "/products"))
	m.HandleFunc("GET /products/{sku}/photo", a.serveProductImage)
	m.HandleFunc("POST /products/{sku}/photo", a.uploadProductImage)
	m.HandleFunc("GET /orders", a.orders)
	m.HandleFunc("GET /orders.csv", func(w http.ResponseWriter, r *http.Request) {
		WriteOrdersCSV(a.S, a.ordersCSV())
		w.Header().Set("Content-Disposition", `attachment; filename="orders.csv"`)
		http.ServeFile(w, r, a.ordersCSV())
	})
	m.HandleFunc("POST /orders/{id}/status", a.setStatus)
	m.HandleFunc("POST /orders/{id}/reply", a.reply)
	m.HandleFunc("POST /orders/{id}/deliver", a.setDeliverTo)
	m.HandleFunc("POST /orders/{id}/book", a.setBooking)
	m.HandleFunc("GET /calendar", a.calendar)
	m.HandleFunc("POST /calendar/block", a.calBlock)
	m.HandleFunc("POST /calendar/book", a.calBook)
	m.HandleFunc("POST /calendar/move", a.calMove)
	m.HandleFunc("POST /calendar/cancel", a.calCancel)
	m.HandleFunc("POST /calendar/day", a.calDay)
	m.HandleFunc("GET /setup/type", a.setupType)
	m.HandleFunc("POST /setup/type", a.applyPreset)
	m.HandleFunc("POST /setup/hours", a.setupHours)
	m.HandleFunc("POST /products/service", a.saveService)
	m.HandleFunc("GET /orders/{id}/receipt.png", a.receipt)
	m.HandleFunc("GET /chats", a.chats)
	m.HandleFunc("POST /handoffs/{id}", a.resolveHandoff)
	m.HandleFunc("POST /channels/whatsapp", a.addWhatsApp)
	m.HandleFunc("POST /channels/whatsapp/{acct}/link", a.link)
	m.HandleFunc("POST /channels/whatsapp/{acct}/remove", a.removeWhatsApp)
	m.HandleFunc("GET /channels/whatsapp/{acct}/qr.png", a.waQR)
	m.HandleFunc("POST /channels/telegram", a.saveTelegram)
	m.HandleFunc("GET /start-over/{kind}", a.startOverPage)
	m.HandleFunc("POST /start-over/{kind}", a.startOver)
	a.ucpRoutes(m)
	a.webchatRoutes(m)
	a.usersRoutes(m)
	a.websiteRoutes(m)
	a.folderRoutes(m)
	return a.hideDirInRedirects(m)
}

func (a *App) page(w http.ResponseWriter, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["Page"] = name
	data["Shop"] = a.S.Get("shop_name")
	data["Cur"] = a.S.Get("currency")
	data["Sells"] = a.S.Get("sells")
	nav := [][2]string{{"orders", "Orders"}, {"products", "Products"}}
	switch data["Sells"] {
	case "services":
		nav = [][2]string{{"orders", "Orders"}, {"calendar", "Calendar"}, {"products", "Services"}}
	}
	if data["Setup"] == true {
		data["Steps"] = setupSteps(data["Sells"].(string))
	}
	data["Nav"] = append(nav, [2]string{"faq", "FAQ"}, [2]string{"chats", "Chats"}, [2]string{"users", "Users"}, [2]string{"settings", "Settings"})
	var b bytes.Buffer
	if err := pages.ExecuteTemplate(&b, name, data); err != nil {
		http.Error(w, a.hideDir(err.Error()), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(a.hideDirBytes(b.Bytes()))
}

func back(w http.ResponseWriter, r *http.Request, path, msg string) {
	http.Redirect(w, r, path+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// ret is where a channel action returns to: the wizard's last step or Settings.
func ret(r *http.Request) string {
	if r.URL.Query().Get("ret") == "setup" {
		return "/setup/4"
	}
	return "/settings"
}

func (a *App) values(keys ...string) map[string]string {
	v := map[string]string{}
	for _, k := range keys {
		v[k] = a.S.Get(k)
	}
	return v
}

// ---- first-run wizard ----

func (a *App) setup(w http.ResponseWriter, r *http.Request) {
	step, _ := strconv.Atoi(r.PathValue("step"))
	if step < 1 || step > 4 {
		step = 1
	}
	if step == 2 && a.S.Get("sells") == "services" { // nothing to upload: services are set on the booking step
		http.Redirect(w, r, "/setup/type?picked=1", http.StatusSeeOther)
		return
	}
	d := map[string]any{"Setup": true, "Step": step, "Msg": r.URL.Query().Get("msg"),
		"V": a.values("shop_name", "currency", "qr_path", "pay_link", "sells", "upi_id", "upi_name", "pay_note"), "Ret": "setup"}
	if a.File != nil {
		d["File"] = a.File.Status()
	} else {
		d["File"] = FileStatus{}
	}
	if step == 4 {
		a.channelData(d)
	}
	if step == 2 {
		d["Have"] = a.haveText()
	}
	if step == 3 {
		d["Pay"] = a.payForm(r.URL.Query().Get("pay"), true)
	}
	a.page(w, "setup", d)
}

func (a *App) setupShop(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("shop_name"))
	if name == "" {
		back(w, r, "/setup/1", "Give your shop a name.")
		return
	}
	a.S.Set("shop_name", name)
	if cur := strings.ToUpper(strings.TrimSpace(r.FormValue("currency"))); cur != "" {
		a.S.Set("currency", cur)
	}
	sells := r.FormValue("sells")
	if sells != "services" {
		sells = "products"
	}
	a.S.Set("sells", sells)
	if sells != "products" {
		http.Redirect(w, r, "/setup/type", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/setup/2", http.StatusSeeOther)
}

// ---- services setup: pick a business type, accept its defaults with one OK ----

func (a *App) setupType(w http.ResponseWriter, r *http.Request) {
	ps, _ := a.S.Products()
	var svcs []Product
	for _, p := range ps {
		if p.IsService() {
			svcs = append(svcs, p)
		}
	}
	step := 1
	if a.S.Get("sells") == "services" {
		step = 2
	}
	a.page(w, "biztype", map[string]any{"Setup": true, "Step": step, "Presets": presets, "Picked": r.URL.Query().Get("picked"),
		"V": a.values("biz_type"), "H": a.hoursForm(), "Services": svcs, "Msg": r.URL.Query().Get("msg")})
}

func (a *App) applyPreset(w http.ResponseWriter, r *http.Request) {
	if err := ApplyPreset(a.S, a.File, r.FormValue("preset")); err != nil {
		back(w, r, "/setup/type", err.Error())
		return
	}
	http.Redirect(w, r, "/setup/type?picked=1", http.StatusSeeOther)
}

func (a *App) setupHours(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	fail := func(msg string) {
		http.Redirect(w, r, "/setup/type?picked=1&msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	hours, err := parseHoursForm(r)
	if err != nil {
		fail(err.Error())
		return
	}
	rows, err := parseServices(r)
	if err != nil {
		fail(err.Error())
		return
	}
	if err := a.saveServices(rows); err != nil {
		fail("Services not saved (is products.xlsx open in Excel?): " + err.Error())
		return
	}
	for k, v := range hours {
		a.S.Set(k, v)
	}
	if msg := copiedMsg(r); msg != "" { // stay on this step
		http.Redirect(w, r, "/setup/type?picked=1&msg="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}
	if a.S.Get("sells") == "services" {
		http.Redirect(w, r, "/setup/3", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/setup/2", http.StatusSeeOther)
}

// saveService is the owner's edit of one service's price and minutes on the Products page.
func (a *App) saveService(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	price, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(r.FormValue("price")), ",", "."), 64)
	if err != nil {
		back(w, r, "/products", "The price must be a number.")
		return
	}
	min, _ := strconv.Atoi(r.FormValue("minutes"))
	p := Product{Name: name, Price: price, Kind: KindService, Minutes: min}
	if err := a.File.PutRows([]Product{p}); err != nil {
		back(w, r, "/products", "Not saved (is products.xlsx open in Excel?): "+err.Error())
		return
	}
	back(w, r, "/products", name+" saved.")
}

// setBooking is the owner changing an order's booked time on the Orders page.
func (a *App) setBooking(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	at := strings.Replace(strings.TrimSpace(r.FormValue("at")), "T", " ", 1)
	if err := a.S.MoveBooking(id, at); err != nil {
		back(w, r, "/orders", err.Error())
		return
	}
	a.notifyMoved(id)
	back(w, r, "/orders", fmt.Sprintf("Order %d is now booked for %s.", id, bookLabel(at)))
}

func (a *App) setupPayment(w http.ResponseWriter, r *http.Request) {
	if err := a.savePayment(r); err != nil {
		back(w, r, "/setup/3", err.Error())
		return
	}
	http.Redirect(w, r, "/setup/4", http.StatusSeeOther)
}

func (a *App) sampleFile(w http.ResponseWriter, r *http.Request) {
	dir, err := os.MkdirTemp("", "ownshop-sample")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer os.RemoveAll(dir)
	if err := WriteSample(dir); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="products.xlsx"`)
	http.ServeFile(w, r, filepath.Join(dir, "products.xlsx"))
}

// ---- products file ----

// uploadProducts copies the uploaded workbook to the data folder as products.xlsx and reads it.
// A file that does not read is not kept.
func (a *App) uploadProducts(okPath, failPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, h, err := r.FormFile("file")
		if err != nil {
			back(w, r, failPath, "Choose your Excel file first.")
			return
		}
		defer f.Close()
		if strings.ToLower(filepath.Ext(h.Filename)) != ".xlsx" {
			back(w, r, failPath, "Use an Excel .xlsx file.")
			return
		}
		tmp := a.File.Path + ".upload"
		out, err := os.Create(tmp)
		if err != nil {
			back(w, r, failPath, err.Error())
			return
		}
		io.Copy(out, f)
		out.Close()
		test := NewShopFile(a.S, tmp)
		prev, _ := a.S.Products()
		if err := test.Load(); err != nil {
			os.Remove(tmp)
			if len(prev) > 0 {
				a.File.Load() // back to the file that worked
			}
			back(w, r, failPath, "That file did not read: "+err.Error())
			return
		}
		if err := os.Rename(tmp, a.File.Path); err != nil {
			back(w, r, failPath, "Could not save the file (is products.xlsx open in Excel?): "+err.Error())
			return
		}
		a.File.Load()
		st := a.File.Status()
		back(w, r, okPath, fmt.Sprintf("Read %d products and %d questions. Your file is kept as products.xlsx in %s.", st.Products, st.FAQ, ownFolder))
	}
}

func (a *App) reloadProducts(w http.ResponseWriter, r *http.Request) {
	if err := a.File.Load(); err != nil {
		back(w, r, "/products", "Not read: "+err.Error())
		return
	}
	st := a.File.Status()
	back(w, r, "/products", fmt.Sprintf("Read %d products and %d questions.", st.Products, st.FAQ))
}

func (a *App) products(w http.ResponseWriter, r *http.Request) {
	all, err := a.S.Products()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	ps, _ := a.S.Sellable()
	ignored := ""
	if n := len(all) - len(ps); n > 0 { // the other kind stays in the Excel file, unused
		if a.S.Get("sells") == "services" {
			ignored = fmt.Sprintf("%s in your Excel %s ignored in a services shop.", plural(n, "product row"), map[bool]string{true: "is", false: "are"}[n == 1])
		} else {
			ignored = fmt.Sprintf("%s in your Excel %s ignored in a products shop.", plural(n, "service row"), map[bool]string{true: "is", false: "are"}[n == 1])
		}
	}
	photos := map[string]bool{}
	for _, p := range ps {
		photos[p.SKU] = ProductImage(ImagesDir(a.Dir), p.SKU) != ""
	}
	a.page(w, "products", map[string]any{"Products": ps, "Photos": photos, "File": a.File.Status(), "Ignored": ignored, "Msg": r.URL.Query().Get("msg")})
}

// ---- settings ----

var settingKeys = []string{"shop_name", "address", "currency", "receipt_footer", "daily_send_cap", "pay_link", "sells", "ucp_enabled", "privacy_note"}

func (a *App) settings(w http.ResponseWriter, r *http.Request) {
	d := map[string]any{"V": a.values(append(settingKeys, "qr_path", "upi_id", "upi_name", "pay_note")...), "H": a.hoursForm(), "Msg": r.URL.Query().Get("msg"), "Ret": "settings", "Refresh": a.pairing(), "Pay": a.payForm(r.URL.Query().Get("pay"), false)}
	a.channelData(d)
	a.page(w, "settings", d)
}

func (a *App) savePayment(r *http.Request) error {
	if err := r.ParseMultipartForm(10 << 20); err != nil && err != http.ErrNotMultipart {
		return err
	}
	if m := r.FormValue("pay_remove"); validPay(m) { // Settings: Remove one way to pay
		for _, k := range payKeys[m] {
			a.S.Set(k, "")
		}
		return nil
	}
	// With a chosen method only its fields count; the other methods stay as they are.
	method := r.FormValue("pay_method")
	if validPay(method) {
		for k := range r.Form {
			if (k == "pay_link" || k == "upi_id" || k == "upi_name" || k == "pay_note") && !slices.Contains(payKeys[method], k) {
				delete(r.Form, k)
			}
		}
		if method != "qr" && r.MultipartForm != nil {
			delete(r.MultipartForm.File, "qr")
		}
	}
	if link := strings.TrimSpace(r.FormValue("pay_link")); link != "" {
		if u, err := url.Parse(link); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("the payment link must start with https://")
		}
	}
	if _, ok := r.Form["pay_link"]; ok {
		a.S.Set("pay_link", strings.TrimSpace(r.FormValue("pay_link")))
	}
	for _, k := range []string{"upi_id", "upi_name", "pay_note"} {
		if v, ok := r.Form[k]; ok {
			a.S.Set(k, strings.TrimSpace(v[0]))
		}
	}
	f, h, err := r.FormFile("qr")
	if err != nil {
		return nil
	}
	defer f.Close()
	ext := strings.ToLower(filepath.Ext(h.Filename))
	if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
		return fmt.Errorf("the payment QR must be a .png or .jpg image")
	}
	os.MkdirAll(filepath.Join(a.Dir, "uploads"), 0o755)
	path := filepath.Join(a.Dir, "uploads", "payment-qr"+ext)
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	io.Copy(out, f)
	out.Close()
	return a.S.Set("qr_path", path)
}

func (a *App) saveSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil && err != http.ErrNotMultipart {
		back(w, r, "/settings", err.Error())
		return
	}
	var hours map[string]string
	if r.FormValue("hours_form") != "" { // the booking controls are on the page
		h, err := parseHoursForm(r)
		if err != nil {
			back(w, r, "/settings", err.Error())
			return
		}
		hours = h
	}
	if err := a.savePayment(r); err != nil {
		back(w, r, "/settings", err.Error())
		return
	}
	for k, v := range hours {
		a.S.Set(k, v)
	}
	for _, k := range settingKeys {
		if v, ok := r.Form[k]; ok && k != "pay_link" {
			a.S.Set(k, strings.TrimSpace(v[0]))
		}
	}
	if msg := copiedMsg(r); msg != "" {
		back(w, r, "/settings", msg)
		return
	}
	back(w, r, "/settings", "Saved.")
}

func (a *App) serveSetting(key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := a.S.Get(key)
		if p == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, p)
	}
}

// ---- orders ----

func (a *App) orders(w http.ResponseWriter, r *http.Request) {
	list, err := a.S.Orders()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	d := map[string]any{"Orders": list, "Msg": r.URL.Query().Get("msg"), "PubPhones": a.S.pubPhones()}
	if a.Tunnel != nil {
		d["PubURL"] = a.Tunnel.URL()
	}
	a.page(w, "orders", d)
}

// setStatus moves an order one step (or cancels it), updates the files, and tells the customer.
func (a *App) setStatus(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	o, err := a.S.Advance(id, r.FormValue("to"))
	if err != nil {
		back(w, r, "/orders", err.Error())
		return
	}
	msg := fmt.Sprintf("Order %d is now %s.", id, o.StatusLabel())
	if err := a.Bot.Notify(o.Customer, a.Bot.StatusMessage(o, a.Dir)); err != nil {
		msg += " The message to the customer was NOT sent: " + err.Error()
	} else {
		msg += " Customer notified."
	}
	back(w, r, "/orders", msg)
}

func (a *App) reply(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	o, _ := a.S.Order(id)
	text := strings.TrimSpace(r.FormValue("text"))
	if o == nil || text == "" {
		back(w, r, "/orders", "Nothing to send.")
		return
	}
	if err := a.Bot.Notify(o.Customer, Out{Text: text}); err != nil {
		back(w, r, "/orders", "Not sent: "+err.Error())
		return
	}
	back(w, r, "/orders", "Reply sent.")
}

// setDeliverTo is the owner's edit of an order's delivery address (it also updates the customer's).
func (a *App) setDeliverTo(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := a.S.SetDeliverTo(id, strings.TrimSpace(r.FormValue("deliver_to"))); err != nil {
		back(w, r, "/orders", err.Error())
		return
	}
	back(w, r, "/orders", fmt.Sprintf("Delivery address of order %d saved.", id))
}

func (a *App) receipt(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	o, _ := a.S.Order(id)
	if o == nil || o.Receipt == "" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="receipt-%d.png"`, id))
	}
	http.ServeFile(w, r, o.Receipt)
}

// ---- chats ----

func (a *App) chats(w http.ResponseWriter, r *http.Request) {
	hs, _ := a.S.Handoffs()
	logs, _ := a.S.ChatLog(60)
	a.page(w, "chats", map[string]any{"Support": a.S.SupportQueue(), "Handoffs": hs, "Log": logs, "Msg": r.URL.Query().Get("msg")})
}

func (a *App) resolveHandoff(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var phone string
	a.S.DB.QueryRow(`SELECT phone FROM handoffs WHERE id=?`, id).Scan(&phone)
	msg := "Marked done."
	if text := strings.TrimSpace(r.FormValue("text")); text != "" && phone != "" {
		c, _ := a.S.Customer(phone, "", "")
		if err := a.Bot.Notify(c, Out{Text: text}); err != nil {
			back(w, r, "/chats", "Not sent: "+err.Error())
			return
		}
		msg = "Reply sent."
	}
	a.S.DB.Exec(`UPDATE handoffs SET resolved=1 WHERE id=?`, id)
	back(w, r, "/chats", msg)
}

// ---- chat channels ----

type chanRow struct {
	Name, Account, Label, Status string
	QR, Linkable                 bool
}

func (a *App) channelRows() []chanRow {
	var rows []chanRow
	if a.Hub == nil {
		return rows
	}
	for _, ch := range a.Hub.List() {
		if ch.Name() == "terminal" || ch.Name() == pubChannel { // the Website card shows the public chat
			continue
		}
		r := chanRow{Name: ch.Name(), Account: ch.Account(), Status: ch.Status()}
		r.Label = "Telegram bot"
		if ch.Name() == webChannel {
			r.Label = "Web chat (test customers)"
		}
		if w, ok := ch.(*Wacli); ok {
			r.QR, r.Linkable, r.Label = w.QR() != "", true, "WhatsApp number "+ch.Account()
		}
		rows = append(rows, r)
	}
	// numbers in Settings that could not start still show up
	for _, acct := range waAccounts(a.S) {
		if a.Hub.Get("whatsapp", acct) == nil {
			rows = append(rows, chanRow{Name: "whatsapp", Account: acct, Label: "WhatsApp number " + acct,
				Status: "not running: the WhatsApp helper (wacli) is not installed"})
		}
	}
	return rows
}

// pairing is true while a WhatsApp pairing code is on screen (the page then refreshes itself).
func (a *App) pairing() bool {
	for _, r := range a.channelRows() {
		if r.QR || strings.HasPrefix(r.Status, "starting the link") {
			return true
		}
	}
	return false
}

func (a *App) channelData(d map[string]any) {
	d["Chans"] = a.channelRows()
	d["TG"] = MaskToken(a.S.Get("telegram_token"))
	d["NextWA"] = a.nextWAName()
	a.websiteData(d)
	if a.pairing() {
		d["Refresh"] = true
	}
}

// nextWAName names a new number shop1, shop2, ... (a wacli account name).
func (a *App) nextWAName() string {
	have := waAccounts(a.S)
	for i := 1; ; i++ {
		n := "shop" + strconv.Itoa(i)
		if !slices.Contains(have, n) {
			return n
		}
	}
}

func (a *App) wacli(acct string) *Wacli {
	if a.Hub == nil {
		return nil
	}
	w, _ := a.Hub.Get("whatsapp", acct).(*Wacli)
	return w
}

// addWhatsApp adds a number (a wacli account) and starts pairing; the code shows on the page.
func (a *App) addWhatsApp(w http.ResponseWriter, r *http.Request) {
	acct := strings.TrimSpace(r.FormValue("account"))
	if acct == "" {
		acct = a.nextWAName()
	}
	if !validWacliAccount(acct) {
		back(w, r, ret(r), "That name cannot be used for a number.")
		return
	}
	if a.Hub == nil {
		back(w, r, ret(r), "Chat channels are not running.")
		return
	}
	wa := newWacli(a.S, acct)
	if err := a.Hub.Add(wa); err != nil {
		back(w, r, ret(r), "WhatsApp cannot start: "+err.Error())
		return
	}
	if list := waAccounts(a.S); !slices.Contains(list, acct) {
		a.S.Set("wa_accounts", strings.Join(append(list, acct), ","))
	}
	if err := wa.Link(); err != nil {
		back(w, r, ret(r), err.Error())
		return
	}
	back(w, r, ret(r), "Getting a pairing code. Open WhatsApp on the spare phone and get ready to scan.")
}

func (a *App) link(w http.ResponseWriter, r *http.Request) {
	wa := a.wacli(r.PathValue("acct"))
	if wa == nil {
		back(w, r, ret(r), "That number is not running.")
		return
	}
	if err := wa.Link(); err != nil {
		back(w, r, ret(r), err.Error())
		return
	}
	back(w, r, ret(r), "Getting a pairing code.")
}

// removeWhatsApp stops answering on a number. The phone keeps the link until it is removed there.
func (a *App) removeWhatsApp(w http.ResponseWriter, r *http.Request) {
	acct := r.PathValue("acct")
	a.S.Set("wa_accounts", strings.Join(slices.DeleteFunc(waAccounts(a.S), func(s string) bool { return s == acct }), ","))
	if a.Hub != nil {
		a.Hub.Remove("whatsapp", acct)
	}
	back(w, r, ret(r), "Removed. To unlink it on the phone too: WhatsApp > Settings > Linked devices.")
}

func (a *App) saveTelegram(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(r.FormValue("token"))
	switch {
	case r.FormValue("remove") != "":
		a.S.Set("telegram_token", "")
	case tok == "":
		back(w, r, ret(r), "Paste the token from @BotFather.")
		return
	case !strings.Contains(tok, ":") || strings.ContainsAny(tok, " \t"):
		back(w, r, ret(r), "That does not look like a bot token (it looks like 123456:ABC...).")
		return
	default:
		a.S.Set("telegram_token", tok)
	}
	if a.Hub != nil {
		startTelegram(a.S, a.Hub)
	}
	back(w, r, ret(r), "Telegram saved.")
}

func (a *App) waQR(w http.ResponseWriter, r *http.Request) {
	wa := a.wacli(r.PathValue("acct"))
	if wa == nil || wa.QR() == "" {
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode(wa.QR(), qrcode.Medium, 300)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(png)
}

func serveHTTP(addr string, h http.Handler) {
	go func() {
		if err := http.ListenAndServe(addr, h); err != nil {
			log.Fatalf("web: %v", err)
		}
	}()
}

// plural writes "1 product", "3 products".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

type setupStep struct{ Label, Href string }

// setupSteps are the wizard's pills; a services-only shop has a Services step instead of Products.
func setupSteps(sells string) []setupStep {
	second := setupStep{"Products", ""}
	if sells == "services" {
		second = setupStep{"Services", "/setup/type?picked=1"}
	}
	return []setupStep{{"Shop", ""}, second, {"Payment", ""}, {"Chat channels", ""}}
}

// haveText is what "Keep ..." on the products step offers: "1 product", "15 products".
func (a *App) haveText() string {
	ps, _ := a.S.Sellable()
	if len(ps) == 0 {
		return ""
	}
	return plural(len(ps), "product")
}
