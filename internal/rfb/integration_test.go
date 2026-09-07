// Integration tests against the real macOS Screen Sharing server on
// 127.0.0.1:5900 (dev machine, see AGENTS.md). Self-skips when the port is
// unreachable. Mirrors the integration block of tests/vncBridge.test.ts.
package rfb_test

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"deskmux/internal/rfb"
)

func screenSharingReachable() bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:5900", time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func TestIntegrationScreenSharing(t *testing.T) {
	if !screenSharingReachable() {
		t.Skip("macOS Screen Sharing not reachable on 127.0.0.1:5900")
	}
	// macOS Screen Sharing stalls new handshakes while it verifies credentials
	// for another connection (~1s for a failed auth). Stagger in case other
	// tests probe the same port concurrently.
	time.Sleep(1500 * time.Millisecond)

	dial5900 := func(t *testing.T) net.Conn {
		t.Helper()
		conn, err := net.DialTimeout("tcp", "127.0.0.1:5900", 5*time.Second)
		if err != nil {
			t.Fatalf("dial 5900: %v", err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}

	t.Run("offers the Apple DH security type 30", func(t *testing.T) {
		conn := dial5900(t)
		version, types, err := rfb.ProbeSecurityTypes(conn, 5*time.Second)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if !strings.HasPrefix(version, "003.") {
			t.Errorf("server version = %q", version)
		}
		found := false
		for _, typ := range types {
			if typ == 30 {
				found = true
			}
		}
		if !found {
			t.Errorf("security types %v do not contain 30", types)
		}
	})

	// The probe above abandons the connection mid-handshake; Screen Sharing
	// takes a moment to release that session and serializes new handshakes.
	time.Sleep(1500 * time.Millisecond)

	t.Run("wrong credentials yield a clean RFB auth failure", func(t *testing.T) {
		conn := dial5900(t)
		_, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{
			Username: "deskmux-probe",
			Password: "definitely-wrong-password",
			// Screen Sharing stalls while verifying credentials (~1s, more
			// when another handshake is still being released).
			Timeout: 15 * time.Second,
		})
		var authErr *rfb.RfbAuthError
		var protocolErr *rfb.RfbProtocolError
		var timeoutErr *rfb.RfbTimeoutError
		if !errors.As(err, &authErr) {
			t.Fatalf("expected RfbAuthError, got %T: %v", err, err)
		}
		if errors.As(err, &protocolErr) || errors.As(err, &timeoutErr) {
			t.Fatalf("auth error must not be protocol/timeout: %T", err)
		}
		if authErr.Message == "" {
			t.Error("empty auth error message")
		}
	})
}
