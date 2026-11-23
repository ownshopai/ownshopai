package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Telegram is a bot on the official Telegram Bot API (token from @BotFather), read with
// long polling (getUpdates). The token is part of every API URL, so no error that could
// contain a URL is ever shown or logged as is.
type Telegram struct {
	Token string
	Base  string // API base, https://api.telegram.org unless a test sets it
	Voice func() bool
	Poll  time.Duration // long-poll timeout

	mu     sync.Mutex
	status string
	user   string
	hc     *http.Client
}

func NewTelegram(token string) *Telegram {
	return &Telegram{Token: token, Base: "https://api.telegram.org", Poll: 25 * time.Second,
		status: "starting", hc: &http.Client{Timeout: 60 * time.Second}}
}

// MaskToken shows only the bot id part: "123456:****".
func MaskToken(t string) string {
	if t == "" {
		return ""
	}
	if id, _, ok := strings.Cut(t, ":"); ok {
		return id + ":****"
	}
	return "****"
}

func (t *Telegram) Name() string { return "telegram" }

// Account is the bot username once known; the bot id before that.
func (t *Telegram) Account() string {
	id, _, _ := strings.Cut(t.Token, ":")
	return id
}

func (t *Telegram) Status() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.user != "" && t.status == "connected" {
		return "connected as @" + t.user
	}
	return t.status
}

func (t *Telegram) set(s string) { t.mu.Lock(); t.status = s; t.mu.Unlock() }

// scrub removes the token from an error (a *url.Error carries the request URL).
func (t *Telegram) scrub(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := err.Error()
	if t.Token != "" {
		msg = strings.ReplaceAll(msg, t.Token, MaskToken(t.Token))
	}
	return errors.New(msg)
}

type tgResp struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

func (t *Telegram) call(ctx context.Context, method string, body io.Reader, ctype string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Base+"/bot"+t.Token+"/"+method, body)
	if err != nil {
		return t.scrub(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := t.hc.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, t.scrub(err))
	}
	defer res.Body.Close()
	var r tgResp
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d", method, res.StatusCode)
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %s", method, t.scrub(errors.New(r.Description)))
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func (t *Telegram) callJSON(ctx context.Context, method string, args map[string]any, out any) error {
	b, _ := json.Marshal(args)
	return t.call(ctx, method, bytes.NewReader(b), "application/json", out)
}

type tgLocation struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Date int64  `json:"date"`
		Text string `json:"text"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		From struct {
			IsBot     bool   `json:"is_bot"`
			FirstName string `json:"first_name"`
			LastName  string `json:"last_name"`
			Username  string `json:"username"`
		} `json:"from"`
		Voice *struct {
			FileID string `json:"file_id"`
		} `json:"voice"`
		Location *tgLocation `json:"location"`
		Venue    *struct {
			Title   string `json:"title"`
			Address string `json:"address"`
		} `json:"venue"`
	} `json:"message"`
}

func (t *Telegram) Start(ctx context.Context, on func(Inbound)) error {
	if t.Token == "" {
		return fmt.Errorf("no bot token")
	}
	go t.loop(ctx, on)
	return nil
}

func (t *Telegram) loop(ctx context.Context, on func(Inbound)) {
	var me struct{ Username string }
	for ctx.Err() == nil {
		if err := t.callJSON(ctx, "getMe", nil, &me); err != nil {
			t.set("not connected: " + err.Error())
			wait(ctx, 30*time.Second)
			continue
		}
		break
	}
	t.mu.Lock()
	t.user = me.Username
	t.mu.Unlock()
	// Skip the backlog: messages sent while ownshop was off are not answered in a burst.
	var offset int64
	var old []tgUpdate
	if t.callJSON(ctx, "getUpdates", map[string]any{"timeout": 0, "offset": -1}, &old) == nil && len(old) > 0 {
		offset = old[len(old)-1].UpdateID + 1
	}
	t.set("connected")
	for ctx.Err() == nil {
		var ups []tgUpdate
		err := t.callJSON(ctx, "getUpdates", map[string]any{"timeout": int(t.Poll / time.Second), "offset": offset,
			"allowed_updates": []string{"message"}}, &ups)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			t.set("reconnecting: " + err.Error())
			wait(ctx, 5*time.Second)
			continue
		}
		t.set("connected")
		for _, u := range ups {
			offset = u.UpdateID + 1
			if in, ok := t.inbound(ctx, u); ok {
				on(in)
			}
		}
	}
}

func (t *Telegram) inbound(ctx context.Context, u tgUpdate) (Inbound, bool) {
	m := u.Message
	if m == nil || m.Chat.Type != "private" || m.From.IsBot {
		return Inbound{}, false // groups and channels are never answered
	}
	id := strconv.FormatInt(m.Chat.ID, 10)
	name := strings.TrimSpace(m.From.FirstName + " " + m.From.LastName)
	if name == "" {
		name = m.From.Username
	}
	in := Inbound{Channel: "telegram", Account: t.Account(), SenderID: id, Phone: "tg:" + id, Name: name,
		Text: strings.TrimSpace(m.Text), Time: time.Unix(m.Date, 0)}
	if m.Voice != nil {
		in.Voice = true
		if t.Voice != nil && t.Voice() {
			in.AudioPath, _ = t.download(ctx, m.Voice.FileID)
		}
	}
	if m.Location != nil {
		in.Lat, in.Lng = m.Location.Latitude, m.Location.Longitude
		if m.Venue != nil {
			in.LocLabel = strings.TrimSpace(m.Venue.Title + " " + m.Venue.Address)
		}
	}
	return in, in.Text != "" || in.Voice || in.HasLocation()
}

// download fetches a voice note with getFile into a temp file.
func (t *Telegram) download(ctx context.Context, fileID string) (string, error) {
	var f struct {
		FilePath string `json:"file_path"`
	}
	if err := t.callJSON(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, t.Base+"/file/bot"+t.Token+"/"+f.FilePath, nil)
	res, err := t.hc.Do(req)
	if err != nil {
		return "", t.scrub(err)
	}
	defer res.Body.Close()
	out, err := os.CreateTemp("", "ownshop-voice-*"+filepath.Ext(f.FilePath))
	if err != nil {
		return "", err
	}
	defer out.Close()
	_, err = io.Copy(out, io.LimitReader(res.Body, 20<<20))
	return out.Name(), err
}

func (t *Telegram) Send(to, text string) error {
	return t.callJSON(context.Background(), "sendMessage", map[string]any{"chat_id": to, "text": text}, nil)
}

func (t *Telegram) SendImage(to, path, caption string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	mw.WriteField("chat_id", to)
	mw.WriteField("caption", caption)
	fw, _ := mw.CreateFormFile("photo", filepath.Base(path))
	io.Copy(fw, f)
	mw.Close()
	return t.call(context.Background(), "sendPhoto", &b, mw.FormDataContentType(), nil)
}

// Typing shows "typing..." (Telegram clears it by itself when the message arrives).
func (t *Telegram) Typing(to string, on bool) {
	if on {
		t.callJSON(context.Background(), "sendChatAction", map[string]any{"chat_id": to, "action": "typing"}, nil)
	}
}
