// Tests for the WebSocket VNC bridge (internal/vncbridge), mirroring
// tests/vncBridge.test.ts. A fake noVNC client speaks the minimal RFB
// handshake over WebSocket and checks the synthesized None-security replay
// plus byte-exact passthrough.
//
// Mock-server scripts run in their own goroutines, so they must not fail the
// test directly: read/write helpers return errors and scripts bail out
// silently (the client side performs the assertions).
package vncbridge_test

import (
	"bytes"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"deskmux/internal/rfb"
	"deskmux/internal/vncbridge"
)

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

func startMockServer(t *testing.T, script func(conn net.Conn)) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	accepted := make(map[net.Conn]struct{})
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for conn := range accepted {
			conn.Close()
		}
		mu.Unlock()
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			accepted[conn] = struct{}{}
			mu.Unlock()
			go script(conn)
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

// sread reads exactly n bytes on the mock-server side.
func sread(conn net.Conn, n int) ([]byte, error) {
	buf := make([]byte, n)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := io.ReadFull(conn, buf)
	return buf, err
}

func swrite(conn net.Conn, data []byte) error {
	_, err := conn.Write(data)
	return err
}

// noneServer speaks the RFB None handshake, sends serverInit, then hands the
// connection over for pipe-phase inspection via the upstream channel (nil =
// no handoff; the connection stays open until test cleanup).
func noneServer(serverInit []byte, upstream chan<- net.Conn) func(net.Conn) {
	return func(conn net.Conn) {
		if swrite(conn, []byte("RFB 003.008\n")) != nil {
			return
		}
		if _, err := sread(conn, 12); err != nil {
			return
		}
		if swrite(conn, []byte{1, 1}) != nil { // one security type: None
			return
		}
		if _, err := sread(conn, 1); err != nil {
			return
		}
		if swrite(conn, []byte{0, 0, 0, 0}) != nil { // SecurityResult OK
			return
		}
		if _, err := sread(conn, 1); err != nil { // ClientInit
			return
		}
		if swrite(conn, serverInit) != nil {
			return
		}
		if upstream != nil {
			upstream <- conn
		}
	}
}

func buildServerInit(width, height uint16, name string) []byte {
	nameBytes := []byte(name)
	buf := make([]byte, 24+len(nameBytes))
	binary.BigEndian.PutUint16(buf[0:2], width)
	binary.BigEndian.PutUint16(buf[2:4], height)
	buf[4] = 32
	buf[5] = 24
	buf[6] = 0
	buf[7] = 1
	binary.BigEndian.PutUint16(buf[8:10], 255)
	binary.BigEndian.PutUint16(buf[10:12], 255)
	binary.BigEndian.PutUint16(buf[12:14], 255)
	buf[14] = 16
	buf[15] = 8
	buf[16] = 0
	binary.BigEndian.PutUint32(buf[20:24], uint32(len(nameBytes)))
	copy(buf[24:], nameBytes)
	return buf
}

func startBridge(t *testing.T, opts vncbridge.Options) *vncbridge.Bridge {
	t.Helper()
	bridge, err := vncbridge.Start(opts)
	if err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	t.Cleanup(func() { bridge.Close() })
	return bridge
}

// ---------------------------------------------------------------------------
// Minimal noVNC stand-in over gorilla/websocket
// ---------------------------------------------------------------------------

type closeEvent struct {
	code   int
	reason string
}

type noVncClient struct {
	t        *testing.T
	conn     *websocket.Conn
	messages chan []byte
	closeCh  chan closeEvent
	buf      []byte
}

func dialNoVnc(t *testing.T, wsPort int) *noVncClient {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{"binary"}}
	conn, _, err := dialer.Dial(fmt.Sprintf("ws://127.0.0.1:%d", wsPort), nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	c := &noVncClient{
		t:        t,
		conn:     conn,
		messages: make(chan []byte, 64),
		closeCh:  make(chan closeEvent, 1),
	}
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				event := closeEvent{code: -1, reason: err.Error()}
				var closeErr *websocket.CloseError
				if errors.As(err, &closeErr) {
					event = closeEvent{code: closeErr.Code, reason: closeErr.Text}
				}
				c.closeCh <- event
				return
			}
			c.messages <- data
		}
	}()
	return c
}

func (c *noVncClient) read(n int) []byte {
	c.t.Helper()
	for len(c.buf) < n {
		select {
		case chunk := <-c.messages:
			c.buf = append(c.buf, chunk...)
		case <-time.After(5 * time.Second):
			c.t.Fatalf("ws client read(%d): timed out", n)
		}
	}
	out := make([]byte, n)
	copy(out, c.buf[:n])
	c.buf = c.buf[n:]
	return out
}

func (c *noVncClient) send(data []byte) {
	c.t.Helper()
	if err := c.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		c.t.Fatalf("ws send: %v", err)
	}
}

func (c *noVncClient) awaitClose() closeEvent {
	c.t.Helper()
	select {
	case event := <-c.closeCh:
		return event
	case <-time.After(5 * time.Second):
		c.t.Fatal("ws client: close timed out")
		return closeEvent{}
	}
}

