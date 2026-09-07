# AnyRemote

[![CI](https://github.com/terayang/AnyRemote/actions/workflows/ci.yml/badge.svg)](https://github.com/terayang/AnyRemote/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/terayang/AnyRemote)](https://github.com/terayang/AnyRemote/releases)
[![License](https://img.shields.io/github/license/terayang/AnyRemote)](LICENSE)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Windows-blue)](https://github.com/terayang/AnyRemote/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/terayang/AnyRemote)](go.mod)

**English** | [简体中文](README.zh-CN.md) | [Releases](https://github.com/terayang/AnyRemote/releases) | [Issues](https://github.com/terayang/AnyRemote/issues) | [Contributing](CONTRIBUTING.md)

---

AnyRemote is a cross-platform (macOS / Windows) desktop remote session manager: enter a target IP, auto-detect its available remote protocols (SSH / VNC / RDP / Telnet / FTP / SMB / HTTP(S)), pick several, and connect in one click — remote desktop, SSH terminal, and SFTP file management in a single app, on par with 1Remote / Tabby / Termius.

> Fair use: AnyRemote is a remote administration tool — use it only on devices you own or are explicitly authorized to manage. The authors are not responsible for any misuse.

### Screenshots

![Operator Console workbench: activity bar, session rail, terminal session](docs/design/workbench-sessions.png)

![Device fleet view with per-protocol one-click entries](docs/design/workbench-devices.png)

### Features

- **Operator Console workbench**: a 48px activity bar + contextual rail + stage layout — sessions view (live sessions + saved devices, live filter) and a device fleet view with inline per-protocol entries (terminal / desktop)
- **⌘K command palette**: quick-connect any address, search saved devices and live sessions, switch sessions (⌘1…⌘9 jump straight to the Nth), all keyboard-first
- **Protocol auto-detection**: concurrent probing of common ports with protocol fingerprinting, shown as selectable cards with plain-language descriptions
- **VNC remote desktop**: noVNC rendering + smart Go-side bridge; supports macOS Screen Sharing (Apple DH auth) and standard VNC password auth; two-way clipboard sync (Latin-1 per the RFB protocol), fullscreen, view-only mode, special keys (Ctrl+Alt+Del / Alt+F4 / Super), adjustable scaling / cursor / encoding / color depth / quality / compression
- **SSH terminal**: xterm.js + Go SSH/SFTP session management, password and private-key authentication, dual-layer keepalive (TCP + SSH application-level) with dead-connection detection
- **SFTP file manager**: browse, upload / download, create / delete / rename remote files
- **Connection management**: saved hosts with one-click reconnect; edit name / host / username / protocols / credentials after saving — opening the editor probes the host and offers one-click adding of newly detected open protocols; passwords / private keys go to the OS keychain by default (macOS Keychain / Windows Credential Manager), or to a local encrypted file (AES-256-GCM, key derived from the machine hardware UUID) via Settings
- **Multi-session**: concurrent sessions to different targets coexist, each with its own protocol tabs (desktop / terminal / files); terminals, file managers and desktops keep their state and connections while switched away
- **i18n**: Simplified Chinese by default, English (en-US) built in

> RDP / Telnet / FTP / SMB / HTTP(S) are detection-only for now; see the roadmap.

### Download & install

Grab the latest version from [GitHub Releases](https://github.com/terayang/AnyRemote/releases):

- **macOS**: `AnyRemote-<version>-mac-arm64.dmg` (Apple Silicon) or `AnyRemote-<version>-mac-x64.dmg` (Intel)
- **Windows**: `AnyRemote-<version>-windows-x64-installer.exe` (NSIS installer, per-user) or `AnyRemote-<version>-windows-x64-portable.exe` (no-install portable)

Development builds (CI on every push to main or PR) are available as artifacts at the bottom of the corresponding [workflow run page](https://github.com/terayang/AnyRemote/actions/workflows/ci.yml).

**The installers are not code-signed**, so the OS will warn on first launch — this is expected:

- **macOS**: right-click (Control-click) `AnyRemote.app` → **Open** → confirm **Open** again
- **Windows**: on the blue SmartScreen prompt, click **More info** → **Run anyway**

See [docs/RELEASE.md](docs/RELEASE.md) for details.

### Build installers locally

```bash
npm install && npm --prefix frontend install
npm run dist       # macOS: per-arch builds → dist/AnyRemote-<version>-mac-arm64.dmg and -mac-x64.dmg
npm run dist:win   # Windows: versioned installer + portable → build/bin/
```

Both packaging commands auto-bump the patch version first (via `scripts/bump-version.mjs`), so every build produces a uniquely named artifact.

### FAQ

- **Keychain prompt when saving a connection?** Passwords / private keys live in the OS keychain by default, and macOS asks for permission on first write — click **Allow**. You can also switch to "Local file" storage (AES-256-GCM, undecryptable off this machine, but weaker than the keychain) in Settings (gear icon at the bottom of the left activity bar).
- **No VNC mouse cursor?** Apple's Screen Sharing delivers cursor shapes unreliably, so AnyRemote defaults to the local cursor (switchable to "Remote cursor" in the toolbar). If it ever goes invisible, toggle the cursor mode once.
- **Clipboard sync not working for Chinese text?** The RFB clipboard message (CutText) is Latin-1 only; macOS Screen Sharing does not implement any extended-clipboard encoding, so non-Latin-1 characters cannot cross the VNC clipboard — this is a protocol limitation, not a bug.
- **Laggy or blurry desktop?** Open the toolbar gear ("Display"): **ZRLE** encoding + **16-bit** color depth + **Saver** compression is recommended; for crisp text choose "Actual size" scaling.
- **Which protocols are supported?** SSH terminal, SFTP file manager, and VNC desktop (including macOS Apple DH auth). RDP / Telnet / FTP are detection-only for now — see the roadmap.

### Development

Prerequisites: Go 1.26, Node.js 22+, wails CLI v2.13 (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0`, with `~/go/bin` on your PATH).

```bash
npm install && npm --prefix frontend install   # install dependencies
npm run dev        # wails dev (HMR; pure-frontend preview: npm --prefix frontend run dev, bridge falls back to mock)
npm test           # Go tests (go test ./...)
npm run typecheck  # go vet ./... + frontend tsc --noEmit
npm run build      # wails build → build/bin/ (regenerates frontend/wailsjs/ bindings)
```

See [CONTRIBUTING.md](CONTRIBUTING.md) to get involved, [SECURITY.md](SECURITY.md) for security issues, and [CHANGELOG.md](CHANGELOG.md) / [Releases](https://github.com/terayang/AnyRemote/releases) for version history.

### Architecture

Wails v2 (Go backend + system webview), migrated from Electron in 2026-07 (rationale and measurements: [docs/MIGRATION.md](docs/MIGRATION.md) — dmg 133MB→11MB, first window 340ms→241ms). A single Go process carries the entire network & protocol layer — a protocol fingerprint scanner (`internal/scanner`), SSH/SFTP sessions with keepalive (`internal/sshx`), RFB handshake with Apple DH auth (`internal/rfb`), a WS↔TCP VNC bridge (`internal/vncbridge`), and connection storage with configurable secrets backend (`internal/store`, OS keychain or local encrypted file). The React 18 + antd v5 + zustand + i18next frontend calls Wails bindings through the `window.anyremote` adapter in `frontend/src/bridge/`; noVNC renders the remote desktop and xterm.js the terminal. Full rationale and module layout: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md); packaging & release: [docs/RELEASE.md](docs/RELEASE.md).

### Roadmap

- Actual RDP / Telnet / FTP connections (detection-only today)
- Deeper SMB / HTTP(S) integration
- More UI languages (Simplified Chinese + English today; the i18n framework is extensible)

### Project layout

```
anyremote/
├─ main.go / app.go / bindings.go   # Wails entry, app facade, bindings & error convention
├─ internal/                        # Go service layer
│  ├─ scanner/                      # Protocol probing (port scan + fingerprinting)
│  ├─ sshx/                         # SSH/SFTP session management + keepalive
│  ├─ rfb/                          # RFB handshake, security type 2/30 auth
│  ├─ vncbridge/                    # WS↔TCP VNC bridge
│  └─ store/                        # Connection store, keychain / local-file secrets
├─ frontend/
│  ├─ src/                          # React UI (pages / components / hooks / utils / store / i18n / bridge)
│  └─ shared/                       # Types & protocol constants shared with the Go side
├─ scripts/                         # build-dmg.sh / bump-version.mjs / measure-startup.sh / mockvnc (VNC test server)
├─ build/                           # appicon.png, darwin/windows templates, build output (build/bin/, not committed)
├─ docs/                            # Architecture, migration, release & design docs
└─ .github/                         # CI / Release workflows, issue & PR templates
```

### License

[MIT](LICENSE) © 2026 Silica Yang
