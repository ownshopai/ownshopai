package main

import (
	"net"
	"net/http"
	"time"

	"bufio"
	"context"
	"flag"
	"fmt"
	llmkit "github.com/muthuishere/toolnexus/golang"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const usage = `ownshop - OwnShop AI, a free, local shop assistant that works with WhatsApp, Telegram and your website

Shop owners: double-click OwnShop AI. Everything else happens in the browser.

For developers:
  ownshop                                                     same as a double-click (add --background to skip the browser)
  ownshop serve   [--addr 127.0.0.1:2007] [--db ownshop.db]              the shop in the foreground; files next to the database
  ownshop chat    [--http :8081] [--phone +31...] [--name ..]  terminal customer simulator (same bot, a fake channel)
  ownshop import  <products.xlsx> [--db ownshop.db]             read a products workbook (sheets Products, FAQ, Delivery slots)
  ownshop eval    [--db ownshop.db]                             score the model on the built-in customer messages
  ownshop sample  [dir]                                        write the example products.xlsx and demo QR
  ownshop webuser add <email|phone> <password> [--name ..] | list | delete <email|phone> web chat test customers [--db]
  ownshop user    block|unblock <id or phone> | list          block a customer on every channel [--db]
  ownshop say     --user <email|phone> --password <p> [--url http://127.0.0.1:2007] [--location lat,lng] "text"
                                                             chat as a test customer with a running shop
`

// wacliHome holds one wacli store per linked number (<data dir>/wacli/<account>), set by startShop.
var wacliHome string

// newWacli is a WhatsApp number run by the wacli inside ownshop, with its store in ownshop's data dir.
func newWacli(s *Store, acct string) *Wacli {
	bin, err := helper("wacli", helperOverride(s, "wacli_bin"))
	if err != nil {
		log.Printf("whatsapp: %v", err)
	}
	w := NewWacli(bin, acct)
	if wacliHome != "" {
		w.Store = filepath.Join(wacliHome, acct)
	}
	return w
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "--background" || strings.HasPrefix(os.Args[1], "-psn_") {
		runApp(len(os.Args) > 1 && os.Args[1] == "--background")
		return
	}
	cmd, args := os.Args[1], os.Args[2:]
	if runCLI(cmd, args) {
		return
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dbPath := fs.String("db", "ownshop.db", "SQLite database file")
	addr := fs.String("addr", "127.0.0.1:2007", "web address (serve)")
	httpAddr := fs.String("http", ":8081", "web address for owner actions while chatting (chat); empty to disable")
	phone := fs.String("phone", "+31600000001", "customer phone (chat)")
	name := fs.String("name", "Emma Novak", "customer name (chat)")
	quiet := fs.Bool("quiet", false, "hide the raw model call lines (chat)")
	fs.Parse(args)

	switch cmd {
	case "serve":
		startShop(filepath.Dir(*dbPath), *dbPath, *addr, nil)
	case "chat":
		chat(*dbPath, *httpAddr, *phone, *name, *quiet)
	case "import":
		if fs.NArg() != 1 {
			log.Fatal("usage: ownshop import <products.xlsx>")
		}
		s := mustStore(*dbPath)
		sf := NewShopFile(s, fs.Arg(0))
		if err := sf.Load(); err != nil {
			log.Fatal(err)
		}
		st := sf.Status()
		fmt.Printf("read %d products and %d questions from %s\n", st.Products, st.FAQ, fs.Arg(0))
	case "eval":
		runEval(*dbPath)
	case "transcribe": // check voice notes on this machine: ownshop transcribe <note.ogg>
		if fs.NArg() != 1 {
			log.Fatal("usage: ownshop transcribe <voice-note.ogg>")
		}
		t0 := time.Now()
		text, err := Transcribe(mustStore(*dbPath), fs.Arg(0))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s\n(%.2fs)\n", text, time.Since(t0).Seconds())
	case "netcheck": // hidden: can this machine resolve and reach the internet over TLS? (Termux has no resolv.conf)
		url := "https://api.telegram.org/bot0:netcheck/getMe"
		if fs.NArg() > 0 {
			url = fs.Arg(0)
		}
		t0 := time.Now()
		res, err := (&http.Client{Timeout: 20 * time.Second}).Get(url)
		if err != nil {
			log.Fatal(err)
		}
		res.Body.Close()
		fmt.Printf("%s -> HTTP %d (%.2fs)\n", url, res.StatusCode, time.Since(t0).Seconds())
	case "sample":
		dir := "sample"
		if fs.NArg() > 0 {
			dir = fs.Arg(0)
		}
		if err := WriteSample(dir); err != nil {
			log.Fatal(err)
		}
		fmt.Println("wrote", dir)
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
}

func setLogOutput(w io.Writer) { log.SetOutput(w) }

func mustStore(path string) *Store {
	s, err := OpenStore(path)
	if err != nil {
		log.Fatal(err)
	}
	return s
}

// startModel starts the local model. If it cannot start, the shop still runs: every customer
// message goes to the owner as a hand-off, and the reason is in the log.
func startModel(s *Store) (Brain, func()) {
	m, stop, err := StartBrain(s)
	if err != nil {
		log.Printf("model: %v (customers are handed to you until the model starts)", err)
		return noBrain{err}, func() {}
	}
	return m, stop
}

type noBrain struct{ err error }

func (n noBrain) Run(context.Context, string, *llmkit.Toolkit) (llmkit.RunResult, error) {
	return llmkit.RunResult{}, n.err
}

func onSignal(f func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() { <-ch; f(); os.Exit(0) }()
}

// startShop runs the shop: model, chat channels, products file, orders file, web page.
// ready (optional) runs once the web page answers.
func startShop(dir, dbPath, addr string, ready func()) {
	if abs, err := filepath.Abs(dir); err == nil {
		wacliHome = filepath.Join(abs, "wacli")
	}
	s := mustStore(dbPath)
	m, stopModel := startModel(s)
	hub := NewHub(nil)
	g := NewGuard(hub, s)
	g.Typing = hub.Typing
	bot := &Bot{S: s, M: m, Send: g, Debug: func(m string) { log.Println(m) }, Dir: dir}
	hub.OnMessage = func(in Inbound) { receive(s, bot, in) }
	sf := NewShopFile(s, filepath.Join(dir, "products.xlsx"))
	if err := sf.Load(); err != nil && sf.Status().Exists {
		log.Printf("products file: %v", err)
	}
	ordersCSV := filepath.Join(dir, "orders.csv")
	s.AfterOrder = func() {
		sf.WriteStock()
		if err := WriteOrdersCSV(s, ordersCSV); err != nil {
			log.Printf("orders file: %v", err)
		}
	}
	WriteOrdersCSV(s, ordersCSV)
	ctx, cancel := context.WithCancel(context.Background())
	go sf.Watch(ctx, 2*time.Second)
	startChannels(s, hub)
	stopTunnel := func() {}
	onSignal(func() { cancel(); hub.Close(); stopModel(); stopTunnel() })
	app := &App{S: s, Bot: bot, Hub: hub, File: sf, Dir: dir, Links: newLinkService()}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("web: %v", err)
	}
	stopTunnel = app.startWebsite(ln.Addr().(*net.TCPAddr).Port)
	go linkHeartbeat(ctx, s, app.Links, func() {
		if app.Tunnel != nil {
			app.restartTunnel()
		}
	})
	log.Printf("OwnShop AI is running: open http://localhost:%d", ln.Addr().(*net.TCPAddr).Port)
	if ready != nil {
		ready()
	}
	log.Fatal(http.Serve(ln, app.Routes()))
}

// receive is where every channel's messages arrive. Voice notes are turned into text first.
func receive(s *Store, bot *Bot, in Inbound) {
	if in.Voice {
		if in.AudioPath != "" {
			defer os.Remove(in.AudioPath)
		}
		switch {
		case !voiceOn(s):
			s.Handoff(in.Key(), "[voice note]", "voice notes are off (Settings)")
			return
		case in.AudioPath == "":
			s.Handoff(in.Key(), "[voice note]", in.Channel+": this channel cannot fetch voice notes yet")
			return
		}
		text, err := Transcribe(s, in.AudioPath)
		if err != nil {
			s.Handoff(in.Key(), "[voice note]", "voice: "+err.Error())
			return
		}
		log.Printf("voice note from %s: %s", in.Key(), text)
		in.Text = text
	}
	bot.Handle(context.Background(), in)
}

// waAccounts is the list of linked WhatsApp numbers (wacli account names), in Settings.
func waAccounts(s *Store) []string {
	var out []string
	for _, a := range strings.Split(s.Get("wa_accounts"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func startChannels(s *Store, hub *Hub) {
	for _, a := range waAccounts(s) {
		if err := hub.Add(newWacli(s, a)); err != nil {
			log.Printf("whatsapp %s: %v", a, err)
		}
	}
	startTelegram(s, hub)
	if err := hub.Add(&WebChat{S: s}); err != nil {
		log.Printf("web chat: %v", err)
	}
}

func startTelegram(s *Store, hub *Hub) {
	for _, ch := range hub.List() {
		if ch.Name() == "telegram" {
			hub.Remove("telegram", ch.Account())
		}
	}
	tok := s.Get("telegram_token")
	if tok == "" {
		return
	}
	t := NewTelegram(tok)
	if base := s.Get("telegram_api"); base != "" {
		t.Base = base
	}
	t.Voice = func() bool { return voiceOn(s) }
	if err := hub.Add(t); err != nil {
		log.Printf("telegram: %v", err)
	}
}

// termChannel is the terminal in `ownshop chat`: it prints what the customer would receive.
type termChannel struct{ mu sync.Mutex }

func (t *termChannel) Name() string                               { return "terminal" }
func (t *termChannel) Account() string                            { return "chat" }
func (t *termChannel) Start(context.Context, func(Inbound)) error { return nil }
func (t *termChannel) Status() string                             { return "terminal" }
func (t *termChannel) SendImage(to, path, caption string) error {
	t.Send(to, caption)
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Println("shop> [image] " + filepath.ToSlash(path))
	return nil
}
func (t *termChannel) Send(to, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, line := range strings.Split(text, "\n") {
		fmt.Println("shop> " + line)
	}
	return nil
}

func chat(dbPath, httpAddr, phone, name string, quiet bool) {
	s := mustStore(dbPath)
	m, stopModel := startModel(s)
	defer stopModel()
	onSignal(stopModel)
	ts := &termChannel{}
	hub := NewHub(nil)
	hub.Add(ts)
	bot := &Bot{S: s, M: m, Send: hub}
	if !quiet {
		bot.Debug = func(m string) {
			ts.mu.Lock()
			fmt.Println("      [" + m + "]")
			ts.mu.Unlock()
		}
	}
	if httpAddr != "" {
		dir := filepath.Dir(dbPath)
		sf := NewShopFile(s, filepath.Join(dir, "products.xlsx"))
		sf.Load()
		s.AfterOrder = func() { sf.WriteStock(); WriteOrdersCSV(s, filepath.Join(dir, "orders.csv")) }
		app := &App{S: s, Bot: bot, Hub: hub, File: sf, Dir: dir}
		serveHTTP(httpAddr, app.Routes())
		fmt.Printf("ownshop chat as %s (%s). Owner page: http://localhost%s  Type a message, Ctrl-D to quit.\n", name, phone, httpAddr)
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		text := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if text == "" {
			continue
		}
		if f, ok := strings.CutPrefix(text, "voice:"); ok {
			// a voice note from a file: same path as a WhatsApp voice note after download
			heard, err := Transcribe(s, strings.TrimSpace(f))
			ts.mu.Lock()
			fmt.Println("\ncustomer> [voice note " + filepath.Base(strings.TrimSpace(f)) + "]")
			if err != nil {
				fmt.Println("      [voice: " + err.Error() + "]")
				ts.mu.Unlock()
				continue
			}
			fmt.Println("      [heard: " + heard + "]")
			ts.mu.Unlock()
			bot.Handle(context.Background(), Inbound{Channel: "terminal", Account: "chat", SenderID: phone, Phone: phone, Name: name, Text: heard})
			continue
		}
		if strings.HasPrefix(text, "#") {
			fmt.Println(text)
			continue
		}
		ts.mu.Lock()
		fmt.Println("\ncustomer> " + text)
		ts.mu.Unlock()
		t0 := time.Now()
		bot.Handle(context.Background(), Inbound{Channel: "terminal", Account: "chat", SenderID: phone, Phone: phone, Name: name, Text: text})
		fmt.Printf("      (%.2fs)\n", time.Since(t0).Seconds())
	}
}

func runEval(dbPath string) {
	s := mustStore(dbPath)
	soul := s.Get("soul")
	if soul == "" {
		if b, err := os.ReadFile("sample/soul.txt"); err == nil {
			soul = string(b)
		}
	}
	path := s.Get("model_path")
	if path == "" {
		path = DefaultModelPath
	}
	l, err := StartLlama(helperOverride(s, "llama_server"), findFile(path))
	if err != nil {
		log.Fatal(err)
	}
	defer l.Stop()
	out := io.MultiWriter(os.Stdout)
	a, n := RunEval(NewBrain(l.URL, ""), "fixed prompt only (default)", out)
	b, _ := RunEval(NewBrain(l.URL, CompactSoul(soul)), "fixed prompt + shop soul", out)
	fmt.Printf("SUMMARY without soul %d/%d, with soul %d/%d\n", a, n, b, n)
}
