// Tests for the RFB client handshake (internal/rfb), mirroring tests/rfb.test.ts.
//
// The Apple DH fixture is a true interop check: the mock server implements
// the server side of the TigerVNC CSecurityDH wire format with its own
// crypto (big.Int modpow, AES-128-ECB, MD5) and decrypts the credential
// block — the test only passes if the server recovers the exact plaintext
// username/password.
//
// Mock-server scripts run in their own goroutines, so they must not fail the
// test directly: read/write helpers return errors and scripts bail out
// silently (the client side performs the assertions).
package rfb_test

import (
	"bytes"
	"crypto/aes"
	"crypto/des"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"deskmux/internal/rfb"
)

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

// startMockServer starts a mock RFB server on an ephemeral loopback port.
// The script runs in a goroutine per connection; accepted connections stay
// open until test cleanup.
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

func dial(t *testing.T, port int) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
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

// buildServerInit builds a ServerInit message with a 32-bit true-color pixel
// format.
func buildServerInit(width, height uint16, name string) []byte {
	nameBytes := []byte(name)
	buf := make([]byte, 24+len(nameBytes))
	binary.BigEndian.PutUint16(buf[0:2], width)
	binary.BigEndian.PutUint16(buf[2:4], height)
	buf[4] = 32 // bits-per-pixel
	buf[5] = 24 // depth
	buf[6] = 0  // little-endian flag
	buf[7] = 1  // true-color flag
	binary.BigEndian.PutUint16(buf[8:10], 255)
	binary.BigEndian.PutUint16(buf[10:12], 255)
	binary.BigEndian.PutUint16(buf[12:14], 255)
	buf[14] = 16 // red-shift
	buf[15] = 8  // green-shift
	buf[16] = 0  // blue-shift
	binary.BigEndian.PutUint32(buf[20:24], uint32(len(nameBytes)))
	copy(buf[24:], nameBytes)
	return buf
}

func securityResultFail(reason string) []byte {
	buf := make([]byte, 8+len(reason))
	binary.BigEndian.PutUint32(buf[0:4], 1)
	binary.BigEndian.PutUint32(buf[4:8], uint32(len(reason)))
	copy(buf[8:], reason)
	return buf
}

func asAuthError(t *testing.T, err error) *rfb.RfbAuthError {
	t.Helper()
	var authErr *rfb.RfbAuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected RfbAuthError, got %T: %v", err, err)
	}
	return authErr
}

func asProtocolError(t *testing.T, err error) *rfb.RfbProtocolError {
	t.Helper()
	var protocolErr *rfb.RfbProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("expected RfbProtocolError, got %T: %v", err, err)
	}
	return protocolErr
}

// ---------------------------------------------------------------------------
// Crypto primitives
// ---------------------------------------------------------------------------

func TestDESCanonicalVector(t *testing.T) {
	key, _ := hex.DecodeString("0123456789abcdef")
	plain, _ := hex.DecodeString("4e6f772069732074") // "Now is t"
	cipher, err := des.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 8)
	cipher.Encrypt(out, plain)
	if hex.EncodeToString(out) != "3fa40e8a984d4815" {
		t.Fatalf("DES vector mismatch: %x", out)
	}
}

func TestVncPasswordKey(t *testing.T) {
	cases := map[string]string{
		"password":      "0e86ceceeef64e26",
		"passwordEXTRA": "0e86ceceeef64e26", // >8 chars are truncated
		"":              "0000000000000000", // short passwords are zero-padded
	}
	for password, want := range cases {
		if got := hex.EncodeToString(rfb.VncPasswordKey(password)); got != want {
			t.Errorf("VncPasswordKey(%q) = %s, want %s", password, got, want)
		}
	}
}

