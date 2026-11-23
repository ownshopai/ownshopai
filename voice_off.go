//go:build !voice

package main

import "errors"

// Transcribe in a build without the "voice" tag (for example the Windows cross-build): the shop owner gets
// the note as a hand-off instead.
func Transcribe(s *Store, audio string) (string, error) {
	return "", errors.New("this OwnShop AI build has no voice-note support (built without the voice tag)")
}
