// Connection keepalive: idle SSH sessions are silently dropped by NATs and
// server-side idle timeouts, so every session is kept warm at two layers.
// TCP keepalive (set on dial) keeps NAT/stateful-firewall mappings alive;
// an SSH-level "keepalive@openssh.com" global request per connection detects
// half-open connections that TCP keepalive alone is too slow to catch.
package sshx

import (
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// TCPKeepAlivePeriod is the OS TCP keepalive probe interval set on dial.
	TCPKeepAlivePeriod = 30 * time.Second
	// DefaultKeepaliveInterval is how often each session sends an SSH-level
	// keepalive probe.
	DefaultKeepaliveInterval = 30 * time.Second
	// keepaliveRequestType is the OpenSSH-style global request used as ping.
	keepaliveRequestType = "keepalive@openssh.com"
	// keepaliveMaxFailures is the number of consecutive failed probes after
	// which the connection is declared dead and the session is closed.
	keepaliveMaxFailures = 3
)

// keepaliveLoop pings the connection every keepaliveInterval until the
// session ends (sess.done closed by CloseSession/CloseAll/remote drop).
// After keepaliveMaxFailures consecutive probe failures the connection is
// declared dead: closing the client runs the normal teardown path — the Wait
// watcher removes the session and stops this loop, shell channels error out
// and fire onClose — so the frontend sees the disconnect instead of a
// zombie session.
func (m *Manager) keepaliveLoop(sess *session) {
	ticker := time.NewTicker(m.keepaliveInterval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-sess.done:
			return
		case <-ticker.C:
		}
		if err := sess.sendKeepalive(); err != nil {
			failures++
			if failures >= keepaliveMaxFailures {
				_ = sess.client.Close()
				return
			}
		} else {
			failures = 0
		}
	}
}

// newSession wraps an authenticated client. sendKeepalive defaults to a
// "keepalive@openssh.com" global request and is replaceable in tests. A
// request the server rejects (ok=false) still proves the peer is alive —
// only a transport error counts as a failure.
func newSession(client *ssh.Client) *session {
	sess := &session{client: client, done: make(chan struct{})}
	sess.sendKeepalive = func() error {
		_, _, err := client.SendRequest(keepaliveRequestType, true, nil)
		return err
	}
	return sess
}
