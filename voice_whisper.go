//go:build voice

package main

/*
#cgo CFLAGS: -I${SRCDIR}/third_party/whisper.cpp/include -I${SRCDIR}/third_party/whisper.cpp/ggml/include
#cgo darwin CFLAGS: -I/opt/homebrew/opt/opus/include/opus
#cgo darwin LDFLAGS: ${SRCDIR}/third_party/whisper.cpp/build/src/libwhisper.a
#cgo darwin LDFLAGS: ${SRCDIR}/third_party/whisper.cpp/build/ggml/src/libggml.a
#cgo darwin LDFLAGS: ${SRCDIR}/third_party/whisper.cpp/build/ggml/src/libggml-cpu.a
#cgo darwin LDFLAGS: ${SRCDIR}/third_party/whisper.cpp/build/ggml/src/ggml-metal/libggml-metal.a
#cgo darwin LDFLAGS: ${SRCDIR}/third_party/whisper.cpp/build/ggml/src/ggml-blas/libggml-blas.a
#cgo darwin LDFLAGS: ${SRCDIR}/third_party/whisper.cpp/build/ggml/src/libggml-base.a
#cgo darwin LDFLAGS: /opt/homebrew/opt/opus/lib/libopus.a
#cgo darwin LDFLAGS: -lc++ -framework Accelerate -framework Metal -framework MetalKit -framework Foundation
#cgo windows CFLAGS: -I${SRCDIR}/third_party/opus/include
#cgo windows,amd64 LDFLAGS: ${SRCDIR}/third_party/win/whisper/src/libwhisper.a
#cgo windows,arm64 LDFLAGS: ${SRCDIR}/third_party/winarm/whisper/src/libwhisper.a
#cgo windows,amd64 LDFLAGS: ${SRCDIR}/third_party/win/whisper/ggml/src/ggml.a
#cgo windows,arm64 LDFLAGS: ${SRCDIR}/third_party/winarm/whisper/ggml/src/ggml.a
#cgo windows,amd64 LDFLAGS: ${SRCDIR}/third_party/win/whisper/ggml/src/ggml-cpu.a
#cgo windows,arm64 LDFLAGS: ${SRCDIR}/third_party/winarm/whisper/ggml/src/ggml-cpu.a
#cgo windows,amd64 LDFLAGS: ${SRCDIR}/third_party/win/whisper/ggml/src/ggml-base.a
#cgo windows,arm64 LDFLAGS: ${SRCDIR}/third_party/winarm/whisper/ggml/src/ggml-base.a
#cgo windows,amd64 LDFLAGS: ${SRCDIR}/third_party/win/opus/libopus.a
#cgo windows,arm64 LDFLAGS: ${SRCDIR}/third_party/winarm/opus/libopus.a
#cgo windows LDFLAGS: -lc++ -lws2_32
#cgo linux,arm64 CFLAGS: -I${SRCDIR}/third_party/opus/include
#cgo linux,arm64 LDFLAGS: ${SRCDIR}/third_party/linuxarm/whisper-pie/src/libwhisper.a
#cgo linux,arm64 LDFLAGS: ${SRCDIR}/third_party/linuxarm/whisper-pie/ggml/src/libggml.a
#cgo linux,arm64 LDFLAGS: ${SRCDIR}/third_party/linuxarm/whisper-pie/ggml/src/libggml-cpu.a
#cgo linux,arm64 LDFLAGS: ${SRCDIR}/third_party/linuxarm/whisper-pie/ggml/src/libggml-base.a
#cgo linux,arm64 LDFLAGS: ${SRCDIR}/third_party/linuxarm/opus-pie/libopus.a
#cgo linux,arm64 LDFLAGS: -lc++ -lm
#include <stdlib.h>
#include <opus.h>
#include <whisper.h>

static void quiet(enum ggml_log_level level, const char * text, void * user) {}
static void ownshop_quiet(void) { whisper_log_set(quiet, NULL); }

static struct whisper_context * ownshop_load(const char * path) {
	struct whisper_context_params p = whisper_context_default_params();
	return whisper_init_from_file_with_params(path, p);
}

static int ownshop_run(struct whisper_context * ctx, const float * pcm, int n, int threads) {
	struct whisper_full_params p = whisper_full_default_params(WHISPER_SAMPLING_BEAM_SEARCH);
	p.beam_search.beam_size = 5; // same decoding as whisper-cli
	p.language = "auto";
	p.n_threads = threads;
	p.print_progress = false;
	p.print_realtime = false;
	p.print_timestamps = false;
	p.print_special = false;
	p.no_timestamps = true;
	return whisper_full(ctx, p, pcm, n);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const voiceRate = 16000 // whisper wants 16 kHz mono float32; libopus resamples to it while decoding

// The whisper model is loaded on the first voice note and kept while notes keep coming, then freed after
// whisperIdle without one: ggml-base holds about 355 MB resident, and loading it again takes about 0.1 s.
// A changed model setting reloads it.
const whisperIdle = 2 * time.Minute

var whisper struct {
	sync.Mutex
	ctx   *C.struct_whisper_context
	path  string
	timer *time.Timer
}

func freeWhisper() {
	whisper.Lock()
	defer whisper.Unlock()
	if whisper.ctx != nil {
		C.whisper_free(whisper.ctx)
		whisper.ctx, whisper.path = nil, ""
	}
}

func init() { C.ownshop_quiet() }

// Transcribe turns an audio file (WhatsApp and Telegram voice notes are OGG/Opus) into text.
func Transcribe(s *Store, audio string) (string, error) {
	data, err := os.ReadFile(audio)
	if err != nil {
		return "", err
	}
	pcm, err := decodeOpus(data)
	if err != nil {
		return "", err
	}
	if len(pcm) < voiceRate/10 {
		return "", errors.New("voice note too short")
	}
	whisper.Lock()
	defer whisper.Unlock()
	if err := loadWhisper(whisperModel(s)); err != nil {
		return "", err
	}
	if whisper.timer == nil {
		whisper.timer = time.AfterFunc(whisperIdle, freeWhisper)
	} else {
		whisper.timer.Reset(whisperIdle)
	}
	threads := min(runtime.NumCPU(), 8)
	if C.ownshop_run(whisper.ctx, (*C.float)(unsafe.Pointer(&pcm[0])), C.int(len(pcm)), C.int(threads)) != 0 {
		return "", errors.New("whisper: transcription failed")
	}
	var b strings.Builder
	for i := range int(C.whisper_full_n_segments(whisper.ctx)) {
		b.WriteString(C.GoString(C.whisper_full_get_segment_text(whisper.ctx, C.int(i))))
		b.WriteByte(' ')
	}
	text := strings.Join(strings.Fields(b.String()), " ")
	if text == "" {
		return "", errors.New("whisper: no speech found")
	}
	return text, nil
}

func loadWhisper(path string) error {
	if whisper.ctx != nil && whisper.path == path {
		return nil
	}
	if whisper.ctx != nil {
		C.whisper_free(whisper.ctx)
		whisper.ctx = nil
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("whisper model: %v", err)
	}
	cp := C.CString(path)
	defer C.free(unsafe.Pointer(cp))
	if whisper.ctx = C.ownshop_load(cp); whisper.ctx == nil {
		return fmt.Errorf("whisper: cannot load model %s", path)
	}
	whisper.path = path
	return nil
}

// decodeOpus decodes an Ogg Opus file to 16 kHz mono float32 with libopus (SILK, CELT and hybrid frames).
func decodeOpus(data []byte) ([]float32, error) {
	packets, preSkip, err := oggOpus(data)
	if err != nil {
		return nil, err
	}
	var e C.int
	dec := C.opus_decoder_create(voiceRate, 1, &e)
	if e != C.OPUS_OK {
		return nil, fmt.Errorf("opus: %s", C.GoString(C.opus_strerror(e)))
	}
	defer C.opus_decoder_destroy(dec)
	frame := make([]float32, voiceRate*120/1000) // longest Opus packet: 120 ms
	var pcm []float32
	for _, p := range packets {
		if len(p) == 0 {
			continue
		}
		n := C.opus_decode_float(dec, (*C.uchar)(unsafe.Pointer(&p[0])), C.opus_int32(len(p)),
			(*C.float)(unsafe.Pointer(&frame[0])), C.int(len(frame)), 0)
		if n < 0 {
			return nil, fmt.Errorf("opus: %s", C.GoString(C.opus_strerror(n)))
		}
		pcm = append(pcm, frame[:n]...)
	}
	skip := preSkip * voiceRate / 48000
	if skip > len(pcm) {
		skip = len(pcm)
	}
	return pcm[skip:], nil
}
