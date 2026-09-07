// SSH session manager: owns one *ssh.Client connection per session and
// exposes a single interactive shell per session on top of it. Ported from
// src/main/ssh/sshService.ts. The SFTP service (sftp.go) reuses the same
// connections.
//
// Connect-time failures return *SshError carrying a distinguishable code:
// ErrAuthFailed (credentials rejected), ErrTimeout (handshake timeout),
// ErrUnreachable (socket/DNS/protocol failure before the session was ready).
package sshx

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// DefaultReadyTimeout bounds the TCP connect plus SSH handshake/auth, like
// the TS service's readyTimeout of 10000 ms.
const DefaultReadyTimeout = 10 * time.Second

// Manager owns all live SSH sessions. It is safe for concurrent use:
// callbacks (onData/onClose) may fire from internal goroutines at any time
// until the shell/session is closed.
type Manager struct {
	mu                sync.Mutex
	sessions          map[string]*session
	readyTimeout      time.Duration
	keepaliveInterval time.Duration

	// HostKeyCallback verifies server host keys. When nil it defaults to
	// accepting any key, matching the TS service (ssh2 without a
	// hostVerifier). Set it to enforce known-hosts verification.
	HostKeyCallback ssh.HostKeyCallback
}

// NewManager returns a Manager with DefaultReadyTimeout.
func NewManager() *Manager {
	return newManager(DefaultReadyTimeout)
}

// newManager allows tests to shrink the ready timeout.
func newManager(readyTimeout time.Duration) *Manager {
	if readyTimeout <= 0 {
		readyTimeout = DefaultReadyTimeout
	}
	return &Manager{
		sessions:          make(map[string]*session),
		readyTimeout:      readyTimeout,
		keepaliveInterval: DefaultKeepaliveInterval,
	}
}

// session is one authenticated connection plus its (optional) single shell.
type session struct {
	client *ssh.Client

	// done is closed when the connection ends (CloseSession, CloseAll,
	// remote drop, or keepalive failure) and stops the keepalive goroutine.
	done     chan struct{}
	doneOnce sync.Once
	// wg tracks the keepalive goroutine so tests can assert it exits.
	wg sync.WaitGroup
	// sendKeepalive sends one application-level probe (see keepalive.go).
	sendKeepalive func() error

	mu    sync.Mutex
	shell *shell
}

// signalDone marks the connection as ended. Idempotent.
func (s *session) signalDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

// shell is the one interactive shell channel of a session.
type shell struct {
	channel ssh.Channel
	onData  func(string)
	onClose func()

	dataMu    sync.Mutex // serializes onData callbacks from read goroutines
	closeOnce sync.Once
}

// CreateSession opens one SSH connection (password or private-key auth) and
// returns its session id once the connection is authenticated and ready.
func (m *Manager) CreateSession(cfg AuthConfig) (string, error) {
	keyPEM, err := resolvePrivateKey(cfg)
	if err != nil {
		return "", err
	}

	var auth []ssh.AuthMethod
	if keyPEM != "" {
		signer, err := parsePrivateKey([]byte(keyPEM), cfg.Passphrase)
		if err != nil {
			return "", err
		}
		auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else {
		auth = []ssh.AuthMethod{ssh.Password(cfg.Password)}
	}

	hostKeyCallback := m.HostKeyCallback
	if hostKeyCallback == nil {
		hostKeyCallback = ssh.InsecureIgnoreHostKey()
	}
	sshConfig := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
		Timeout:         m.readyTimeout,
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: m.readyTimeout, KeepAlive: TCPKeepAlivePeriod}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return "", mapDialError(err)
	}
	// Bound the whole handshake/auth phase; x/crypto/ssh does not apply
	// ClientConfig.Timeout after the TCP connect.
	_ = conn.SetDeadline(time.Now().Add(m.readyTimeout))
	clientConn, chans, reqs, err := ssh.NewClientConn(conn, addr, sshConfig)
	_ = conn.SetDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return "", mapHandshakeError(err)
	}
	client := ssh.NewClient(clientConn, chans, reqs)

	sessionID := newSessionID()
	sess := newSession(client)
	m.mu.Lock()
	m.sessions[sessionID] = sess
	m.mu.Unlock()

	// A dropped connection removes its session, like the TS 'close' handler,
	// and stops the keepalive goroutine. This also runs when CloseSession,
	// CloseAll, or the keepalive loop closes the client.
	go func() {
		_ = client.Wait()
		sess.signalDone()
		m.mu.Lock()
		delete(m.sessions, sessionID)
		m.mu.Unlock()
	}()

	sess.wg.Add(1)
	go func() {
		defer sess.wg.Done()
		m.keepaliveLoop(sess)
	}()

	return sessionID, nil
}

