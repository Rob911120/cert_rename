#!/usr/bin/env bash
# Bygger cert-renamer för Mac (.app + raw) och Windows (.exe).
# Pure Go, ingen CGO, inget Docker.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

GO="${GO:-$HOME/.local/go/bin/go}"
APP_NAME="cert-renamer"
APP_DISPLAY="Cert Renamer"
APP_ID="se.rob.cert-renamer"
APP_VERSION="0.1.0"

DIST_MAC="$SCRIPT_DIR/dist/mac"
DIST_WIN="$SCRIPT_DIR/dist/windows"

echo "🧹 Rensar dist/"
rm -rf dist
mkdir -p "$DIST_MAC" "$DIST_WIN"

# ---------- Bygg råa binärer ----------
echo ""
echo "🍏 Mac (arm64 — Apple Silicon)..."
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 "$GO" build -ldflags="-s -w" -o "$DIST_MAC/$APP_NAME" ./cmd/cert-renamer

echo "🍏 Mac (amd64 — Intel)..."
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 "$GO" build -ldflags="-s -w" -o "$DIST_MAC/${APP_NAME}-intel" ./cmd/cert-renamer

echo "🪟 Windows (amd64)..."
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 "$GO" build -ldflags="-s -w" -o "$DIST_WIN/${APP_NAME}.exe" ./cmd/cert-renamer

# ---------- Bygg universal-binär (arm64+amd64) ----------
echo ""
echo "🔗 Bygger universal-binär (arm64 + amd64)..."
UNIVERSAL="$DIST_MAC/${APP_NAME}-universal"
lipo -create -output "$UNIVERSAL" \
    "$DIST_MAC/$APP_NAME" "$DIST_MAC/${APP_NAME}-intel"
echo "   ✅ $UNIVERSAL"

# ---------- Wrappa i .app-bundle ----------
echo ""
echo "📦 Wrappar till .app-bundle..."
APP_DIR="$DIST_MAC/${APP_DISPLAY}.app"
rm -rf "$APP_DIR"
mkdir -p "$APP_DIR/Contents/MacOS" "$APP_DIR/Contents/Resources"
cp "$UNIVERSAL" "$APP_DIR/Contents/MacOS/$APP_NAME"
chmod +x "$APP_DIR/Contents/MacOS/$APP_NAME"

cat > "$APP_DIR/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>${APP_DISPLAY}</string>
    <key>CFBundleDisplayName</key>
    <string>${APP_DISPLAY}</string>
    <key>CFBundleExecutable</key>
    <string>${APP_NAME}</string>
    <key>CFBundleIdentifier</key>
    <string>${APP_ID}</string>
    <key>CFBundleVersion</key>
    <string>${APP_VERSION}</string>
    <key>CFBundleShortVersionString</key>
    <string>${APP_VERSION}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleSignature</key>
    <string>????</string>
    <key>LSMinimumSystemVersion</key>
    <string>10.15</string>
    <key>NSHighResolutionCapable</key>
    <true/>
</dict>
</plist>
EOF

echo "   ✅ $APP_DIR"

echo ""
echo "📦 Klart:"
echo ""
echo "   macOS:"
echo "     - $APP_DIR  (dubbelklicka eller dra till /Applications)"
echo "     - $DIST_MAC/$APP_NAME           (rå arm64-binär)"
echo "     - $DIST_MAC/${APP_NAME}-intel   (rå amd64-binär)"
echo ""
echo "   Windows:"
echo "     - $DIST_WIN/${APP_NAME}.exe     (dubbelklicka)"
echo ""
du -sh "$APP_DIR" "$DIST_WIN/${APP_NAME}.exe" 2>/dev/null