// expectHandshake runs the client side of the synthesized handshake and
// asserts every step.
func (c *noVncClient) expectHandshake(expectedServerInit []byte) {
	c.t.Helper()
	if version := string(c.read(12)); version != "RFB 003.008\n" {
		c.t.Fatalf("banner = %q", version)
	}
	c.send([]byte("RFB 003.008\n"))
	if types := c.read(2); !bytes.Equal(types, []byte{1, 1}) {
		c.t.Fatalf("security types = %v, want [1 1]", types)
	}
	c.send([]byte{1})
	if result := c.read(4); !bytes.Equal(result, []byte{0, 0, 0, 0}) {
		c.t.Fatalf("SecurityResult = %v", result)
	}
	c.send([]byte{1}) // ClientInit
	head := c.read(24)
	name := c.read(int(binary.BigEndian.Uint32(head[20:24])))
	if got := append(head, name...); !bytes.Equal(got, expectedServerInit) {
		c.t.Fatalf("ServerInit mismatch")
	}
}

type bridgeErrorPayload struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

func parseBridgeError(t *testing.T, reason string) bridgeErrorPayload {
	t.Helper()
	var payload bridgeErrorPayload
	if err := json.Unmarshal([]byte(reason), &payload); err != nil {
		t.Fatalf("close reason %q is not JSON: %v", reason, err)
	}
	return payload
}

// ---------------------------------------------------------------------------
// Bridge behavior
// ---------------------------------------------------------------------------

func TestBridgeNonePassthrough(t *testing.T) {
	serverInit := buildServerInit(1440, 900, "bridge-none")
	upstream := make(chan net.Conn, 1)
	port := startMockServer(t, noneServer(serverInit, upstream))
	bridge := startBridge(t, vncbridge.Options{Host: "127.0.0.1", Port: port})

	client := dialNoVnc(t, bridge.WSPort)
	client.expectHandshake(serverInit)
	if got := client.conn.Subprotocol(); got != "binary" {
		t.Errorf("subprotocol = %q, want binary", got)
	}
	serverConn := <-upstream

	// TCP -> WS direction: server bytes arrive at the client untouched.
	downlink := make([]byte, 64)
	rand.Read(downlink)
	if _, err := serverConn.Write(downlink); err != nil {
		t.Fatalf("server write: %v", err)
	}
	if got := client.read(len(downlink)); !bytes.Equal(got, downlink) {
		t.Errorf("downlink mismatch")
	}

	// WS -> TCP direction: client frames arrive at the server untouched.
	uplink := make([]byte, 48)
	rand.Read(uplink)
	client.send(uplink)
	got := make([]byte, len(uplink))
	serverConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(serverConn, got); err != nil {
		t.Fatalf("server read uplink: %v", err)
	}
	if !bytes.Equal(got, uplink) {
		t.Errorf("uplink mismatch")
	}

	client.conn.Close()
	client.awaitClose()
}

func TestBridgeAppleDHPassthrough(t *testing.T) {
	serverInit := buildServerInit(2560, 1440, "mac-studio")
	seenCh := make(chan appleDhSeen, 1)
	upstream := make(chan net.Conn, 1)
	port := startMockServer(t, func(conn net.Conn) {
		if !appleDhHandshake(conn, struct{ username, password string }{"u", "p"}, seenCh, serverInit) {
			return
		}
		upstream <- conn
	})
	bridge := startBridge(t, vncbridge.Options{
		Host: "127.0.0.1", Port: port, Username: "u", Password: "p",
	})

	client := dialNoVnc(t, bridge.WSPort)
	client.expectHandshake(serverInit)
	if seen := <-seenCh; seen.username != "u" || seen.password != "p" {
		t.Errorf("server recovered %q/%q", seen.username, seen.password)
	}
	serverConn := <-upstream

	downlink := make([]byte, 32)
	rand.Read(downlink)
	if _, err := serverConn.Write(downlink); err != nil {
		t.Fatalf("server write: %v", err)
	}
	if got := client.read(len(downlink)); !bytes.Equal(got, downlink) {
		t.Errorf("downlink mismatch")
	}

	client.conn.Close()
	client.awaitClose()
}

func TestBridgeBadCredentialsStructuredClose(t *testing.T) {
	serverInit := buildServerInit(640, 480, "unused")
	seenCh := make(chan appleDhSeen, 1)
	port := startMockServer(t, func(conn net.Conn) {
		appleDhHandshake(conn, struct{ username, password string }{"u", "right"}, seenCh, serverInit)
	})
	bridge := startBridge(t, vncbridge.Options{
		Host: "127.0.0.1", Port: port, Username: "u", Password: "wrong",
	})

	client := dialNoVnc(t, bridge.WSPort)
	event := client.awaitClose()
	if event.code != 4001 {
		t.Errorf("close code = %d, want 4001", event.code)
	}
	payload := parseBridgeError(t, event.reason)
	if payload.Kind != "auth" {
		t.Errorf("kind = %q", payload.Kind)
	}
	if !bytes.Contains([]byte(payload.Message), []byte("Authentication failed")) {
		t.Errorf("message = %q", payload.Message)
	}
}