func TestEncryptVncChallenge(t *testing.T) {
	challenge := make([]byte, 16)
	rand.Read(challenge)
	got, err := rfb.EncryptVncChallenge(challenge, "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	// Independent ECB computation with crypto/des.
	cipher, _ := des.NewCipher(rfb.VncPasswordKey("hunter2"))
	want := make([]byte, 16)
	cipher.Encrypt(want[0:8], challenge[0:8])
	cipher.Encrypt(want[8:16], challenge[8:16])
	if !bytes.Equal(got, want) {
		t.Fatalf("challenge response mismatch: %x != %x", got, want)
	}
}

func TestModPow(t *testing.T) {
	// Wikipedia DHKE: p=23, g=5, a=6 -> A=8, b=15 -> B=19, shared secret 2.
	p := big.NewInt(23)
	if got := rfb.ModPow(big.NewInt(5), big.NewInt(6), p); got.Cmp(big.NewInt(8)) != 0 {
		t.Errorf("5^6 mod 23 = %v, want 8", got)
	}
	if got := rfb.ModPow(big.NewInt(5), big.NewInt(15), p); got.Cmp(big.NewInt(19)) != 0 {
		t.Errorf("5^15 mod 23 = %v, want 19", got)
	}
	if got := rfb.ModPow(big.NewInt(19), big.NewInt(6), p); got.Cmp(big.NewInt(2)) != 0 {
		t.Errorf("19^6 mod 23 = %v, want 2", got)
	}
	if got := rfb.ModPow(big.NewInt(8), big.NewInt(15), p); got.Cmp(big.NewInt(2)) != 0 {
		t.Errorf("8^15 mod 23 = %v, want 2", got)
	}
}

func TestBigIntToBytesPadding(t *testing.T) {
	if got := hex.EncodeToString(rfb.BigIntToBytes(big.NewInt(2), 4)); got != "00000002" {
		t.Errorf("BigIntToBytes(2, 4) = %s", got)
	}
	if got := hex.EncodeToString(rfb.BigIntToBytes(big.NewInt(0), 2)); got != "0000" {
		t.Errorf("BigIntToBytes(0, 2) = %s", got)
	}
}

// ---------------------------------------------------------------------------
// Handshake against mock servers
// ---------------------------------------------------------------------------

func TestHandshakeNoneParsesServerInit(t *testing.T) {
	serverInit := buildServerInit(1024, 768, "test-desktop")
	seen := make(chan byte, 2)
	port := startMockServer(t, func(conn net.Conn) {
		if swrite(conn, []byte("RFB 003.008\n")) != nil {
			return
		}
		if _, err := sread(conn, 12); err != nil {
			return
		}
		if swrite(conn, []byte{2, 2, 1}) != nil { // offers VNC auth + None
			return
		}
		selection, err := sread(conn, 1)
		if err != nil {
			return
		}
		seen <- selection[0]
		if swrite(conn, []byte{0, 0, 0, 0}) != nil {
			return
		}
		clientInit, err := sread(conn, 1)
		if err != nil {
			return
		}
		seen <- clientInit[0]
		swrite(conn, serverInit)
	})

	conn := dial(t, port)
	info, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if info.ServerVersion != "003.008" || info.SecurityType != 1 {
		t.Errorf("version/type = %s/%d", info.ServerVersion, info.SecurityType)
	}
	if info.Width != 1024 || info.Height != 768 || info.Name != "test-desktop" {
		t.Errorf("ServerInit = %+v", info)
	}
	wantPF := rfb.VncPixelFormat{
		BitsPerPixel: 32, Depth: 24, BigEndian: false, TrueColor: true,
		RedMax: 255, GreenMax: 255, BlueMax: 255,
		RedShift: 16, GreenShift: 8, BlueShift: 0,
	}
	if info.PixelFormat != wantPF {
		t.Errorf("pixel format = %+v, want %+v", info.PixelFormat, wantPF)
	}
	if !bytes.Equal(info.Raw, serverInit) {
		t.Errorf("raw ServerInit mismatch")
	}
	if selection := <-seen; selection != 1 {
		t.Errorf("selection = %d, want 1", selection)
	}
	if clientInit := <-seen; clientInit != 1 {
		t.Errorf("ClientInit shared flag = %d, want 1", clientInit)
	}
}

func TestHandshakeRFB33None(t *testing.T) {
	serverInit := buildServerInit(800, 600, "legacy")
	port := startMockServer(t, func(conn net.Conn) {
		if swrite(conn, []byte("RFB 003.003\n")) != nil {
			return
		}
		if _, err := sread(conn, 12); err != nil {
			return
		}
		typeMsg := make([]byte, 4)
		binary.BigEndian.PutUint32(typeMsg, 1) // server dictates None
		if swrite(conn, typeMsg) != nil {
			return
		}
		if _, err := sread(conn, 1); err != nil { // ClientInit comes straight away in 3.3
			return
		}
		swrite(conn, serverInit)
	})

	conn := dial(t, port)
	info, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if info.SecurityType != 1 || info.Width != 800 || info.Name != "legacy" {
		t.Errorf("ServerInit = %+v", info)
	}
}

func TestHandshakeVNCAuthSuccess(t *testing.T) {
	port := startMockServer(t, func(conn net.Conn) {
		if swrite(conn, []byte("RFB 003.008\n")) != nil {
			return
		}
		if _, err := sread(conn, 12); err != nil {
			return
		}
		if swrite(conn, []byte{1, 2}) != nil {
			return
		}
		if _, err := sread(conn, 1); err != nil {
			return
		}
		challenge := make([]byte, 16)
		rand.Read(challenge)
		if swrite(conn, challenge) != nil {
			return
		}
		response, err := sread(conn, 16)
		if err != nil {
			return
		}
		expected, err := rfb.EncryptVncChallenge(challenge, "s3cret")
		if err != nil {
			return
		}
		if bytes.Equal(response, expected) {
			if swrite(conn, []byte{0, 0, 0, 0}) != nil {
				return
			}
			if _, err := sread(conn, 1); err != nil {
				return
			}
			swrite(conn, buildServerInit(640, 480, "vnc"))
		} else {
			swrite(conn, securityResultFail("bad password"))
		}
	})

	conn := dial(t, port)
	info, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{Password: "s3cret"})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if info.SecurityType != 2 || info.Name != "vnc" {
		t.Errorf("ServerInit = %+v", info)
	}
}

