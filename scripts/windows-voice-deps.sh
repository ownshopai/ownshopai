#!/bin/sh
# Cross-build whisper.cpp and libopus as static libraries for Windows (amd64 and arm64) with zig,
# so `task build` can make Windows binaries with voice notes from a Mac. Needs zig and cmake.
set -eu
cd "$(dirname "$0")/../third_party"
test -d opus || git clone -q --depth 1 --branch v1.5.2 https://github.com/xiph/opus opus
for pair in win:x86_64:x86_64 winarm:aarch64:ARM64; do
  dir=${pair%%:*}; rest=${pair#*:}; zt=${rest%%:*}; proc=${rest#*:}
  T=$PWD/$dir; mkdir -p "$T"
  printf '#!/bin/sh\nexec zig cc -target %s-windows-gnu "$@"\n' "$zt" > "$T/zcc"
  printf '#!/bin/sh\nexec zig c++ -target %s-windows-gnu "$@"\n' "$zt" > "$T/zcxx"
  printf '#!/bin/sh\nexec zig ar "$@"\n' > "$T/zar"
  printf '#!/bin/sh\nexec zig ranlib "$@"\n' > "$T/zranlib"
  chmod +x "$T"/z*
  cmake -S whisper.cpp -B "$T/whisper" -G "Unix Makefiles" -DCMAKE_SYSTEM_NAME=Windows -DCMAKE_SYSTEM_PROCESSOR=$proc \
    -DCMAKE_C_COMPILER="$T/zcc" -DCMAKE_CXX_COMPILER="$T/zcxx" -DCMAKE_AR="$T/zar" -DCMAKE_RANLIB="$T/zranlib" \
    -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DGGML_NATIVE=OFF -DGGML_OPENMP=OFF \
    -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_EXAMPLES=OFF >/dev/null
  cmake --build "$T/whisper" -j 8 >/dev/null
  cmake -S opus -B "$T/opus" -G "Unix Makefiles" -DCMAKE_SYSTEM_NAME=Windows -DCMAKE_SYSTEM_PROCESSOR=$proc \
    -DCMAKE_C_COMPILER="$T/zcc" -DCMAKE_AR="$T/zar" -DCMAKE_RANLIB="$T/zranlib" -DCMAKE_BUILD_TYPE=Release \
    -DBUILD_SHARED_LIBS=OFF -DOPUS_BUILD_TESTING=OFF -DOPUS_BUILD_PROGRAMS=OFF >/dev/null
  cmake --build "$T/opus" -j 8 >/dev/null
done
