package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xuri/excelize/v2"
)

// The owner's files. ownshop keeps SQLite inside, but what the owner sees and edits is:
//   - products.xlsx: sheet "Products" (sku, name, description, price, stock, keywords, and the
//     optional type (product or service; blank = product) and minutes (a service's length)),
//     sheet "FAQ" (question, answer, keywords) and an optional sheet "Delivery slots"
//     (label, capacity). ownshop re-reads it when it changes and writes stock back to it.
//   - orders.csv: one row per order, rewritten whenever an order changes.

const (
	sheetProducts = "Products"
	sheetFAQ      = "FAQ"
	sheetSlots    = "Delivery slots"
)

type ShopFile struct {
	S    *Store
	Path string // products.xlsx

	mu      sync.Mutex
	mod     time.Time
	loaded  time.Time
	nProd   int
	nFAQ    int
	lastErr string
}

func NewShopFile(s *Store, path string) *ShopFile { return &ShopFile{S: s, Path: path} }

func modTime(p string) time.Time {
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// sheetRows returns the rows of the named sheet (case-insensitive), or of the first sheet
// when name is Products and no such sheet exists.
func sheetRows(f *excelize.File, name string) ([]map[string]string, bool, error) {
	sh := ""
	for _, n := range f.GetSheetList() {
		if strings.EqualFold(strings.TrimSpace(n), name) {
			sh = n
		}
	}
	if sh == "" && name == sheetProducts {
		sh = f.GetSheetName(0)
	}
	if sh == "" {
		return nil, false, nil
	}
	rows, err := f.GetRows(sh)
	if err != nil {
		return nil, true, err
	}
	return tableRows(rows), true, nil
}

func tableRows(rows [][]string) []map[string]string {
	if len(rows) == 0 {
		return nil
	}
	var head []string
	for _, h := range rows[0] {
		head = append(head, strings.ToLower(strings.TrimSpace(h)))
	}
	var out []map[string]string
	for _, row := range rows[1:] {
		m := map[string]string{}
		empty := true
		for i, h := range head {
			if i < len(row) {
				m[h] = strings.TrimSpace(row[i])
				if m[h] != "" {
					empty = false
				}
			}
		}
		if !empty {
			out = append(out, m)
		}
	}
	return out
}

// Load reads the whole file into the shop. Products missing from the file stop being sold.
func (sf *ShopFile) Load() error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	prev := sf.lastErr
	sf.lastErr = ""
	err := sf.load()
	switch {
	case err != nil:
		sf.lastErr = err.Error()
	case sf.S.Get("stock_dirty") == "1":
		if sf.lastErr == "" {
			sf.lastErr = prev // still waiting to write the stock
		}
	default:
		sf.lastErr = ""
	}
	return err
}

