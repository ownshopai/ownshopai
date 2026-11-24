package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Website channel puts the customer web chat on a public https link through Cloudflare's free
// tunnel program, cloudflared (Apache-2.0). It is not inside OwnShop AI: the owner installs it once
// (winget / brew / Cloudflare's download page) and OwnShop AI finds it. The link is the free fixed
// name.ownshopai.store from the OwnShop AI link service (linkservice_client.go): `cloudflared tunnel
// run` with the tunnel token in TUNNEL_TOKEN (never on the command line, never logged). The tunnel
// points at the public listener on 127.0.0.1:2008 (website.go), never at the owner page.

const (
	setWebPublic     = "web_public" // "on" while the owner wants the link up (restarted on app start)
	cloudflaredDLURL = "https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/"
)

// cloudflaredCandidates are the usual install places, after PATH.
func cloudflaredCandidates() []string {
	if runtime.GOOS == "windows" {
		var out []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
			if d := os.Getenv(env); d != "" {
				out = append(out, filepath.Join(d, "cloudflared", "cloudflared.exe"))
			}
		}
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			out = append(out, filepath.Join(d, "Microsoft", "WinGet", "Links", "cloudflared.exe"))
		}
		return out
	}
	return []string{"/opt/homebrew/bin/cloudflared", "/usr/local/bin/cloudflared", "/usr/bin/cloudflared"}
}

// findCloudflared returns the program's path or "". lookPath is exec.LookPath outside tests.
func findCloudflared(lookPath func(string) (string, error), candidates []string) string {
	if p, err := lookPath("cloudflared"); err == nil && p != "" {
		return p
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

var versionRe = regexp.MustCompile(`\d{4}\.\d+\.\d+`)

func cloudflaredVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return versionRe.FindString(string(out))
}

// Tunnel runs at most one cloudflared.
type Tunnel struct {
	S    *Store
	Port int // the public listener's port

	Find func() string // locate cloudflared; nil means the real search

	mu      sync.Mutex
	path    string
	version string
	cmd     *exec.Cmd
	url     string // https://... once known
	state   string // "off", "starting", "on", "error"
	errMsg  string
	gen     int
}

// TunnelView is what the Website card shows. The token is never in it.
type TunnelView struct {
	Found             bool
	Path, Version     string
	State, URL, Err   string
	LinkHost          string
	DownloadURL, Port string
}

func (t *Tunnel) Detect() {
	find := t.Find
	if find == nil {
		find = func() string { return findCloudflared(exec.LookPath, cloudflaredCandidates()) }
	}
	p := find()
	v := ""
	if p != "" {
		v = cloudflaredVersion(p)
	}
	t.mu.Lock()
	t.path, t.version = p, v
	t.mu.Unlock()
}

// named is the free link's tunnel token and host; ok=false until a name is claimed.
func (t *Tunnel) named() (token, host string, ok bool) {
	token, host = t.S.Get(setLinkToken), t.S.Get(setLinkHost)
	return token, host, token != "" && host != ""
}

func (t *Tunnel) View() TunnelView {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := TunnelView{Found: t.path != "", Path: t.path, Version: t.version, State: t.state, URL: t.url, Err: t.errMsg,
		DownloadURL: cloudflaredDLURL}
	if v.State == "" {
		v.State = "off"
	}
	v.Port = strconv.Itoa(t.Port)
	v.LinkHost = t.S.Get(setLinkHost)
	return v
}

// PublicHost is the host name the public listener answers to, "" while the link is not up.
func (t *Tunnel) PublicHost() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != "on" {
		return ""
	}
	return strings.TrimPrefix(t.url, "https://")
}

// URL is the public link while it is up.
func (t *Tunnel) URL() string {
	if t.PublicHost() == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.url
}

// command builds the cloudflared command. The token goes in the environment.
func (t *Tunnel) command(path string) *exec.Cmd {
	token, _, _ := t.named()
	c := exec.Command(path, "tunnel", "--no-autoupdate", "run")
	c.Env = append(os.Environ(), "TUNNEL_TOKEN="+token)
	return c
}

// Start starts the tunnel and remembers that the owner wants it on.
func (t *Tunnel) Start() error {
	t.S.Set(setWebPublic, "on")
	t.mu.Lock()
	if t.cmd != nil {
		t.mu.Unlock()
		return nil
	}
	path := t.path
	t.mu.Unlock()
	if path == "" {
		t.Detect()
		t.mu.Lock()
		path = t.path
		t.mu.Unlock()
	}
	if path == "" {
		return errors.New("Cloudflare's tunnel program (cloudflared) is not installed")
	}
	if _, _, ok := t.named(); !ok {
		return errors.New("get your free link first")
	}
	// The link's tunnel always points at 127.0.0.1:2008. If another program holds 2008 the public
	// chat is not there, and whatever is there must never be put on the internet.
	if t.Port != pubDefaultPort {
		return fmt.Errorf("port %d is used by another program; close it and restart OwnShop AI to use your link", pubDefaultPort)
	}
	cmd := t.command(path)
	hideConsole(cmd)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		return err
	}
	_, host, _ := t.named()
	t.mu.Lock()
	t.gen++
	gen := t.gen
	t.cmd, t.state, t.errMsg, t.url = cmd, "starting", "", "https://"+host
	t.mu.Unlock()
	go t.read(pr, gen)
	go func() {
		err := cmd.Wait()
		pw.Close()
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.gen != gen {
			return
		}
		t.cmd = nil
		if t.state != "off" {
			t.state, t.url = "error", ""
			t.errMsg = "the tunnel program stopped"
			if err != nil {
				t.errMsg += ": " + err.Error()
			}
		}
	}()
	return nil
}

// read follows cloudflared's log until a connection is registered. Lines are never logged as they
// are: they could carry account details.
func (t *Tunnel) read(r io.Reader, gen int) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		t.mu.Lock()
		if t.gen == gen && t.state == "starting" && strings.Contains(line, "Registered tunnel connection") {
			t.state = "on"
			log.Printf("website: public chat link %s", t.url)
		}
		t.mu.Unlock()
	}
}

// Stop stops the tunnel and remembers that the owner wants it off.
func (t *Tunnel) Stop() {
	t.S.Set(setWebPublic, "")
	t.halt()
}

// halt stops the program without changing the owner's choice (app exit).
func (t *Tunnel) halt() {
	t.mu.Lock()
	cmd := t.cmd
	t.cmd, t.state, t.url, t.errMsg = nil, "off", "", ""
	t.gen++
	t.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
}

// Resume starts the tunnel on app start when it was on.
func (t *Tunnel) Resume() {
	t.Detect()
	if t.S.Get(setWebPublic) == "on" {
		if err := t.Start(); err != nil {
			log.Printf("website: %v", err)
		}
	}
}
