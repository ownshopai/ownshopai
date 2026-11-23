#!/bin/sh
# Package dist/ownshop-darwin-arm64 as "OwnShop AI.app" (models in Contents/Resources/models) inside
# dist/ownshop-macos-arm64.dmg: the one file a Mac shop owner downloads. Double-clicking the app
# copies it to ~/Applications, starts the shop and opens the browser; no Terminal window.
# Signing: set APPLE_SIGN_IDENTITY (a "Developer ID Application" identity) to codesign the app and
# the image; notarization is left to the owner (xcrun notarytool submit dist/ownshop-macos-arm64.dmg).
set -eu
cd "$(dirname "$0")/.."
VERSION=${VERSION:-$(git describe --tags --always 2>/dev/null || echo dev)}
for m in qwen35-0.8b-q4km.gguf ggml-base.bin; do test -f models/$m || { echo "models/$m missing"; exit 1; }; done
S=dist/mac-stage; rm -rf "$S"; A="$S/OwnShop AI.app/Contents"
mkdir -p "$A/MacOS" "$A/Resources/models"
cp dist/ownshop-darwin-arm64 "$A/MacOS/ownshop"
cp models/qwen35-0.8b-q4km.gguf models/ggml-base.bin "$A/Resources/models/"
cat > "$A/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>ownshop</string>
<key>CFBundleIdentifier</key><string>com.deemwar.ownshop</string>
<key>CFBundleName</key><string>OwnShop AI</string><key>CFBundleDisplayName</key><string>OwnShop AI</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>$VERSION</string>
<key>CFBundleVersion</key><string>$VERSION</string>
<key>LSMinimumSystemVersion</key><string>12.0</string>
<key>LSUIElement</key><true/>
</dict></plist>
PLIST
if [ -n "${APPLE_SIGN_IDENTITY:-}" ]; then
  codesign --force --options runtime --timestamp --sign "$APPLE_SIGN_IDENTITY" "$S/OwnShop AI.app"
fi
ln -s /Applications "$S/Applications"
rm -f dist/ownshop-macos-arm64.dmg
hdiutil create -quiet -volname "OwnShop AI" -srcfolder "$S" -fs HFS+ -format UDZO dist/ownshop-macos-arm64.dmg
if [ -n "${APPLE_SIGN_IDENTITY:-}" ]; then codesign --sign "$APPLE_SIGN_IDENTITY" --timestamp dist/ownshop-macos-arm64.dmg; fi
rm -rf "$S"
ls -l dist/ownshop-macos-arm64.dmg
