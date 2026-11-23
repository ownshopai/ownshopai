package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWacliSegment(t *testing.T) {
	if got := wacliSegment("919876543210@s.whatsapp.net"); got != "919876543210_s.whatsapp.net" {
		t.Fatalf("got %q", got)
	}
	if got := wacliSegment(" "); got != "unknown" {
		t.Fatalf("got %q", got)
	}
}

func TestWaitVoiceFindsDownloadedNote(t *testing.T) {
	store := t.TempDir()
	dir := filepath.Join(store, "media", "91_s.whatsapp.net", "ABC123")
	os.MkdirAll(dir, 0o755)
	go func() {
		time.Sleep(700 * time.Millisecond)
		os.WriteFile(filepath.Join(dir, "note.ogg"), []byte("OggS-fake"), 0o644)
	}()
	got := waitVoice(store, "91@s.whatsapp.net", "ABC123", 5*time.Second)
	if got == "" {
		t.Fatal("voice note not found")
	}
	defer os.Remove(got)
	if b, _ := os.ReadFile(got); string(b) != "OggS-fake" {
		t.Fatalf("copy wrong: %q", b)
	}
}

func TestWaitVoiceTimesOut(t *testing.T) {
	if got := waitVoice(t.TempDir(), "x@s.whatsapp.net", "nope", time.Second); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}