// OpenShell opens the session's interactive shell with a pseudo-TTY. One
// shell per session; output (stdout and stderr merged) streams to onData;
// remote exit fires onClose. Both callbacks run on internal goroutines.
func (m *Manager) OpenShell(id string, cols, rows uint32, onData func(string), onClose func()) error {
	sess, err := m.requireSession(id)
	if err != nil {
		return err
	}

	// Hold the session lock across the channel open so concurrent OpenShell
	// calls cannot both claim the single shell slot.
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.shell != nil {
		return &SshError{Code: ErrRemoteError, Message: "A shell is already open for this session"}
	}

	channel, requests, err := sess.client.OpenChannel("session", nil)
	if err != nil {
		return &SshError{Code: ErrRemoteError, Message: "Failed to open shell: " + err.Error()}
	}
	go ssh.DiscardRequests(requests)

	pty := ptyRequest{Term: "xterm-256color", Cols: cols, Rows: rows}
	ok, err := channel.SendRequest("pty-req", true, ssh.Marshal(&pty))
	if err != nil || !ok {
		_ = channel.Close()
		return &SshError{Code: ErrRemoteError, Message: fmt.Sprintf("Failed to request pseudo-TTY: %v (ok=%v)", err, ok)}
	}
	ok, err = channel.SendRequest("shell", true, nil)
	if err != nil || !ok {
		_ = channel.Close()
		return &SshError{Code: ErrRemoteError, Message: fmt.Sprintf("Failed to start shell: %v (ok=%v)", err, ok)}
	}

	sh := &shell{channel: channel, onData: onData, onClose: onClose}
	sess.shell = sh

	go sh.readLoop(sess, channel)
	go sh.readStderr(channel)
	return nil
}

// WriteToShell writes raw input to the session's open shell. It fails when
// no shell is open.
func (m *Manager) WriteToShell(id, data string) error {
	sh, err := m.requireShell(id)
	if err != nil {
		return err
	}
	if _, err := sh.channel.Write([]byte(data)); err != nil {
		return &SshError{Code: ErrRemoteError, Message: "Failed to write to shell: " + err.Error()}
	}
	return nil
}

// ResizeShell resizes the session's pseudo-TTY. It fails when no shell is open.
func (m *Manager) ResizeShell(id string, cols, rows uint32) error {
	sh, err := m.requireShell(id)
	if err != nil {
		return err
	}
	req := windowChangeRequest{Cols: cols, Rows: rows}
	if _, err := sh.channel.SendRequest("window-change", false, ssh.Marshal(&req)); err != nil {
		return &SshError{Code: ErrRemoteError, Message: "Failed to resize shell: " + err.Error()}
	}
	return nil
}

// CloseSession closes the session and its connection. It fails with
// SESSION_NOT_FOUND on an unknown/closed id.
func (m *Manager) CloseSession(id string) error {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return &SshError{Code: ErrSessionNotFound, Message: "Unknown or closed session: " + id}
	}
	return sess.client.Close()
}

// CloseAll closes every open session. Idempotent.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	all := make([]*session, 0, len(m.sessions))
	for id, sess := range m.sessions {
		all = append(all, sess)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	for _, sess := range all {
		_ = sess.client.Close()
	}
}

// requireSession looks up a live session or fails with SESSION_NOT_FOUND.
func (m *Manager) requireSession(id string) (*session, error) {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return nil, &SshError{Code: ErrSessionNotFound, Message: "Unknown or closed session: " + id}
	}
	return sess, nil
}

// requireShell looks up the session's open shell.
func (m *Manager) requireShell(id string) (*shell, error) {
	sess, err := m.requireSession(id)
	if err != nil {
		return nil, err
	}
	sess.mu.Lock()
	sh := sess.shell
	sess.mu.Unlock()
	if sh == nil {
		return nil, &SshError{Code: ErrRemoteError, Message: "No open shell for this session"}
	}
	return sh, nil
}

