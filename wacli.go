package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// WhatsApp goes through wacli (an unofficial WhatsApp Web client, run as a separate program).
// ownshop never speaks the WhatsApp protocol and stores no WhatsApp session: every number is a
// named wacli account (`wacli --account <name>`), paired with `wacli --account <name> auth`.
//
// Inbound: one follower per account,
//
//	wacli --account <name> sync --follow --webhook http://127.0.0.1:<port>/wacli/<name> \
//	      --webhook-allow-private --webhook-secret <random> --webhook-events message --events
//
// posts every live message as JSON to a listener ownshop keeps on 127.0.0.1 only, signed with
// X-Wacli-Signature (HMAC-SHA256). The follower owns the account's store lock; while it runs,
// `wacli send ...` and `wacli presence ...` are handed to it by wacli itself.

var wacliAccountRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)

func validWacliAccount(name string) bool { return wacliAccountRe.MatchString(name) }

// wacliHook is the local webhook listener shared by every wacli account in this process.
type wacliHook struct {
	once   sync.Once
	err    error
	base   string
	secret string
	mu     sync.Mutex
	accts  map[string]*Wacli
}

var hook = &wacliHook{accts: map[string]*Wacli{}}

func (h *wacliHook) start() error {
	h.once.Do(func() {
		b := make([]byte, 24)
		rand.Read(b)
		h.secret = hex.EncodeToString(b)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			h.err = err
			return
		}
		h.base = "http://" + ln.Addr().String()
		go http.Serve(ln, http.HandlerFunc(h.serve))
	})
	return h.err
}

func (h *wacliHook) url(account string) string { return h.base + "/wacli/" + account }

func (h *wacliHook) serve(w http.ResponseWriter, r *http.Request) {
	acct, ok := strings.CutPrefix(r.URL.Path, "/wacli/")
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if r.Method != http.MethodPost || !ok || !hmac.Equal([]byte(r.Header.Get("X-Wacli-Signature")), []byte(want)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	h.mu.Lock()
	wa := h.accts[acct]
	h.mu.Unlock()
	// wacli waits at most 5 seconds and does not retry: answer now, handle the message after.
	w.WriteHeader(http.StatusOK)
	if wa != nil {
		go wa.deliver(body)
	}
}

// wacliMessage is the part of wacli's webhook message payload ownshop reads.
type wacliMessage struct {
	EventType string
	Chat      string
	ID        string
	Timestamp time.Time
	FromMe    bool
	Text      string
	PushName  string
	ChatName  string
	Media     *struct{ Type, Caption string }
	Location  *struct {
		Latitude, Longitude float64
		Name, Address       string
	}
}

// Wacli is one WhatsApp number, linked as wacli account Acct.
type Wacli struct {
	Bin   string // wacli binary (the one bundled inside ownshop, see bundle.go)
	Acct  string
	Store string // this number's own wacli store dir; "" uses wacli's named account (tests, old setups)

	mu      sync.Mutex
	status  string
	qr      string
	on      func(Inbound)
	ctx     context.Context
	follow  context.CancelFunc
	pairing bool
	seen    map[string]bool
	start   time.Time
	store   string // wacli store dir for this account (from doctor); voice notes land in <store>/media
}

func NewWacli(bin, account string) *Wacli {
	return &Wacli{Bin: bin, Acct: account, status: "not linked", seen: map[string]bool{}}
}

func (w *Wacli) Name() string    { return "whatsapp" }
func (w *Wacli) Account() string { return w.Acct }

func (w *Wacli) set(qr, status string) {
	w.mu.Lock()
	w.qr, w.status = qr, status
	w.mu.Unlock()
}

func (w *Wacli) Status() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

// QR is the current pairing code while linking, else "".
func (w *Wacli) QR() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.qr
}

func (w *Wacli) cmd(ctx context.Context, args ...string) *exec.Cmd {
	sel := []string{"--account", w.Acct}
	if w.Store != "" {
		// ownshop's own store under its data dir: it never reads or edits ~/.wacli/config.yaml,
		// which other wacli users on this machine own.
		sel = []string{"--store", w.Store}
	}
	return exec.CommandContext(ctx, w.Bin, append(sel, args...)...)
}