func TestHandshakeVNCAuthWrongPassword(t *testing.T) {
	port := startMockServer(t, func(conn net.Conn) {
		if swrite(conn, []byte("RFB 003.008\n")) != nil {
			return
		}
		if _, err := sread(conn, 12); err != nil {
			return
		}
		if swrite(conn, []byte{1, 2}) != nil {
			return
		}
		if _, err := sread(conn, 1); err != nil {
			return
		}
		challenge := make([]byte, 16)
		rand.Read(challenge)
		if swrite(conn, challenge) != nil {
			return
		}
		if _, err := sread(conn, 16); err != nil {
			return
		}
		swrite(conn, securityResultFail("VNC authentication failed"))
	})

	conn := dial(t, port)
	_, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{Password: "wrong"})
	authErr := asAuthError(t, err)
	if !strings.Contains(authErr.Message, "VNC authentication failed") {
		t.Errorf("message = %q", authErr.Message)
	}
}

func TestHandshakePrefersVNCAuthOverNone(t *testing.T) {
	seen := make(chan byte, 1)
	port := startMockServer(t, func(conn net.Conn) {
		if swrite(conn, []byte("RFB 003.008\n")) != nil {
			return
		}
		if _, err := sread(conn, 12); err != nil {
			return
		}
		if swrite(conn, []byte{2, 1, 2}) != nil { // None first, VNC auth second
			return
		}
		selection, err := sread(conn, 1)
		if err != nil {
			return
		}
		seen <- selection[0]
		challenge := make([]byte, 16)
		rand.Read(challenge)
		if swrite(conn, challenge) != nil {
			return
		}
		if _, err := sread(conn, 16); err != nil {
			return
		}
		if swrite(conn, []byte{0, 0, 0, 0}) != nil {
			return
		}
		if _, err := sread(conn, 1); err != nil {
			return
		}
		swrite(conn, buildServerInit(640, 480, "vnc"))
	})

	conn := dial(t, port)
	info, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{Password: "s3cret"})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if info.SecurityType != 2 {
		t.Errorf("securityType = %d, want 2", info.SecurityType)
	}
	if selection := <-seen; selection != 2 {
		t.Errorf("selection = %d, want 2", selection)
	}
}

// ---------------------------------------------------------------------------
// Apple DH (security type 30)
// ---------------------------------------------------------------------------

// dhPrime is the RFC 2409 group 2 prime (1024-bit), 128 bytes.
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
	clientVersion string
	selectedType  byte
	username      string
	password      string
}

func readCString(field []byte) string {
	if i := bytes.IndexByte(field, 0); i >= 0 {
		return string(field[:i])
	}
	return string(field)
}

