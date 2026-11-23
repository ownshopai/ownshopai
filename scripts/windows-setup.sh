#!/bin/sh
# Make dist/ownshop-setup-windows-<arch>.exe: the setup program with a zip appended that holds
# ownshop.exe and the two model files. The one file a Windows shop owner downloads.
set -eu
cd "$(dirname "$0")/.."
for m in qwen35-0.8b-q4km.gguf ggml-base.bin; do test -f models/$m || { echo "models/$m missing"; exit 1; }; done
for arch in amd64 arm64; do
  S=dist/win-stage-$arch; rm -rf "$S"; mkdir -p "$S/models"
  cp dist/ownshop-windows-$arch.exe "$S/ownshop.exe"
  cp models/qwen35-0.8b-q4km.gguf models/ggml-base.bin "$S/models/"
  CGO_ENABLED=0 GOOS=windows GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$S/setup.exe" ./cmd/ownshop-setup
  (cd "$S" && zip -q -9 ../payload-$arch.zip ownshop.exe && zip -q -0 ../payload-$arch.zip models/*)
  cat "$S/setup.exe" dist/payload-$arch.zip > dist/ownshop-setup-windows-$arch.exe
  rm -rf "$S" dist/payload-$arch.zip
done
ls -l dist/ownshop-setup-windows-*.exe
