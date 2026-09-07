// Tests for the SetEncodings rewriter (internal/vncbridge), mirroring
// tests/vncBridgeEncodings.test.ts. The rewriter is exercised as a pure
// function; the passthrough case goes through the full bridge (rewriting is
// only enabled when an encoding preference is configured).
package vncbridge_test

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"deskmux/internal/vncbridge"
)

// clientSetEncodings: [Raw, ZRLE, cursor pseudo-encoding].
var clientSetEncodings = concatBytes(
	[]byte{2, 0, 0, 3},
	[]byte{0, 0, 0, 0},             // 0 Raw
	[]byte{0, 0, 0, 16},            // 16 ZRLE
	[]byte{0xff, 0xff, 0xff, 0x11}, // -239 cursor pseudo-encoding
)

// expectedRewritten with preference [16]: [CopyRect, ZRLE, Raw] + pseudo.
var expectedRewritten = concatBytes(
	[]byte{2, 0, 0, 4},
	[]byte{0, 0, 0, 1},             // 1 CopyRect
	[]byte{0, 0, 0, 16},            // 16 ZRLE
	[]byte{0, 0, 0, 0},             // 0 Raw fallback
	[]byte{0xff, 0xff, 0xff, 0x11}, // -239 preserved
)

func concatBytes(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func flatten(chunks [][]byte) []byte {
	var out []byte
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}

func TestRewriterRewrite(t *testing.T) {
	r := vncbridge.NewSetEncodingsRewriter([]int32{16})
	out := r.Push(clientSetEncodings)
	if got := flatten(out); !bytes.Equal(got, expectedRewritten) {
		t.Fatalf("rewritten = %x, want %x", got, expectedRewritten)
	}
}

func TestRewriterSplitAcrossChunks(t *testing.T) {
	r := vncbridge.NewSetEncodingsRewriter([]int32{16})
	if out := r.Push(clientSetEncodings[:5]); len(out) != 0 {
		t.Fatalf("first chunk produced %d chunks, want 0", len(out))
	}
	out := r.Push(clientSetEncodings[5:])
	if got := flatten(out); !bytes.Equal(got, expectedRewritten) {
		t.Fatalf("rewritten = %x, want %x", got, expectedRewritten)
	}
}

func TestRewriterPassesOtherMessagesVerbatim(t *testing.T) {
	r := vncbridge.NewSetEncodingsRewriter([]int32{16})
	// KeyEvent (type 4, 8 bytes) followed by FramebufferUpdateRequest (type 3,
	// 10 bytes) in one chunk.
	keyEvent := []byte{4, 1, 0, 0, 0, 0, 0x61, 0x62}
	fbuRequest := []byte{3, 1, 0, 0, 0, 0, 0, 0, 0, 0}
	out := r.Push(concatBytes(keyEvent, fbuRequest))
	if got := flatten(out); !bytes.Equal(got, concatBytes(keyEvent, fbuRequest)) {
		t.Fatalf("passthrough = %x", got)
	}
	// The rewriter is still armed afterwards.
	if got := flatten(r.Push(clientSetEncodings)); !bytes.Equal(got, expectedRewritten) {
		t.Fatalf("rewrite after passthrough = %x", got)
	}
}

func TestRewriterFailsOpenOnUnknownType(t *testing.T) {
	r := vncbridge.NewSetEncodingsRewriter([]int32{16})
	unknown := []byte{0x77, 0x01, 0x02, 0x03, 0x04}
	out := r.Push(unknown)
	if got := flatten(out); !bytes.Equal(got, unknown) {
		t.Fatalf("unknown message = %x, want verbatim", got)
	}
	// After fail-open everything passes verbatim, including SetEncodings.
	out = r.Push(clientSetEncodings)
	if got := flatten(out); !bytes.Equal(got, clientSetEncodings) {
		t.Fatalf("post-fail-open = %x, want verbatim", got)
	}
}

// TestBridgeSetEncodingsPassthrough covers the TS "no preference" case end to
// end: without Options.Encodings the client byte stream flows untouched.
func TestBridgeSetEncodingsPassthrough(t *testing.T) {
	serverInit := buildServerInit(800, 600, "enc-auto")
	upstream := make(chan net.Conn, 1)
	port := startMockServer(t, noneServer(serverInit, upstream))
	bridge := startBridge(t, vncbridge.Options{Host: "127.0.0.1", Port: port})

	client := dialNoVnc(t, bridge.WSPort)
	client.expectHandshake(serverInit)
	serverConn := <-upstream

	client.send(clientSetEncodings)
	got := make([]byte, len(clientSetEncodings))
	serverConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(serverConn, got); err != nil {
		t.Fatalf("server read: %v", err)
	}
	if !bytes.Equal(got, clientSetEncodings) {
		t.Fatalf("passthrough = %x, want %x", got, clientSetEncodings)
	}

	client.conn.Close()
	client.awaitClose()
}
