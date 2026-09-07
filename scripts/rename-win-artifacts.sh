#!/usr/bin/env bash
# Rename Windows build outputs to versioned, flavor-distinct filenames:
#   DeskMux.exe                      -> DeskMux-<version>-windows-x64-portable.exe
#   DeskMux-amd64-installer.exe      -> DeskMux-<version>-windows-x64-installer.exe
# (<version> is read from the root package.json)
set -euo pipefail

cd "$(dirname "$0")/.."
VERSION="$(node -p "require('./package.json').version")"

cd build/bin
for f in DeskMux.exe DeskMux-amd64-installer.exe; do
  [ -f "$f" ] || { echo "error: expected artifact missing: $f" >&2; exit 1; }
done
mv -f DeskMux.exe "DeskMux-${VERSION}-windows-x64-portable.exe"
mv -f DeskMux-amd64-installer.exe "DeskMux-${VERSION}-windows-x64-installer.exe"
ls -lh ./*.exe
