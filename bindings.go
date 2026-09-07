// Wails binding layer: the methods below are exported on *App and therefore
// callable from the frontend as window.go.main.App.<Method>. They stay thin
// delegates over the service layer (scanner, sshx, vncbridge, local fs) and
// the native dialogs, matching the Electron IPC handlers one-to-one
// (src/main/ipc.ts, kept for reference until M6).
//
// Streaming channels reuse the Electron per-session names so the frontend's
// helpers (frontend/shared/ipc.ts) work unchanged:
//   - "ssh:data:<id>"      shell output chunks (string)
//   - "ssh:close:<id>"     fired once when the shell closes
//   - "sftp:progress:<id>" transfer progress (sftpProgressEvent)
//
// Error convention: every failure crosses the bridge as a Go error whose
// message is prefixed "[CODE] " (bindError), the same convention the Electron
// preload established; the renderer recovers the code with ipcErrorCode().
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"deskmux/internal/rfb"
	"deskmux/internal/scanner"
	"deskmux/internal/sshx"
	"deskmux/internal/store"
	"deskmux/internal/vncbridge"
)

// Streaming event channel names, mirroring frontend/shared/ipc.ts.
func sshDataChannel(sessionID string) string      { return "ssh:data:" + sessionID }
func sshCloseChannel(sessionID string) string     { return "ssh:close:" + sessionID }
func sftpProgressChannel(sessionID string) string { return "sftp:progress:" + sessionID }

// bindError wraps a service-layer failure so the machine-readable code
// survives the Wails IPC boundary: the returned error's message carries the
// "[CODE] message" prefix the frontend's ipcErrorCode() recovers. An
// *sshx.SshError keeps its own code; the RFB handshake error classes map onto
// the VNC codes the renderer expects (mirroring mapVncErrorCode in
// src/main/ipc.ts); filesystem failures keep their errno-style code; anything
// else is REMOTE_ERROR.
func bindError(err error) error {
	if err == nil {
		return nil
	}
	var sshErr *sshx.SshError
	if errors.As(err, &sshErr) {
		return fmt.Errorf("[%s] %s", sshErr.Code, sshErr.Message)
	}
	var storeErr *store.StoreError
	if errors.As(err, &storeErr) {
		return fmt.Errorf("[%s] %s", storeErr.Code, storeErr.Message)
	}
	var authErr *rfb.RfbAuthError
	if errors.As(err, &authErr) {
		return fmt.Errorf("[AUTH_FAILED] %s", authErr.Message)
	}
	var timeoutErr *rfb.RfbTimeoutError
	if errors.As(err, &timeoutErr) {
		return fmt.Errorf("[TIMEOUT] %s", timeoutErr.Message)
	}
	var protoErr *rfb.RfbProtocolError
	if errors.As(err, &protoErr) {
		return fmt.Errorf("[PROTOCOL_ERROR] %s", protoErr.Message)
	}
	var connErr *rfb.RfbConnectionError
	if errors.As(err, &connErr) {
		return fmt.Errorf("[UNREACHABLE] %s", connErr.Message)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("[ENOENT] %s", err.Error())
	}
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("[EACCES] %s", err.Error())
	}
	return fmt.Errorf("[REMOTE_ERROR] %s", err.Error())
}

// --- Protocol scanner ---

// Scan probes the well-known protocol ports on host concurrently.
func (a *App) Scan(host string) (scanner.TargetScanReport, error) {
	report, err := scanner.ScanTarget(host)
	if err != nil {
		return scanner.TargetScanReport{}, bindError(err)
	}
	return report, nil
}

// --- SSH terminal sessions ---

// SshConnect opens one SSH session (password or private-key auth) and returns
// its session id.
func (a *App) SshConnect(cfg sshx.AuthConfig) (string, error) {
	id, err := a.ssh.CreateSession(cfg)
	if err != nil {
		return "", bindError(err)
	}
	return id, nil
}

// SshOpenShell opens the session's interactive shell with a pseudo-TTY.
// Output streams to the frontend as "ssh:data:<id>" events; shell exit fires
// one "ssh:close:<id>" event.
func (a *App) SshOpenShell(id string, cols, rows uint32) error {
	return bindError(a.ssh.OpenShell(id, cols, rows,
		func(data string) { runtime.EventsEmit(a.ctx, sshDataChannel(id), data) },
		func() { runtime.EventsEmit(a.ctx, sshCloseChannel(id)) },
	))
}

// SshWrite writes raw input to the session's open shell.
func (a *App) SshWrite(id, data string) error {
	return bindError(a.ssh.WriteToShell(id, data))
}

// SshResize resizes the session's pseudo-TTY.
func (a *App) SshResize(id string, cols, rows uint32) error {
	return bindError(a.ssh.ResizeShell(id, cols, rows))
}

