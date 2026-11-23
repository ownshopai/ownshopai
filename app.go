package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Double-click behaviour. The owner never types a command:
//   - first double-click: start the shop, make it start at login, put an "OwnShop AI" shortcut on
//     the desktop that opens the shop page, and open the browser;
//   - any later double-click while it runs: just open the browser.

const (
	appID       = "ownshop"
	defaultPort = 2007
	launchID    = "com.deemwar.ownshop"
	displayName = "OwnShop AI" // what the owner sees: shortcuts, the Mac app, messages
)

// The shop page address. It starts at port 2007; if another program holds that port, ownshop moves to the
// next free one (2008, 2009, ...) and remembers it in its SQLite settings ("port"), so shortcuts stay valid.
var (
	appAddr = addrFor(defaultPort)
	appURL  = urlFor(defaultPort)
)

func addrFor(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }
func urlFor(port int) string  { return fmt.Sprintf("http://localhost:%d", port) }

// pickPort returns the port to serve on, starting from the saved one. found=true means a ownshop is
// already answering there (open the browser instead of starting a second copy).
func pickPort(saved int, free func(int) bool, isOwnshop func(int) bool) (port int, found bool, ok bool) {
	if saved <= 0 || saved > 65535 {
		saved = defaultPort
	}
	for p := saved; p < saved+100 && p <= 65535; p++ {
		if p == pubDefaultPort { // the public web chat listener's port: the fixed links point there
			continue
		}
		if isOwnshop(p) {
			return p, true, true
		}
		if free(p) {
			return p, false, true
		}
	}
	return 0, false, false
}

func portFree(p int) bool {
	ln, err := net.Listen("tcp", addrFor(p))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// DataDir is where the owner's files live: products.xlsx, orders.csv, receipts, the database.
func DataDir() (string, error) {
	base, err := os.UserConfigDir() // macOS: ~/Library/Application Support, Windows: %AppData%
	if err != nil {
		return "", err
	}
	if moved, err := migrateLegacy(base); err != nil {
		// Do not start with an empty shop: the owner's shop is still in the old folder, and the next
		// start tries again.
		return "", fmt.Errorf("could not copy your shop from the old washop folder (it is unchanged): %w", err)
	} else if moved {
		fmt.Println("copied your shop from the old washop folder; the old folder is kept as a backup")
	}
	d := filepath.Join(base, appID)
	return d, os.MkdirAll(d, 0o755)
}

// running reports whether a ownshop is already answering at url.
func running(url string) bool {
	c := http.Client{Timeout: time.Second}
	res, err := c.Get(url + "/health")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64))
	return string(b) == appID
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default: // Termux on Android has termux-open-url; a desktop Linux has xdg-open; else the URL is in the log
		for _, c := range []string{"termux-open-url", "xdg-open"} {
			if p, err := exec.LookPath(c); err == nil {
				return exec.Command(p, url).Start()
			}
		}
		fmt.Println("OwnShop AI is running: open " + url)
		return nil
	}
}

// inTermux reports whether ownshop runs inside Termux on Android (its own Linux userland, no desktop).
func inTermux() bool {
	return os.Getenv("TERMUX_VERSION") != "" || strings.Contains(os.Getenv("PREFIX"), "com.termux")
}