func (sf *ShopFile) load() error {
	// A stock change that could not be written (file open in Excel) wins over the file.
	if sf.S.Get("stock_dirty") == "1" {
		sf.writeStock()
	}
	keepStock := sf.S.Get("stock_dirty") == "1"
	mod := modTime(sf.Path)
	f, err := excelize.OpenFile(sf.Path)
	if err != nil {
		return fmt.Errorf("cannot open %s: %w", filepath.Base(sf.Path), err)
	}
	defer f.Close()
	prods, _, err := sheetRows(f, sheetProducts)
	if err != nil {
		return err
	}
	if len(prods) == 0 {
		return fmt.Errorf("the %s sheet has no products", sheetProducts)
	}
	var ps []Product
	for i, m := range prods {
		if m["name"] == "" {
			return fmt.Errorf("%s row %d: name is empty", sheetProducts, i+2)
		}
		price, err := strconv.ParseFloat(strings.ReplaceAll(m["price"], ",", "."), 64)
		if err != nil {
			return fmt.Errorf("%s row %d: price %q is not a number", sheetProducts, i+2, m["price"])
		}
		stock, _ := strconv.Atoi(m["stock"])
		kind := "product"
		if t := strings.ToLower(m["type"]); t == KindService || t == "services" {
			kind = KindService
		}
		minutes, _ := strconv.Atoi(m["minutes"])
		ps = append(ps, Product{SKU: m["sku"], Name: m["name"], Description: m["description"], Price: price, Stock: stock, Keywords: m["keywords"], Kind: kind, Minutes: minutes})
	}
	faq, hasFAQ, err := sheetRows(f, sheetFAQ)
	if err != nil {
		return err
	}
	slots, _, err := sheetRows(f, sheetSlots)
	if err != nil {
		return err
	}

	tx, err := sf.S.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tx.Exec(`UPDATE products SET active=0`)
	for _, p := range ps {
		stockSQL := "excluded.stock"
		if keepStock {
			stockSQL = "products.stock"
		}
		if _, err := tx.Exec(`INSERT INTO products(sku, name, description, price, stock, keywords, kind, minutes, active) VALUES (?,?,?,?,?,?,?,?,1)
		  ON CONFLICT(name) DO UPDATE SET sku=excluded.sku, description=excluded.description, price=excluded.price,
		  stock=`+stockSQL+`, keywords=excluded.keywords, kind=excluded.kind, minutes=excluded.minutes, active=1`, p.SKU, p.Name, p.Description, p.Price, p.Stock, p.Keywords, p.Kind, p.Minutes); err != nil {
			return err
		}
	}
	if hasFAQ {
		tx.Exec(`DELETE FROM faq`)
		for _, m := range faq {
			if m["question"] != "" && m["answer"] != "" {
				tx.Exec(`INSERT INTO faq(question, answer, keywords) VALUES (?,?,?)`, m["question"], m["answer"], m["keywords"])
			}
		}
	}
	for _, m := range slots {
		if m["label"] == "" {
			continue
		}
		c, err := strconv.Atoi(m["capacity"])
		if err != nil || c <= 0 {
			c = 10
		}
		tx.Exec(`INSERT INTO slots(label, capacity) VALUES (?,?) ON CONFLICT(label) DO UPDATE SET capacity=excluded.capacity`, m["label"], c)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	sf.mod, sf.loaded, sf.nProd, sf.nFAQ = mod, time.Now(), len(ps), len(faq)
	return nil
}

// WriteStock puts the current stock back into the Products sheet, leaving everything else as is.
func (sf *ShopFile) WriteStock() error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.writeStock()
}

func (sf *ShopFile) writeStock() error {
	err := sf.writeStockFile()
	if err != nil {
		sf.S.Set("stock_dirty", "1")
		sf.lastErr = "stock not written to the file yet (is it open in Excel?): " + err.Error()
		return err
	}
	sf.S.Set("stock_dirty", "")
	sf.mod = modTime(sf.Path) // our own write is not an owner edit
	return nil
}

func (sf *ShopFile) writeStockFile() error {
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
	nameCol, stockCol := -1, -1
	for i, h := range rows[0] {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "name":
			nameCol = i
		case "stock":
			stockCol = i
		}
	}
	if nameCol < 0 || stockCol < 0 {
		return fmt.Errorf("the %s sheet needs name and stock columns", sheetProducts)
	}
	stock := map[string]int{}
	ps, _ := sf.S.Products()
	for _, p := range ps {
		stock[p.Name] = p.Stock
	}
	for r, row := range rows[1:] {
		if nameCol >= len(row) {
			continue
		}
		if n, ok := stock[strings.TrimSpace(row[nameCol])]; ok {
			cell, _ := excelize.CoordinatesToCellName(stockCol+1, r+2)
			f.SetCellValue(sh, cell, n)
		}
	}
	return f.Save()
}

// PutRows writes products into the Products sheet: an existing row (same name) gets its price,
// type and minutes; a new name is appended. The file is created when missing. Then it is re-read.
func (sf *ShopFile) PutRows(ps []Product) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if err := sf.putRows(ps); err != nil {
		return err
	}
	return sf.load()
}

