// Saved-connection store (M5): persists connection bookmarks to
// <dataDir>/connections.json with each secret kept behind a SecretStore
// abstraction — the OS keychain by default (macOS Keychain / Windows
// Credential Manager / Secret Service), or the encrypted local file
// (FileSecrets) when the user switches the secret-storage mode (settings.go)
// — the file never contains a secret in any form.
//
// The JSON shapes mirror the shared TS types (SavedConnection /
// SavedConnectionSummary / SavedConnectionInput in frontend/shared/ipc.ts) so
// results cross the Wails bridge unchanged. The store replaces both the
// retired Electron safeStorage store (src/main/store.ts) and the interim
// in-memory bridge implementation.
//
// Secret lifecycle on Save: an omitted secret (nil) preserves the existing
// keychain entry, an explicitly empty secret (non-nil, empty Data) clears it,
// anything else replaces it. A keychain write failure aborts the save with
// code ENCRYPTION_UNAVAILABLE — the store never falls back to persisting a
// plaintext secret.
package store

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/zalando/go-keyring"
)

// ErrorCode classifies store failures so the UI can react to them (mirrors
// the code-carrying SshError convention in internal/sshx).
type ErrorCode string

const (
	// ErrNotFound: no saved connection with the requested id.
	ErrNotFound ErrorCode = "NOT_FOUND"
	// ErrEncryptionUnavailable: the OS keychain rejected a secret operation;
	// the store refuses to degrade to plaintext persistence.
	ErrEncryptionUnavailable ErrorCode = "ENCRYPTION_UNAVAILABLE"
)

// StoreError carries a machine-readable ErrorCode alongside the message.
type StoreError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (e *StoreError) Error() string { return e.Message }

// SecretStore abstracts the OS keychain so tests can inject an in-memory
// implementation. Get and Delete report a missing entry by returning an error
// matching ErrSecretNotFound.
type SecretStore interface {
	Set(key, value string) error
	Get(key string) (string, error)
	Delete(key string) error
}

// ErrSecretNotFound is the SecretStore-level "no such entry" signal.
var ErrSecretNotFound = errors.New("store: secret not found")

// secretKey namespaces one connection's keychain entry.
func secretKey(id string) string { return "conn:" + id }

// keyringService groups every keychain entry under one service name.
// "AnyRemote" is a legacy identifier kept for user-data continuity
// (config dir / keychain entries predate the DeskMux rename).
const keyringService = "AnyRemote"

// keyringSecrets is the production SecretStore backed by zalando/go-keyring
// (on macOS it shells out to /usr/bin/security, no cgo).
type keyringSecrets struct{}

func (keyringSecrets) Set(key, value string) error {
	return keyring.Set(keyringService, key, value)
}

func (keyringSecrets) Get(key string) (string, error) {
	value, err := keyring.Get(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrSecretNotFound
	}
	return value, err
}

func (keyringSecrets) Delete(key string) error {
	err := keyring.Delete(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrSecretNotFound
	}
	return err
}

// KeyringSecrets returns the production SecretStore backed by the OS keychain.
func KeyringSecrets() SecretStore { return keyringSecrets{} }

// Secret is the plaintext credential as it crosses the bridge: kind is
// "password" or "privateKeyPath" (SavedSecretKind in frontend/shared/ipc.ts).
// Only the keychain holds it at rest, as one JSON document per connection.
type Secret struct {
	Kind string `json:"kind"`
	Data string `json:"data"`
}

// Connection is one saved connection; Secret is populated only by Get.
type Connection struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Host      string   `json:"host"`
	Protocols []string `json:"protocols"`
	Username  string   `json:"username"`
	Secret    *Secret  `json:"secret,omitempty"`
}

// Summary is a connection without its secret, as returned by List.
type Summary struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Host      string   `json:"host"`
	Protocols []string `json:"protocols"`
	Username  string   `json:"username"`
}

// Input is the Save payload: a new connection (empty ID — the store assigns a
// random UUID) or an update (existing ID). Secret follows the lifecycle
// documented on the package: nil preserves, empty clears, otherwise replaces.
type Input struct {
	ID        string   `json:"id,omitempty"`
	Name      string   `json:"name"`
	Host      string   `json:"host"`
	Protocols []string `json:"protocols"`
	Username  string   `json:"username"`
	Secret    *Secret  `json:"secret,omitempty"`
}

// Store is the saved-connection repository: bookmark metadata on disk,
// secrets in the active SecretStore backend. Safe for concurrent use.
type Store struct {
	mu      sync.Mutex
	file    string
	secrets SecretStore
	// mode names the backend secrets currently points at (keychain for stores
	// opened with New); MigrateSecrets keeps it in sync on a hot swap.
	mode SecretBackend
	// connections holds the bookmark metadata in file order; the Secret field
	// of every entry is always nil (secrets never touch this slice).
	connections []Connection
}

