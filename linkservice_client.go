package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

// The free fixed link: <name>.ownshopai.store, handed out by the OwnShop AI link service
// (contract: linkservice/API.md). The owner signs in with Google on the service's page (OAuth
// loopback + PKCE S256); the app gets an app key and claims a name; the shop is then reached through
// the link service's relay (weblink.go, package relay), which needs the same app key. The app key is
// a secret: kept in the settings table, never shown, never logged; it only ever goes in the
// Authorization header. (Older versions saved a tunnel token in web_link_token; it is ignored now.)

const (
	linkUIBase  = "https://links.ownshopai.store"
	linkAPIBase = "https://linksapi.ownshopai.store"
	linkDomain  = "ownshopai.store"

	setLinkName     = "web_link_name"
	setLinkHost     = "web_link_host"
	setLinkStatus   = "web_link_status" // "", "disabled:<reason>"
	setLinkBeatDate = "web_link_heartbeat"
	setLinkAppKey   = "web_link_app_key"  // secret: the signed-in session with the link service
	setLinkState    = "web_link_state"    // the state of the sign-in in progress
	setLinkVerifier = "web_link_verifier" // secret: the PKCE verifier of the sign-in in progress
)

type LinkClaim struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	URL      string `json:"url"`
}

type LinkInfo struct {
	Name           string `json:"name"`
	Hostname       string `json:"hostname"`
	Status         string `json:"status"`
	DisabledReason string `json:"disabled_reason"`
}