func (w *Wacli) Start(ctx context.Context, on func(Inbound)) error {
	if !validWacliAccount(w.Acct) {
		return fmt.Errorf("account name %q: use letters, digits, . _ - and start with a letter or digit", w.Acct)
	}
	if w.Bin == "" {
		return fmt.Errorf("wacli is not available in this OwnShop AI build")
	}
	if err := hook.start(); err != nil {
		return fmt.Errorf("local webhook: %w", err)
	}
	hook.mu.Lock()
	hook.accts[w.Acct] = w
	hook.mu.Unlock()
	w.mu.Lock()
	w.ctx, w.on, w.start = ctx, on, time.Now()
	w.mu.Unlock()
	go func() {
		<-ctx.Done()
		hook.mu.Lock()
		if hook.accts[w.Acct] == w {
			delete(hook.accts, w.Acct)
		}
		hook.mu.Unlock()
	}()
	if w.linked(ctx) {
		w.startFollower()
	} else {
		w.set("", "not linked")
	}
	return nil
}

func (w *Wacli) linked(ctx context.Context) bool {
	out, err := w.cmd(ctx, "--json", "auth", "status").Output()
	if err != nil {
		return false
	}
	var st struct {
		Data struct{ Authenticated bool } `json:"data"`
	}
	return json.Unmarshal(out, &st) == nil && st.Data.Authenticated
}

func (w *Wacli) lockHeld(ctx context.Context) bool {
	out, err := w.cmd(ctx, "--json", "doctor").Output()
	if err != nil {
		return false // fail open: the follower itself reports a held lock
	}
	var d struct {
		Data struct {
			LockHeld bool   `json:"lock_held"`
			StoreDir string `json:"store_dir"`
		} `json:"data"`
	}
	if json.Unmarshal(out, &d) != nil {
		return false
	}
	if d.Data.StoreDir != "" {
		w.mu.Lock()
		w.store = d.Data.StoreDir
		w.mu.Unlock()
	}
	return d.Data.LockHeld
}

