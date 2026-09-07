#!/usr/bin/env bash
# Build a distributable dmg from a built .app bundle.
#
# Usage:   bash scripts/build-dmg.sh [path/to/DeskMux.app] [arch]
# Input:   .app path (default: build/bin/DeskMux.app, produced by `wails build`)
#          arch label for the filename (default: universal)
# Output:  dist/DeskMux-<version>-mac-<arch>.dmg
#          (<version> is read from the root package.json)
#
# The dmg contains the .app plus an /Applications symlink for drag-install.
# Window styling is intentionally minimal (no custom background/icon layout).
set -euo pipefail

cd "$(dirname "$0")/.."

APP_PATH="${1:-build/bin/DeskMux.app}"
ARCH="${2:-universal}"
if [ ! -d "$APP_PATH" ]; then
  echo "error: app bundle not found: $APP_PATH" >&2
  exit 1
fi

APP_NAME="$(basename "$APP_PATH" .app)"
VERSION="$(node -p "require('./package.json').version")"
DMG_NAME="${APP_NAME}-${VERSION}-mac-${ARCH}.dmg"

mkdir -p dist
STAGING="$(mktemp -d)"
trap 'rm -rf "$STAGING"' EXIT

cp -R "$APP_PATH" "$STAGING/"
ln -s /Applications "$STAGING/Applications"

rm -f "dist/$DMG_NAME"
hdiutil create \
  -volname "$APP_NAME" \
  -srcfolder "$STAGING" \
  -ov \
  -format UDZO \
  "dist/$DMG_NAME"

echo "built: dist/$DMG_NAME ($(du -h "dist/$DMG_NAME" | cut -f1))"