// appleDhServer implements the server side of the CSecurityDH exchange and
// decrypts the credentials: the recovered username/password are the interop
// ground truth. If they match `expected`, the server completes the handshake.
func appleDhServer(expected struct{ username, password string }, seenCh chan<- appleDhSeen) func(net.Conn) {
	return func(conn net.Conn) {
		seen := appleDhSeen{}
		fail := func() { seenCh <- seen }
		if swrite(conn, []byte("RFB 003.889\n")) != nil {
			fail()
			return
		}
		version, err := sread(conn, 12)
		if err != nil {
			fail()
			return
		}
		seen.clientVersion = string(version)
		if swrite(conn, []byte{1, 30}) != nil { // only Apple DH offered
			fail()
			return
		}
		selection, err := sread(conn, 1)
		if err != nil {
			fail()
			return
		}
		seen.selectedType = selection[0]

		p := new(big.Int).SetBytes(dhPrime)
		g := big.NewInt(2)
		aBytes := make([]byte, dhKeyLength)
		rand.Read(aBytes)
		a := new(big.Int).SetBytes(aBytes)
		serverPublic := new(big.Int).Exp(g, a, p)

		header := make([]byte, 4)
		binary.BigEndian.PutUint16(header[0:2], 2) // generator g = 2
		binary.BigEndian.PutUint16(header[2:4], dhKeyLength)
		params := append(header, dhPrime...)
		params = append(params, rfb.BigIntToBytes(serverPublic, dhKeyLength)...)
		if swrite(conn, params) != nil {
			fail()
			return
		}

		reply, err := sread(conn, 128+dhKeyLength)
		if err != nil {
			fail()
			return
		}
		encryptedCredentials := reply[:128]
		clientPublic := new(big.Int).SetBytes(reply[128:])
		sharedSecret := new(big.Int).Exp(clientPublic, a, p)
		key := md5.Sum(rfb.BigIntToBytes(sharedSecret, dhKeyLength))
		block, err := aes.NewCipher(key[:])
		if err != nil {
			fail()
			return
		}
		plain := make([]byte, 128)
		for offset := 0; offset < 128; offset += aes.BlockSize {
			block.Decrypt(plain[offset:offset+aes.BlockSize], encryptedCredentials[offset:offset+aes.BlockSize])
		}
		seen.username = readCString(plain[0:64])
		seen.password = readCString(plain[64:128])
		seenCh <- seen

		if seen.username == expected.username && seen.password == expected.password {
			if swrite(conn, []byte{0, 0, 0, 0}) != nil {
				return
			}
			if _, err := sread(conn, 1); err != nil {
				return
			}
			swrite(conn, buildServerInit(1920, 1080, "mac"))
		} else {
			swrite(conn, securityResultFail("Authentication failed"))
		}
	}
}

func TestAppleDHSuccess(t *testing.T) {
	seenCh := make(chan appleDhSeen, 1)
	port := startMockServer(t, appleDhServer(struct{ username, password string }{"silica", "p@ss w0rd"}, seenCh))

	conn := dial(t, port)
	info, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{
		Username: "silica",
		Password: "p@ss w0rd",
	})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if info.SecurityType != 30 || info.ServerVersion != "003.889" || info.Width != 1920 || info.Name != "mac" {
		t.Errorf("ServerInit = %+v", info)
	}
	seen := <-seenCh
	// The client clamps Apple's 3.889 to 3.8, exactly like TigerVNC.
	if seen.clientVersion != "RFB 003.008\n" {
		t.Errorf("client version = %q", seen.clientVersion)
	}
	if seen.selectedType != 30 {
		t.Errorf("selected type = %d", seen.selectedType)
	}
	// True interop check: the mock server decrypted these independently.
	if seen.username != "silica" || seen.password != "p@ss w0rd" {
		t.Errorf("server recovered %q/%q", seen.username, seen.password)
	}
}

func TestAppleDHWrongPassword(t *testing.T) {
	seenCh := make(chan appleDhSeen, 1)
	port := startMockServer(t, appleDhServer(struct{ username, password string }{"silica", "right"}, seenCh))

	conn := dial(t, port)
	_, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{Username: "silica", Password: "wrong"})
	authErr := asAuthError(t, err)
	var protocolErr *rfb.RfbProtocolError
	var timeoutErr *rfb.RfbTimeoutError
	if errors.As(err, &protocolErr) || errors.As(err, &timeoutErr) {
		t.Fatalf("auth error must not be protocol/timeout: %T", err)
	}
	if !strings.Contains(authErr.Message, "Authentication failed") {
		t.Errorf("message = %q", authErr.Message)
	}
	seen := <-seenCh
	// The wire format still worked — the server decrypted a coherent block.
	if seen.username != "silica" || seen.password != "wrong" {
		t.Errorf("server recovered %q/%q", seen.username, seen.password)
	}
}

func TestAppleDHNoPasswordFailsFast(t *testing.T) {
	port := startMockServer(t, func(conn net.Conn) {
		if swrite(conn, []byte("RFB 003.889\n")) != nil {
			return
		}
		if _, err := sread(conn, 12); err != nil {
			return
		}
		if swrite(conn, []byte{1, 30}) != nil {
			return
		}
		sread(conn, 1) // client fails fast and disconnects; ignore the error
	})

	conn := dial(t, port)
	_, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{})
	authErr := asAuthError(t, err)
	if !strings.Contains(authErr.Message, "no password") {
		t.Errorf("message = %q", authErr.Message)
	}
}

