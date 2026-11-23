//go:build voice

package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Needs models/ggml-base.bin (not in git); skipped without it. Extra files: VOICE_FILES=a.ogg,b.ogg
func TestTranscribeVoiceNote(t *testing.T) {
	if _, err := os.Stat(DefaultWhisperModel); err != nil {
		t.Skip("no whisper model at " + DefaultWhisperModel)
	}
	s, err := OpenStore(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	files := []string{"testdata/voice-note.ogg"}
	if v := os.Getenv("VOICE_FILES"); v != "" {
		files = append(files, strings.Split(v, ",")...)
	}
	for _, f := range files {
		start := time.Now()
		text, err := Transcribe(s, f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		t.Logf("%s (%v): %s", f, time.Since(start).Round(time.Millisecond), text)
		low := strings.ToLower(text)
		if !strings.Contains(low, "honey") || !strings.Contains(low, "jar") {
			t.Fatalf("%s: unexpected transcript %q", f, text)
		}
	}
}
