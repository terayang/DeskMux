package sshx

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Healthy probes must not disturb the session, and CloseSession must stop
// the keepalive goroutine (no leak).
func TestKeepaliveStopsOnCloseSession(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)
	m.keepaliveInterval = 10 * time.Millisecond

	id, err := m.CreateSession(passwordAuth(server.port))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Several probe rounds against the live server: the mock replies
	// ok=false to keepalive@openssh.com, which must not count as failure.
	time.Sleep(10 * m.keepaliveInterval)
	if _, err := m.requireSession(id); err != nil {
		t.Fatalf("session killed by healthy keepalive probes: %v", err)
	}

	m.mu.Lock()
	sess := m.sessions[id]
	m.mu.Unlock()
	if err := m.CloseSession(id); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	exited := make(chan struct{})
	go func() {
		sess.wg.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("keepalive goroutine still running after CloseSession")
	}
}

// keepaliveMaxFailures consecutive probe failures must close the connection
// through the normal teardown path (client closed, done signaled).
func TestKeepaliveFailureThresholdClosesConnection(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)
	m.keepaliveInterval = 10 * time.Millisecond

	client, err := ssh.Dial("tcp",
		net.JoinHostPort("127.0.0.1", fmt.Sprint(server.port)),
		&ssh.ClientConfig{
			User:            mockUsername,
			Auth:            []ssh.AuthMethod{ssh.Password(mockPassword)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         5 * time.Second,
		})
	if err != nil {
		t.Fatalf("ssh.Dial: %v", err)
	}

	sess := &session{
		client:        client,
		done:          make(chan struct{}),
		sendKeepalive: func() error { return errors.New("probe failed") },
	}
	go func() {
		_ = client.Wait()
		sess.signalDone()
	}()
	go m.keepaliveLoop(sess)

	waitFor(t, "connection closed after repeated keepalive failures", func() bool {
		select {
		case <-sess.done:
			return true
		default:
			return false
		}
	})
	if _, _, err := client.SendRequest(keepaliveRequestType, true, nil); err == nil {
		t.Fatal("expected the client to be closed after keepalive failure threshold")
	}
}

// When the server drops the connection, the session must disappear from the
// manager and the shell's onClose must fire, so the frontend sees the
// disconnect instead of a zombie session.
func TestServerDropRemovesSessionAndFiresOnClose(t *testing.T) {
	server := startMockSSHServer(t)
	m := newTestManager(t, 5*time.Second)
	m.keepaliveInterval = 10 * time.Millisecond

	id, err := m.CreateSession(passwordAuth(server.port))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	collector := &shellCollector{}
	if err := m.OpenShell(id, 80, 24, collector.onData, collector.onClose); err != nil {
		t.Fatalf("OpenShell: %v", err)
	}

	server.dropConnections()

	waitFor(t, "session removed after connection drop", func() bool {
		var sshErr *SshError
		err := m.WriteToShell(id, "x")
		return errors.As(err, &sshErr) && sshErr.Code == ErrSessionNotFound
	})
	waitFor(t, "onClose after connection drop", collector.isClosed)
}
