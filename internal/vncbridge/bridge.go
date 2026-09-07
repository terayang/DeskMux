// Local WebSocket <-> TCP bridge for noVNC (Go port of src/main/vncBridge.ts).
//
// The bridge terminates the RFB security handshake locally: on each WebSocket
// connection it opens a TCP connection to the VNC target, authenticates
// (Apple DH / VNC auth / None) via rfb.PerformHandshake, then replays a
// synthesized handshake to the noVNC client presenting security type None:
//
//	bridge -> noVNC:  'RFB 003.008\n'
//	noVNC  -> bridge: client version (validated, value ignored)
//	bridge -> noVNC:  security types [1] = [None]
//	noVNC  -> bridge: selected type (must be 1)
//	bridge -> noVNC:  SecurityResult OK
//	noVNC  -> bridge: ClientInit
//	bridge -> noVNC:  the target's real ServerInit, verbatim
//
// Afterwards frames are relayed transparently in both directions — with one
// exception: when Options.Encodings is set, the client->server direction goes
// through the SetEncodings rewriter (see rewriter.go).
//
// Single-client semantics: a bridge instance serves ONE VNC session. A second
// WebSocket client connecting while one is active is rejected with close code
// 1013 and a 'busy' payload (no queueing). Failures before the pipe phase
// close the socket with a structured JSON VncBridgeError reason (see
// src/shared/vnc.ts): auth 4001 / connection 4002 / protocol 4003 /
// timeout 4008 / busy 1013.
package vncbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"deskmux/internal/rfb"
)

const defaultTimeout = 10 * time.Second

// closeCodes mirrors VNC_BRIDGE_CLOSE_CODES in src/shared/vnc.ts. 4000-4999
// is the WebSocket private-use range; 1013 is RFC 6455 "Try Again Later".
var closeCodes = map[string]int{
	"auth":       4001,
	"connection": 4002,
	"protocol":   4003,
	"timeout":    4008,
	"busy":       1013,
}

// Options configures a Bridge.
type Options struct {
	Host string
	Port int
	// Credentials for the upstream RFB handshake (types 2 and 30).
	Username string
	Password string
	// Timeout is the TCP connect + RFB handshake deadline (default 10s).
	Timeout time.Duration
	// Encodings is the pixel-encoding preference (RFB encoding numbers, e.g.
	// [16] = ZRLE). When non-nil, client SetEncodings messages are rewritten
	// to prefer these encodings; nil = passthrough without rewriting.
	Encodings []int32
}

// Bridge is a running WebSocket <-> TCP VNC bridge.
type Bridge struct {
	// WSPort is the loopback port the WebSocket server listens on (random).
	WSPort int

	opts     Options
	server   *http.Server
	upgrader websocket.Upgrader

	mu       sync.Mutex
	active   *websocket.Conn
	clients  map[*websocket.Conn]struct{}
	isClosed bool
}

// Start launches a bridge on 127.0.0.1 with a random port.
func Start(opts Options) (*Bridge, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := &Bridge{
		WSPort:  listener.Addr().(*net.TCPAddr).Port,
		opts:    opts,
		clients: make(map[*websocket.Conn]struct{}),
		upgrader: websocket.Upgrader{
			// noVNC offers the 'binary' subprotocol; echo it back when requested.
			Subprotocols: []string{"binary"},
			CheckOrigin:  func(*http.Request) bool { return true },
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", b.handleWebSocket)
	b.server = &http.Server{Handler: mux}
	go func() { _ = b.server.Serve(listener) }()
	return b, nil
}

// Close terminates clients and closes the server.
func (b *Bridge) Close() error {
	b.mu.Lock()
	if b.isClosed {
		b.mu.Unlock()
		return nil
	}
	b.isClosed = true
	clients := make([]*websocket.Conn, 0, len(b.clients))
	for c := range b.clients {
		clients = append(clients, c)
	}
	b.mu.Unlock()
	for _, c := range clients {
		c.Close()
	}
	return b.server.Close()
}

func (b *Bridge) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := b.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // upgrade already replied with an HTTP error
	}
	b.mu.Lock()
	if b.active != nil {
		b.mu.Unlock()
		writer := &wsWriter{ws: ws}
		writer.closeWithError("busy", errors.New("bridge already serves another client"))
		ws.Close()
		return
	}
	b.active = ws
	b.clients[ws] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.clients, ws)
		if b.active == ws {
			b.active = nil
		}
		b.mu.Unlock()
	}()
	b.serveClient(ws)
}

