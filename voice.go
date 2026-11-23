package main

// Voice notes (optional, Settings > Voice notes). A note (WhatsApp and Telegram send OGG/Opus) is decoded
// and transcribed inside ownshop: libopus decodes it straight to 16 kHz mono and whisper.cpp turns it into
// text, both linked statically into the program (build tag "voice", see voice_whisper.go). The text then
// goes through the same bot as a typed message. Builds without the tag answer voice notes with a hand-off.

const DefaultWhisperModel = "models/ggml-base.bin"

func voiceOn(s *Store) bool { return s.Get("voice") == "on" }

func whisperModel(s *Store) string {
	if m := s.Get("whisper_model"); m != "" {
		return findFile(m)
	}
	return findFile(DefaultWhisperModel)
}