func (sf *ShopFile) putRows(ps []Product) error {
	if modTime(sf.Path).IsZero() {
		if err := writeWorkbook(sf.Path, []sheet{{sheetProducts, [][]any{{"sku", "name", "description", "price", "stock", "keywords", "type", "minutes"}}}}); err != nil {
			return err
		}
	}
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
	col := map[string]int{}
	for i, h := range rows[0] {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, h := range []string{"sku", "name", "description", "price", "stock", "keywords", "type", "minutes"} {
		if _, ok := col[h]; !ok { // old files: add the column at the end
			col[h] = len(col)
			cell, _ := excelize.CoordinatesToCellName(col[h]+1, 1)
			f.SetCellValue(sh, cell, h)
		}
	}
	byName := map[string]int{}
	for r, row := range rows[1:] {
		if i := col["name"]; i < len(row) {
			byName[strings.TrimSpace(row[i])] = r + 2
		}
	}
	next := len(rows) + 1
	set := func(r int, h string, v any) {
		cell, _ := excelize.CoordinatesToCellName(col[h]+1, r)
		f.SetCellValue(sh, cell, v)
	}
	for _, p := range ps {
		r, ok := byName[p.Name]
		if !ok {
			r, next = next, next+1
			set(r, "sku", p.SKU)
			set(r, "name", p.Name)
			set(r, "description", p.Description)
			set(r, "stock", p.Stock)
			set(r, "keywords", p.Keywords)
		}
		set(r, "price", p.Price)
		set(r, "type", p.Kind)
		if p.Minutes > 0 {
			set(r, "minutes", p.Minutes)
		}
	}
	return f.Save()
}

// Watch re-reads the file when the owner saves it.
func (sf *ShopFile) Watch(ctx context.Context, every time.Duration) {
	for {
		wait(ctx, every)
		if ctx.Err() != nil {
			return
		}
		sf.mu.Lock()
		changed := !sf.mod.IsZero() && !modTime(sf.Path).Equal(sf.mod) || sf.mod.IsZero() && !modTime(sf.Path).IsZero()
		sf.mu.Unlock()
		if changed {
			if err := sf.Load(); err != nil {
				log.Printf("products file: %v", err)
			} else {
				log.Printf("products file changed: reloaded")
			}
		}
	}
}

type FileStatus struct {
	Path, Loaded, Err string
	Products, FAQ     int
	Exists            bool
}

func (sf *ShopFile) Status() FileStatus {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	st := FileStatus{Path: sf.Path, Err: sf.lastErr, Products: sf.nProd, FAQ: sf.nFAQ, Exists: !modTime(sf.Path).IsZero()}
	if !sf.loaded.IsZero() {
		st.Loaded = sf.loaded.Format("2006-01-02 15:04:05")
	}
	return st
}

// WriteOrdersCSV rewrites orders.csv: one row per placed order.
func WriteOrdersCSV(s *Store, path string) error {
	list, err := s.Orders()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write([]string{"order", "date", "customer", "channel", "items", "total", "status", "invoice", "deliver_to"})
	for i := len(list) - 1; i >= 0; i-- { // oldest first, like a ledger
		o := list[i]
		var items []string
		for _, it := range o.Items {
			items = append(items, fmt.Sprintf("%d x %s", it.Qty, it.Name))
		}
		cust := strings.TrimSpace(o.Customer.Name + " " + o.Customer.Phone)
		w.Write([]string{strconv.FormatInt(o.ID, 10), o.Created, cust, o.Customer.Channel, strings.Join(items, "; "),
			fmt.Sprintf("%.2f", o.Total), o.StatusLabel(), o.Invoice, o.DeliverTo})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("orders.csv not updated (is it open in Excel?): %w", err)
	}
	return nil
}
