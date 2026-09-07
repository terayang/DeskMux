// Tests for the saved-connection store, using an injected in-memory
// SecretStore (the production keychain implementation is not exercised here —
// macOS Keychain access is covered by manual UAT).
package store_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"deskmux/internal/store"
)

// memorySecrets is the in-memory SecretStore injected into the store; the
// fail* switches simulate keychain outages.
type memorySecrets struct {
	values     map[string]string
	failSet    bool
	failGet    bool
	failDelete bool
}

func newMemorySecrets() *memorySecrets {
	return &memorySecrets{values: map[string]string{}}
}

func (m *memorySecrets) Set(key, value string) error {
	if m.failSet {
		return errors.New("injected keychain set failure")
	}
	m.values[key] = value
	return nil
}

func (m *memorySecrets) Get(key string) (string, error) {
	if m.failGet {
		return "", errors.New("injected keychain get failure")
	}
	value, ok := m.values[key]
	if !ok {
		return "", store.ErrSecretNotFound
	}
	return value, nil
}

func (m *memorySecrets) Delete(key string) error {
	if m.failDelete {
		return errors.New("injected keychain delete failure")
	}
	if _, ok := m.values[key]; !ok {
		return store.ErrSecretNotFound
	}
	delete(m.values, key)
	return nil
}

func newTestStore(t *testing.T, secrets store.SecretStore) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	return store.New(dir, secrets), dir
}

func requireStoreError(t *testing.T, err error, code store.ErrorCode) {
	t.Helper()
	var storeErr *store.StoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("expected *store.StoreError with code %s, got %v", code, err)
	}
	if storeErr.Code != code {
		t.Fatalf("expected code %s, got %s (%s)", code, storeErr.Code, storeErr.Message)
	}
}

func sampleInput() store.Input {
	return store.Input{
		Name:      "Mac Studio",
		Host:      "100.103.82.44",
		Protocols: []string{"ssh", "vnc"},
		Username:  "admin",
		Secret:    &store.Secret{Kind: "password", Data: "s3cret"},
	}
}

// Save (new) -> List shows the summary without a secret; Get returns the
// connection with its decrypted secret; the file holds no secret material.
func TestSaveNewListAndGet(t *testing.T) {
	secrets := newMemorySecrets()
	s, dir := newTestStore(t, secrets)

	if got := s.List(); len(got) != 0 {
		t.Fatalf("fresh store: expected empty list, got %d entries", len(got))
	}

	summary, err := s.Save(sampleInput())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if summary.ID == "" {
		t.Fatal("Save: expected an assigned id")
	}

	list := s.List()
	if len(list) != 1 || !reflect.DeepEqual(list[0], summary) {
		t.Fatalf("List: expected [%+v], got %+v", summary, list)
	}

	conn, err := s.Get(summary.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if conn.Name != "Mac Studio" || conn.Host != "100.103.82.44" || conn.Username != "admin" {
		t.Fatalf("Get: unexpected connection %+v", conn)
	}
	if len(conn.Protocols) != 2 || conn.Protocols[0] != "ssh" || conn.Protocols[1] != "vnc" {
		t.Fatalf("Get: unexpected protocols %+v", conn.Protocols)
	}
	if conn.Secret == nil || conn.Secret.Kind != "password" || conn.Secret.Data != "s3cret" {
		t.Fatalf("Get: expected decrypted secret, got %+v", conn.Secret)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "connections.json"))
	if err != nil {
		t.Fatalf("read connections.json: %v", err)
	}
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "secret") {
		t.Fatalf("connections.json must not contain secret material, got %s", raw)
	}
	info, err := os.Stat(filepath.Join(dir, "connections.json"))
	if err != nil {
		t.Fatalf("stat connections.json: %v", err)
	}
	// Windows has no Unix permission bits (Chmod only honors the read-only
	// flag), so the 0600 guarantee is only verifiable elsewhere.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("connections.json mode: expected 0600, got %o", info.Mode().Perm())
	}
}

