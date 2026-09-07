// In-process mock SSH server for sshx tests, built on x/crypto/ssh's server
// side. No real credentials or keys: the host key is a fresh throwaway RSA
// pair generated per server instance, and the only accepted login is
// test/secret (publickey is accepted for the same test user regardless of
// key).
//
//   - Shell: echoes any received data back, records window-change sizes, and
//     closes the channel when it receives "exit".
//   - SFTP: one shared pkg/sftp InMemHandler (an in-memory filesystem
//     guarded by its own lock) backs every SFTP subsystem channel, so state
//     persists across the short-lived channels the client opens per call.
package sshx

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	mockUsername = "test"
	mockPassword = "secret"
)

type windowChangeRecord struct {
	cols uint32
	rows uint32
}

type mockSSHServer struct {
	port int

	listener net.Listener
	// One in-memory filesystem shared by all SFTP channels of this server.
	sftpHandlers sftp.Handlers

	mu            sync.Mutex
	windowChanges []windowChangeRecord
	conns         map[*ssh.ServerConn]struct{}
	rawConns      map[net.Conn]struct{}
}

// startMockSSHServer starts a mock SSH server on an ephemeral loopback port;
// t.Cleanup shuts it down and disconnects all clients.
func startMockSSHServer(t *testing.T) *mockSSHServer {
	t.Helper()

	hostKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatalf("host key signer: %v", err)
	}

	config := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if meta.User() == mockUsername && string(pass) == mockPassword {
				return nil, nil
			}
			return nil, fmt.Errorf("password rejected for %q", meta.User())
		},
		PublicKeyCallback: func(meta ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
			// Any key is fine for the test user; password is the guarded path.
			if meta.User() == mockUsername {
				return nil, nil
			}
			return nil, fmt.Errorf("publickey rejected for %q", meta.User())
		},
	}
	config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := &mockSSHServer{
		port:         listener.Addr().(*net.TCPAddr).Port,
		listener:     listener,
		sftpHandlers: sftp.InMemHandler(),
		conns:        make(map[*ssh.ServerConn]struct{}),
		rawConns:     make(map[net.Conn]struct{}),
	}
	t.Cleanup(server.close)
	go server.acceptLoop(config)
	return server
}

func (s *mockSSHServer) close() {
	_ = s.listener.Close()
	s.mu.Lock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
}

func (s *mockSSHServer) acceptLoop(config *ssh.ServerConfig) {
	for {
		netConn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handleConn(netConn, config)
	}
}

func (s *mockSSHServer) handleConn(netConn net.Conn, config *ssh.ServerConfig) {
	s.mu.Lock()
	s.rawConns[netConn] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.rawConns, netConn)
		s.mu.Unlock()
	}()

	serverConn, chans, reqs, err := ssh.NewServerConn(netConn, config)
	if err != nil {
		_ = netConn.Close()
		return
	}
	s.mu.Lock()
	s.conns[serverConn] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, serverConn)
		s.mu.Unlock()
	}()

	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go s.handleSessionChannel(channel, requests)
	}
}

func (s *mockSSHServer) handleSessionChannel(channel ssh.Channel, requests <-chan *ssh.Request) {
	for req := range requests {
		switch req.Type {
		case "pty-req":
			_ = req.Reply(true, nil)
		case "window-change":
			var wc windowChangeRequest
			if err := ssh.Unmarshal(req.Payload, &wc); err == nil {
				s.mu.Lock()
				s.windowChanges = append(s.windowChanges, windowChangeRecord{cols: wc.Cols, rows: wc.Rows})
				s.mu.Unlock()
			}
			_ = req.Reply(true, nil)
		case "shell":
			_ = req.Reply(true, nil)
			go s.echoShell(channel)
		case "subsystem":
			var subsystem struct{ Name string }
			if err := ssh.Unmarshal(req.Payload, &subsystem); err == nil && subsystem.Name == "sftp" {
				_ = req.Reply(true, nil)
				go s.serveSFTP(channel)
			} else {
				_ = req.Reply(false, nil)
			}
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// echoShell echoes received data back and closes the channel on "exit".
func (s *mockSSHServer) echoShell(channel ssh.Channel) {
	buf := make([]byte, 4096)
	for {
		n, err := channel.Read(buf)
		if err != nil {
			_ = channel.Close()
			return
		}
		data := buf[:n]
		if strings.TrimSpace(string(data)) == "exit" {
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			_ = channel.Close()
			return
		}
		if _, err := channel.Write(data); err != nil {
			_ = channel.Close()
			return
		}
	}
}

// serveSFTP runs one pkg/sftp request server on the channel against the
// shared in-memory filesystem.
func (s *mockSSHServer) serveSFTP(channel ssh.Channel) {
	server := sftp.NewRequestServer(channel, s.sftpHandlers)
	_ = server.Serve()
	_ = channel.Close()
}

// dropConnections abruptly closes the underlying TCP connections without an
// SSH-level disconnect, simulating a NAT/server-side teardown.
func (s *mockSSHServer) dropConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.rawConns {
		_ = conn.Close()
	}
}

func (s *mockSSHServer) recordedWindowChanges() []windowChangeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]windowChangeRecord(nil), s.windowChanges...)
}
