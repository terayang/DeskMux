package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// passwordAuth returns credentials accepted by the mock server.
func passwordAuth(port int) AuthConfig {
	return AuthConfig{
		Host:     "127.0.0.1",
		Port:     port,
		Username: mockUsername,
		Password: mockPassword,
	}
}

func newTestManager(t *testing.T, readyTimeout time.Duration) *Manager {
	t.Helper()
	m := newManager(readyTimeout)
	t.Cleanup(m.CloseAll)
	return m
}

// requireSshError asserts err is a *SshError carrying the expected code.
func requireSshError(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected SshError %s, got nil error", code)
	}
	var sshErr *SshError
	if !errors.As(err, &sshErr) {
		t.Fatalf("expected *SshError, got %T (%v)", err, err)
	}
	if sshErr.Code != code {
		t.Fatalf("expected code %s, got %s (%v)", code, sshErr.Code, err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("waitFor timed out: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// closedPort returns a loopback port that is guaranteed closed.
func closedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return port
}

// generateTestKeyPEM returns a fresh ed25519 private key in PKCS8 PEM form.
func generateTestKeyPEM(t *testing.T) []byte {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// shellCollector records shell callbacks from the service's goroutines.
type shellCollector struct {
	mu     sync.Mutex
	data   strings.Builder
	closed bool
}

func (c *shellCollector) onData(data string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.WriteString(data)
}

func (c *shellCollector) onClose() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

func (c *shellCollector) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data.String()
}

func (c *shellCollector) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func TestCreateSessionPasswordAuth(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	id, err := m.CreateSession(passwordAuth(server.port))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty session id")
	}
	if err := m.CloseSession(id); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
}

func TestCreateSessionPrivateKeyAuth(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	cfg := passwordAuth(server.port)
	cfg.Password = ""
	cfg.PrivateKey = string(generateTestKeyPEM(t))
	id, err := m.CreateSession(cfg)
	if err != nil {
		t.Fatalf("CreateSession with inline private key: %v", err)
	}
	if err := m.CloseSession(id); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
}

func TestCreateSessionPrivateKeyPath(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, generateTestKeyPEM(t), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	cfg := passwordAuth(server.port)
	cfg.Password = ""
	cfg.PrivateKeyPath = keyPath
	id, err := m.CreateSession(cfg)
	if err != nil {
		t.Fatalf("CreateSession with privateKeyPath: %v", err)
	}
	if err := m.CloseSession(id); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
}

// The TS renderer forwards key file paths through the privateKey field; a
// value without a BEGIN header must be treated as a path.
func TestCreateSessionPrivateKeyFieldAsPath(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, generateTestKeyPEM(t), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	cfg := passwordAuth(server.port)
	cfg.Password = ""
	cfg.PrivateKey = keyPath
	id, err := m.CreateSession(cfg)
	if err != nil {
		t.Fatalf("CreateSession with privateKey holding a path: %v", err)
	}
	if err := m.CloseSession(id); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
}

func TestCreateSessionUnreadableKeyPath(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	keyPath := filepath.Join(t.TempDir(), "no-such-key")
	cfg := passwordAuth(server.port)
	cfg.Password = ""
	cfg.PrivateKeyPath = keyPath

	_, err := m.CreateSession(cfg)
	requireSshError(t, err, ErrUnreachable)
	if !strings.Contains(err.Error(), keyPath) {
		t.Fatalf("error should name the key path %q, got: %v", keyPath, err)
	}
}

func TestCreateSessionAuthFailed(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	cfg := passwordAuth(server.port)
	cfg.Password = "wrong"
	_, err := m.CreateSession(cfg)
	requireSshError(t, err, ErrAuthFailed)
}

func TestCreateSessionUnreachable(t *testing.T) {
	m := newTestManager(t, 5*time.Second)

	_, err := m.CreateSession(passwordAuth(closedPort(t)))
	requireSshError(t, err, ErrUnreachable)
}

func TestCreateSessionTimeout(t *testing.T) {
	// Accepts connections but never speaks SSH.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var heldMu sync.Mutex
	var held []net.Conn
	t.Cleanup(func() {
		_ = listener.Close()
		heldMu.Lock()
		for _, conn := range held {
			_ = conn.Close()
		}
		heldMu.Unlock()
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			heldMu.Lock()
			held = append(held, conn)
			heldMu.Unlock()
		}
	}()

	m := newTestManager(t, 300*time.Millisecond)
	_, err = m.CreateSession(passwordAuth(listener.Addr().(*net.TCPAddr).Port))
	requireSshError(t, err, ErrTimeout)
}

func TestResolvePrivateKey(t *testing.T) {
	keyPEM := string(generateTestKeyPEM(t))
	if got, err := resolvePrivateKey(AuthConfig{PrivateKey: keyPEM}); err != nil || got != keyPEM {
		t.Fatalf("inline key content: got %q, %v", got, err)
	}
	if got, err := resolvePrivateKey(AuthConfig{Password: "x"}); err != nil || got != "" {
		t.Fatalf("no key configured: got %q, %v", got, err)
	}

	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, []byte(keyPEM), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	if got, err := resolvePrivateKey(AuthConfig{PrivateKeyPath: keyPath}); err != nil || got != keyPEM {
		t.Fatalf("privateKeyPath: got %q, %v", got, err)
	}
	// A path in the privateKey field wins over privateKeyPath (TS parity).
	if got, err := resolvePrivateKey(AuthConfig{PrivateKey: keyPath, PrivateKeyPath: "ignored"}); err != nil || got != keyPEM {
		t.Fatalf("privateKey as path: got %q, %v", got, err)
	}

	_, err := resolvePrivateKey(AuthConfig{PrivateKeyPath: filepath.Join(t.TempDir(), "missing")})
	requireSshError(t, err, ErrUnreachable)
}

func TestShellEchoRoundtrip(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	id, err := m.CreateSession(passwordAuth(server.port))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	collector := &shellCollector{}
	if err := m.OpenShell(id, 80, 24, collector.onData, collector.onClose); err != nil {
		t.Fatalf("OpenShell: %v", err)
	}
	if err := m.WriteToShell(id, "hello-deskmux"); err != nil {
		t.Fatalf("WriteToShell: %v", err)
	}
	waitFor(t, "echo of written data", func() bool {
		return strings.Contains(collector.String(), "hello-deskmux")
	})
	if collector.isClosed() {
		t.Fatal("shell closed unexpectedly")
	}
	if err := m.CloseSession(id); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
}

func TestShellResizeRecordedByServer(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	id, err := m.CreateSession(passwordAuth(server.port))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := m.OpenShell(id, 80, 24, func(string) {}, func() {}); err != nil {
		t.Fatalf("OpenShell: %v", err)
	}
	if err := m.ResizeShell(id, 120, 40); err != nil {
		t.Fatalf("ResizeShell: %v", err)
	}
	waitFor(t, "server to record 120x40", func() bool {
		for _, wc := range server.recordedWindowChanges() {
			if wc.cols == 120 && wc.rows == 40 {
				return true
			}
		}
		return false
	})
	if err := m.CloseSession(id); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
}

func TestShellExitTriggersOnClose(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	id, err := m.CreateSession(passwordAuth(server.port))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	collector := &shellCollector{}
	if err := m.OpenShell(id, 80, 24, collector.onData, collector.onClose); err != nil {
		t.Fatalf("OpenShell: %v", err)
	}
	if err := m.WriteToShell(id, "exit"); err != nil {
		t.Fatalf("WriteToShell: %v", err)
	}
	waitFor(t, "onClose after remote exit", collector.isClosed)

	// The shell slot is freed, so a new shell can be opened (TS parity).
	if err := m.OpenShell(id, 80, 24, func(string) {}, func() {}); err != nil {
		t.Fatalf("reopen shell after remote exit: %v", err)
	}
	if err := m.CloseSession(id); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
}

func TestOpenShellTwiceFails(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	id, err := m.CreateSession(passwordAuth(server.port))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := m.OpenShell(id, 80, 24, func(string) {}, func() {}); err != nil {
		t.Fatalf("OpenShell: %v", err)
	}
	err = m.OpenShell(id, 80, 24, func(string) {}, func() {})
	requireSshError(t, err, ErrRemoteError)
	if err := m.CloseSession(id); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
}

func TestShellOpsWithoutShell(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	id, err := m.CreateSession(passwordAuth(server.port))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	requireSshError(t, m.WriteToShell(id, "x"), ErrRemoteError)
	requireSshError(t, m.ResizeShell(id, 80, 24), ErrRemoteError)
	if err := m.CloseSession(id); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
}

func TestOperationsAfterCloseSession(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	id, err := m.CreateSession(passwordAuth(server.port))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := m.CloseSession(id); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	requireSshError(t, m.WriteToShell(id, "x"), ErrSessionNotFound)
	requireSshError(t, m.ResizeShell(id, 80, 24), ErrSessionNotFound)
	requireSshError(t, m.OpenShell(id, 80, 24, func(string) {}, func() {}), ErrSessionNotFound)
	// Closing twice fails loudly for a single id.
	requireSshError(t, m.CloseSession(id), ErrSessionNotFound)
}

func TestCloseAllIdempotent(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	a, err := m.CreateSession(passwordAuth(server.port))
	if err != nil {
		t.Fatalf("CreateSession a: %v", err)
	}
	b, err := m.CreateSession(passwordAuth(server.port))
	if err != nil {
		t.Fatalf("CreateSession b: %v", err)
	}
	if a == b {
		t.Fatal("expected distinct session ids")
	}
	m.CloseAll()
	m.CloseAll()

	// Everything is gone after CloseAll.
	requireSshError(t, m.WriteToShell(a, "x"), ErrSessionNotFound)
	requireSshError(t, m.CloseSession(b), ErrSessionNotFound)
}

// TestConcurrentManagerAccess hammers the session maps from goroutines; it
// is meant to be run with -race.
func TestConcurrentManagerAccess(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := m.CreateSession(passwordAuth(server.port))
			if err != nil {
				t.Errorf("CreateSession: %v", err)
				return
			}
			if i%2 == 0 {
				_ = m.WriteToShell(id, "x") // no shell open: REMOTE_ERROR, fine
				_ = m.CloseSession(id)
			} else {
				_, _ = m.List(id, "/")
			}
		}(i)
	}
	wg.Wait()
	m.CloseAll()
	m.CloseAll()
}
