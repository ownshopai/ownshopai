#!/bin/sh
# Build ownshop for linux/arm64 (Termux on Android, Raspberry Pi, ARM servers) as fully static programs
# with zig (aarch64-linux-musl), from a Mac. Makes, cached:
#   third_party/linuxarm/{whisper,opus}     whisper.cpp + libopus static libraries (voice notes)
#   bundled/linux-arm64/{wacli,llama-server} the two helpers ownshop embeds (CPU only, no curl/openssl)
#   dist/ownshop-linux-arm64                 ownshop -tags "bundle voice"
#   dist/ownshop-linux-arm64.tar.gz          ownshop + models/ + run-termux.sh + README-termux.txt
# Needs go, zig, cmake, git, and models/ for the tarball. WACLI_SRC as in bundle-deps.sh.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
LLAMA_TAG=${LLAMA_TAG:-b9620}
WACLI_SRC=${WACLI_SRC:-$ROOT/third_party/wacli}
TP=$ROOT/third_party
T=$TP/linuxarm
mkdir -p "$T"
test -f "$TP/go.mod" || echo "module thirdparty" > "$TP/go.mod"
test -d "$TP/whisper.cpp" || git clone -q --depth 1 --branch "${WHISPER_VERSION:-v1.9.0}" https://github.com/ggml-org/whisper.cpp "$TP/whisper.cpp"
test -d "$TP/opus" || git clone -q --depth 1 --branch v1.5.2 https://github.com/xiph/opus "$TP/opus"
test -d "$TP/llama.cpp" || git clone -q --depth 1 --branch "$LLAMA_TAG" https://github.com/ggml-org/llama.cpp "$TP/llama.cpp"
test -d "$WACLI_SRC" || { echo "wacli source not found at $WACLI_SRC (set WACLI_SRC)"; exit 1; }

printf '#!/bin/sh\nexec zig cc -target aarch64-linux-musl "$@"\n' > "$T/zcc"
printf '#!/bin/sh\nexec zig c++ -target aarch64-linux-musl "$@"\n' > "$T/zcxx"
printf '#!/bin/sh\nexec zig ar "$@"\n' > "$T/zar"
printf '#!/bin/sh\nexec zig ranlib "$@"\n' > "$T/zranlib"
chmod +x "$T"/z*
cm() { # cmake configure for aarch64-linux-musl
  src=$1 build=$2; shift 2
  cmake -S "$src" -B "$build" -G "Unix Makefiles" -DCMAKE_SYSTEM_NAME=Linux -DCMAKE_SYSTEM_PROCESSOR=aarch64 \
    -DCMAKE_C_COMPILER="$T/zcc" -DCMAKE_CXX_COMPILER="$T/zcxx" -DCMAKE_AR="$T/zar" -DCMAKE_RANLIB="$T/zranlib" \
    -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DGGML_NATIVE=OFF -DGGML_OPENMP=OFF -DCMAKE_POSITION_INDEPENDENT_CODE=ON "$@" >/dev/null
}

if [ ! -f "$T/whisper-pie/src/libwhisper.a" ]; then
  echo "whisper.cpp linux-arm64"
  cm "$TP/whisper.cpp" "$T/whisper-pie" -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_EXAMPLES=OFF
  cmake --build "$T/whisper-pie" -j 8 >/dev/null
fi
if [ ! -f "$T/opus-pie/libopus.a" ]; then
  echo "libopus linux-arm64"
  cm "$TP/opus" "$T/opus-pie" -DOPUS_BUILD_TESTING=OFF -DOPUS_BUILD_PROGRAMS=OFF
  cmake --build "$T/opus-pie" -j 8 >/dev/null
fi

out=$ROOT/bundled/linux-arm64; mkdir -p "$out"
if [ ! -f "$out/llama-server" ]; then
  echo "llama-server linux-arm64"
  cm "$TP/llama.cpp" "$TP/llama-build/linux-arm64-pie" -DLLAMA_CURL=OFF -DLLAMA_OPENSSL=OFF -DLLAMA_BUILD_TESTS=OFF \
    -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_TOOLS=ON -DLLAMA_BUILD_SERVER=ON '-DCMAKE_EXE_LINKER_FLAGS=-static-pie -s' \
    "-DCMAKE_C_FLAGS=-mcpu=cortex_a55 -fPIE" "-DCMAKE_CXX_FLAGS=-mcpu=cortex_a55 -fPIE" # ARMv8.2 + dotprod (phones from 2018 on): ~1.4x tokens/s
  cmake --build "$TP/llama-build/linux-arm64-pie" --target llama-server -j 8 >/dev/null
  cp "$TP/llama-build/linux-arm64-pie/bin/llama-server" "$out/"
fi

# Go: pure-Go DNS (netgo) and a static musl link. netfix_linux.go (no resolv.conf / CA roots in Termux)
# is copied into wacli's main package too, so both programs find DNS and TLS roots the same way.
GOENV="CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC=$T/zcc CXX=$T/zcxx"
LDF="-s -w -linkmode external -extldflags '-static-pie -s'"
cp "$ROOT/netfix_linux.go" "$WACLI_SRC/cmd/wacli/zz_netfix_linux.go"
if [ ! -f "$out/wacli" ] || [ "$ROOT/netfix_linux.go" -nt "$out/wacli" ]; then
  echo "wacli linux-arm64"
  (cd "$WACLI_SRC" && env $GOENV go build -buildmode=pie -tags netgo,osusergo -trimpath -ldflags "$LDF" -o "$out/wacli" ./cmd/wacli)
fi
cp "$WACLI_SRC/LICENSE" "$ROOT/bundled/LICENSE.wacli" 2>/dev/null || true
cp "$TP/llama.cpp/LICENSE" "$ROOT/bundled/LICENSE.llama.cpp"

mkdir -p "$ROOT/dist"
echo "ownshop linux-arm64"
(cd "$ROOT" && env $GOENV go build -buildmode=pie -tags bundle,voice,netgo,osusergo -trimpath -ldflags "$LDF" -o dist/ownshop-linux-arm64 .)

test -f "$ROOT/models/qwen35-0.8b-q4km.gguf" -a -f "$ROOT/models/ggml-base.bin" || { echo "models/ missing: no tarball"; exit 0; }
P=$ROOT/dist/pkg-linux-arm64/ownshop
rm -rf "$ROOT/dist/pkg-linux-arm64"; mkdir -p "$P/models"
cp "$ROOT/dist/ownshop-linux-arm64" "$P/ownshop"
cp "$ROOT/models/qwen35-0.8b-q4km.gguf" "$ROOT/models/ggml-base.bin" "$P/models/"
cp "$ROOT/packaging/termux/run-termux.sh" "$ROOT/packaging/termux/README-termux.txt" "$P/"
chmod +x "$P/ownshop" "$P/run-termux.sh"
COPYFILE_DISABLE=1 tar --no-xattrs -C "$ROOT/dist/pkg-linux-arm64" -czf "$ROOT/dist/ownshop-linux-arm64.tar.gz" ownshop
ls -l "$out" "$ROOT/dist/ownshop-linux-arm64" "$ROOT/dist/ownshop-linux-arm64.tar.gz"
