package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS products (
  id INTEGER PRIMARY KEY, sku TEXT NOT NULL DEFAULT '', name TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '', price REAL NOT NULL DEFAULT 0, stock INTEGER NOT NULL DEFAULT 0,
  keywords TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS faq (
  id INTEGER PRIMARY KEY, question TEXT NOT NULL, answer TEXT NOT NULL, keywords TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS customers (
  id INTEGER PRIMARY KEY, phone TEXT NOT NULL UNIQUE, name TEXT NOT NULL DEFAULT '', jid TEXT NOT NULL DEFAULT '',
  created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS slots (
  id INTEGER PRIMARY KEY, label TEXT NOT NULL UNIQUE, capacity INTEGER NOT NULL DEFAULT 10, used INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS orders (
  id INTEGER PRIMARY KEY AUTOINCREMENT, customer_id INTEGER NOT NULL REFERENCES customers(id),
  status TEXT NOT NULL DEFAULT 'cart', slot_id INTEGER REFERENCES slots(id), total REAL NOT NULL DEFAULT 0,
  created TEXT NOT NULL, updated TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS order_items (
  id INTEGER PRIMARY KEY, order_id INTEGER NOT NULL REFERENCES orders(id), product_id INTEGER NOT NULL REFERENCES products(id),
  name TEXT NOT NULL, qty INTEGER NOT NULL, price REAL NOT NULL);
CREATE TABLE IF NOT EXISTS invoices (
  id INTEGER PRIMARY KEY, order_id INTEGER NOT NULL UNIQUE REFERENCES orders(id), number TEXT NOT NULL,
  total REAL NOT NULL, paid INTEGER NOT NULL DEFAULT 0, created TEXT NOT NULL, paid_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS chat_log (
  id INTEGER PRIMARY KEY, ts TEXT NOT NULL, phone TEXT NOT NULL, direction TEXT NOT NULL,
  text TEXT NOT NULL, tool TEXT NOT NULL DEFAULT '', raw TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS receipts (
  id INTEGER PRIMARY KEY, order_id INTEGER NOT NULL UNIQUE REFERENCES orders(id), path TEXT NOT NULL, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS handoffs (
  id INTEGER PRIMARY KEY, ts TEXT NOT NULL, phone TEXT NOT NULL, text TEXT NOT NULL, reason TEXT NOT NULL,
  resolved INTEGER NOT NULL DEFAULT 0);
`

// Order statuses, in the order the owner moves them.
const (
	StCart     = "cart"
	StAwaiting = "awaiting_payment"
	StPaid     = "paid"
	StPacked   = "packed"
	StShipped  = "shipped"
	StDone     = "delivered"
	StCancel   = "cancelled"
)

var nextStatus = map[string]string{StAwaiting: StPaid, StPaid: StPacked, StPacked: StShipped, StShipped: StDone}

var statusLabel = map[string]string{
	StCart: "In cart", StAwaiting: "Waiting for payment", StPaid: "Payment received",
	StPacked: "Packed", StShipped: "Shipped", StDone: "Delivered", StCancel: "Cancelled",
}

type Store struct {
	DB *sql.DB
	// AfterOrder runs after an order is placed or changes status (orders.csv, stock write-back).
	AfterOrder func()
}

func (s *Store) afterOrder() {
	if s.AfterOrder != nil {
		s.AfterOrder()
	}
}

func now() string { return time.Now().Format("2006-01-02 15:04:05") }

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + webSchema + pubSchema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	// Order numbers start at 1042 so they look like a real shop's.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_sequence WHERE name='orders'`).Scan(&n)
	if n == 0 {
		db.Exec(`INSERT INTO sqlite_sequence(name, seq) VALUES ('orders', 1041)`)
	}
	// Channel columns arrived with the channel adapters; older databases get them here.
	for _, q := range []string{
		`ALTER TABLE customers ADD COLUMN channel TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE customers ADD COLUMN account TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orders ADD COLUMN channel TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orders ADD COLUMN account TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE products ADD COLUMN active INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE customers ADD COLUMN address TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE customers ADD COLUMN wait_addr INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE orders ADD COLUMN deliver_to TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE products ADD COLUMN kind TEXT NOT NULL DEFAULT 'product'`,
		`ALTER TABLE products ADD COLUMN minutes INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE orders ADD COLUMN book_day TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orders ADD COLUMN book_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orders ADD COLUMN book_min INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS blocks (at TEXT PRIMARY KEY)`,
		// Orders placed by an AI agent over UCP: the checkout id and buyer contact that may look them up.
		`ALTER TABLE orders ADD COLUMN ucp_checkout TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orders ADD COLUMN ucp_phone TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE orders ADD COLUMN ucp_email TEXT NOT NULL DEFAULT ''`,
		// A blocked customer gets no reply on any channel (Users page).
		`ALTER TABLE customers ADD COLUMN blocked INTEGER NOT NULL DEFAULT 0`,
	} {
		db.Exec(q) // fails harmlessly when the column exists
	}
	s := &Store{DB: db}
	seedSettings(db)
	s.migrateAvail()
	s.migrateSells()
	// Databases made before the model switch still point at the old bundled model.
	db.Exec(`UPDATE settings SET value=? WHERE key='model_path' AND value='models/fg-q8.gguf'`, DefaultModelPath)
	return s, nil
}

// seedSettings fills in every setting that is missing with its first-run default.
func seedSettings(db interface {
	Exec(string, ...any) (sql.Result, error)
}) {
	for k, v := range map[string]string{"shop_name": "My Shop", "address": "", "currency": "INR", "hours": "", "qr_path": "",
		"receipt_footer": "Thank you for shopping with us.", "sells": "products",
		"work_days": "Mon-Sat", "open_hours": "10:00-19:00", "slot_minutes": "30", "days_ahead": "7", "soul": "", "soul_in_prompt": "off", "llama_server": "", "model_path": DefaultModelPath} {
		db.Exec(`INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`, k, v)
	}
}

func (s *Store) Get(key string) string {
	var v string
	s.DB.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return v
}

func (s *Store) Set(key, val string) error {
	_, err := s.DB.Exec(`INSERT INTO settings(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, val)
	if err == nil && (key == "work_days" || key == "open_hours") {
		s.legacyChanged() // keep the weekly schedule in step with the old keys
	}
	return err
}

type Product struct {
	ID                               int64
	SKU, Name, Description, Keywords string
	Price                            float64
	Stock                            int
	Kind                             string // "product" or "service"; a service has no stock
	Minutes                          int    // service length; 0 means one slot
}

func (p Product) IsService() bool { return p.Kind == KindService }

const KindService = "service"

// Sellable are the rows this shop sells: services in a services shop, products otherwise.
// The other kind stays in the file and the database but is ignored.
func (s *Store) Sellable() ([]Product, error) {
	ps, err := s.Products()
	svc := s.Get("sells") == "services"
	var out []Product
	for _, p := range ps {
		if p.IsService() == svc {
			out = append(out, p)
		}
	}
	return out, err
}

// migrateSells: a shop is products or services. An old "both" shop becomes services when it
// has services and no products, else products.
func (s *Store) migrateSells() {
	if s.Get("sells") != "both" {
		return
	}
	var np, ns int
	s.DB.QueryRow(`SELECT COUNT(*) FROM products WHERE active=1 AND kind='service'`).Scan(&ns)
	s.DB.QueryRow(`SELECT COUNT(*) FROM products WHERE active=1 AND kind<>'service'`).Scan(&np)
	if ns > 0 && np == 0 {
		s.Set("sells", "services")
	} else {
		s.Set("sells", "products")
	}
}

func (s *Store) Products() ([]Product, error) {
	rows, err := s.DB.Query(`SELECT id, sku, name, description, price, stock, keywords, kind, minutes FROM products WHERE active=1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Product
	for rows.Next() {
		var p Product
		rows.Scan(&p.ID, &p.SKU, &p.Name, &p.Description, &p.Price, &p.Stock, &p.Keywords, &p.Kind, &p.Minutes)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpsertProduct(p Product) error {
	if p.Kind == "" {
		p.Kind = "product"
	}
	_, err := s.DB.Exec(`INSERT INTO products(sku, name, description, price, stock, keywords, kind, minutes) VALUES (?,?,?,?,?,?,?,?)
	  ON CONFLICT(name) DO UPDATE SET sku=excluded.sku, description=excluded.description, price=excluded.price,
	  stock=excluded.stock, keywords=excluded.keywords, kind=excluded.kind, minutes=excluded.minutes, active=1`, p.SKU, p.Name, p.Description, p.Price, p.Stock, p.Keywords, p.Kind, p.Minutes)
	return err
}

type FAQ struct {
	ID                         int64
	Question, Answer, Keywords string
}

func (s *Store) FAQs() ([]FAQ, error) {
	rows, err := s.DB.Query(`SELECT id, question, answer, keywords FROM faq ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FAQ
	for rows.Next() {
		var f FAQ
		rows.Scan(&f.ID, &f.Question, &f.Answer, &f.Keywords)
		out = append(out, f)
	}
	return out, rows.Err()
}

type Slot struct {
	ID             int64
	Label          string
	Capacity, Used int
}

func (s *Store) Slots() ([]Slot, error) {
	rows, err := s.DB.Query(`SELECT id, label, capacity, used FROM slots WHERE used < capacity ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Slot
	for rows.Next() {
		var sl Slot
		rows.Scan(&sl.ID, &sl.Label, &sl.Capacity, &sl.Used)
		out = append(out, sl)
	}
	return out, rows.Err()
}

// Customer is one person on one channel. Phone is the unique key: a phone number for WhatsApp,
// "tg:<chat id>" for Telegram. JID is the address replies go to on Channel/Account (a WhatsApp
// JID, a Telegram chat id); it is only set by an inbound message, so ownshop can only reply.
type Customer struct {
	ID                                 int64
	Phone, Name, JID, Channel, Account string
	Address                            string // delivery address: typed text or a shared location with a map link
	WaitAddr                           bool   // asked for the address at YES; the next message is the address
}

const custCols = `id, phone, name, jid, channel, account, address, wait_addr`

func scanCustomer(r interface{ Scan(...any) error }) (Customer, error) {
	var c Customer
	err := r.Scan(&c.ID, &c.Phone, &c.Name, &c.JID, &c.Channel, &c.Account, &c.Address, &c.WaitAddr)
	return c, err
}

// SetCustomerChannel records where the customer last wrote from, so replies go back there.
func (s *Store) SetCustomerChannel(id int64, channel, account string) {
	if channel != "" {
		s.DB.Exec(`UPDATE customers SET channel=?, account=? WHERE id=?`, channel, account, id)
	}
}

// SetAddress saves the customer's delivery address and ends any wait for it.
func (s *Store) SetAddress(id int64, addr string) error {
	_, err := s.DB.Exec(`UPDATE customers SET address=?, wait_addr=0 WHERE id=?`, addr, id)
	return err
}

// SetWaitAddr marks that the customer was asked for a delivery address.
func (s *Store) SetWaitAddr(id int64, on bool) {
	s.DB.Exec(`UPDATE customers SET wait_addr=? WHERE id=?`, boolInt(on), id)
}

// SetDeliverTo is the owner's edit of an order's delivery address; it also becomes the customer's address.
func (s *Store) SetDeliverTo(orderID int64, addr string) error {
	if _, err := s.DB.Exec(`UPDATE orders SET deliver_to=?, updated=? WHERE id=?`, addr, now(), orderID); err != nil {
		return err
	}
	_, err := s.DB.Exec(`UPDATE customers SET address=? WHERE id=(SELECT customer_id FROM orders WHERE id=?)`, addr, orderID)
	if err == nil {
		s.afterOrder()
	}
	return err
}

func (s *Store) Customer(phone, name, jid string) (Customer, error) {
	_, err := s.DB.Exec(`INSERT INTO customers(phone, name, jid, created) VALUES (?,?,?,?)
	  ON CONFLICT(phone) DO UPDATE SET name=CASE WHEN excluded.name<>'' THEN excluded.name ELSE customers.name END,
	  jid=CASE WHEN excluded.jid<>'' THEN excluded.jid ELSE customers.jid END`, phone, name, jid, now())
	if err != nil {
		return Customer{}, err
	}
	return scanCustomer(s.DB.QueryRow(`SELECT `+custCols+` FROM customers WHERE phone=?`, phone))
}

func (s *Store) CustomerByID(id int64) (Customer, error) {
	return scanCustomer(s.DB.QueryRow(`SELECT `+custCols+` FROM customers WHERE id=?`, id))
}

type Item struct {
	ProductID int64
	Name      string
	Qty       int
	Price     float64
	Service   bool
	Minutes   int
}

type Order struct {
	ID         int64
	CustomerID int64
	Status     string
	SlotID     sql.NullInt64
	SlotLabel  string
	Total      float64
	Created    string
	Items      []Item
	Customer   Customer
	Invoice    string
	Paid       bool
	PaidAt     string
	Receipt    string
	DeliverTo  string // the customer's address, copied at confirmation
	BookDay    string // service booking: the day picked ("2006-01-02"), before the time
	BookAt     string // service booking: start, "2006-01-02 15:04"; empty = not booked
	BookMin    int    // service booking: minutes held from BookAt
	// Channel and Account the order was placed on; status updates go back there.
	Channel, Account string
}

func (o Order) StatusLabel() string { return statusLabel[o.Status] }
func (o Order) Next() string        { return nextStatus[o.Status] }

// MapURL is the map link inside DeliverTo, or "".
func (o Order) MapURL() string {
	if i := strings.Index(o.DeliverTo, mapPrefix); i >= 0 {
		return strings.Fields(o.DeliverTo[i:])[0]
	}
	return ""
}

func (s *Store) loadOrder(where string, args ...any) (*Order, error) {
	var o Order
	err := s.DB.QueryRow(`SELECT o.id, o.customer_id, o.status, o.slot_id, COALESCE(sl.label,''), o.total, o.created,
	  COALESCE(i.number,''), COALESCE(i.paid,0), COALESCE(i.paid_at,''), COALESCE(r.path,''), o.channel, o.account, o.deliver_to, o.book_day, o.book_at, o.book_min
	  FROM orders o LEFT JOIN slots sl ON sl.id=o.slot_id LEFT JOIN invoices i ON i.order_id=o.id
	  LEFT JOIN receipts r ON r.order_id=o.id `+where, args...).
		Scan(&o.ID, &o.CustomerID, &o.Status, &o.SlotID, &o.SlotLabel, &o.Total, &o.Created, &o.Invoice, &o.Paid, &o.PaidAt, &o.Receipt, &o.Channel, &o.Account, &o.DeliverTo, &o.BookDay, &o.BookAt, &o.BookMin)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT oi.product_id, oi.name, oi.qty, oi.price, COALESCE(p.kind,'')='service', COALESCE(p.minutes,0)
	  FROM order_items oi LEFT JOIN products p ON p.id=oi.product_id WHERE oi.order_id=? ORDER BY oi.id`, o.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it Item
		rows.Scan(&it.ProductID, &it.Name, &it.Qty, &it.Price, &it.Service, &it.Minutes)
		o.Items = append(o.Items, it)
	}
	if o.BookAt != "" {
		o.SlotLabel = bookLabel(o.BookAt)
	}
	o.Customer, _ = s.CustomerByID(o.CustomerID)
	if o.Channel != "" {
		o.Customer.Channel, o.Customer.Account = o.Channel, o.Account
	}
	return &o, nil
}

func (s *Store) Order(id int64) (*Order, error) { return s.loadOrder(`WHERE o.id=?`, id) }

// Cart returns the customer's open cart, or nil.
func (s *Store) Cart(cid int64) (*Order, error) {
	return s.loadOrder(`WHERE o.customer_id=? AND o.status='cart' ORDER BY o.id DESC LIMIT 1`, cid)
}

// LatestOrder returns the customer's newest placed (non-cart) order, or nil.
func (s *Store) LatestOrder(cid int64) (*Order, error) {
	return s.loadOrder(`WHERE o.customer_id=? AND o.status<>'cart' ORDER BY o.id DESC LIMIT 1`, cid)
}

func (s *Store) Orders() ([]*Order, error) {
	rows, err := s.DB.Query(`SELECT id FROM orders WHERE status<>'cart' ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	var out []*Order
	for _, id := range ids {
		o, err := s.Order(id)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func (s *Store) EnsureCart(cid int64) (*Order, error) {
	o, err := s.Cart(cid)
	if err != nil || o != nil {
		return o, err
	}
	if _, err := s.DB.Exec(`INSERT INTO orders(customer_id, status, created, updated, channel, account)
	  SELECT ?, 'cart', ?, ?, channel, account FROM customers WHERE id=?`, cid, now(), now(), cid); err != nil {
		return nil, err
	}
	return s.Cart(cid)
}

func (s *Store) AddItem(orderID int64, p Product, qty int) error {
	res, err := s.DB.Exec(`UPDATE order_items SET qty=qty+? WHERE order_id=? AND product_id=?`, qty, orderID, p.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.DB.Exec(`INSERT INTO order_items(order_id, product_id, name, qty, price) VALUES (?,?,?,?,?)`, orderID, p.ID, p.Name, qty, p.Price); err != nil {
			return err
		}
	}
	return s.retotal(orderID)
}

func (s *Store) retotal(orderID int64) error {
	_, err := s.DB.Exec(`UPDATE orders SET total=(SELECT COALESCE(SUM(qty*price),0) FROM order_items WHERE order_id=?), updated=? WHERE id=?`, orderID, now(), orderID)
	return err
}

func (s *Store) SetSlot(orderID, slotID int64) error {
	_, err := s.DB.Exec(`UPDATE orders SET slot_id=?, updated=? WHERE id=?`, slotID, now(), orderID)
	return err
}

// Confirm turns a cart into an order: checks and cuts stock, books the slot and creates the invoice, in one transaction.
func (s *Store) Confirm(o *Order) (string, error) {
	sc := s.Schedule() // read before the transaction: the store has one connection
	tx, err := s.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	for _, it := range o.Items {
		// Services have no stock: never cut, never short.
		res, err := tx.Exec(`UPDATE products SET stock=CASE WHEN kind='service' THEN stock ELSE stock-? END
		  WHERE id=? AND (kind='service' OR stock>=?)`, it.Qty, it.ProductID, it.Qty)
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return "", fmt.Errorf("not enough stock for %s", it.Name)
		}
	}
	if o.BookAt != "" {
		// A booked time holds capacity 1: another customer may have taken it since the pick.
		if !s.timeFree(tx, sc, o.BookAt, o.BookMin, o.ID) {
			return "", errTaken
		}
	} else if o.SlotID.Valid {
		tx.Exec(`UPDATE slots SET used=used+1 WHERE id=?`, o.SlotID.Int64)
	}
	number := fmt.Sprintf("INV-%d", o.ID)
	if _, err := tx.Exec(`INSERT INTO invoices(order_id, number, total, created) VALUES (?,?,?,?)`, o.ID, number, o.Total, now()); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE orders SET status=?, updated=?, deliver_to=(SELECT address FROM customers WHERE id=?) WHERE id=?`, StAwaiting, now(), o.CustomerID, o.ID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	s.afterOrder()
	return number, nil
}

// Advance moves an order to the given status. Payment received also marks the invoice paid.
func (s *Store) Advance(id int64, to string) (*Order, error) {
	o, err := s.Order(id)
	if err != nil || o == nil {
		return nil, fmt.Errorf("order %d not found", id)
	}
	if to == StCancel {
		return s.cancel(o)
	}
	if nextStatus[o.Status] != to {
		return nil, fmt.Errorf("order %d is %q; it cannot move to %q", id, statusLabel[o.Status], statusLabel[to])
	}
	if _, err := s.DB.Exec(`UPDATE orders SET status=?, updated=? WHERE id=?`, to, now(), id); err != nil {
		return nil, err
	}
	if to == StPaid {
		s.DB.Exec(`UPDATE invoices SET paid=1, paid_at=? WHERE order_id=?`, now(), id)
	}
	s.afterOrder()
	return s.Order(id)
}

// cancel stops an order that is not delivered yet and puts its items back in stock.
// ownshop never moves money: any refund is between the owner and the customer.
func (s *Store) cancel(o *Order) (*Order, error) {
	if o.Status == StCart || o.Status == StDone || o.Status == StCancel {
		return nil, fmt.Errorf("order %d is %q; it cannot be cancelled", o.ID, statusLabel[o.Status])
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, it := range o.Items {
		tx.Exec(`UPDATE products SET stock=stock+? WHERE id=? AND kind<>'service'`, it.Qty, it.ProductID)
	}
	if o.SlotID.Valid && o.BookAt == "" {
		tx.Exec(`UPDATE slots SET used=MAX(used-1,0) WHERE id=?`, o.SlotID.Int64)
	}
	tx.Exec(`UPDATE orders SET status=?, updated=? WHERE id=?`, StCancel, now(), o.ID)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.afterOrder()
	return s.Order(o.ID)
}

func (s *Store) Log(phone, dir, text, tool, raw string) {
	s.DB.Exec(`INSERT INTO chat_log(ts, phone, direction, text, tool, raw) VALUES (?,?,?,?,?,?)`, now(), phone, dir, text, tool, raw)
}

type LogRow struct{ TS, Phone, Direction, Text, Tool, Raw string }

func (s *Store) ChatLog(limit int) ([]LogRow, error) {
	rows, err := s.DB.Query(`SELECT ts, phone, direction, text, tool, raw FROM chat_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogRow
	for rows.Next() {
		var r LogRow
		rows.Scan(&r.TS, &r.Phone, &r.Direction, &r.Text, &r.Tool, &r.Raw)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Handoff(phone, text, reason string) {
	s.DB.Exec(`INSERT INTO handoffs(ts, phone, text, reason) VALUES (?,?,?,?)`, now(), phone, text, reason)
}

type HandoffRow struct {
	ID                      int64
	TS, Phone, Text, Reason string
}

func (s *Store) Handoffs() ([]HandoffRow, error) {
	rows, err := s.DB.Query(`SELECT id, ts, phone, text, reason FROM handoffs WHERE resolved=0 ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HandoffRow
	for rows.Next() {
		var h HandoffRow
		rows.Scan(&h.ID, &h.TS, &h.Phone, &h.Text, &h.Reason)
		out = append(out, h)
	}
	return out, rows.Err()
}

// words lower-cases and splits text into plain words.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	})
}

func (s *Store) SaveReceipt(orderID int64, path string) error {
	_, err := s.DB.Exec(`INSERT INTO receipts(order_id, path, created) VALUES (?,?,?)
	  ON CONFLICT(order_id) DO UPDATE SET path=excluded.path`, orderID, path, now())
	return err
}
