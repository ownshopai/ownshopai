package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// "Start over" in Settings: clear the test orders and chats, or delete the whole shop and set up a new one.
// Both ask the owner to type the shop name or CLEAR first, and both write a backup before touching anything.

const (
	startClear = "clear" // test data only
	startReset = "reset" // the whole shop
)

// confirmOK reports whether the typed text is the shop name or CLEAR.
func confirmOK(typed, shop string) bool {
	typed = strings.TrimSpace(typed)
	return typed == "CLEAR" || (strings.TrimSpace(shop) != "" && strings.EqualFold(typed, strings.TrimSpace(shop)))
}

func (a *App) startOverPage(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != startClear && kind != startReset {
		http.NotFound(w, r)
		return
	}
	a.page(w, "startover", map[string]any{"Kind": kind, "Msg": r.URL.Query().Get("msg")})
}

func (a *App) startOver(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != startClear && kind != startReset {
		http.NotFound(w, r)
		return
	}
	if !confirmOK(r.FormValue("confirm"), a.S.Get("shop_name")) {
		back(w, r, "/start-over/"+kind, "Nothing was changed: type the shop name or CLEAR to confirm.")
		return
	}
	var (
		dir string
		err error
	)
	if kind == startClear {
		dir, err = a.ClearTestData()
	} else {
		dir, err = a.ResetShop(r.FormValue("disconnect") == "on")
	}
	if err != nil {
		back(w, r, "/start-over/"+kind, "Stopped: "+err.Error())
		return
	}
	if kind == startClear {
		back(w, r, "/orders", "Test orders, customers and chats are cleared. A backup is in the backups folder inside your OwnShop folder ("+filepath.Base(dir)+").")
		return
	}
	back(w, r, "/setup/1", "The old shop is deleted. Set up your new shop. A backup of the old one is in the backups folder inside your OwnShop folder ("+filepath.Base(dir)+").")
}

// lock holds the bot's message lock so no customer message is handled halfway through.
func (a *App) lock() func() {
	if a.Bot == nil {
		return func() {}
	}
	a.Bot.mu.Lock()
	return a.Bot.mu.Unlock
}

// Backup copies the database and products.xlsx into <data dir>/backups/<timestamp>/ and returns that folder.
func (a *App) Backup() (string, error) {
	base := filepath.Join(a.Dir, "backups", time.Now().Format("20060102-150405"))
	dir := base
	for i := 2; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = fmt.Sprintf("%s-%d", base, i)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if _, err := a.S.DB.Exec(`VACUUM INTO ?`, filepath.Join(dir, "ownshop.db")); err != nil {
		return "", fmt.Errorf("database backup: %w", err)
	}
	if a.File != nil {
		if err := copyFile(a.File.Path, filepath.Join(dir, "products.xlsx")); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("products file backup: %w", err)
		}
	}
	return dir, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ClearTestData deletes orders, customers, chats, hand-offs, bookings, blocks and checkouts, plus the
// invoice and receipt images. Products, photos, settings, payment details and channels stay.
func (a *App) ClearTestData() (string, error) {
	defer a.lock()()
	dir, err := a.Backup()
	if err != nil {
		return "", err
	}
	if err := a.clearData(); err != nil {
		return dir, err
	}
	a.S.afterOrder() // orders.csv becomes empty
	return dir, nil
}