// serveClient handles one WebSocket client from connect through auth to the
// pipe phase.
func (b *Bridge) serveClient(ws *websocket.Conn) {
	timeout := b.opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	writer := &wsWriter{ws: ws}
	reader := newWSReader(ws)
	defer reader.stop()
	defer ws.Close()

	tcp, err := dialTarget(b.opts.Host, b.opts.Port, timeout)
	if err != nil {
		writer.closeWithError(classify(err), err)
		return
	}
	defer tcp.Close()

	serverInit, err := rfb.PerformHandshake(tcp, rfb.HandshakeOptions{
		Username: b.opts.Username,
		Password: b.opts.Password,
		Timeout:  timeout,
	})
	if err != nil {
		writer.closeWithError(classify(err), err)
		return
	}
	if err := presentServerSideHandshake(reader, writer, serverInit.Raw); err != nil {
		writer.closeWithError(classify(err), err)
		return
	}

	// Pipe phase. The client may have coalesced ClientInit with its first
	// protocol message; forward any such leftover before switching modes.
	var rewriter *SetEncodingsRewriter
	if b.opts.Encodings != nil {
		rewriter = NewSetEncodingsRewriter(b.opts.Encodings)
	}
	forward := func(chunk []byte) bool {
		if rewriter != nil {
			for _, out := range rewriter.Push(chunk) {
				if _, err := tcp.Write(out); err != nil {
					return false
				}
			}
			return true
		}
		_, err := tcp.Write(chunk)
		return err == nil
	}
	if leftover := reader.drainBuffer(); len(leftover) > 0 {
		if !forward(leftover) {
			return
		}
	}

	tcpDone := make(chan struct{})
	go func() {
		defer close(tcpDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := tcp.Read(buf)
			if n > 0 {
				if writeErr := writer.send(buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WS -> TCP until either side drops.
	open := true
	for open {
		select {
		case <-tcpDone:
			open = false
		case chunk := <-reader.messages:
			if !forward(chunk) {
				open = false
			}
		case <-reader.errs:
			open = false
		}
	}
	ws.Close() // unblock the WS read pump; the deferred close is idempotent
}

var clientBannerRe = regexp.MustCompile(`^RFB \d{3}\.\d{3}\n$`)

// presentServerSideHandshake replays the synthesized None-security handshake
// towards the noVNC client.
func presentServerSideHandshake(reader *wsReader, writer *wsWriter, serverInitRaw []byte) error {
	if err := writer.send([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	banner, err := reader.read(12)
	if err != nil {
		return err
	}
	if !clientBannerRe.MatchString(string(banner)) {
		return &rfb.RfbProtocolError{
			Message: fmt.Sprintf("bridge client sent invalid RFB version %q", string(banner)),
		}
	}
	if err := writer.send([]byte{1, rfb.SecTypeNone}); err != nil { // exactly one offered type: None
		return err
	}
	selection, err := reader.read(1)
	if err != nil {
		return err
	}
	if selection[0] != rfb.SecTypeNone {
		return &rfb.RfbProtocolError{
			Message: fmt.Sprintf("bridge client selected unsupported security type %d", selection[0]),
		}
	}
	if err := writer.send([]byte{0, 0, 0, 0}); err != nil { // SecurityResult: OK
		return err
	}
	if _, err := reader.read(1); err != nil { // ClientInit (shared flag, value ignored)
		return err
	}
	return writer.send(serverInitRaw)
}

func dialTarget(host string, port int, timeout time.Duration) (net.Conn, error) {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, &rfb.RfbTimeoutError{
				Message: fmt.Sprintf("connect to %s:%d timed out after %d ms", host, port, timeout.Milliseconds()),
			}
		}
		return nil, &rfb.RfbConnectionError{Message: err.Error()}
	}
	return conn, nil
}

// classify maps an error onto a VncBridgeErrorKind (src/shared/vnc.ts).
func classify(err error) string {
	var authErr *rfb.RfbAuthError
	var timeoutErr *rfb.RfbTimeoutError
	var protocolErr *rfb.RfbProtocolError
	switch {
	case errors.As(err, &authErr):
		return "auth"
	case errors.As(err, &timeoutErr):
		return "timeout"
	case errors.As(err, &protocolErr):
		return "protocol"
	default:
		// RfbConnectionError, ECONNREFUSED and other socket errors
		return "connection"
	}
}

// errorPayload is the structured VncBridgeError sent as the WS close reason.
type errorPayload struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// wsWriter serializes writes to a client WebSocket (the pipe goroutine and
// the handshake/error paths may both write).
type wsWriter struct {
	mu sync.Mutex
	ws *websocket.Conn
}

func (w *wsWriter) send(data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ws.WriteMessage(websocket.BinaryMessage, data)
}

// closeWithError closes the WebSocket with the mapped code and a JSON
// VncBridgeError reason.
func (w *wsWriter) closeWithError(kind string, err error) {
	payload := closePayload(kind, err.Error())
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.ws.WriteControl(websocket.CloseMessage, payload, time.Now().Add(2*time.Second))
}

// closePayload builds the close frame for a structured error. WS close
// reasons are limited to 123 UTF-8 bytes (125 minus the 2-byte code), so the
// message is first truncated to 80 runes (parity with the TS 80-char slice)
// and then rune-by-rune until the whole JSON payload fits.
func closePayload(kind, message string) []byte {
	runes := []rune(message)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	message = string(runes)
	for {
		payload, _ := json.Marshal(errorPayload{Kind: kind, Message: message})
		if len(payload) <= 123 || message == "" {
			return websocket.FormatCloseMessage(closeCodes[kind], string(payload))
		}
		runes := []rune(message)
		message = string(runes[:len(runes)-1])
	}
}

// wsReader adapts the WebSocket message stream to exact-size byte reads for
// the handshake replay, mirroring the TS ChunkReader.
type wsReader struct {
	messages chan []byte
	errs     chan error
	done     chan struct{}
	buf      []byte
}

func newWSReader(ws *websocket.Conn) *wsReader {
	r := &wsReader{
		messages: make(chan []byte, 32),
		errs:     make(chan error, 1),
		done:     make(chan struct{}),
	}
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				select {
				case r.errs <- err:
				case <-r.done:
				}
				return
			}
			select {
			case r.messages <- data:
			case <-r.done:
				return
			}
		}
	}()
	return r
}

// read consumes exactly n bytes from the message stream.
func (r *wsReader) read(n int) ([]byte, error) {
	for len(r.buf) < n {
		select {
		case chunk := <-r.messages:
			r.buf = append(r.buf, chunk...)
		case err := <-r.errs:
			return nil, err
		}
	}
	out := make([]byte, n)
	copy(out, r.buf[:n])
	r.buf = r.buf[n:]
	return out, nil
}

// drainBuffer returns and clears buffered-but-unread bytes (post-handshake
// flush).
func (r *wsReader) drainBuffer() []byte {
	rest := r.buf
	r.buf = nil
	return rest
}

func (r *wsReader) stop() {
	select {
	case <-r.done:
	default:
		close(r.done)
	}
}
