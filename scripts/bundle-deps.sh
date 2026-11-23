#!/bin/sh
# Build the two helper programs ownshop carries inside itself, for darwin/arm64, windows/amd64 and
# windows/arm64, into bundled/<goos>-<goarch>/ (gitignored). ownshop embeds them with -tags bundle and
# unpacks them to its app data dir on first run, so the shop owner installs nothing else.
#   wacli         WhatsApp (whatsmeow), from WACLI_SRC (default: the wacli source in third_party/wacli)
#   llama-server  llama.cpp's OpenAI-compatible server, static, no curl/openssl, Metal embedded on mac
# Needs go, zig, cmake, git. Cached: a target is skipped when its output exists.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
LLAMA_TAG=${LLAMA_TAG:-b9620}
WACLI_SRC=${WACLI_SRC:-$ROOT/third_party/wacli}
TP=$ROOT/third_party
mkdir -p "$TP"
test -f "$TP/go.mod" || echo "module thirdparty" > "$TP/go.mod" # keep third_party out of ./...
test -d "$WACLI_SRC" || { echo "wacli source not found at $WACLI_SRC (set WACLI_SRC)"; exit 1; }
test -d "$TP/llama.cpp" || git clone -q --depth 1 --branch "$LLAMA_TAG" https://github.com/ggml-org/llama.cpp "$TP/llama.cpp"

# zig wrappers (same as windows-voice-deps.sh)
for pair in win:x86_64 winarm:aarch64; do
  T=$TP/${pair%%:*}; zt=${pair#*:}; mkdir -p "$T"
  printf '#!/bin/sh\nexec zig cc -target %s-windows-gnu "$@"\n' "$zt" > "$T/zcc"
  printf '#!/bin/sh\nexec zig c++ -target %s-windows-gnu "$@"\n' "$zt" > "$T/zcxx"
  printf '#!/bin/sh\nexec zig ar "$@"\n' > "$T/zar"
  printf '#!/bin/sh\nexec zig ranlib "$@"\n' > "$T/zranlib"
  chmod +x "$T"/z*
done

LLAMA_OPTS="-DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DGGML_NATIVE=OFF -DLLAMA_CURL=OFF -DLLAMA_OPENSSL=OFF \
 -DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_TOOLS=ON -DLLAMA_BUILD_SERVER=ON"

for t in darwin-arm64 windows-amd64 windows-arm64; do
  out=$ROOT/bundled/$t; mkdir -p "$out"
  exe=""; case $t in windows-*) exe=.exe;; esac
  case $t in windows-amd64) W=$TP/win proc=x86_64;; windows-arm64) W=$TP/winarm proc=ARM64;; esac
  if [ ! -f "$out/wacli$exe" ]; then
    echo "wacli $t"
    if [ $t = darwin-arm64 ]; then
      (cd "$WACLI_SRC" && CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o "$out/wacli" ./cmd/wacli)
    else
      (cd "$WACLI_SRC" && CGO_ENABLED=1 GOOS=windows GOARCH=${t#windows-} CC=$W/zcc CXX=$W/zcxx \
        go build -trimpath -ldflags "-s -w" -o "$out/wacli.exe" ./cmd/wacli)
    fi
  fi
  if [ ! -f "$out/llama-server$exe" ]; then
    echo "llama-server $t"
    B=$TP/llama-build/$t
    if [ $t = darwin-arm64 ]; then
      cmake -S "$TP/llama.cpp" -B "$B" $LLAMA_OPTS -DGGML_METAL_EMBED_LIBRARY=ON -DGGML_BLAS=OFF >/dev/null
    else
      cmake -S "$TP/llama.cpp" -B "$B" -G "Unix Makefiles" -DCMAKE_SYSTEM_NAME=Windows -DCMAKE_SYSTEM_PROCESSOR=$proc \
        -DCMAKE_C_COMPILER="$W/zcc" -DCMAKE_CXX_COMPILER="$W/zcxx" -DCMAKE_AR="$W/zar" -DCMAKE_RANLIB="$W/zranlib" \
        $LLAMA_OPTS -DGGML_OPENMP=OFF >/dev/null
    fi
    cmake --build "$B" --target llama-server -j 8 >/dev/null
    cp "$B/bin/llama-server$exe" "$out/"
  fi
done
cp "$WACLI_SRC/LICENSE" "$ROOT/bundled/LICENSE.wacli" 2>/dev/null || true
cp "$TP/llama.cpp/LICENSE" "$ROOT/bundled/LICENSE.llama.cpp"
# the full notices (every component, its license and source) ship in every installer next to the program
cp "$ROOT/THIRD-PARTY-NOTICES" "$ROOT/CREDITS" "$ROOT/LICENSE" "$ROOT/bundled/"
ls -l "$ROOT"/bundled/*/