// startFollower runs `wacli sync --follow` for this account and restarts it if it exits.
func (w *Wacli) startFollower() {
	w.mu.Lock()
	if w.follow != nil || w.ctx == nil {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(w.ctx)
	w.follow = cancel
	w.mu.Unlock()
	go func() {
		for ctx.Err() == nil {
			if w.lockHeld(ctx) {
				// Another program (often another wacli sync) owns this number. Never kill it.
				w.set("", "busy: another wacli program is using this number; OwnShop AI retries every 30 seconds")
				wait(ctx, 30*time.Second)
				continue
			}
			w.set("", "connected")
			c := w.cmd(ctx, "sync", "--follow", "--webhook", hook.url(w.Acct), "--webhook-allow-private",
				"--webhook-secret", hook.secret, "--webhook-events", "message", "--download-media", "--events")
			stderr, _ := c.StderrPipe()
			if err := c.Start(); err != nil {
				w.set("", "could not start wacli: "+err.Error())
				wait(ctx, 30*time.Second)
				continue
			}
			go drainEvents(stderr, func(ev string, data map[string]any) {
				if ev == "error" {
					w.set("", fmt.Sprintf("wacli: %v", data["message"]))
				}
			})
			err := c.Wait()
			if ctx.Err() != nil {
				return
			}
			w.set("", fmt.Sprintf("disconnected (%v); reconnecting in 10 seconds", err))
			wait(ctx, 10*time.Second)
		}
	}()
}

func (w *Wacli) stopFollower() {
	w.mu.Lock()
	stop := w.follow
	w.follow = nil
	w.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func wait(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// drainEvents reads wacli's NDJSON lifecycle events ({"event":..,"data":{..}}) from r.
func drainEvents(r io.Reader, f func(event string, data map[string]any)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Event != "" {
			f(e.Event, e.Data)
		}
	}
}

// Link pairs this account: `wacli --account <name> auth --qr-format text --events`.
// The QR code comes back as a qr_code event; the web page draws it. When pairing succeeds
// the follower starts.
func (w *Wacli) Link() error {
	w.mu.Lock()
	ctx, busy := w.ctx, w.pairing
	w.pairing = true
	w.mu.Unlock()
	if ctx == nil {
		return fmt.Errorf("not started")
	}
	if busy {
		return nil
	}
	w.stopFollower()
	// A new number needs its own isolated wacli store first. "already exists" is fine.
	if w.Store != "" {
		os.MkdirAll(w.Store, 0o700)
	} else if out, err := exec.CommandContext(ctx, w.Bin, "accounts", "add", w.Acct, "--no-auth").CombinedOutput(); err != nil &&
		!strings.Contains(string(out), "already exists") {
		w.mu.Lock()
		w.pairing = false
		w.mu.Unlock()
		return fmt.Errorf("wacli accounts add: %s", strings.TrimSpace(string(out)))
	}
	// A shop needs no chat history, so the phone's history bootstrap is kept tiny: linking finishes sooner.
	c := w.cmd(ctx, "auth", "--qr-format", "text", "--events", "--idle-exit", "10s", "--history-days", "1", "--history-max-per-chat", "1")
	stderr, _ := c.StderrPipe()
	c.Stdout = io.Discard
	if err := c.Start(); err != nil {
		w.mu.Lock()
		w.pairing = false
		w.mu.Unlock()
		return err
	}
	w.set("", "starting the link")
	// WhatsApp sends a new code about every 20 s until the phone scans one. When the codes stop
	// and auth is still running, the phone has scanned it and the link is finishing.
	var quiet *time.Timer
	go func() {
		drainEvents(stderr, func(ev string, data map[string]any) {
			switch ev {
			case "qr_code":
				if code, _ := data["code"].(string); code != "" {
					w.set(code, "scan the code with the phone")
					if quiet != nil {
						quiet.Stop()
					}
					quiet = time.AfterFunc(qrQuiet, func() {
						w.mu.Lock()
						still := w.pairing
						w.mu.Unlock()
						if still {
							w.set("", "Linked. Finishing setup, this can take a minute...")
						}
					})
				}
			case "error":
				w.set("", fmt.Sprintf("link failed: %v", data["message"]))
			}
		})
		err := c.Wait()
		if quiet != nil {
			quiet.Stop()
		}
		w.mu.Lock()
		w.pairing = false
		w.mu.Unlock()
		if err != nil {
			w.set("", "link did not finish; try again")
			return
		}
		w.set("", "linked")
		w.startFollower()
	}()
	return nil
}

// qrQuiet is how long without a new code means the phone scanned the last one.
var qrQuiet = 30 * time.Second

// deliver turns one webhook payload into an Inbound. Only direct chats from other people,
// only new messages, each message once.
func (w *Wacli) deliver(body []byte) {
	var m wacliMessage
	if json.Unmarshal(body, &m) != nil || (m.EventType != "" && m.EventType != "message") {
		return
	}
	if m.FromMe || m.ID == "" || !(strings.HasSuffix(m.Chat, "@s.whatsapp.net") || strings.HasSuffix(m.Chat, "@lid")) {
		return // groups, broadcasts, channels and our own messages are never answered
	}
	w.mu.Lock()
	on, start, dup := w.on, w.start, w.seen[m.ID]
	w.seen[m.ID] = true
	if len(w.seen) > 5000 {
		w.seen = map[string]bool{m.ID: true}
	}
	w.mu.Unlock()
	// Old messages replayed at connect are not answered: replying to a backlog is a burst of sends.
	if dup || on == nil || (!m.Timestamp.IsZero() && m.Timestamp.Before(start.Add(-10*time.Minute))) {
		return
	}
	in := Inbound{Channel: "whatsapp", Account: w.Acct, SenderID: m.Chat, Name: m.PushName, Text: strings.TrimSpace(m.Text), Time: m.Timestamp}
	if in.Name == "" {
		in.Name = m.ChatName
	}
	if user, ok := strings.CutSuffix(m.Chat, "@s.whatsapp.net"); ok {
		in.Phone = "+" + user
	}
	if l := m.Location; l != nil && (l.Latitude != 0 || l.Longitude != 0) {
		// A shared location: its text/caption is only the place name, never a typed message.
		in.Lat, in.Lng, in.Text = l.Latitude, l.Longitude, ""
		in.LocLabel = strings.TrimSpace(strings.Join(strings.Fields(l.Name+" "+l.Address), " "))
		on(in)
		return
	}
	if m.Media != nil && m.Media.Type == "audio" {
		// The follower runs with --download-media, so it saves the note itself (it holds the store
		// lock, so ownshop can't call `wacli media download`). Wait for the file, then hand it on.
		in.Voice = true
		w.mu.Lock()
		store := w.store
		w.mu.Unlock()
		go func() {
			in.AudioPath = waitVoice(store, m.Chat, m.ID, 30*time.Second)
			on(in)
		}()
		return
	} else if in.Text == "" && m.Media != nil {
		in.Text = strings.TrimSpace(m.Media.Caption)
	}
	if in.Text == "" && !in.Voice {
		return
	}
	on(in)
}

func (w *Wacli) run(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := w.cmd(ctx, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("wacli %s: %v %s", args[0], err, msg)
	}
	return nil
}

func (w *Wacli) Send(to, text string) error {
	if to == "" {
		return fmt.Errorf("no WhatsApp address")
	}
	return w.run("send", "text", "--to", to, "--message", text, "--no-preview")
}

func (w *Wacli) SendImage(to, path, caption string) error {
	if to == "" {
		return fmt.Errorf("no WhatsApp address")
	}
	return w.run("send", "file", "--to", to, "--file", path, "--caption", caption, "--as", "image")
}

// Typing is best effort: a failed presence update never blocks a reply.
func (w *Wacli) Typing(to string, on bool) {
	verb := "paused"
	if on {
		verb = "typing"
	}
	if err := w.run("presence", verb, "--to", to); err != nil {
		log.Printf("whatsapp %s: typing indicator: %v", w.Acct, err)
	}
}

// waitVoice waits for wacli's background download of a voice note
// (<store>/media/<chat>/<msg id>/<file>) and copies it to a temp file the caller may delete.
// It returns "" if the file does not appear in time.
func waitVoice(store, chat, id string, timeout time.Duration) string {
	if store == "" || id == "" {
		return ""
	}
	dir := filepath.Join(store, "media", wacliSegment(chat), wacliSegment(id))
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if f.IsDir() || strings.HasSuffix(f.Name(), ".tmp") || strings.HasPrefix(f.Name(), ".") {
				continue
			}
			src := filepath.Join(dir, f.Name())
			if info, err := f.Info(); err != nil || info.Size() == 0 || time.Since(info.ModTime()) < 300*time.Millisecond {
				continue // still being written
			}
			data, err := os.ReadFile(src)
			if err != nil {
				continue
			}
			tmp, err := os.CreateTemp("", "ownshop-voice-*"+filepath.Ext(src))
			if err != nil {
				return ""
			}
			tmp.Write(data)
			tmp.Close()
			return tmp.Name()
		}
	}
	return ""
}

// wacliSegment mirrors wacli's pathutil.SanitizeSegment for chat JIDs and message IDs.
func wacliSegment(seg string) string {
	seg = strings.Map(func(r rune) rune {
		if r == 0 || unicode.IsControl(r) {
			return -1
		}
		return r
	}, seg)
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return "unknown"
	}
	seg = strings.NewReplacer("/", "_", "\\", "_", ":", "_", "@", "_", "?", "_", "#", "_", "*", "_", "<", "_", ">", "_", "|", "_").Replace(seg)
	seg = strings.ReplaceAll(seg, "..", "_")
	seg = strings.ReplaceAll(seg, string(filepath.Separator), "_")
	if seg == "." {
		return "unknown"
	}
	return seg
}
