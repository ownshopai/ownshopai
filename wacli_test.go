package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeWacli is a stand-in for the wacli binary. It records every argv line to $WACLI_LOG and:
//   - auth status: linked; doctor: lock free
//   - auth: prints a qr_code event on stderr, then succeeds
//   - sync --follow: POSTs canned live messages to --webhook, signed with --webhook-secret
//     exactly as wacli does (X-Wacli-Signature: sha256=<hmac>), then waits
//   - send / presence / accounts: succeed
const fakeWacli = `#!/bin/sh
echo "$*" >> "$WACLI_LOG"
case "$*" in
  *"auth status"*) echo '{"success":true,"data":{"authenticated":true,"phone":"31600000000"}}'; exit 0;;
  *doctor*) echo '{"success":true,"data":{"lock_held":false}}'; exit 0;;
  *" auth "*) echo '{"event":"qr_code","data":{"code":"2@fake-qr-payload"},"ts":1}' >&2; sleep 1; exit 0;;
  *"sync --follow"*)
    url=""; secret=""; prev=""
    for a in "$@"; do
      [ "$prev" = "--webhook" ] && url="$a"
      [ "$prev" = "--webhook-secret" ] && secret="$a"
      prev="$a"
    done
    now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    post() {
      sig=$(printf '%s' "$1" | openssl dgst -sha256 -hmac "$secret" | sed 's/^.* //')
      curl -s -o /dev/null -X POST -H "X-Wacli-Signature: sha256=$sig" --data-binary "$1" "$url"
    }
    post '{"Chat":"31600000001@s.whatsapp.net","ID":"M1","SenderJID":"31600000001@s.whatsapp.net","Timestamp":"'$now'","FromMe":false,"Text":"do you have honey?","PushName":"Emma"}'
    post '{"Chat":"31600000001@s.whatsapp.net","ID":"M1","Timestamp":"'$now'","FromMe":false,"Text":"duplicate"}'
    post '{"Chat":"31600000001@s.whatsapp.net","ID":"M2","Timestamp":"'$now'","FromMe":true,"Text":"from the shop phone"}'
    post '{"Chat":"120363000000000000@g.us","ID":"M3","Timestamp":"'$now'","FromMe":false,"Text":"group"}'
    post '{"Chat":"31600000001@s.whatsapp.net","ID":"M4","Timestamp":"2020-01-01T00:00:00Z","FromMe":false,"Text":"backlog"}'
    post '{"EventType":"receipt","Chat":"31600000001@s.whatsapp.net","MessageIDs":["X"]}'
    post '{"Chat":"31600000002@s.whatsapp.net","ID":"M5","Timestamp":"'$now'","FromMe":false,"Media":{"Type":"audio"},"PushName":"Bo"}'
    curl -s -o /dev/null -X POST -H "X-Wacli-Signature: sha256=bad" --data-binary '{"Chat":"1@s.whatsapp.net","ID":"F","Text":"forged"}' "$url"
    sleep 30; exit 0;;
  *) exit 0;;
esac
`

func installFakeWacli(t *testing.T) (bin, logf string) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	dir := t.TempDir()
	bin, logf = filepath.Join(dir, "wacli"), filepath.Join(dir, "argv.log")
	if err := os.WriteFile(bin, []byte(fakeWacli), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WACLI_LOG", logf)
	return bin, logf
}

func readLog(t *testing.T, f string) string {
	b, _ := os.ReadFile(f)
	return string(b)
}

func TestWacliInboundFollowerAndSend(t *testing.T) {
	bin, logf := installFakeWacli(t)
	var mu sync.Mutex
	var got []Inbound
	w := NewWacli(bin, "shop1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx, func(in Inbound) { mu.Lock(); got = append(got, in); mu.Unlock() }); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // anything else that would wrongly arrive
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("want the text and the voice note only, got %d: %+v", len(got), got)
	}
	text, voice := got[0], got[1]
	if text.Text != "do you have honey?" {
		text, voice = voice, text
	}
	if text.Channel != "whatsapp" || text.Account != "shop1" || text.SenderID != "31600000001@s.whatsapp.net" ||
		text.Phone != "+31600000001" || text.Name != "Emma" {
		t.Fatalf("inbound: %+v", text)
	}
	if !voice.Voice || voice.Phone != "+31600000002" {
		t.Fatalf("voice note: %+v", voice)
	}
	if w.Status() != "connected" {
		t.Fatalf("status %q", w.Status())
	}
	if err := w.Send("31600000001@s.whatsapp.net", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := w.SendImage("31600000001@s.whatsapp.net", "/tmp/qr.png", "please pay"); err != nil {
		t.Fatal(err)
	}
	w.Typing("31600000001@s.whatsapp.net", true)
	log := readLog(t, logf)
	for _, want := range []string{
		"--account shop1 sync --follow --webhook http://127.0.0.1:",
		"--webhook-allow-private --webhook-secret ",
		"--account shop1 send text --to 31600000001@s.whatsapp.net --message hello --no-preview",
		"--account shop1 send file --to 31600000001@s.whatsapp.net --file /tmp/qr.png --caption please pay --as image",
		"--account shop1 presence typing --to 31600000001@s.whatsapp.net",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("argv log has no %q:\n%s", want, log)
		}
	}
}

func TestWacliLinkShowsQRThenFollows(t *testing.T) {
	bin, logf := installFakeWacli(t)
	w := NewWacli(bin, "shop2")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.ctx, w.on, w.start = ctx, func(Inbound) {}, time.Now() // started, not linked yet
	if err := w.Link(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for w.QR() == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if w.QR() != "2@fake-qr-payload" {
		t.Fatalf("QR not captured from wacli events: %q (%s)", w.QR(), w.Status())
	}
	for !strings.Contains(readLog(t, logf), "sync --follow") && time.Now().Before(deadline.Add(5*time.Second)) {
		time.Sleep(50 * time.Millisecond)
	}
	log := readLog(t, logf)
	for _, want := range []string{"accounts add shop2 --no-auth", "--account shop2 auth --qr-format text --events", "--account shop2 sync --follow"} {
		if !strings.Contains(log, want) {
			t.Errorf("argv log has no %q:\n%s", want, log)
		}
	}
}

func TestWacliRejectsBadAccountNames(t *testing.T) {
	for _, n := range []string{"", "-rf", "a b", "../x"} {
		if validWacliAccount(n) {
			t.Errorf("%q accepted", n)
		}
	}
	if !validWacliAccount("shop1") {
		t.Error("shop1 rejected")
	}
}
