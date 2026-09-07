// FileSecrets: a SecretStore that keeps every secret in one encrypted file
// (<configDir>/secrets.json) instead of the OS keychain, selectable through
// the "localFile" secret-storage mode (see settings.go).
//
// Security semantics — obfuscation grade, NOT keychain-grade, and the UI says
// so: the AES-256 key is derived as SHA-256(machineUUID || salt) from the
// hardware UUID (macOS IOPlatformUUID, Windows MachineGuid), so the key never
// travels with the data and a copied secrets.json (another machine, a plain
// backup) cannot be decrypted. But any process running on the same machine
// can re-derive the key, so this backend only defeats casual plaintext
// reading — it is not a defense against a local attacker. When the machine
// UUID cannot be obtained, a random 256-bit key is generated once and kept in
// <configDir>/secrets.key (0600); that file then IS the key and must be
// protected like the data itself.
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// keyDerivationSalt domains the FileSecrets key so a machine-UUID hash used
// anywhere else never collides with it. The "anyremote" tag is a legacy
// identifier kept for user-data continuity: it is part of the KDF input for
// every already-encrypted secrets.json, so it must never change.
const keyDerivationSalt = "|anyremote-secret-v1"

// FileSecrets implements SecretStore with AES-256-GCM sealed entries in a
// single JSON document: a map of secret key to base64(nonce || ciphertext).
// Writes are atomic (tmp file + rename, mode 0600). Safe for concurrent use.
type FileSecrets struct {
	mu      sync.Mutex
	file    string // <configDir>/secrets.json
	keyFile string // <configDir>/secrets.key (fallback random key)
	// machineID resolves the hardware UUID; injectable in tests. Production
	// value is machineUUID.
	machineID func() (string, error)
	// key caches the derived AES key so the (exec-backed) machineID lookup
	// runs once per process and a transient lookup failure cannot flip the
	// key mid-session.
	key []byte
}

// NewFileSecrets returns the local-file SecretStore rooted at configDir.
func NewFileSecrets(configDir string) *FileSecrets {
	return &FileSecrets{
		file:      filepath.Join(configDir, "secrets.json"),
		keyFile:   filepath.Join(configDir, "secrets.key"),
		machineID: machineUUID,
	}
}

// encryptionKeyLocked derives (once) and caches the AES-256 key. An existing
// secrets.key always wins — it only exists because the machine UUID was
// unavailable when the store was first used, and switching back to the UUID
// key would orphan every entry written since. Caller holds f.mu.
func (f *FileSecrets) encryptionKeyLocked() ([]byte, error) {
	if f.key != nil {
		return f.key, nil
	}
	key, err := deriveKey(f.machineID, f.keyFile)
	if err != nil {
		return nil, err
	}
	f.key = key
	return key, nil
}

// deriveKey resolves the AES-256 key: the persisted random fallback key when
// present, else the machine-UUID derivation, else a freshly generated random
// key persisted (0600) for reuse.
func deriveKey(machineID func() (string, error), keyFile string) ([]byte, error) {
	if raw, err := os.ReadFile(keyFile); err == nil {
		if len(raw) != 32 {
			return nil, fmt.Errorf("store: %s is corrupt (want 32 bytes, got %d)", keyFile, len(raw))
		}
		return raw, nil
	}
	if id, err := machineID(); err == nil && id != "" {
		sum := sha256.Sum256([]byte(id + keyDerivationSalt))
		return sum[:], nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		return nil, err
	}
	// WriteFile ignores the perm bits when the file already exists.
	if err := os.Chmod(keyFile, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// machineUUID returns the hardware UUID: IOPlatformUUID from IOKit on macOS,
// MachineGuid from the registry (queried via reg.exe, no cgo) on Windows.
// Other platforms have no source and fall back to the random key file.
func machineUUID() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err != nil {
			return "", err
		}
		// The property line looks like: "IOPlatformUUID" = "6C4A..."
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.Contains(line, "IOPlatformUUID") {
				continue
			}
			if at := strings.Index(line, `" = "`); at >= 0 {
				return strings.Trim(strings.TrimSpace(line[at+len(`" = "`):]), `"`), nil
			}
		}
		return "", errors.New("store: IOPlatformUUID not found in ioreg output")
	case "windows":
		out, err := exec.Command("reg", "query", `HKLM\SOFTWARE\Microsoft\Cryptography`, "/v", "MachineGuid").Output()
		if err != nil {
			return "", err
		}
		// The value line looks like: "    MachineGuid    REG_SZ    xxxx-..."
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[0] == "MachineGuid" {
				return fields[len(fields)-1], nil
			}
		}
		return "", errors.New("store: MachineGuid not found in reg query output")
	default:
		return "", errors.New("store: no machine UUID source on " + runtime.GOOS)
	}
}

// seal encrypts plaintext with AES-256-GCM; the random nonce prefixes the
// ciphertext so one entry is self-contained.
func seal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// open reverses seal.
func open(key, sealed []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("store: malformed ciphertext (shorter than the nonce)")
	}
	return gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// loadLocked reads secrets.json; a missing file means an empty entry set.
// Caller holds f.mu.
func (f *FileSecrets) loadLocked() (map[string]string, error) {
	entries := map[string]string{}
	raw, err := os.ReadFile(f.file)
	if errors.Is(err, fs.ErrNotExist) {
		return entries, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("store: %s is unreadable: %w", f.file, err)
	}
	return entries, nil
}

// persistLocked writes secrets.json atomically (tmp file + rename, mode
// 0600), the same convention as connections.json. Caller holds f.mu.
func (f *FileSecrets) persistLocked(entries map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(f.file), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.file)
}

// Set encrypts value and records it under key, replacing any previous entry.
func (f *FileSecrets) Set(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, err := f.loadLocked()
	if err != nil {
		return err
	}
	aesKey, err := f.encryptionKeyLocked()
	if err != nil {
		return err
	}
	sealed, err := seal(aesKey, []byte(value))
	if err != nil {
		return err
	}
	entries[key] = base64.StdEncoding.EncodeToString(sealed)
	return f.persistLocked(entries)
}

// Get decrypts the entry under key; a missing key reports ErrSecretNotFound
// and an undecryptable one (wrong machine, tampered file) reports a plain
// error the store surfaces as ENCRYPTION_UNAVAILABLE.
func (f *FileSecrets) Get(key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, err := f.loadLocked()
	if err != nil {
		return "", err
	}
	encoded, ok := entries[key]
	if !ok {
		return "", ErrSecretNotFound
	}
	aesKey, err := f.encryptionKeyLocked()
	if err != nil {
		return "", err
	}
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("store: malformed ciphertext for %q: %w", key, err)
	}
	plaintext, err := open(aesKey, sealed)
	if err != nil {
		return "", fmt.Errorf("store: cannot decrypt the entry for %q: %w", key, err)
	}
	return string(plaintext), nil
}

// Delete removes the entry under key; a missing key reports
// ErrSecretNotFound (the store treats that as a no-op).
func (f *FileSecrets) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, err := f.loadLocked()
	if err != nil {
		return err
	}
	if _, ok := entries[key]; !ok {
		return ErrSecretNotFound
	}
	delete(entries, key)
	return f.persistLocked(entries)
}
