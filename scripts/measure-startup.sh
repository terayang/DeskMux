#!/usr/bin/env bash
# measure-startup.sh — launch-latency comparison: Wails build vs legacy Electron build.
#
# Metric (identical for both apps):
#   wall time from posix_spawn of the GUI process to the first poll where the
#   process owns >= 1 normal (layer-0) on-screen window, reported by
#   CGWindowListCopyWindowInfo. A tiny Swift helper (compiled once per run into
#   a temp dir) spawns the child, polls every 20 ms, prints elapsed ms, then
#   terminates the child. 1 warmup + 3 measured runs per app; median reported.
#
# Known limitations (be honest when quoting numbers):
#   - "first window exists" != "UI interactive": frontend JS init / first paint
#     may trail the window by tens of ms, differently per toolkit.
#   - Runs are warm launches (dyld/Gatekeeper caches primed by the warmup run);
#     absolute cold-start cost is higher for both.
#   - Sequential, same-machine, same-session measurement; background load adds
#     noise. Treat as indicative, not benchmark-grade.
set -euo pipefail
cd "$(dirname "$0")/.."

WAILS_BIN="build/bin/DeskMux.app/Contents/MacOS/DeskMux"
ELECTRON_BIN="dist/mac-arm64/AnyRemote.app/Contents/MacOS/AnyRemote"
RUNS=3
TIMEOUT=30

# Kill stale instances from earlier runs so single-instance locks can't
# short-circuit a measured launch.
pkill -f 'DeskMux.app/Contents/MacOS/DeskMux' 2>/dev/null || true
sleep 1

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cat > "$TMP/winwait.swift" <<'SWIFT'
import Cocoa

// winwait <binary> [timeoutSeconds]
// Spawns the binary, measures wall time (ms) until the process owns its first
// normal-layer window, prints the elapsed ms on stdout, then terminates it.
let args = CommandLine.arguments
guard args.count >= 2 else { fputs("usage: winwait <binary> [timeout]\n", stderr); exit(2) }
let binary = args[1]
let timeout: Double = args.count > 2 ? Double(args[2]) ?? 30 : 30

let proc = Process()
proc.executableURL = URL(fileURLWithPath: binary)
proc.standardOutput = FileHandle.nullDevice
proc.standardError = FileHandle.nullDevice

let start = Date()
do { try proc.run() } catch {
    fputs("spawn failed: \(error.localizedDescription)\n", stderr)
    exit(1)
}
let pid = proc.processIdentifier

func hasMainWindow(_ pid: Int32) -> Bool {
    guard let list = CGWindowListCopyWindowInfo([.optionAll], kCGNullWindowID) as? [[String: Any]] else { return false }
    for w in list {
        guard let owner = w[kCGWindowOwnerPID as String] as? Int, owner == pid else { continue }
        guard let layer = w[kCGWindowLayer as String] as? Int, layer == 0 else { continue }
        if let b = w[kCGWindowBounds as String] as? [String: Any], let width = b["Width"] as? Double, width < 50 { continue }
        return true
    }
    return false
}

var found = false
while Date().timeIntervalSince(start) < timeout {
    if hasMainWindow(pid) { found = true; break }
    usleep(20_000) // 20 ms
}
let elapsedMs = Int(Date().timeIntervalSince(start) * 1000)

// Shut the child down completely so the next launch is a fresh process
// (and any single-instance lock is released).
proc.terminate()
for _ in 0..<50 where proc.isRunning { usleep(40_000) }
if proc.isRunning { kill(pid, SIGKILL) }
proc.waitUntilExit()

if found {
    print(elapsedMs)
} else {
    fputs("timeout after \(Int(timeout * 1000)) ms\n", stderr)
    exit(3)
}
SWIFT

swiftc -O -o "$TMP/winwait" "$TMP/winwait.swift"

measure() {
  local label="$1" bin="$2"
  if [ ! -x "$bin" ]; then
    echo "$label: SKIP (binary not found: $bin)"
    return
  fi
  "$TMP/winwait" "$bin" "$TIMEOUT" >/dev/null # warmup (primes dyld/Gatekeeper caches)
  local times=()
  for _ in $(seq "$RUNS"); do
    times+=("$("$TMP/winwait" "$bin" "$TIMEOUT")")
  done
  local median
  median="$(printf '%s\n' "${times[@]}" | sort -n | awk '{a[NR]=$1} END {print a[int((NR+1)/2)]}')"
  echo "$label: runs(ms)=$(printf '%s ' "${times[@]}") median=${median}ms"
}

measure "wails   " "$WAILS_BIN"
measure "electron" "$ELECTRON_BIN"
