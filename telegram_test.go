package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeToken = "123456:FAKE-test-token"

// fakeTelegram is a tiny Bot API: getMe, getUpdates (one backlog update, then new ones),
// sendMessage, sendPhoto, sendChatAction, getFile and file download.
type fakeTelegram struct {
	mu    sync.Mutex
	calls []string
	sent  []map[string]any
	photo string
	polls int
}

func (f *fakeTelegram) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/file/bot"+fakeToken+"/") {
			w.Write([]byte("OggS-fake-audio"))
			return
		}
		method, ok := strings.CutPrefix(r.URL.Path, "/bot"+fakeToken+"/")
		if !ok {
			w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, method)
		reply := func(v string) { w.Write([]byte(`{"ok":true,"result":` + v + `}`)) }
		var args map[string]any
		if r.Header.Get("Content-Type") == "application/json" {
			json.NewDecoder(r.Body).Decode(&args)
		}
		switch method {
		case "getMe":
			reply(`{"id":123456,"is_bot":true,"username":"greenleaf_bot"}`)
		case "getUpdates":
			if args["offset"] == float64(-1) {
				reply(`[{"update_id":10,"message":{"date":1,"text":"old backlog","chat":{"id":5,"type":"private"},"from":{"first_name":"Old"}}}]`)
				return
			}
			f.polls++
			if f.polls == 1 && args["offset"] == float64(11) {
				reply(`[
				 {"update_id":11,"message":{"date":2,"text":"do you have honey?","chat":{"id":77,"type":"private"},"from":{"first_name":"Emma","last_name":"Novak"}}},
				 {"update_id":12,"message":{"date":2,"text":"group chat","chat":{"id":-9,"type":"group"},"from":{"first_name":"G"}}},
				 {"update_id":13,"message":{"date":2,"voice":{"file_id":"v1"},"chat":{"id":78,"type":"private"},"from":{"first_name":"Bo"}}}]`)
				return
			}
			time.Sleep(50 * time.Millisecond)
			reply(`[]`)
		case "getFile":
			reply(`{"file_id":"v1","file_path":"voice/file_1.oga"}`)
		case "sendMessage", "sendChatAction":
			f.sent = append(f.sent, args)
			reply(`{}`)
		case "sendPhoto":
			r.ParseMultipartForm(1 << 20)
			fh, _, _ := r.FormFile("photo")
			b, _ := io.ReadAll(fh)
			f.photo = r.FormValue("chat_id") + "|" + r.FormValue("caption") + "|" + string(b)
			reply(`{}`)
		default:
			w.Write([]byte(`{"ok":false,"description":"unknown method"}`))
		}
	})
}

func TestTelegramPollSendPhotoVoice(t *testing.T) {
	f := &fakeTelegram{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	tg := NewTelegram(fakeToken)
	tg.Base, tg.Poll = srv.URL, time.Second
	tg.Voice = func() bool { return true }
	var mu sync.Mutex
	var got []Inbound
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tg.Start(ctx, func(in Inbound) { mu.Lock(); got = append(got, in); mu.Unlock() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	if len(got) != 2 {
		t.Fatalf("want 2 private messages (backlog and group skipped), got %+v", got)
	}
	in, voice := got[0], got[1]
	mu.Unlock()
	if in.Channel != "telegram" || in.Account != "123456" || in.SenderID != "77" || in.Phone != "tg:77" ||
		in.Name != "Emma Novak" || in.Text != "do you have honey?" {
		t.Fatalf("inbound: %+v", in)
	}
	if !voice.Voice || voice.AudioPath == "" {
		t.Fatalf("voice: %+v", voice)
	}
	if b, _ := os.ReadFile(voice.AudioPath); string(b) != "OggS-fake-audio" {
		t.Fatalf("voice download: %q", b)
	}
	os.Remove(voice.AudioPath)
	if st := tg.Status(); st != "connected as @greenleaf_bot" {
		t.Fatalf("status %q", st)
	}

	if err := tg.Send("77", "Here is what we have"); err != nil {
		t.Fatal(err)
	}
	tg.Typing("77", true)
	img := filepath.Join(t.TempDir(), "qr.png")
	os.WriteFile(img, []byte("PNGDATA"), 0o644)
	if err := tg.SendImage("77", img, "please pay"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) != 2 || f.sent[0]["chat_id"] != "77" || f.sent[0]["text"] != "Here is what we have" || f.sent[1]["action"] != "typing" {
		t.Fatalf("sent: %+v", f.sent)
	}
	if f.photo != "77|please pay|PNGDATA" {
		t.Fatalf("photo: %q", f.photo)
	}
}

func TestTelegramTokenNeverInErrors(t *testing.T) {
	tg := NewTelegram(fakeToken)
	tg.Base = "http://127.0.0.1:1" // nothing listens: the transport error carries the URL
	err := tg.Send("1", "x")
	if err == nil || strings.Contains(err.Error(), "FAKE-test-token") {
		t.Fatalf("token leaked or no error: %v", err)
	}
	if MaskToken(fakeToken) != "123456:****" {
		t.Fatalf("mask: %q", MaskToken(fakeToken))
	}
}