// SshClose closes the session and its connection.
func (a *App) SshClose(id string) error {
	return bindError(a.ssh.CloseSession(id))
}

// --- SFTP file management ---

// sftpProgressEvent mirrors SftpProgressEvent (frontend/shared/ipc.ts):
// TransferProgress plus the transfer direction.
type sftpProgressEvent struct {
	Transferred int64   `json:"transferred"`
	Total       int64   `json:"total"`
	Percent     float64 `json:"percent"`
	Direction   string  `json:"direction"`
}

// SftpHomeDir resolves the session's initial (home) directory.
func (a *App) SftpHomeDir(id string) (string, error) {
	home, err := a.ssh.HomeDir(id)
	if err != nil {
		return "", bindError(err)
	}
	return home, nil
}

// SftpList lists one remote directory.
func (a *App) SftpList(id, path string) ([]sshx.FileEntry, error) {
	entries, err := a.ssh.List(id, path)
	if err != nil {
		return nil, bindError(err)
	}
	return entries, nil
}

// SftpMkdir creates one remote directory (non-recursive).
func (a *App) SftpMkdir(id, path string) error {
	return bindError(a.ssh.Mkdir(id, path))
}

// SftpRename renames/moves a remote file or directory.
func (a *App) SftpRename(id, oldPath, newPath string) error {
	return bindError(a.ssh.Rename(id, oldPath, newPath))
}

// SftpDeleteFile deletes one remote file (or symlink).
func (a *App) SftpDeleteFile(id, path string) error {
	return bindError(a.ssh.DeleteFile(id, path))
}

// SftpDeleteDir removes one remote directory (must be empty).
func (a *App) SftpDeleteDir(id, path string) error {
	return bindError(a.ssh.DeleteDir(id, path))
}

// SftpUpload copies a local file to the remote host; progress streams as
// "sftp:progress:<id>" events with direction "upload".
func (a *App) SftpUpload(id, localPath, remotePath string) error {
	return bindError(a.ssh.Upload(id, localPath, remotePath, func(p sshx.TransferProgress) {
		runtime.EventsEmit(a.ctx, sftpProgressChannel(id), sftpProgressEvent{
			Transferred: p.Transferred,
			Total:       p.Total,
			Percent:     p.Percent,
			Direction:   "upload",
		})
	}))
}

// SftpDownload copies a remote file to the local disk; progress streams as
// "sftp:progress:<id>" events with direction "download".
func (a *App) SftpDownload(id, remotePath, localPath string) error {
	return bindError(a.ssh.Download(id, remotePath, localPath, func(p sshx.TransferProgress) {
		runtime.EventsEmit(a.ctx, sftpProgressChannel(id), sftpProgressEvent{
			Transferred: p.Transferred,
			Total:       p.Total,
			Percent:     p.Percent,
			Direction:   "download",
		})
	}))
}

// --- VNC WebSocket bridges ---

// VncBridgeHandle mirrors VncBridgeHandle (frontend/shared/ipc.ts): the id
// used to stop the bridge and the loopback WebSocket port it listens on.
type VncBridgeHandle struct {
	BridgeID string `json:"bridgeId"`
	WSPort   int    `json:"wsPort"`
}

// VncStartBridge launches a loopback WebSocket <-> TCP bridge that terminates
// the RFB security handshake (including Apple DH) for a noVNC client.
// encodings is the pixel-encoding preference (RFB encoding numbers, e.g.
// [16] = ZRLE); empty/nil leaves the client's SetEncodings untouched.
func (a *App) VncStartBridge(host string, port int, username, password string, encodings []int) (VncBridgeHandle, error) {
	var preferred []int32
	if len(encodings) > 0 {
		preferred = make([]int32, len(encodings))
		for i, encoding := range encodings {
			preferred[i] = int32(encoding)
		}
	}
	bridge, err := vncbridge.Start(vncbridge.Options{
		Host:      host,
		Port:      port,
		Username:  username,
		Password:  password,
		Encodings: preferred,
	})
	if err != nil {
		return VncBridgeHandle{}, bindError(err)
	}
	a.bridgesMu.Lock()
	id := a.nextBridgeID()
	a.bridges[id] = bridge
	a.bridgesMu.Unlock()
	return VncBridgeHandle{BridgeID: id, WSPort: bridge.WSPort}, nil
}

// VncStopBridge stops one bridge. An unknown id is a no-op (idempotent,
// matching the Electron handler).
func (a *App) VncStopBridge(id string) error {
	a.bridgesMu.Lock()
	bridge, ok := a.bridges[id]
	if ok {
		delete(a.bridges, id)
	}
	a.bridgesMu.Unlock()
	if !ok {
		return nil
	}
	return bridge.Close()
}