// LinkError is the service's error body: {"error": {"code", "message", "request_id"}}.
type LinkError struct {
	Status    int
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func (e *LinkError) Error() string {
	m := e.Message
	if m == "" {
		m = e.Code
	}
	if m == "" {
		m = fmt.Sprintf("HTTP %d", e.Status)
	}
	return "link service: " + m
}

type LinkService interface {
	Up(ctx context.Context) bool
	LoginURL(state, challenge, returnURL string) string
	Exchange(ctx context.Context, code, verifier string) (string, error)
	Revoke(ctx context.Context, appKey string) error
	Available(ctx context.Context, name string) (bool, string, error)
	Claim(ctx context.Context, appKey, name string) (LinkClaim, error)
	Heartbeat(ctx context.Context, appKey, name string) (LinkInfo, error)
	Release(ctx context.Context, appKey, name string) error
}

type httpLinkService struct {
	UI, API string
	HC      *http.Client
}

func newLinkService() LinkService {
	return &httpLinkService{UI: linkUIBase, API: linkAPIBase, HC: &http.Client{Timeout: 20 * time.Second}}
}

func (h *httpLinkService) call(ctx context.Context, method, path, key string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.API+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := h.HC.Do(req)
	if err != nil {
		return errors.New("the link service cannot be reached; check the internet connection")
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode/100 != 2 {
		var e struct {
			Error LinkError `json:"error"`
		}
		json.Unmarshal(raw, &e)
		e.Error.Status = res.StatusCode
		return &e.Error
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (h *httpLinkService) Up(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", h.API+"/up", nil)
	if err != nil {
		return false
	}
	res, err := h.HC.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

func (h *httpLinkService) LoginURL(state, challenge, ret string) string {
	return h.UI + "/login?state=" + url.QueryEscape(state) + "&challenge=" + url.QueryEscape(challenge) + "&return=" + url.QueryEscape(ret)
}

func (h *httpLinkService) Exchange(ctx context.Context, code, verifier string) (string, error) {
	var r struct {
		Key string `json:"app_key"`
	}
	client := "OwnShop AI " + runtime.GOOS + "/" + runtime.GOARCH
	err := h.call(ctx, "POST", "/v1/session/exchange", "", map[string]string{"code": code, "code_verifier": verifier, "client": client}, &r)
	return r.Key, err
}

func (h *httpLinkService) Revoke(ctx context.Context, key string) error {
	return h.call(ctx, "POST", "/v1/session/revoke", key, struct{}{}, nil)
}

func (h *httpLinkService) Available(ctx context.Context, name string) (bool, string, error) {
	var r struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	}
	err := h.call(ctx, "GET", "/v1/available?name="+url.QueryEscape(name), "", nil, &r)
	return r.Available, r.Reason, err
}

func (h *httpLinkService) Claim(ctx context.Context, key, name string) (LinkClaim, error) {
	var r LinkClaim
	err := h.call(ctx, "POST", "/v1/claim", key, map[string]string{"name": name}, &r)
	return r, err
}

func (h *httpLinkService) Heartbeat(ctx context.Context, key, name string) (LinkInfo, error) {
	var r LinkInfo
	err := h.call(ctx, "POST", "/v1/heartbeat", key, map[string]string{"name": name}, &r)
	return r, err
}

func (h *httpLinkService) Release(ctx context.Context, key, name string) error {
	return h.call(ctx, "POST", "/v1/release", key, map[string]string{"name": name}, nil)
}

// validLinkName: 3 to 30 of a-z 0-9 -, no leading, trailing or double hyphen.
func validLinkName(n string) bool {
	if len(n) < 3 || len(n) > 30 || n[0] == '-' || n[len(n)-1] == '-' || strings.Contains(n, "--") {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// friendly turns the service's error codes into words for the owner.
func friendly(err error) error {
	var le *LinkError
	if !errors.As(err, &le) {
		return err
	}
	msg := map[string]string{
		"invalid_code":          "the sign-in expired or was already used; press Sign in with Google again",
		"account_disabled":      "this account is disabled; write to io@deemwar.com",
		"banned":                "this account is blocked; write to io@deemwar.com",
		"registration_required": "finish the registration on the sign-in page first",
		"invalid_name":          "the name can have 3 to 30 small letters, digits and single dashes",
		"reserved_name":         "that name is reserved; pick another",
		"name_taken":            "that name is taken; pick another",
		"link_limit":            "you already have a free link; release it first to take another name",
		"rate_limited":          "too many tries; wait a while and try again",
		"link_disabled":         "this link is turned off by the link service; write to io@deemwar.com",
		"link_not_found":        "the link service does not know this link any more",
		"unauthorized":          "the sign-in is no longer valid; sign in with Google again",
	}[le.Code]
	if msg == "" {
		return err
	}
	return errors.New(msg)
}

// pkce makes a verifier (43 chars) and its S256 challenge.
func pkce() (verifier, challenge string) {
	b := make([]byte, 32)
	rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// StartLinkLogin makes a fresh state and PKCE verifier and returns the page to open in the browser.
func StartLinkLogin(s *Store, ls LinkService, returnURL string) string {
	b := make([]byte, 16)
	rand.Read(b)
	st := hex.EncodeToString(b)
	ver, ch := pkce()
	s.Set(setLinkState, st)
	s.Set(setLinkVerifier, ver)
	return ls.LoginURL(st, ch, returnURL)
}

// FinishLinkLogin checks the state the browser brought back and trades the code for the app key.
func FinishLinkLogin(ctx context.Context, s *Store, ls LinkService, code, state string) error {
	want, ver := s.Get(setLinkState), s.Get(setLinkVerifier)
	if want == "" || state == "" || subtle.ConstantTimeCompare([]byte(want), []byte(state)) != 1 {
		return errors.New("this sign-in did not start here; press Sign in with Google again")
	}
	s.Set(setLinkState, "") // one use
	s.Set(setLinkVerifier, "")
	if code == "" || len(code) > 512 {
		return errors.New("the sign-in came back without a code; try again")
	}
	key, err := ls.Exchange(ctx, code, ver)
	if err != nil {
		return friendly(err)
	}
	if key == "" {
		return errors.New("the link service gave an answer OwnShop AI does not understand")
	}
	s.Set(setLinkAppKey, key)
	return nil
}

// SignOutLink revokes the app key and forgets it. The name stays claimed, but the relay needs the app
// key, so the link is offline until the owner signs in again.
func SignOutLink(ctx context.Context, s *Store, ls LinkService) error {
	key := s.Get(setLinkAppKey)
	s.Set(setLinkAppKey, "")
	if key == "" {
		return nil
	}
	return friendly(ls.Revoke(ctx, key))
}

// ClaimLink reserves name.ownshopai.store and keeps the name in the settings.
func ClaimLink(ctx context.Context, s *Store, ls LinkService, name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !validLinkName(name) {
		return "", errors.New("the name can have 3 to 30 small letters, digits and single dashes")
	}
	key := s.Get(setLinkAppKey)
	if key == "" {
		return "", errors.New("sign in with Google first")
	}
	c, err := ls.Claim(ctx, key, name)
	if err != nil {
		return "", friendly(err)
	}
	if c.Hostname == "" || !strings.HasSuffix(c.Hostname, "."+linkDomain) {
		return "", errors.New("the link service gave an answer OwnShop AI does not understand")
	}
	if c.Name == "" {
		c.Name = name
	}
	s.Set(setLinkName, c.Name)
	s.Set(setLinkHost, c.Hostname)
	s.Set(setLinkStatus, "")
	return c.Hostname, nil
}

// ReleaseLink gives the name back and forgets the link (also when the service cannot be told, so
// the owner is never stuck; the service disables unused links on its own).
func ReleaseLink(ctx context.Context, s *Store, ls LinkService) error {
	name, key := s.Get(setLinkName), s.Get(setLinkAppKey)
	var err error
	switch {
	case name == "":
	case key == "":
		err = errors.New("sign in with Google to release the name on the link service")
	default:
		err = friendly(ls.Release(ctx, key, name))
	}
	for _, k := range []string{setLinkName, setLinkHost, setLinkStatus, setLinkBeatDate} {
		s.Set(k, "")
	}
	return err
}

// beatOnce sends the daily heartbeat and records whether the service turned the link off.
func beatOnce(ctx context.Context, s *Store, ls LinkService) {
	name, key := s.Get(setLinkName), s.Get(setLinkAppKey)
	today := time.Now().Format("2006-01-02")
	if name == "" || key == "" || s.Get(setLinkBeatDate) == today {
		return
	}
	info, err := ls.Heartbeat(ctx, key, name)
	if err != nil {
		log.Printf("website: link heartbeat: %v", friendly(err))
		return
	}
	s.Set(setLinkBeatDate, today)
	if info.Status == "disabled" {
		s.Set(setLinkStatus, "disabled:"+info.DisabledReason)
	} else {
		s.Set(setLinkStatus, "")
	}
}

// linkHeartbeat runs beatOnce every hour (it only calls the service once a day).
func linkHeartbeat(ctx context.Context, s *Store, ls LinkService) {
	for {
		beatOnce(ctx, s, ls)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}