// Save (update): same id updates metadata; a nil secret preserves the stored
// one; an explicitly empty secret clears it; a new secret replaces it.
func TestSaveUpdateSecretSemantics(t *testing.T) {
	secrets := newMemorySecrets()
	s, _ := newTestStore(t, secrets)

	created, err := s.Save(sampleInput())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Update with nil secret: metadata changes, secret is preserved.
	updated, err := s.Save(store.Input{
		ID:        created.ID,
		Name:      "renamed",
		Host:      "100.103.82.44",
		Protocols: []string{"ssh"},
		Username:  "root",
	})
	if err != nil {
		t.Fatalf("Save (nil secret): %v", err)
	}
	if updated.Name != "renamed" || updated.Username != "root" {
		t.Fatalf("Save (nil secret): unexpected summary %+v", updated)
	}
	conn, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if conn.Secret == nil || conn.Secret.Data != "s3cret" {
		t.Fatalf("nil secret must preserve the stored one, got %+v", conn.Secret)
	}

	// Update with a new secret: replaces the stored one.
	if _, err := s.Save(store.Input{
		ID:        created.ID,
		Name:      "renamed",
		Host:      "100.103.82.44",
		Protocols: []string{"ssh"},
		Username:  "root",
		Secret:    &store.Secret{Kind: "privateKeyPath", Data: "/keys/id_ed25519"},
	}); err != nil {
		t.Fatalf("Save (new secret): %v", err)
	}
	conn, err = s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after secret replace: %v", err)
	}
	if conn.Secret == nil || conn.Secret.Kind != "privateKeyPath" || conn.Secret.Data != "/keys/id_ed25519" {
		t.Fatalf("expected replaced secret, got %+v", conn.Secret)
	}

	// Update with an explicitly empty secret: clears the stored one.
	if _, err := s.Save(store.Input{
		ID:        created.ID,
		Name:      "renamed",
		Host:      "100.103.82.44",
		Protocols: []string{"ssh"},
		Username:  "root",
		Secret:    &store.Secret{Kind: "password", Data: ""},
	}); err != nil {
		t.Fatalf("Save (empty secret): %v", err)
	}
	conn, err = s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after secret clear: %v", err)
	}
	if conn.Secret != nil {
		t.Fatalf("explicitly empty secret must clear the stored one, got %+v", conn.Secret)
	}
	if _, err := secrets.Get("conn:" + created.ID); !errors.Is(err, store.ErrSecretNotFound) {
		t.Fatalf("keychain entry must be gone, got %v", err)
	}

	// Updates never duplicate the entry.
	if list := s.List(); len(list) != 1 {
		t.Fatalf("expected 1 entry after updates, got %d", len(list))
	}
}

