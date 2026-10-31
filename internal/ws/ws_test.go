package ws_test

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andygeiss/baseline-reference/v5/internal/ws"
)

// The RFC's own handshake example, section 1.3: this key gets this answer.
const (
	rfcKey    = "dGhlIHNhbXBsZSBub25jZQ=="
	rfcAccept = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
)

// pipeListener hands one end of a net.Pipe to an http.Server for every dial,
// so a handshake runs through the real server and its hijack without a port.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

func (l *pipeListener) dial(t *testing.T) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	select {
	case l.conns <- server:
	case <-l.done:
		t.Fatal("the server is gone")
	}
	return client
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// serve runs h on a pipe listener until the test ends, under the timeouts
// the real server sets, so a test can show the socket outlives them.
func serve(t *testing.T, h http.Handler) *pipeListener {
	t.Helper()
	ln := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       100 * time.Millisecond,
		WriteTimeout:      100 * time.Millisecond,
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln
}

// echo accepts a socket and sends every message back until the peer goes.
// Each accepted connection's error lands on errs.
func echo(errs chan<- error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := ws.Accept(w, r)
		if err != nil {
			errs <- err
			return
		}
		c.MaxMessage = 16
		for {
			msg, err := c.Read()
			if err != nil {
				errs <- err
				return
			}
			if err := c.Write(msg); err != nil {
				errs <- err
				return
			}
		}
	})
}

// handshake writes a raw handshake and returns the status line's code and the
// accept header.
func handshake(t *testing.T, nc net.Conn, headers string) (int, string, *bufio.Reader) {
	t.Helper()
	go io.WriteString(nc, "GET /ws HTTP/1.1\r\nHost: chat.test\r\n"+headers+"\r\n")
	br := bufio.NewReader(nc)
	res, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	return res.StatusCode, res.Header.Get("Sec-WebSocket-Accept"), br
}

func TestTheHandshakeAnswersTheRFCsExample(t *testing.T) {
	t.Parallel()
	ln := serve(t, echo(make(chan error, 4)))
	code, accept, _ := handshake(t, ln.dial(t), "Upgrade: websocket\r\nConnection: keep-alive, Upgrade\r\n"+
		"Sec-WebSocket-Key: "+rfcKey+"\r\nSec-WebSocket-Version: 13\r\nOrigin: http://chat.test\r\n")
	if code != http.StatusSwitchingProtocols || accept != rfcAccept {
		t.Errorf("got %d with accept %q, want 101 with %q", code, accept, rfcAccept)
	}
}

// The tier-1 rule (patterns/wasm-client.md rule 5): the CSRF middleware waves a
// GET through, so this check is the only thing between another site's page and
// a socket opened with the reader's cookie.
func TestTheHandshakeRefusesAnotherOrigin(t *testing.T) {
	t.Parallel()
	for name, origin := range map[string]string{
		"another site":             "Origin: https://evil.example\r\n",
		"no origin at all":         "",
		"this host as a subdomain": "Origin: http://chat.test.evil.example\r\n",
		"not a URL":                "Origin: ::\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			errs := make(chan error, 1)
			ln := serve(t, echo(errs))
			code, _, _ := handshake(t, ln.dial(t), "Upgrade: websocket\r\nConnection: Upgrade\r\n"+
				"Sec-WebSocket-Key: "+rfcKey+"\r\nSec-WebSocket-Version: 13\r\n"+origin)
			if code != http.StatusForbidden {
				t.Fatalf("got %d, want 403", code) // past here a socket is open and errs never fills
			}
			if err := <-errs; err == nil || !strings.Contains(err.Error(), "origin") {
				t.Errorf("Accept said %v, want it to name the origin", err)
			}
		})
	}
}