// New opens the store rooted at dataDir (production: os.UserConfigDir()/
// AnyRemote — legacy identifier kept for user-data continuity, predating the
// DeskMux rename), loading any existing connections.json. A missing file
// means an
// empty store; an unreadable or legacy-format file is set aside (see load).
// secrets must be non-nil (KeyringSecrets in production, in-memory in tests).
// The store reports the keychain mode; NewWithMode picks the backend from
// the persisted setting instead.
func New(dataDir string, secrets SecretStore) *Store {
	s := &Store{
		file:    filepath.Join(dataDir, "connections.json"),
		secrets: secrets,
		mode:    SecretBackendKeychain,
	}
	s.load()
	return s
}

// diskEntry mirrors one persisted record. The Electron-era format embedded an
// additional "secret" field (base64 safeStorage ciphertext this build cannot
// decrypt); load detects it through this field.
type diskEntry struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Host      string          `json:"host"`
	Protocols []string        `json:"protocols"`
	Username  string          `json:"username"`
	Secret    json.RawMessage `json:"secret"`
}

// load reads connections.json into memory. A file that fails to parse — or
// that parses but carries the Electron-era per-entry "secret" field — is
// renamed to connections.json.bak (best effort) and the store starts empty,
// so one undecryptable legacy file never blocks startup.
func (s *Store) load() {
	s.connections = []Connection{}
	raw, err := os.ReadFile(s.file)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	legacy := true
	if err == nil {
		var entries []diskEntry
		if err = json.Unmarshal(raw, &entries); err == nil {
			legacy = false
			s.connections = make([]Connection, 0, len(entries))
			for _, entry := range entries {
				if len(entry.Secret) > 0 {
					legacy = true
					break
				}
				s.connections = append(s.connections, Connection{
					ID:        entry.ID,
					Name:      entry.Name,
					Host:      entry.Host,
					Protocols: normalizeProtocols(entry.Protocols),
					Username:  entry.Username,
				})
			}
		}
	}
	if legacy {
		// Corrupt JSON or the Electron safeStorage format: move the file aside
		// and start from an empty set. The rename is best effort — if it fails
		// (e.g. permissions) the store still starts empty and the next Save
		// rewrites the file.
		_ = os.Rename(s.file, s.file+".bak")
		s.connections = []Connection{}
	}
}

// persistLocked writes the store atomically (tmp file + rename, mode 0600) so
// a crash cannot truncate it. The caller holds s.mu.
func (s *Store) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.file), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.connections, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	// WriteFile ignores the perm bits when the tmp file already exists.
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.file)
}

// summaryOf strips the secret for list results.
func summaryOf(conn Connection) Summary {
	return Summary{
		ID:        conn.ID,
		Name:      conn.Name,
		Host:      conn.Host,
		Protocols: conn.Protocols,
		Username:  conn.Username,
	}
}

// normalizeProtocols keeps the JSON shape `[]` (never null) and detaches the
// slice from the caller's.
func normalizeProtocols(protocols []string) []string {
	out := make([]string, len(protocols))
	copy(out, protocols)
	return out
}

// findLocked locates one connection by id; -1 when unknown. Caller holds s.mu.
func (s *Store) findLocked(id string) int {
	for i, conn := range s.connections {
		if conn.ID == id {
			return i
		}
	}
	return -1
}

// deleteSecretLocked removes the keychain entry; a missing entry is not an
// error. Caller holds s.mu.
func (s *Store) deleteSecretLocked(id string) error {
	err := s.secrets.Delete(secretKey(id))
	if err != nil && !errors.Is(err, ErrSecretNotFound) {
		return &StoreError{
			Code:    ErrEncryptionUnavailable,
			Message: "Failed to remove the stored secret: " + err.Error(),
		}
	}
	return nil
}

// List returns every saved connection without secrets, in insertion order.
func (s *Store) List() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	summaries := make([]Summary, 0, len(s.connections))
	for _, conn := range s.connections {
		summaries = append(summaries, summaryOf(conn))
	}
	return summaries
}