// Delete removes the connection and its keychain entry; unknown ids are
// idempotent no-ops.
func TestDelete(t *testing.T) {
	secrets := newMemorySecrets()
	s, _ := newTestStore(t, secrets)

	first, err := s.Save(sampleInput())
	if err != nil {
		t.Fatalf("Save first: %v", err)
	}
	secondIn := sampleInput()
	secondIn.Name = "relay"
	secondIn.Secret = nil
	second, err := s.Save(secondIn)
	if err != nil {
		t.Fatalf("Save second: %v", err)
	}

	if err := s.Delete(first.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list := s.List()
	if len(list) != 1 || list[0].ID != second.ID {
		t.Fatalf("expected only the second entry, got %+v", list)
	}
	_, err = s.Get(first.ID)
	requireStoreError(t, err, store.ErrNotFound)
	if _, err := secrets.Get("conn:" + first.ID); !errors.Is(err, store.ErrSecretNotFound) {
		t.Fatalf("keychain entry must be deleted, got %v", err)
	}

	// Idempotent: deleting again (or a never-known id) succeeds.
	if err := s.Delete(first.ID); err != nil {
		t.Fatalf("Delete (repeat): %v", err)
	}
	if err := s.Delete("never-existed"); err != nil {
		t.Fatalf("Delete (unknown id): %v", err)
	}
}

// Persistence: a store reopened on the same dataDir sees the same data; the
// secret comes from the SecretStore, and a lost keychain entry degrades to a
// secret-less connection instead of an error.
func TestPersistenceAcrossReopen(t *testing.T) {
	secrets := newMemorySecrets()
	dir := t.TempDir()

	first := store.New(dir, secrets)
	summary, err := first.Save(sampleInput())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened := store.New(dir, secrets)
	list := reopened.List()
	if len(list) != 1 || !reflect.DeepEqual(list[0], summary) {
		t.Fatalf("reopened store: expected [%+v], got %+v", summary, list)
	}
	conn, err := reopened.Get(summary.ID)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if conn.Secret == nil || conn.Secret.Data != "s3cret" {
		t.Fatalf("secret must survive a reopen via the SecretStore, got %+v", conn.Secret)
	}

	// Lost keychain entry: Get still succeeds, without a secret.
	delete(secrets.values, "conn:"+summary.ID)
	conn, err = reopened.Get(summary.ID)
	if err != nil {
		t.Fatalf("Get with lost keychain entry: %v", err)
	}
	if conn.Secret != nil {
		t.Fatalf("expected no secret after keychain loss, got %+v", conn.Secret)
	}
}

// Legacy formats: an unreadable file or the Electron-era shape (per-entry
// safeStorage "secret" field) is renamed to connections.json.bak and the
// store starts empty.
func TestLegacyFormatMigration(t *testing.T) {
	cases := map[string]string{
		"electron safeStorage format": `[{"id":"abc","name":"old","host":"h","protocols":["ssh"],"username":"u","secret":{"kind":"password","data":"YmFzZTY0LW1vY2s="}}]`,
		"corrupt JSON":                `{not json`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			secrets := newMemorySecrets()
			dir := t.TempDir()
			file := filepath.Join(dir, "connections.json")
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatalf("seed legacy file: %v", err)
			}

			s := store.New(dir, secrets)
			if got := s.List(); len(got) != 0 {
				t.Fatalf("legacy store must start empty, got %+v", got)
			}
			if _, err := os.Stat(file); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("legacy file must be moved aside, stat: %v", err)
			}
			backup, err := os.ReadFile(file + ".bak")
			if err != nil {
				t.Fatalf("expected connections.json.bak: %v", err)
			}
			if string(backup) != content {
				t.Fatalf("backup content mismatch: %q", backup)
			}

			// The store stays usable: a fresh save recreates connections.json.
			if _, err := s.Save(sampleInput()); err != nil {
				t.Fatalf("Save after migration: %v", err)
			}
			if got := s.List(); len(got) != 1 {
				t.Fatalf("expected 1 entry after save, got %+v", got)
			}
		})
	}
}

// Keychain failures: Save with a failing SecretStore reports
// ENCRYPTION_UNAVAILABLE and never writes the connection (nor any plaintext)
// to disk; a failing read reports the same code.
func TestKeyringFailures(t *testing.T) {
	secrets := newMemorySecrets()
	s, dir := newTestStore(t, secrets)

	secrets.failSet = true
	_, err := s.Save(sampleInput())
	requireStoreError(t, err, store.ErrEncryptionUnavailable)
	if _, statErr := os.Stat(filepath.Join(dir, "connections.json")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("failed keychain write must not persist the connection, stat: %v", statErr)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("failed save must not be visible in List, got %+v", got)
	}

	// Recovery: the save works once the keychain does.
	secrets.failSet = false
	summary, err := s.Save(sampleInput())
	if err != nil {
		t.Fatalf("Save after keychain recovery: %v", err)
	}

	secrets.failGet = true
	_, err = s.Get(summary.ID)
	requireStoreError(t, err, store.ErrEncryptionUnavailable)
}