func TestTheHandshakeRefusesWhatIsNotOne(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		headers string
		want    int
	}{
		"no upgrade":  {"Sec-WebSocket-Key: " + rfcKey + "\r\nSec-WebSocket-Version: 13\r\nOrigin: http://chat.test\r\n", http.StatusBadRequest},
		"version 8":   {"Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + rfcKey + "\r\nSec-WebSocket-Version: 8\r\nOrigin: http://chat.test\r\n", http.StatusUpgradeRequired},
		"a short key": {"Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: c2hvcnQ=\r\nSec-WebSocket-Version: 13\r\nOrigin: http://chat.test\r\n", http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ln := serve(t, echo(make(chan error, 1)))
			if code, _, _ := handshake(t, ln.dial(t), tt.headers); code != tt.want {
				t.Errorf("got %d, want %d", code, tt.want)
			}
		})
	}
}

// dial opens a socket to the echo server through the client half.
func dial(t *testing.T, errs chan error) *ws.Conn {
	t.Helper()
	ln := serve(t, echo(errs))
	c, err := ws.Dial(ln.dial(t), "chat.test", "/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// A pipe write blocks until the other end reads, so a bounded write is
	// what keeps a test whose server already stopped reading from hanging.
	c.WriteTimeout = time.Second
	t.Cleanup(func() { c.Close(ws.CloseNormal, "") })
	return c
}

// Rule 8: the server's ReadTimeout and WriteTimeout are 100 ms here, and they
// stay on the connection after Hijack unless Accept clears them. A socket that
// still answers after three times that has had them cleared.
func TestTheSocketOutlivesTheServersTimeouts(t *testing.T) {
	t.Parallel()
	c := dial(t, make(chan error, 1))
	time.Sleep(300 * time.Millisecond)
	if err := c.Write([]byte("still here")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, err := c.Read(); err != nil || string(got) != "still here" {
		t.Fatalf("read %q, %v; want the echo", got, err)
	}
}

// Rule 7: a message over the cap closes with 1009, read from the header
// before anything is allocated for it.
func TestAMessageOverTheCapClosesWith1009(t *testing.T) {
	t.Parallel()
	errs := make(chan error, 1)
	c := dial(t, errs)
	if err := c.Write(make([]byte, 17)); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The client reads first: on a pipe the server's close frame waits for it.
	var ce *ws.CloseError
	if _, err := c.Read(); !errors.As(err, &ce) || ce.Code != ws.CloseTooLarge {
		t.Errorf("the client read %v, want the 1009 close", err)
	}
	if err := <-errs; !errors.As(err, &ce) || ce.Code != ws.CloseTooLarge {
		t.Errorf("the server ended with %v, want 1009", err)
	}
}

// Rule 7 caps a message, not a frame: two fragments of ten bytes each are
// under the 16-byte cap alone and over it together, and the second one closes
// the socket with 1009 before its payload is read.
func TestAFragmentedMessageOverTheCapClosesWith1009(t *testing.T) {
	t.Parallel()
	errs := make(chan error, 1)
	ln := serve(t, echo(errs))
	nc := ln.dial(t)
	code, _, br := handshake(t, nc, "Upgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: "+rfcKey+"\r\nSec-WebSocket-Version: 13\r\nOrigin: http://chat.test\r\n")
	if code != http.StatusSwitchingProtocols {
		t.Fatalf("handshake answered %d", code)
	}
	// Masked with a zero key, so the payload goes as written.
	first := append([]byte{0x02, 0x80 | 10, 0, 0, 0, 0}, make([]byte, 10)...) // binary, not FIN
	last := append([]byte{0x80, 0x80 | 10, 0, 0, 0, 0}, make([]byte, 10)...)  // continuation, FIN
	go nc.Write(append(first, last...))
	go io.Copy(io.Discard, br) // the server's close frame waits for a reader
	var ce *ws.CloseError
	select {
	case err := <-errs:
		if !errors.As(err, &ce) || ce.Code != ws.CloseTooLarge {
			t.Errorf("the server ended with %v, want 1009", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the server took the 20-byte message: the cap counts a frame, not the message")
	}
}

// A browser masks every frame; an unmasked one is the RFC's one MUST-close.
func TestAnUnmaskedClientFrameClosesWith1002(t *testing.T) {
	t.Parallel()
	errs := make(chan error, 1)
	ln := serve(t, echo(errs))
	nc := ln.dial(t)
	code, _, br := handshake(t, nc, "Upgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: "+rfcKey+"\r\nSec-WebSocket-Version: 13\r\nOrigin: http://chat.test\r\n")
	if code != http.StatusSwitchingProtocols {
		t.Fatalf("handshake answered %d", code)
	}
	go nc.Write([]byte{0x82, 0x01, 'x'}) // FIN binary, unmasked, one byte
	go io.Copy(io.Discard, br)           // the server's close frame waits for a reader
	var ce *ws.CloseError
	if err := <-errs; !errors.As(err, &ce) || ce.Code != ws.CloseProtocolError {
		t.Errorf("the server ended with %v, want 1002", err)
	}
}

// The close a server sends on shutdown carries 1001, and the client reads it
// as that code.
func TestCloseCarriesItsCode(t *testing.T) {
	t.Parallel()
	ln := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := ws.Accept(w, r)
		if err != nil {
			return
		}
		c.Close(ws.CloseGoingAway, "server shutting down")
	}))
	c, err := ws.Dial(ln.dial(t), "chat.test", "/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	var ce *ws.CloseError
	if _, err := c.Read(); !errors.As(err, &ce) || ce.Code != ws.CloseGoingAway {
		t.Errorf("read %v, want 1001", err)
	}
	if got := binary.BigEndian.AppendUint16(nil, ws.CloseGoingAway); got[0] != 0x03 || got[1] != 0xE9 {
		t.Errorf("1001 encodes as %x", got)
	}
}

// capped accepts a socket that allows n frames, then reads until it ends.
// The connection's error lands on errs.
func capped(errs chan<- error, n int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := ws.Accept(w, r)
		if err != nil {
			errs <- err
			return
		}
		frames := 0
		c.AllowFrame = func() bool { frames++; return frames <= n }
		for {
			if _, err := c.Read(); err != nil {
				errs <- err
				return
			}
		}
	})
}