// launchAgent is the macOS login item: start ownshop in the background at login.
func launchAgent(exe string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>` + launchID + `</string>
<key>ProgramArguments</key><array><string>` + xmlEscape(exe) + `</string><string>--background</string></array>
<key>RunAtLoad</key><true/>
</dict></plist>
`
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// Install makes ownshop start at login and puts a shortcut to the shop page on the desktop.
// It only writes files in the user's own folders and is safe to run on every start.
func Install(home, exe, goos string) (written []string, err error) {
	write := func(p, body string) error {
		if b, err := os.ReadFile(p); err == nil && string(b) == body {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		written = append(written, p)
		return os.WriteFile(p, []byte(body), 0o644)
	}
	desk := filepath.Join(home, "Desktop")
	switch goos {
	case "darwin":
		if err := write(filepath.Join(home, "Library", "LaunchAgents", launchID+".plist"), launchAgent(exe)); err != nil {
			return written, err
		}
		err = write(filepath.Join(desk, displayName+".webloc"), `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>URL</key><string>`+appURL+`</string></dict></plist>
`)
	case "windows":
		startup := filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
		if err := write(filepath.Join(startup, "ownshop.cmd"), "@echo off\r\nstart \"\" \""+exe+"\" --background\r\n"); err != nil {
			return written, err
		}
		err = write(filepath.Join(desk, displayName+".url"), "[InternetShortcut]\r\nURL="+appURL+"\r\n")
	default:
		if inTermux() { // no desktop; start at boot is the optional Termux:Boot script from run-termux.sh
			return nil, nil
		}
		err = write(filepath.Join(home, ".config", "autostart", "ownshop.desktop"),
			"[Desktop Entry]\nType=Application\nName="+displayName+"\nExec=\""+exe+"\" --background\n")
	}
	return written, err
}

// runApp is what a double-click does.
func runApp(background bool) {
	if relocate() {
		return
	}
	stopLegacy()
	dir, err := DataDir()
	if err != nil {
		fatalBox("OwnShop AI cannot create its folder: " + err.Error())
	}
	dbPath := filepath.Join(dir, "ownshop.db")
	saved := 0
	if st, err := OpenStore(dbPath); err == nil {
		fmt.Sscan(st.Get("port"), &saved)
		st.DB.Close()
	}
	port, found, ok := pickPort(saved, portFree, func(p int) bool { return running(urlFor(p)) })
	if !ok {
		fatalBox(fmt.Sprintf("OwnShop AI found no free port from %d on. Close some programs and open OwnShop AI again.", saved))
	}
	appAddr, appURL = addrFor(port), urlFor(port)
	if found {
		if !background {
			openBrowser(appURL)
		}
		return
	}
	if st, err := OpenStore(dbPath); err == nil {
		st.Set("port", fmt.Sprint(port))
		st.DB.Close()
	}
	logf, _ := os.OpenFile(filepath.Join(dir, "ownshop.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if logf != nil {
		setLogOutput(logf)
	}
	if exe, err := os.Executable(); err == nil {
		if home, err := os.UserHomeDir(); err == nil {
			if m := retireLegacyStartup(home, runtime.GOOS, dir); len(m) > 0 {
				logf.WriteString(fmt.Sprintf("moved old washop shortcuts: %s\n", strings.Join(m, ", ")))
			}
			if w, err := Install(home, exe, runtime.GOOS); err != nil {
				logf.WriteString(fmt.Sprintf("install: %v\n", err))
			} else if len(w) > 0 {
				logf.WriteString(fmt.Sprintf("installed: %s\n", strings.Join(w, ", ")))
			}
		}
	}
	startShop(dir, dbPath, appAddr, func() {
		if !background {
			openBrowser(appURL)
		}
	})
}

// relocate copies ownshop.app out of the opened disk image to ~/Applications and starts that copy,
// so the login item and the shop keep working after the image is ejected. It reports whether it did.
func relocate() bool {
	exe, err := os.Executable()
	if err != nil || runtime.GOOS != "darwin" || !strings.HasPrefix(exe, "/Volumes/") {
		return false
	}
	i := strings.Index(exe, ".app/Contents/MacOS/")
	home, herr := os.UserHomeDir()
	if i < 0 || herr != nil {
		return false
	}
	dst := filepath.Join(home, "Applications", displayName+".app")
	os.MkdirAll(filepath.Dir(dst), 0o755)
	os.RemoveAll(dst)
	if err := exec.Command("/usr/bin/ditto", exe[:i+4], dst).Run(); err != nil {
		return false
	}
	return exec.Command("/usr/bin/open", dst).Run() == nil
}

func fatalBox(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

// stopLegacy ends a running old washop (and the helpers it unpacked) before its folder is copied, so the
// copy is consistent and the old program does not keep the WhatsApp number busy. On Windows the setup
// program does this.
func stopLegacy() {
	if runtime.GOOS == "windows" || inTermux() {
		return
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return
	}
	if _, err := os.Stat(filepath.Join(base, legacyAppID)); err != nil {
		return
	}
	if _, err := os.Stat(filepath.Join(base, appID)); err == nil {
		return // already moved over
	}
	exec.Command("pkill", "-x", legacyAppID).Run()
	exec.Command("pkill", "-f", filepath.Join(base, legacyAppID, "bin")+string(filepath.Separator)).Run()
	time.Sleep(time.Second)
}
