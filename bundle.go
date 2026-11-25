package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// ownshop carries its two helper programs inside itself (build tag "bundle", files in bundled/,
// made by scripts/bundle-deps.sh): wacli for WhatsApp and llama-server for the model. On first use
// each one is written to <app data>/bin and run from there. A copy whose bytes differ from the one
// inside this ownshop (an older version, a broken download) is replaced, so the owner never installs
// or updates anything by hand. The settings wacli_bin and llama_server still point at another
// program when they are set, for developers.

// bundled maps a helper name ("wacli", "llama-server") to its bytes; set by bundle_<os>_<arch>.go.
var bundled map[string][]byte

var helperMu sync.Mutex

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// helperDir is <data dir>/bin with OWNSHOP_DATA_DIR, else <app data>/bin: ~/Library/Application Support/ownshop/bin, %AppData%\ownshop\bin.
func helperDir() (string, error) {
	if d := os.Getenv("OWNSHOP_BIN_DIR"); d != "" {
		return d, os.MkdirAll(d, 0o755)
	}
	if d := dataDirOverride(); d != "" {
		d = filepath.Join(d, "bin")
		return d, os.MkdirAll(d, 0o755)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(base, appID, "bin")
	return d, os.MkdirAll(d, 0o755)
}

// helper returns the program to run for name: override when it is set, else the copy in
// OWNSHOP_HELPER_DIR when that is set, else the bundled copy.
//
// OWNSHOP_HELPER_DIR is a read-only folder that already holds the programs (the Android app's native
// library folder: Android 10+ refuses to run files an app wrote itself). They are run from there as
// they are; nothing is unpacked.
func helper(name, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if d := os.Getenv("OWNSHOP_HELPER_DIR"); d != "" {
		p := filepath.Join(d, exeName(name))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			return "", fmt.Errorf("%s is not in OWNSHOP_HELPER_DIR (%s)", exeName(name), d)
		}
		return p, nil
	}
	b := bundled[name]
	if b == nil {
		return "", fmt.Errorf("%s is not inside this OwnShop AI build (build with -tags bundle, see task build)", name)
	}
	dir, err := helperDir()
	if err != nil {
		return "", err
	}
	return extract(dir, exeName(name), b)
}

// extract writes b to dir/name unless the file there already has exactly these bytes.
func extract(dir, name string, b []byte) (string, error) {
	helperMu.Lock()
	defer helperMu.Unlock()
	p := filepath.Join(dir, name)
	want := sha256.Sum256(b)
	if f, err := os.Open(p); err == nil {
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err == nil && bytes.Equal(h.Sum(nil), want[:]) {
			return p, nil
		}
	}
	tmp, err := os.CreateTemp(dir, name+".new-*")
	if err != nil {
		return "", err
	}
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err == nil {
		os.Remove(p) // Windows cannot rename over an existing file
		err = os.Rename(tmp.Name(), p)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("unpack %s: %w", name, err)
	}
	return p, nil
}

// oldHelperPaths are values older versions stored in the llama_server setting as a default; they
// now mean "use the bundled one".
var oldHelperPaths = map[string]bool{"/opt/homebrew/bin/llama-server": true, "/usr/local/bin/llama-server": true,
	"llama-server": true, "llama-server.exe": true, "wacli": true, "wacli.exe": true}

func helperOverride(s *Store, key string) string {
	v := s.Get(key)
	if oldHelperPaths[v] {
		return ""
	}
	return v
}

// findFile resolves a model file: an existing path as given, else the same file name in the models
// folders a packaged ownshop ships: next to the program, in the macOS app's Resources, in the app data dir.
func findFile(p string) string {
	if p == "" {
		return p
	}
	if _, err := os.Stat(p); err == nil {
		return p
	}
	base := filepath.Base(p)
	var dirs []string
	if d := os.Getenv("OWNSHOP_MODELS"); d != "" {
		dirs = append(dirs, d)
	}
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "models"), filepath.Join(filepath.Dir(exe), "..", "Resources", "models"))
	}
	if d := dataDirOverride(); d != "" {
		dirs = append(dirs, filepath.Join(d, "models"))
	} else if c, err := os.UserConfigDir(); err == nil {
		dirs = append(dirs, filepath.Join(c, appID, "models"))
	}
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, base)); err == nil {
			return filepath.Join(d, base)
		}
	}
	return p
}
