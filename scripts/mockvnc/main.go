// Mock VNC server + production vncbridge, for local reproduction of the
// noVNC scaling behavior without touching the real macOS Screen Sharing.
// Serves a static 1024x640 raw screen (color bands) with security type None.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"time"

	"deskmux/internal/vncbridge"
)

const (
	width  = 1024
	height = 640
)

func main() {
	// -addr lets several instances run side by side (multi-session VNC tests:
	// one bridge serves only one client, so each session needs its own).
	addr := flag.String("addr", "127.0.0.1:5901", "listen address of the mock RFB server")
	flag.Parse()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		panic(err)
	}
	bridge, err := vncbridge.Start(vncbridge.Options{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port})
	if err != nil {
		panic(err)
	}
	fmt.Printf("MOCKVNC bridge wsPort=%d\n", bridge.WSPort)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go serve(conn)
	}
}

func serve(conn net.Conn) {
	defer conn.Close()
	fail := func(err error) {
		if err != nil {
			fmt.Fprintln(os.Stderr, "mockvnc:", err)
		}
	}

	fail(write(conn, []byte("RFB 003.008\n")))
	if err := readN(conn, 12); err != nil {
		fail(err)
		return
	}
	fail(write(conn, []byte{1, 1})) // one security type: None
	if err := readN(conn, 1); err != nil {
		fail(err)
		return
	}
	fail(write(conn, []byte{0, 0, 0, 0})) // SecurityResult OK
	if err := readN(conn, 1); err != nil { // ClientInit
		fail(err)
		return
	}
	fail(write(conn, serverInit()))
	// Push one ServerCutText right after init so remote->local clipboard
	// sync can be verified independently of any client paste.
	fail(write(conn, serverCutText([]byte("srv-push-88"))))

	pixels := bandedPixels()
	for {
		header := make([]byte, 4)
		if _, err := io.ReadFull(conn, header[:1]); err != nil {
			return
		}
		switch header[0] {
		case 0: // SetPixelFormat
			if err := readN(conn, 19); err != nil {
				return
			}
		case 2: // SetEncodings
			rest := make([]byte, 3)
			if _, err := io.ReadFull(conn, rest); err != nil {
				return
			}
			n := int(binary.BigEndian.Uint16(rest[1:3]))
			if err := readN(conn, n*4); err != nil {
				return
			}
		case 3: // FramebufferUpdateRequest
			if err := readN(conn, 9); err != nil {
				return
			}
			if err := write(conn, fbu(pixels)); err != nil {
				return
			}
			// Throttle to ~10fps so the client main thread stays responsive.
			time.Sleep(100 * time.Millisecond)
			pixels = bandedPixels()
		case 4: // KeyEvent: down-flag(1) + pad(2) + keysym(4) — log and discard.
			body := make([]byte, 7)
			if _, err := io.ReadFull(conn, body); err != nil {
				return
			}
			fmt.Printf("mockvnc: KeyEvent down=%d keysym=%#x\n", body[0], binary.BigEndian.Uint32(body[3:7]))
		case 5: // PointerEvent
			if err := readN(conn, 5); err != nil {
				return
			}
		case 6: // ClientCutText: pad(3) + length(4) + text — log and echo back
			// as ServerCutText so clipboard sync can be tested end to end.
			meta := make([]byte, 7)
			if _, err := io.ReadFull(conn, meta); err != nil {
				return
			}
			n := int(binary.BigEndian.Uint32(meta[3:7]))
			text := make([]byte, n)
			if _, err := io.ReadFull(conn, text); err != nil {
				return
			}
			fmt.Printf("mockvnc: ClientCutText %q\n", text)
			if err := write(conn, serverCutText(text)); err != nil {
				return
			}
		default:
			return
		}
	}
}

func serverCutText(text []byte) []byte {
	out := make([]byte, 8+len(text))
	out[0] = 3 // ServerCutText
	binary.BigEndian.PutUint32(out[4:8], uint32(len(text)))
	copy(out[8:], text)
	return out
}

func serverInit() []byte {
	name := []byte("mockvnc")
	buf := make([]byte, 24+len(name))
	binary.BigEndian.PutUint16(buf[0:2], width)
	binary.BigEndian.PutUint16(buf[2:4], height)
	buf[4] = 32 // bits per pixel
	buf[5] = 24 // depth
	buf[6] = 0  // little endian
	buf[7] = 1  // true color
	binary.BigEndian.PutUint16(buf[8:10], 255)
	binary.BigEndian.PutUint16(buf[10:12], 255)
	binary.BigEndian.PutUint16(buf[12:14], 255)
	buf[14], buf[15], buf[16] = 16, 8, 0 // shifts
	binary.BigEndian.PutUint32(buf[20:24], uint32(len(name)))
	copy(buf[24:], name)
	return buf
}

func bandedPixels() []byte {
	return framePixels(time.Now())
}

// framePixels draws a simple fake desktop: menu bar, two windows (one slowly
// drifting), and a dock — enough motion for demo captures, since noVNC keeps
// requesting frames after each full update.
func framePixels(t time.Time) []byte {
	px := make([]byte, width*height*4)
	set := func(x, y int, r, g, b uint8) {
		i := (y*width + x) * 4
		px[i], px[i+1], px[i+2] = r, g, b
	}
	rect := func(x0, y0, w, h int, r, g, b uint8) {
		for y := y0; y < y0+h && y < height; y++ {
			for x := x0; x < x0+w && x < width; x++ {
				if x >= 0 && y >= 0 {
					set(x, y, r, g, b)
				}
			}
		}
	}
	// wallpaper + menu bar
	rect(0, 0, width, height, 0x2b, 0x3a, 0x55)
	rect(0, 0, width, 22, 0x14, 0x14, 0x14)
	// drifting window (title bar + body)
	phase := float64(t.UnixMilli()%8000) / 8000 * 2 * math.Pi
	wx := 140 + int(90*(1+math.Sin(phase)))
	rect(wx, 90, 420, 24, 0x3c, 0x3c, 0x3c)
	rect(wx, 114, 420, 260, 0xf5, 0xf5, 0xf5)
	rect(wx+16, 130, 200, 12, 0x9c, 0x9c, 0x9c)
	rect(wx+16, 152, 388, 200, 0xdd, 0xdd, 0xdd)
	// static window
	rect(620, 240, 300, 22, 0x3c, 0x3c, 0x3c)
	rect(620, 262, 300, 160, 0x1e, 0x1e, 0x1e)
	// dock
	rect(0, height-48, width, 48, 0x18, 0x18, 0x18)
	for i := 0; i < 8; i++ {
		rect(60+i*52, height-40, 36, 36, 0x4c, 0x8d, 0xff)
	}
	return px
}

func fbu(pixels []byte) []byte {
	head := make([]byte, 12)             // type(1) pad(1) nRects(2) x(2) y(2) w(2) h(2)
	binary.BigEndian.PutUint16(head[2:4], 1)
	binary.BigEndian.PutUint16(head[8:10], width)
	binary.BigEndian.PutUint16(head[10:12], height)
	enc := []byte{0, 0, 0, 0} // raw
	return append(append(head, enc...), pixels...)
}

func write(conn net.Conn, b []byte) error {
	_, err := conn.Write(b)
	return err
}

func readN(conn net.Conn, n int) error {
	_, err := io.ReadFull(conn, make([]byte, n))
	return err
}
