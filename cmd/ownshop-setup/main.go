// ownshop-setup is the OwnShop AI setup, the one file a Windows shop owner downloads. It carries ownshop.exe (with wacli and
// llama-server inside it) and the model files as a zip appended to this program, installs them to
// %LocalAppData%\ownshop, adds a Start menu shortcut, and starts ownshop, which then puts the shop link
// on the desktop, makes itself start at login and opens the browser.
//
//	ownshop-setup.exe              install and open the shop in the browser
//	ownshop-setup.exe /S           silent: install and start ownshop in the background, no browser
//	ownshop-setup.exe /uninstall   remove the program, models and shortcuts (keeps the shop's data)
//
// Built by scripts/windows-setup.sh: the setup program, then a zip appended to it.
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	silent, uninstall := false, false
	for _, a := range os.Args[1:] {
		switch strings.ToLower(strings.TrimLeft(a, "/-")) {
		case "s", "silent", "quiet":
			silent = true
		case "uninstall":
			uninstall = true
		}
	}
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "ownshop")
	if os.Getenv("LOCALAPPDATA") == "" {
		fail(silent, "LOCALAPPDATA is not set")
	}
	stop()
	if uninstall {
		os.RemoveAll(dir)
		os.Remove(startMenuLink())
		if s := os.Getenv("APPDATA"); s != "" {
			os.Remove(filepath.Join(s, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "ownshop.cmd"))
		}
		if h, err := os.UserHomeDir(); err == nil {
			os.Remove(filepath.Join(h, "Desktop", "OwnShop AI.url"))
		}
		say(silent, "OwnShop AI removed. Your shop's data is still in %AppData%\\ownshop.")
		return
	}
	n, err := install(dir)
	if err != nil {
		fail(silent, "OwnShop AI could not be installed: "+err.Error())
	}
	exe := filepath.Join(dir, "ownshop.exe")
	shortcut(startMenuLink(), exe)
	retireOld(dir)
	args := []string{}
	if silent {
		args = append(args, "--background")
	}
	c := exec.Command(exe, args...)
	c.Dir = dir
	if err := c.Start(); err != nil {
		fail(silent, "OwnShop AI was installed but did not start: "+err.Error())
	}
	if silent {
		fmt.Printf("installed %d files to %s and started OwnShop AI\n", n, dir)
	}
}

// install unpacks the appended zip into dir. A file already there with the same size is kept, so a
// re-install does not rewrite 700 MB of models.
func install(dir string) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, err
	}
	f, err := os.Open(self)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	z, err := zip.NewReader(f, st.Size())
	if err != nil {
		return 0, fmt.Errorf("this setup file is incomplete (download it again): %w", err)
	}
	n := 0
	for _, e := range z.File {
		if e.FileInfo().IsDir() {
			continue
		}
		name := filepath.FromSlash(e.Name)
		if !filepath.IsLocal(name) {
			return n, fmt.Errorf("bad file name %q in setup", e.Name)
		}
		p := filepath.Join(dir, name)
		if have, err := os.Stat(p); err == nil && have.Size() == int64(e.UncompressedSize64) && !strings.HasSuffix(p, ".exe") {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return n, err
		}
		if err := unpack(e, p); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func unpack(e *zip.File, p string) error {
	r, err := e.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	tmp := p + ".new"
	w, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		os.Remove(p)
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// stop ends a running ownshop (and its helpers) so its files can be replaced.
func stop() {
	// washop.exe is the same program under its old name: stop it too, so the new one can copy its data
	// folder and take over the WhatsApp number.
	for _, im := range []string{"ownshop.exe", "washop.exe", "llama-server.exe", "wacli.exe"} {
		exec.Command("taskkill", "/F", "/T", "/IM", im).Run()
	}
	time.Sleep(500 * time.Millisecond)
}

func startMenuLink() string {
	return filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "OwnShop AI.lnk")
}

// shortcut makes a .lnk with the shell's own COM object (PowerShell ships with Windows).
func shortcut(lnk, target string) {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	ps := "$s=(New-Object -ComObject WScript.Shell).CreateShortcut(" + q(lnk) + ");$s.TargetPath=" + q(target) +
		";$s.WorkingDirectory=" + q(filepath.Dir(target)) + ";$s.Save()"
	exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Run()
}

func say(silent bool, msg string) {
	if silent {
		fmt.Println(msg)
		return
	}
	box(msg)
}

func fail(silent bool, msg string) {
	say(silent, msg)
	os.Exit(1)
}

// box shows a message in a window, for an owner who double-clicked the setup.
func box(msg string) {
	exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Add-Type -AssemblyName PresentationFramework;[System.Windows.MessageBox]::Show('"+strings.ReplaceAll(msg, "'", "''")+"','OwnShop AI')").Run()
}

// retireOld moves the old washop Start menu entry into dir\retired-shortcuts (nothing is deleted). The old program folder %LocalAppData%\washop and the old data folder
// %AppData%\washop are left as they are: the new program copies the data on its first start.
func retireOld(dir string) {
	ad := os.Getenv("APPDATA")
	if ad == "" {
		return
	}
	old := filepath.Join(ad, "Microsoft", "Windows", "Start Menu", "Programs", "washop.lnk")
	if _, err := os.Stat(old); err != nil {
		return
	}
	dst := filepath.Join(dir, "retired-shortcuts")
	if os.MkdirAll(dst, 0o755) == nil {
		os.Rename(old, filepath.Join(dst, "washop.lnk"))
	}
}
