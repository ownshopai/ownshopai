package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// oggOpus splits an Ogg Opus file into its Opus packets (header packets removed) and returns the
// pre-skip from the OpusHead header, in 48 kHz samples. RFC 7845 / RFC 3533; single logical stream.
func oggOpus(data []byte) (packets [][]byte, preSkip int, err error) {
	var all [][]byte
	var cur []byte
	r := bytes.NewReader(data)
	hdr := make([]byte, 27)
	for {
		if _, err := io.ReadFull(r, hdr); err == io.EOF {
			break
		} else if err != nil {
			return nil, 0, fmt.Errorf("ogg: truncated page")
		}
		if string(hdr[:4]) != "OggS" {
			return nil, 0, errors.New("ogg: not an Ogg file")
		}
		lacing := make([]byte, hdr[26])
		if _, err := io.ReadFull(r, lacing); err != nil {
			return nil, 0, fmt.Errorf("ogg: truncated page")
		}
		if hdr[5]&1 == 0 && len(cur) > 0 { // page does not continue the previous packet
			cur = nil
		}
		for _, n := range lacing {
			seg := make([]byte, n)
			if _, err := io.ReadFull(r, seg); err != nil {
				return nil, 0, fmt.Errorf("ogg: truncated page")
			}
			cur = append(cur, seg...)
			if n < 255 {
				all = append(all, cur)
				cur = nil
			}
		}
	}
	if len(all) < 2 || len(all[0]) < 19 || string(all[0][:8]) != "OpusHead" {
		return nil, 0, errors.New("ogg: not an Opus stream")
	}
	if ch := all[0][9]; ch > 2 {
		return nil, 0, fmt.Errorf("ogg: %d-channel Opus not supported", ch)
	}
	return all[2:], int(binary.LittleEndian.Uint16(all[0][10:12])), nil
}
