package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestEnvFlags(t *testing.T) {
	t.Setenv("OWNSHOP_DATA_DIR", "")
	t.Setenv("OWNSHOP_PUBLIC_PORT", "")
	got := envFlags([]string{"ownshop", "serve", "-data", "/x/shop", "--public-port=3008", "--db", "a.db"})
	if !slices.Equal(got, []string{"ownshop", "serve", "--db", "a.db"}) {
		t.Fatalf("args %v", got)
	}
	if os.Getenv("OWNSHOP_DATA_DIR") != "/x/shop" || os.Getenv("OWNSHOP_PUBLIC_PORT") != "3008" {
		t.Fatal("flags not moved to the environment")
	}
	old := pubDefaultPort
	defer func() { pubDefaultPort = old }()
	applyEnv()
	if pubDefaultPort != 3008 {
		t.Fatalf("public port %d", pubDefaultPort)
	}
}

func TestDataDirOverrideNeedsNoHome(t *testing.T) {
	d := filepath.Join(t.TempDir(), "shop")
	t.Setenv("OWNSHOP_DATA_DIR", d)
	t.Setenv("OWNSHOP_BIN_DIR", "")
	t.Setenv("HOME", "")            // no home folder at all
	t.Setenv("XDG_CONFIG_HOME", "") // UserConfigDir would fail on linux
	got, err := DataDir()
	if err != nil || got != d {
		t.Fatalf("data dir %q %v", got, err)
	}
	if st, err := os.Stat(d); err != nil || !st.IsDir() {
		t.Fatal("data dir not made")
	}
	if b, err := helperDir(); err != nil || b != filepath.Join(d, "bin") {
		t.Fatalf("helper dir %q %v", b, err)
	}
	os.MkdirAll(filepath.Join(d, "models"), 0o755)
	os.WriteFile(filepath.Join(d, "models", "m.gguf"), []byte("x"), 0o644)
	if p := findFile("/nowhere/m.gguf"); p != filepath.Join(d, "models", "m.gguf") {
		t.Fatalf("model %q", p)
	}
}

func TestHelperDirRunsProgramsInPlace(t *testing.T) {
	d := t.TempDir()
	t.Setenv("OWNSHOP_HELPER_DIR", d)
	if _, err := helper("wacli", ""); err == nil {
		t.Fatal("missing helper not reported")
	}
	p := filepath.Join(d, exeName("wacli"))
	os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755)
	got, err := helper("wacli", "")
	if err != nil || got != p {
		t.Fatalf("helper %q %v", got, err)
	}
	if got, _ := helper("wacli", "/dev/override"); got != "/dev/override" {
		t.Fatal("the setting must still win")
	}
}