func TestAppleDHRejectsLongUsername(t *testing.T) {
	port := startMockServer(t, func(conn net.Conn) {
		if swrite(conn, []byte("RFB 003.889\n")) != nil {
			return
		}
		if _, err := sread(conn, 12); err != nil {
			return
		}
		if swrite(conn, []byte{1, 30}) != nil {
			return
		}
		if _, err := sread(conn, 1); err != nil {
			return
		}
		header := make([]byte, 4)
		binary.BigEndian.PutUint16(header[0:2], 2)
		binary.BigEndian.PutUint16(header[2:4], dhKeyLength)
		// Valid-but-arbitrary server public key; the client must reject the
		// over-long username before it ever sends credentials.
		params := append(header, dhPrime...)
		params = append(params, rfb.BigIntToBytes(big.NewInt(12345), dhKeyLength)...)
		swrite(conn, params)
	})

	conn := dial(t, port)
	_, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{
		Username: strings.Repeat("x", 64),
		Password: "pw",
	})
	authErr := asAuthError(t, err)
	if !strings.Contains(authErr.Message, "username too long") {
		t.Errorf("message = %q", authErr.Message)
	}
}

func TestAppleDHRejectsOutOfRangeKeyLength(t *testing.T) {
	port := startMockServer(t, func(conn net.Conn) {
		if swrite(conn, []byte("RFB 003.889\n")) != nil {
			return
		}
		if _, err := sread(conn, 12); err != nil {
			return
		}
		if swrite(conn, []byte{1, 30}) != nil {
			return
		}
		if _, err := sread(conn, 1); err != nil {
			return
		}
		header := make([]byte, 4)
		binary.BigEndian.PutUint16(header[0:2], 2)
		binary.BigEndian.PutUint16(header[2:4], 64) // below the 128-byte minimum
		swrite(conn, header)
	})

	conn := dial(t, port)
	_, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{Username: "u", Password: "p"})
	protocolErr := asProtocolError(t, err)
	if !strings.Contains(protocolErr.Message, "key length") {
		t.Errorf("message = %q", protocolErr.Message)
	}
}

// ---------------------------------------------------------------------------
// Protocol error paths
// ---------------------------------------------------------------------------

func TestHandshakeRejectsNonRFBBanner(t *testing.T) {
	port := startMockServer(t, func(conn net.Conn) {
		swrite(conn, []byte("HELLO WORLD\n"))
	})

	conn := dial(t, port)
	_, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{})
	protocolErr := asProtocolError(t, err)
	if !strings.Contains(protocolErr.Message, "not an RFB server") {
		t.Errorf("message = %q", protocolErr.Message)
	}
}

func TestHandshakeRejectsUnsupportedSecurityTypes(t *testing.T) {
	port := startMockServer(t, func(conn net.Conn) {
		if swrite(conn, []byte("RFB 003.008\n")) != nil {
			return
		}
		if _, err := sread(conn, 12); err != nil {
			return
		}
		swrite(conn, []byte{2, 16, 18}) // Tight / TLS — unsupported
	})

	conn := dial(t, port)
	_, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{})
	protocolErr := asProtocolError(t, err)
	if !strings.Contains(protocolErr.Message, "no supported security type") {
		t.Errorf("message = %q", protocolErr.Message)
	}
}

func TestHandshakeSurfacesServerRefusalReason(t *testing.T) {
	port := startMockServer(t, func(conn net.Conn) {
		if swrite(conn, []byte("RFB 003.008\n")) != nil {
			return
		}
		if _, err := sread(conn, 12); err != nil {
			return
		}
		if swrite(conn, []byte{0}) != nil { // count = 0 -> failure + reason
			return
		}
		head := make([]byte, 4)
		binary.BigEndian.PutUint32(head, uint32(len("Too many connections")))
		swrite(conn, append(head, []byte("Too many connections")...))
	})

	conn := dial(t, port)
	_, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{})
	protocolErr := asProtocolError(t, err)
	if !strings.Contains(protocolErr.Message, "Too many connections") {
		t.Errorf("message = %q", protocolErr.Message)
	}
}

func TestHandshakeTimesOutAgainstSilentServer(t *testing.T) {
	port := startMockServer(t, func(conn net.Conn) {
		// Never sends anything; just waits for the client to go away.
		sread(conn, 1)
	})

	conn := dial(t, port)
	_, err := rfb.PerformHandshake(conn, rfb.HandshakeOptions{Timeout: 300 * time.Millisecond})
	var timeoutErr *rfb.RfbTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected RfbTimeoutError, got %T: %v", err, err)
	}
}