func (a *App) clearData() error {
	tx, err := a.S.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM receipts`, `DELETE FROM invoices`, `DELETE FROM order_items`, `DELETE FROM orders`,
		`DELETE FROM customers`, `DELETE FROM chat_log`, `DELETE FROM web_messages`, `DELETE FROM pub_messages`, `DELETE FROM pub_users`, `DELETE FROM push_subs`, `DELETE FROM handoffs`, `DELETE FROM blocks`,
		`UPDATE slots SET used=0`,
		// order and invoice numbers (INV-<order>) start again at 1042
		`UPDATE sqlite_sequence SET seq=1041 WHERE name='orders'`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if a.UCP != nil {
		a.UCP.mu.Lock()
		a.UCP.sessions, a.UCP.creates = map[string]*ucpSession{}, nil
		a.UCP.mu.Unlock()
	}
	for _, d := range []string{"invoices", "receipts"} {
		if err := os.RemoveAll(filepath.Join(a.Dir, d)); err != nil {
			return err
		}
	}
	return nil
}

// ResetShop deletes the whole shop: everything ClearTestData does, plus products, the products file,
// photos, the payment QR and every setting except the port, so first-run setup shows again. With
// disconnect, the WhatsApp numbers and the Telegram bot are stopped and forgotten too.
func (a *App) ResetShop(disconnect bool) (string, error) {
	defer a.lock()()
	dir, err := a.Backup()
	if err != nil {
		return "", err
	}
	if err := a.clearData(); err != nil {
		return dir, err
	}
	a.releaseLinkBestEffort()
	keep := []string{"port"}
	if disconnect {
		a.disconnectChannels()
	} else {
		keep = append(keep, "wa_accounts", "telegram_token")
	}
	tx, err := a.S.DB.Begin()
	if err != nil {
		return dir, err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM products`, `DELETE FROM web_users`, `DELETE FROM faq`, `DELETE FROM slots`,
		`DELETE FROM settings WHERE key NOT IN ('` + strings.Join(keep, "','") + `')`} {
		if _, err := tx.Exec(q); err != nil {
			return dir, fmt.Errorf("%s: %w", q, err)
		}
	}
	seedSettings(tx)
	if err := tx.Commit(); err != nil {
		return dir, err
	}
	for _, p := range []string{ImagesDir(a.Dir), filepath.Join(a.Dir, "uploads")} {
		if err := os.RemoveAll(p); err != nil {
			return dir, err
		}
	}
	if a.File != nil {
		if err := a.File.Empty(); err != nil {
			return dir, err
		}
	}
	a.S.afterOrder()
	return dir, nil
}

// disconnectChannels stops every WhatsApp number and the Telegram bot, deletes ownshop's own wacli
// stores for those numbers (<data dir>/wacli/<account>) and clears the Telegram token.
func (a *App) disconnectChannels() {
	for _, acct := range waAccounts(a.S) {
		if a.Hub != nil {
			a.Hub.Remove("whatsapp", acct)
		}
		if validWacliAccount(acct) {
			os.RemoveAll(filepath.Join(a.Dir, "wacli", acct))
		}
	}
	if a.Hub != nil {
		for _, ch := range a.Hub.List() {
			if ch.Name() == "telegram" {
				a.Hub.Remove("telegram", ch.Account())
			}
		}
	}
	a.S.Set("wa_accounts", "")
	a.S.Set("telegram_token", "")
}

// Empty replaces the products file with an empty template (headers only) and forgets what was read.
func (sf *ShopFile) Empty() error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if err := writeWorkbook(sf.Path, []sheet{
		{sheetProducts, [][]any{{"sku", "name", "description", "price", "stock", "keywords", "type", "minutes"}}},
		{sheetFAQ, [][]any{{"question", "answer", "keywords"}}},
		{sheetSlots, [][]any{{"label", "capacity"}}},
	}); err != nil {
		return err
	}
	sf.mod, sf.loaded, sf.nProd, sf.nFAQ, sf.lastErr = modTime(sf.Path), time.Time{}, 0, 0, ""
	return nil
}

// releaseLinkBestEffort gives the free fixed link back and signs out before a shop is deleted, so
// the name does not stay tied to a shop that no longer exists. Failures do not stop the reset.
func (a *App) releaseLinkBestEffort() {
	if a.Tunnel != nil {
		a.Tunnel.halt()
	}
	if a.Links == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := ReleaseLink(ctx, a.S, a.Links); err != nil {
		log.Printf("start over: free link not released: %v", err)
	}
	if err := SignOutLink(ctx, a.S, a.Links); err != nil {
		log.Printf("start over: link service sign-out: %v", err)
	}
}