// --- Local filesystem (file manager left pane) ---

// LocalFsHomeDir returns the local user's home directory.
func (a *App) LocalFsHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", bindError(err)
	}
	return home, nil
}

// LocalFsList lists one local directory, directories first (each group sorted
// by name), mirroring the Electron localFs service. Entries that vanish or
// become unreadable mid-listing are skipped; lstat semantics give a symlink
// its own attributes, not its target's.
func (a *App) LocalFsList(path string) ([]sshx.FileEntry, error) {
	dirents, err := os.ReadDir(path)
	if err != nil {
		return nil, bindError(err)
	}
	entries := make([]sshx.FileEntry, 0, len(dirents))
	for _, dirent := range dirents {
		info, err := os.Lstat(filepath.Join(path, dirent.Name()))
		if err != nil {
			continue // raced deletion or unreadable entry: skip it
		}
		entryType := "file"
		switch {
		case info.IsDir():
			entryType = "directory"
		case info.Mode()&os.ModeSymlink != 0:
			entryType = "symlink"
		}
		entries = append(entries, sshx.FileEntry{
			Name:    dirent.Name(),
			Type:    entryType,
			Size:    info.Size(),
			MtimeMs: info.ModTime().UnixMilli(),
			// Permission bits only: the renderer never inspects the type bits
			// (the Type field carries them), and Go's os.FileMode type flags
			// are not POSIX values.
			Mode: uint32(info.Mode().Perm()),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		iDir := entries[i].Type == "directory"
		jDir := entries[j].Type == "directory"
		if iDir != jDir {
			return iDir
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

// --- Saved connections (encrypted persistence, internal/store) ---

// ConnectionsList returns every saved connection without secrets.
func (a *App) ConnectionsList() ([]store.Summary, error) {
	return a.connections.List(), nil
}

// ConnectionsGet returns one connection with its decrypted secret (for
// establishing a session). An unknown id fails with NOT_FOUND; the frontend
// bridge maps that code to null.
func (a *App) ConnectionsGet(id string) (store.Connection, error) {
	conn, err := a.connections.Get(id)
	if err != nil {
		return store.Connection{}, bindError(err)
	}
	return conn, nil
}

// ConnectionsSave creates (empty id) or updates (existing id) one connection.
// An omitted secret keeps the previously saved one, an explicitly empty
// secret clears it; a keychain failure reports ENCRYPTION_UNAVAILABLE.
func (a *App) ConnectionsSave(in store.Input) (store.Summary, error) {
	summary, err := a.connections.Save(in)
	if err != nil {
		return store.Summary{}, bindError(err)
	}
	return summary, nil
}

// ConnectionsDelete removes one connection and its keychain entry; an unknown
// id is a no-op (idempotent, matching the Electron handler).
func (a *App) ConnectionsDelete(id string) error {
	return bindError(a.connections.Delete(id))
}

// --- Secret-storage settings (internal/store) ---

// GetSecretStorage returns the active secret backend: "keychain" (OS
// keychain) or "localFile" (encrypted file in the config dir).
func (a *App) GetSecretStorage() string {
	return string(a.connections.Mode())
}

// SetSecretStorage switches the secret backend and hot-swaps the store —
// every stored secret is migrated to the new backend, the choice persists to
// settings.json, no restart is needed. An unknown mode is rejected; a
// migration failure reports ENCRYPTION_UNAVAILABLE and leaves the previous
// backend (and setting) active.
func (a *App) SetSecretStorage(mode string) error {
	backend := store.SecretBackend(mode)
	if !store.ValidSecretBackend(backend) {
		return fmt.Errorf("[REMOTE_ERROR] unknown secret storage mode %q (want %q or %q)",
			mode, store.SecretBackendKeychain, store.SecretBackendLocalFile)
	}
	if backend == a.connections.Mode() {
		return nil
	}
	var newSecrets store.SecretStore
	if backend == store.SecretBackendLocalFile {
		newSecrets = store.NewFileSecrets(a.configDir)
	} else {
		newSecrets = store.KeyringSecrets()
	}
	return bindError(a.connections.MigrateSecrets(backend, newSecrets))
}

// --- Native file dialogs ---

// DialogPickFiles opens a multi-select file picker; cancel resolves with an
// empty list.
func (a *App) DialogPickFiles() ([]string, error) {
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{})
	if err != nil {
		return nil, bindError(err)
	}
	if paths == nil {
		paths = []string{}
	}
	return paths, nil
}

// DialogPickSavePath opens a save-as picker suggesting defaultName; cancel
// resolves with "" (the frontend maps it to null).
func (a *App) DialogPickSavePath(defaultName string) (string, error) {
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{DefaultFilename: defaultName})
	if err != nil {
		return "", bindError(err)
	}
	return path, nil
}
