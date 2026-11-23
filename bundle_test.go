package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractWritesOnceAndReplacesChangedCopy(t *testing.T) {
	dir := t.TempDir()
	p, err := extract(dir, "tool", []byte("v1"))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode()&0o100 == 0 && os.PathSeparator == '/' {
		t.Fatalf("not executable: %v", st.Mode())
	}
	before := st.ModTime()
	if _, err := extract(dir, "tool", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(p); !st2.ModTime().Equal(before) {
		t.Fatal("same bytes were written again")
	}
	if _, err := extract(dir, "tool", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "v2" {
		t.Fatalf("got %q, want the new version", b)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.new-*")); len(m) != 0 {
		t.Fatalf("temp files left: %v", m)
	}
}

func TestHelperOverrideIgnoresOldDefaults(t *testing.T) {
	s := mustStore(filepath.Join(t.TempDir(), "x.db"))
	s.Set("llama_server", "/opt/homebrew/bin/llama-server")
	if got := helperOverride(s, "llama_server"); got != "" {
		t.Fatalf("old default kept: %q", got)
	}
	s.Set("llama_server", "/my/llama")
	if got := helperOverride(s, "llama_server"); got != "/my/llama" {
		t.Fatalf("override lost: %q", got)
	}
}

func TestWacliStoreIsolation(t *testing.T) {
	w := NewWacli("wacli", "shop1")
	w.Store = "/data/wacli/shop1"
	c := w.cmd(t.Context(), "doctor")
	if c.Args[1] != "--store" || c.Args[2] != "/data/wacli/shop1" {
		t.Fatalf("args %v: want --store, never --account (that edits ~/.wacli/config.yaml)", c.Args)
	}
}
