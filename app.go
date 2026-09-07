package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"deskmux/internal/sshx"
	"deskmux/internal/store"
	"deskmux/internal/vncbridge"
)

// App is the Wails-bound application facade. Bound methods (see bindings.go)
// are callable from the frontend via window.go.main.App.*; long-running data
// streams are pushed with runtime.EventsEmit.
type App struct {
	ctx context.Context

	// ssh owns every live SSH session (shell + SFTP share its connections).
	ssh *sshx.Manager

	// connections persists saved connections; secrets live in the backend the
	// secret-storage setting selects (OS keychain by default, FileSecrets for
	// the local-file mode).
	connections *store.Store
	// configDir is the DeskMux per-user config dir holding connections.json,
	// settings.json and (in local-file mode) secrets.json; SetSecretStorage
	// needs it to construct the FileSecrets backend.
	configDir string

	bridgesMu sync.Mutex
	// bridges holds the live VNC WebSocket bridges by bridge id.
	bridges map[string]*vncbridge.Bridge
	// bridgeSeq numbers bridge ids ("bridge-N"), like the Electron main process.
	bridgeSeq int
}

// NewApp creates a new App instance.
func NewApp() *App {
	connections, configDir := newConnectionStore()
	return &App{
		ssh:         sshx.NewManager(),
		connections: connections,
		configDir:   configDir,
		bridges:     make(map[string]*vncbridge.Bridge),
	}
}

// newConnectionStore opens the saved-connection store at the default per-user
// config location (os.UserConfigDir()/AnyRemote — legacy identifier kept for
// user-data continuity: the config dir predates the DeskMux rename), with the
// secret backend the persisted setting selects (OS keychain by default, the
// encrypted local file for the "localFile" mode). When the config dir cannot
// be resolved ($HOME or platform equivalent unset) the store falls back to
// the temp dir so the app stays usable for the session; it likewise falls
// back to the keychain backend if the configured one cannot be constructed.
// It returns the store and the resolved config dir.
func newConnectionStore() (*store.Store, string) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = os.TempDir()
	}
	// "AnyRemote" is a legacy identifier kept for user-data continuity
	// (config dir / keychain entries predate the DeskMux rename).
	dir := filepath.Join(configDir, "AnyRemote")
	s, _, err := store.NewWithMode(dir, store.KeyringSecrets)
	if err != nil {
		return store.New(dir, store.KeyringSecrets()), dir
	}
	return s, dir
}

// startup is called when the app starts; the context is used for event
// emission and dialog parenting.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// shutdown releases every SSH session and VNC bridge (registered as
// OnShutdown in main.go), mirroring the Electron before-quit cleanup.
func (a *App) shutdown(_ context.Context) {
	a.ssh.CloseAll()
	a.bridgesMu.Lock()
	bridges := make([]*vncbridge.Bridge, 0, len(a.bridges))
	for id, bridge := range a.bridges {
		bridges = append(bridges, bridge)
		delete(a.bridges, id)
	}
	a.bridgesMu.Unlock()
	for _, bridge := range bridges {
		_ = bridge.Close()
	}
}

// nextBridgeID returns the next sequential bridge id ("bridge-N").
func (a *App) nextBridgeID() string {
	a.bridgeSeq++
	return fmt.Sprintf("bridge-%d", a.bridgeSeq)
}