func TestBridgeRejectsSecondClientAsBusy(t *testing.T) {
	serverInit := buildServerInit(800, 600, "busy-target")
	port := startMockServer(t, noneServer(serverInit, nil))
	bridge := startBridge(t, vncbridge.Options{Host: "127.0.0.1", Port: port})

	first := dialNoVnc(t, bridge.WSPort)
	first.expectHandshake(serverInit)

	second := dialNoVnc(t, bridge.WSPort)
	event := second.awaitClose()
	if event.code != 1013 {
		t.Errorf("close code = %d, want 1013", event.code)
	}
	if payload := parseBridgeError(t, event.reason); payload.Kind != "busy" {
		t.Errorf("kind = %q", payload.Kind)
	}

	first.conn.Close()
	first.awaitClose()
}

func TestBridgeUnreachableTargetConnectionError(t *testing.T) {
	// Grab a guaranteed-closed port: listen once, then close the server.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	bridge := startBridge(t, vncbridge.Options{Host: "127.0.0.1", Port: closedPort})
	client := dialNoVnc(t, bridge.WSPort)
	event := client.awaitClose()
	if event.code != 4002 {
		t.Errorf("close code = %d, want 4002", event.code)
	}
	if payload := parseBridgeError(t, event.reason); payload.Kind != "connection" {
		t.Errorf("kind = %q", payload.Kind)
	}
}

// ---------------------------------------------------------------------------
// Apple DH mock (server side of the CSecurityDH wire format)
// ---------------------------------------------------------------------------

var dhPrime = func() []byte {
	b, _ := hex.DecodeString(
		"FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1" +
			"29024E088A67CC74020BBEA63B139B22514A08798E3404DD" +
			"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245" +
			"E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
			"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381" +
			"FFFFFFFFFFFFFFFF")
	return b
}()

const dhKeyLength = 128

type appleDhSeen struct {
	username string
	password string
}

func readCString(field []byte) string {
	if i := bytes.IndexByte(field, 0); i >= 0 {
		return string(field[:i])
	}
	return string(field)
}

// appleDhHandshake runs the server side of the Apple DH exchange against the
// bridge's upstream connection. Returns true when the handshake completed
// (credentials accepted and ServerInit sent).
func appleDhHandshake(conn net.Conn, expected struct{ username, password string }, seenCh chan<- appleDhSeen, serverInit []byte) bool {
	seen := appleDhSeen{}
	fail := func() bool {
		seenCh <- seen
		return false
	}
	if swrite(conn, []byte("RFB 003.889\n")) != nil {
		return fail()
	}
	if _, err := sread(conn, 12); err != nil {
		return fail()
	}
	if swrite(conn, []byte{1, 30}) != nil {
		return fail()
	}
	if _, err := sread(conn, 1); err != nil {
		return fail()
	}

	p := new(big.Int).SetBytes(dhPrime)
	g := big.NewInt(2)
	aBytes := make([]byte, dhKeyLength)
	rand.Read(aBytes)
	a := new(big.Int).SetBytes(aBytes)
	serverPublic := new(big.Int).Exp(g, a, p)

	header := make([]byte, 4)
	binary.BigEndian.PutUint16(header[0:2], 2)
	binary.BigEndian.PutUint16(header[2:4], dhKeyLength)
	params := append(header, dhPrime...)
	params = append(params, rfb.BigIntToBytes(serverPublic, dhKeyLength)...)
	if swrite(conn, params) != nil {
		return fail()
	}

	reply, err := sread(conn, 128+dhKeyLength)
	if err != nil {
		return fail()
	}
	encryptedCredentials := reply[:128]
	clientPublic := new(big.Int).SetBytes(reply[128:])
	sharedSecret := new(big.Int).Exp(clientPublic, a, p)
	key := md5.Sum(rfb.BigIntToBytes(sharedSecret, dhKeyLength))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return fail()
	}
	plain := make([]byte, 128)
	for offset := 0; offset < 128; offset += aes.BlockSize {
		block.Decrypt(plain[offset:offset+aes.BlockSize], encryptedCredentials[offset:offset+aes.BlockSize])
	}
	seen.username = readCString(plain[0:64])
	seen.password = readCString(plain[64:128])
	seenCh <- seen

	if seen.username != expected.username || seen.password != expected.password {
		reason := "Authentication failed"
		head := make([]byte, 8)
		binary.BigEndian.PutUint32(head[0:4], 1)
		binary.BigEndian.PutUint32(head[4:8], uint32(len(reason)))
		swrite(conn, append(head, []byte(reason)...))
		return false
	}
	if swrite(conn, []byte{0, 0, 0, 0}) != nil {
		return false
	}
	if _, err := sread(conn, 1); err != nil {
		return false
	}
	return swrite(conn, serverInit) == nil
}