// readLoop streams the shell's stdout to onData and fires onClose once the
// channel closes (remote exit or connection teardown).
func (sh *shell) readLoop(sess *session, channel ssh.Channel) {
	buf := make([]byte, 32*1024)
	for {
		n, err := channel.Read(buf)
		if n > 0 {
			sh.emit(string(buf[:n]))
		}
		if err != nil {
			break
		}
	}
	sess.mu.Lock()
	if sess.shell == sh {
		sess.shell = nil
	}
	sess.mu.Unlock()
	sh.closeOnce.Do(func() {
		if sh.onClose != nil {
			sh.onClose()
		}
	})
}

// readStderr merges the shell's stderr stream into onData.
func (sh *shell) readStderr(channel ssh.Channel) {
	stderr := channel.Stderr()
	buf := make([]byte, 32*1024)
	for {
		n, err := stderr.Read(buf)
		if n > 0 {
			sh.emit(string(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}

func (sh *shell) emit(data string) {
	if sh.onData == nil {
		return
	}
	sh.dataMu.Lock()
	defer sh.dataMu.Unlock()
	sh.onData(data)
}

// resolvePrivateKey returns the PEM/OpenSSH key content to authenticate
// with, or "" for password auth.
//
// Resolution order (matching the TS service):
//  1. PrivateKey holding actual key content (contains a BEGIN header) is
//     used as-is.
//  2. PrivateKey without a BEGIN header is a key FILE PATH — the renderer's
//     credential flow forwards paths through this field.
//  3. PrivateKeyPath is read from disk (a leading "~/" expands to the
//     user's home directory).
//
// An unreadable key file returns an UNREACHABLE SshError naming the path.
func resolvePrivateKey(cfg AuthConfig) (string, error) {
	path := cfg.PrivateKeyPath
	if cfg.PrivateKey != "" {
		if strings.Contains(cfg.PrivateKey, "-----BEGIN") {
			return cfg.PrivateKey, nil
		}
		path = cfg.PrivateKey
	}
	if path == "" {
		return "", nil
	}
	expanded := expandHome(path)
	content, err := os.ReadFile(expanded)
	if err != nil {
		return "", &SshError{
			Code:    ErrUnreachable,
			Message: fmt.Sprintf("Private key file is not readable: %s (%v)", path, err),
		}
	}
	return string(content), nil
}

func expandHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	switch {
	case path == "~":
		return home
	case strings.HasPrefix(path, "~/"):
		return filepath.Join(home, path[2:])
	default:
		return path
	}
}

// parsePrivateKey parses PEM/OpenSSH key content, using passphrase when the
// key is encrypted. Parse failures map to AUTH_FAILED (credentials problem),
// matching the TS service where ssh2 rejects bad key material at auth time.
func parsePrivateKey(pemBytes []byte, passphrase string) (ssh.Signer, error) {
	signer, err := ssh.ParsePrivateKey(pemBytes)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) && passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pemBytes, []byte(passphrase))
	}
	if err != nil {
		return nil, &SshError{Code: ErrAuthFailed, Message: "Failed to parse private key: " + err.Error()}
	}
	return signer, nil
}

// mapDialError classifies a TCP connect failure.
func mapDialError(err error) *SshError {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &SshError{Code: ErrTimeout, Message: "Connection timed out: " + err.Error()}
	}
	return &SshError{Code: ErrUnreachable, Message: "Host unreachable: " + err.Error()}
}

// mapHandshakeError classifies a handshake/auth failure. x/crypto/ssh wraps
// handshake errors with %v in some paths, so timeout detection falls back
// to string matching when errors.As cannot reach a net.Error.
func mapHandshakeError(err error) *SshError {
	msg := err.Error()
	if strings.Contains(msg, "unable to authenticate") {
		return &SshError{Code: ErrAuthFailed, Message: "Authentication failed: " + msg}
	}
	var netErr net.Error
	if (errors.As(err, &netErr) && netErr.Timeout()) ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "deadline exceeded") {
		return &SshError{Code: ErrTimeout, Message: "Connection timed out: " + msg}
	}
	return &SshError{Code: ErrUnreachable, Message: "Connection failed: " + msg}
}

// newSessionID returns a random RFC 4122 version 4 UUID, like the TS
// service's randomUUID().
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sshx: crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ptyRequest is the wire payload of a "pty-req" channel request.
type ptyRequest struct {
	Term   string
	Cols   uint32
	Rows   uint32
	Width  uint32
	Height uint32
	Modes  string
}

// windowChangeRequest is the wire payload of a "window-change" request.
type windowChangeRequest struct {
	Cols   uint32
	Rows   uint32
	Width  uint32
	Height uint32
}