// Rule 7 caps frames a second, not messages: a peer that sends only pings, or
// one message in empty fragments, sends no message for a message cap to
// count. Each frame is asked about, and the first refused closes with 1008.
func TestTheFrameCapCountsEveryFrame(t *testing.T) {
	t.Parallel()
	// Masked with a zero key, so the payload goes as written.
	ping := []byte{0x89, 0x80, 0, 0, 0, 0}         // FIN ping, empty
	open := []byte{0x02, 0x80, 0, 0, 0, 0}         // binary, not FIN, empty
	continuation := []byte{0x00, 0x80, 0, 0, 0, 0} // continuation, not FIN, empty
	for name, frames := range map[string][][]byte{
		"pings":                  {ping, ping, ping, ping},
		"empty continuations":    {open, continuation, continuation, continuation},
		"pings inside a message": {open, ping, continuation, ping},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			errs := make(chan error, 1)
			ln := serve(t, capped(errs, len(frames)-1))
			nc := ln.dial(t)
			code, _, br := handshake(t, nc, "Upgrade: websocket\r\nConnection: Upgrade\r\n"+
				"Sec-WebSocket-Key: "+rfcKey+"\r\nSec-WebSocket-Version: 13\r\nOrigin: http://chat.test\r\n")
			if code != http.StatusSwitchingProtocols {
				t.Fatalf("handshake answered %d", code)
			}
			var all []byte
			for _, f := range frames {
				all = append(all, f...)
			}
			go nc.Write(all)
			go io.Copy(io.Discard, br) // pongs and the close frame wait for a reader
			var ce *ws.CloseError
			select {
			case err := <-errs:
				if !errors.As(err, &ce) || ce.Code != ws.ClosePolicy {
					t.Errorf("the server ended with %v, want 1008", err)
				}
			case <-time.After(5 * time.Second):
				t.Errorf("the server took %d frames under a cap of %d", len(frames), len(frames)-1)
			}
		})
	}
}