// Get returns one connection with its decrypted secret (for establishing a
// session). An unknown id fails with NOT_FOUND. A missing keychain entry
// (e.g. the keychain was cleared) degrades to a connection without a secret
// rather than an error; a keychain that itself fails reports
// ENCRYPTION_UNAVAILABLE.
func (s *Store) Get(id string) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := s.findLocked(id)
	if at == -1 {
		return Connection{}, &StoreError{
			Code:    ErrNotFound,
			Message: fmt.Sprintf("No saved connection with id %q", id),
		}
	}
	conn := s.connections[at]
	conn.Protocols = normalizeProtocols(conn.Protocols)
	value, err := s.secrets.Get(secretKey(id))
	switch {
	case errors.Is(err, ErrSecretNotFound):
		return conn, nil
	case err != nil:
		return Connection{}, &StoreError{
			Code:    ErrEncryptionUnavailable,
			Message: "Failed to read the stored secret: " + err.Error(),
		}
	}
	var secret Secret
	if err := json.Unmarshal([]byte(value), &secret); err != nil {
		return Connection{}, &StoreError{
			Code:    ErrEncryptionUnavailable,
			Message: "The stored secret is unreadable: " + err.Error(),
		}
	}
	conn.Secret = &secret
	return conn, nil
}

// Save creates (empty ID) or updates (existing ID) one connection and returns
// its summary. The keychain write happens before the file so a keychain
// failure (ENCRYPTION_UNAVAILABLE) never leaves a persisted entry behind —
// and the file never carries a plaintext secret.
func (s *Store) Save(in Input) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := in.ID
	if id == "" {
		id = newUUID()
	}
	switch {
	case in.Secret == nil:
		// Update omitting the secret: keep the existing keychain entry.
	case in.Secret.Data == "":
		// Explicitly empty secret: clear the stored one.
		if err := s.deleteSecretLocked(id); err != nil {
			return Summary{}, err
		}
	default:
		value, err := json.Marshal(in.Secret)
		if err != nil {
			return Summary{}, err
		}
		if err := s.secrets.Set(secretKey(id), string(value)); err != nil {
			return Summary{}, &StoreError{
				Code:    ErrEncryptionUnavailable,
				Message: "Failed to store the secret in the OS keychain: " + err.Error(),
			}
		}
	}
	record := Connection{
		ID:        id,
		Name:      in.Name,
		Host:      in.Host,
		Protocols: normalizeProtocols(in.Protocols),
		Username:  in.Username,
	}
	if at := s.findLocked(id); at == -1 {
		s.connections = append(s.connections, record)
	} else {
		s.connections[at] = record
	}
	if err := s.persistLocked(); err != nil {
		return Summary{}, err
	}
	return summaryOf(record), nil
}

// Delete removes one connection and its keychain entry; an unknown id is a
// no-op (idempotent).
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := s.findLocked(id)
	if at == -1 {
		return nil
	}
	s.connections = append(s.connections[:at], s.connections[at+1:]...)
	if err := s.persistLocked(); err != nil {
		return err
	}
	return s.deleteSecretLocked(id)
}

// Mode returns the secret-storage backend the store currently runs on.
func (s *Store) Mode() SecretBackend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

// MigrateSecrets switches the store to newBackend: every stored secret is
// read from the current backend, written to the new one, then removed from
// the old one; on success the choice persists to settings.json and the store
// hot-swaps to the new backend (no restart). Entries without a stored secret
// are skipped. The copy is best effort, not transactional: the first failing
// entry aborts the migration with an ENCRYPTION_UNAVAILABLE error naming the
// connection, the store keeps running on the previous backend, and settings
// stay unchanged — secrets already copied remain readable from the previous
// backend too (their old entries are only deleted after a successful copy),
// so a retry simply overwrites them.
func (s *Store) MigrateSecrets(newMode SecretBackend, newBackend SecretStore) error {
	if !ValidSecretBackend(newMode) {
		return fmt.Errorf("store: unknown secret storage mode %q", newMode)
	}
	if newBackend == nil {
		return errors.New("store: cannot migrate to a nil secret backend")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, conn := range s.connections {
		key := secretKey(conn.ID)
		value, err := s.secrets.Get(key)
		if errors.Is(err, ErrSecretNotFound) {
			continue
		}
		if err != nil {
			return &StoreError{
				Code:    ErrEncryptionUnavailable,
				Message: fmt.Sprintf("Failed to read the secret of %q from the current backend: %s", conn.Name, err),
			}
		}
		if err := newBackend.Set(key, value); err != nil {
			return &StoreError{
				Code:    ErrEncryptionUnavailable,
				Message: fmt.Sprintf("Failed to write the secret of %q to the new backend: %s", conn.Name, err),
			}
		}
		if err := s.secrets.Delete(key); err != nil && !errors.Is(err, ErrSecretNotFound) {
			return &StoreError{
				Code:    ErrEncryptionUnavailable,
				Message: fmt.Sprintf("The secret of %q was copied but could not be removed from the previous backend: %s", conn.Name, err),
			}
		}
	}
	if err := SaveSecretBackend(filepath.Dir(s.file), newMode); err != nil {
		return err
	}
	s.secrets = newBackend
	s.mode = newMode
	return nil
}

// newUUID returns a random RFC 4122 version 4 UUID, like the TS randomUUID().
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("store: crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
