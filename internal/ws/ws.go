// Package ws is RFC 6455 for the room map's socket: [Accept] turns an HTTP
// request into a WebSocket, [Dial] is the client half for tests, and a [Conn]
// moves binary messages over either.
//
// It is written by hand because the standard library has no WebSocket, and
// writing the server half is the road patterns/wasm-client.md names first. It
// is adapted from game-lab's internal/ws, trimmed to what this app sends. It
// never compiles for the browser, which brings its own WebSocket, so it costs
// the module nothing (deps_test.go keeps net/http out of the browser packages).
//
// A Conn speaks binary messages only. A text frame closes the connection with
// 1003, a broken frame with 1002, and one that claims more bytes than
// [Conn.MaxMessage] allows with 1009 — before anything is allocated for it.
// Read from one goroutine; Write, Ping and Close may be called from any.
package ws

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// The close codes this package sends and reports, from RFC 6455 section 7.4.
const (
	CloseNormal        uint16 = 1000
	CloseGoingAway     uint16 = 1001
	CloseProtocolError uint16 = 1002
	CloseUnsupported   uint16 = 1003
	CloseNoStatus      uint16 = 1005
	CloseBadPayload    uint16 = 1007
	ClosePolicy        uint16 = 1008
	CloseTooLarge      uint16 = 1009
	// CloseTryAgain is 1013. It says the peer did nothing wrong and should
	// come back: a server too busy to keep talking now, or a client that
	// fell behind what the server was sending it. ClosePolicy is the other
	// answer — the peer broke a rule and would break it again — so the two
	// must not be spelled the same, because a client decides whether to
	// dial again by the code.
	CloseTryAgain uint16 = 1013
)

// DefaultMaxMessage is the largest message a Conn accepts when
// [Conn.MaxMessage] is zero: one mebibyte.
const DefaultMaxMessage = 1 << 20

// guid is the string RFC 6455 appends to the client's key before hashing it.
// SHA-1 is the RFC's choice, not a security one: the handshake proves the
// peer speaks WebSocket, nothing more.
const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// ErrClosed is returned by Read and Write on a connection that has already
// closed.
var ErrClosed = errors.New("ws: connection closed")

// CloseError is how a connection ended: the code and reason the peer sent, or
// the ones this side sent when the peer broke the protocol. [Conn.Read]
// returns it when the socket closes.
type CloseError struct {
	Code   uint16
	Reason string
}

// Error names the code, and the reason when there is one.
func (e *CloseError) Error() string {
	if e.Reason == "" {
		return "ws: closed with " + strconv.Itoa(int(e.Code))
	}
	return "ws: closed with " + strconv.Itoa(int(e.Code)) + ": " + e.Reason
}

// The frame opcodes.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// controlMax is the largest control-frame payload the RFC allows.
const controlMax = 125

// Conn is one WebSocket. Zero is not usable; [Accept] and [Dial] make one.
// Set the fields below before the first Read or Write and leave them
// alone after: nothing here guards them, and Read has a goroutine of its own.
type Conn struct {
	// MaxMessage caps a message, fragments included, in bytes. A frame
	// claiming more closes the connection with 1009 before the bytes are read
	// or space is made for them. 0 means [DefaultMaxMessage].
	MaxMessage int
	// ReadTimeout is how long Read waits for the next frame before giving
	// up, or 0 to wait for ever. A server sets it to twice its ping interval,
	// so a peer that has stopped answering is found rather than kept. Every
	// frame refreshes it, a pong included, so what it bounds is silence on
	// the wire, not how long a peer may go without sending a message.
	ReadTimeout time.Duration
	// WriteTimeout bounds each Write, Ping and Close, or 0 for no bound.
	WriteTimeout time.Duration

	nc     net.Conn
	br     *bufio.Reader
	server bool // the peer's frames are masked and ours are not

	// Reassembly of a fragmented message, owned by Read, and the header
	// bytes it reads into: a slice of a local array handed to an interface
	// escapes, and three of those a frame is three allocations for nothing.
	frag []byte
	hdr  [8]byte

	wmu       sync.Mutex
	wbuf      []byte
	sentClose bool
	// closed is read by Read on its goroutine and set by whoever closes, so
	// it is the one field shared without the write lock.
	closed atomic.Bool
}

// Accept upgrades r to a WebSocket and returns the connection, or answers r
// with the status the refusal deserves — 400 for a handshake that is not one,
// 403 for an Origin that is not this host, 426 for a version that is not 13 —
// and returns why. The handler has nothing left to write in either case.
//
// The Origin check is RFC 6455's own and stays here rather than in a
// middleware: the request is a GET, which the CSRF middleware waves through.
// The returned Conn has no deadlines: the ones net/http set on the connection
// are cleared, and the caller sets ReadTimeout and WriteTimeout instead.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if r.Method != http.MethodGet || !headerHasToken(r.Header, "Connection", "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "this URL speaks WebSocket: GET it with Connection: Upgrade and Upgrade: websocket", http.StatusBadRequest)
		return nil, fmt.Errorf("ws: %q is not a websocket handshake", r.Method+" "+r.URL.Path)
	}
	if v := r.Header.Get("Sec-WebSocket-Version"); v != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "this server speaks WebSocket version 13", http.StatusUpgradeRequired)
		return nil, fmt.Errorf("ws: version %q, want 13", v)
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 16 {
		http.Error(w, "Sec-WebSocket-Key must be 16 random bytes in base64", http.StatusBadRequest)
		return nil, fmt.Errorf("ws: key %q is not 16 bytes of base64", key)
	}
	origin := r.Header.Get("Origin")
	if u, err := url.Parse(origin); origin == "" || err != nil || !strings.EqualFold(u.Host, r.Host) {
		http.Error(w, "the page asking for this socket is not served by this host", http.StatusForbidden)
		return nil, fmt.Errorf("ws: origin %q is not this host %q", origin, r.Host)
	}

	nc, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return nil, fmt.Errorf("ws: hijacking the connection: %w", err)
	}
	if err := nc.SetDeadline(time.Time{}); err != nil {
		nc.Close()
		return nil, fmt.Errorf("ws: clearing the deadlines: %w", err)
	}
	reply := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
	if _, err := io.WriteString(nc, reply); err != nil {
		nc.Close()
		return nil, fmt.Errorf("ws: writing the handshake: %w", err)
	}
	// The reader net/http handed back may already hold the first frame, so
	// frames are read through it rather than from the connection directly.
	return &Conn{nc: nc, br: brw.Reader, server: true}, nil
}

// Dial performs the client handshake over nc — a connection the caller
// already opened, so a test can hand in one end of a [net.Pipe] — asking for
// path on host, with an Origin of http://host, which is what a page served by
// that host would send. header, which may be nil, is sent along: a Cookie
// for a seat, or an Origin that replaces the default for a page served over
// https.
func Dial(nc net.Conn, host, path string, header http.Header) (*Conn, error) {
	// host and path go into the request line unescaped, so a newline in
	// either would let a caller write a request of its own.
	if strings.ContainsAny(host, "\r\n") || strings.ContainsAny(path, "\r\n") {
		return nil, fmt.Errorf("ws: host %q and path %q must hold no newline", host, path)
	}
	var nonce [16]byte
	rand.Read(nonce[:]) // crypto/rand.Read never fails
	key := base64.StdEncoding.EncodeToString(nonce[:])
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n"
	if header.Get("Origin") == "" {
		req += "Origin: http://" + host + "\r\n"
	}
	var extra strings.Builder
	if err := header.Write(&extra); err != nil {
		return nil, fmt.Errorf("ws: writing the headers: %w", err)
	}
	req += extra.String() + "\r\n"
	if _, err := io.WriteString(nc, req); err != nil {
		return nil, fmt.Errorf("ws: writing the handshake: %w", err)
	}
	br := bufio.NewReader(nc)
	res, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, fmt.Errorf("ws: reading the handshake: %w", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("ws: handshake answered %s, want 101", res.Status)
	}
	if got := res.Header.Get("Sec-WebSocket-Accept"); got != acceptKey(key) {
		return nil, fmt.Errorf("ws: handshake accept key %q is not the one for this key", got)
	}
	return &Conn{nc: nc, br: br, server: false}, nil
}

// acceptKey is the answer to a client's key: the base64 of the SHA-1 of the
// key with the RFC's GUID appended. The RFC's own example is the test vector.
func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + guid))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// headerHasToken reports whether a comma-separated header lists token,
// ignoring case: browsers send "Connection: keep-alive, Upgrade".
func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for t := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// Read returns the next binary message, fragments reassembled. A text
// message closes the connection with 1003. Pings are
// answered and pongs swallowed on the way. It returns a [*CloseError] once
// the connection is closed — by the peer, or by this side because the peer
// broke a rule — and [ErrClosed] after a Close of our own. A read that ends
// before the frame does gives the connection's own error back: a timeout, or
// [io.ErrUnexpectedEOF] for a peer that hung up mid-frame.
func (c *Conn) Read() ([]byte, error) {
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		if op == opText {
			return nil, c.fail(CloseUnsupported, "text frames are not spoken here")
		}
		switch op {
		case opBinary:
			if c.frag != nil {
				return nil, c.fail(CloseProtocolError, "a new message inside a fragmented one")
			}
			if fin {
				return payload, nil
			}
			c.frag = payload
		case opContinuation:
			if c.frag == nil {
				return nil, c.fail(CloseProtocolError, "a continuation with nothing to continue")
			}
			c.frag = append(c.frag, payload...)
			if fin {
				msg := c.frag
				c.frag = nil
				return msg, nil
			}
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
		case opPong:
			// A pong answers a ping this side sent. Arriving is the whole
			// message; the read deadline it refreshed is what it was for.
		case opClose:
			return nil, c.peerClosed(payload)
		default:
			return nil, c.fail(CloseProtocolError, "opcode "+strconv.Itoa(int(op))+" is not one")
		}
	}
}

// readFrame reads one frame's header and payload, checking every rule that
// can be checked before the payload is read: the reserved bits, the mask, the
// size, and the control-frame limits.
func (c *Conn) readFrame() (fin bool, op byte, payload []byte, err error) {
	if c.closed.Load() {
		return false, 0, nil, ErrClosed
	}
	if c.ReadTimeout > 0 {
		if err := c.nc.SetReadDeadline(time.Now().Add(c.ReadTimeout)); err != nil {
			return false, 0, nil, err
		}
	}
	hdr := c.hdr[:2]
	if _, err := io.ReadFull(c.br, hdr); err != nil {
		return false, 0, nil, c.lost(err)
	}
	fin = hdr[0]&0x80 != 0
	op = hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	length := uint64(hdr[1] & 0x7F)

	if hdr[0]&0x70 != 0 {
		return false, 0, nil, c.fail(CloseProtocolError, "reserved bits set with no extension agreed")
	}
	if op&0x8 != 0 && (!fin || length > controlMax) {
		return false, 0, nil, c.fail(CloseProtocolError, "a control frame must be short and whole")
	}
	switch length {
	case 126:
		ext := c.hdr[:2]
		if _, err := io.ReadFull(c.br, ext); err != nil {
			return false, 0, nil, c.lost(err)
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := c.hdr[:8]
		if _, err := io.ReadFull(c.br, ext); err != nil {
			return false, 0, nil, c.lost(err)
		}
		length = binary.BigEndian.Uint64(ext)
		if length>>63 != 0 {
			return false, 0, nil, c.fail(CloseProtocolError, "a length with its top bit set")
		}
	}
	// A client masks and a server does not, and each side refuses the
	// other's mistake: an unmasked client frame is the RFC's one MUST-close.
	if masked != c.server {
		if c.server {
			return false, 0, nil, c.fail(CloseProtocolError, "a client frame must be masked")
		}
		return false, 0, nil, c.fail(CloseProtocolError, "a server frame must not be masked")
	}
	// The size is checked here, against the count in the header, so a frame
	// that claims a gigabyte costs a header and a close rather than a
	// gigabyte. A control frame is not part of the message and is not
	// counted against the cap: it is held to 125 bytes above, and counting
	// it would close a socket over a ping that landed inside a message
	// already near the cap.
	if op&0x8 == 0 && uint64(len(c.frag))+length > uint64(c.maxMessage()) {
		return false, 0, nil, c.fail(CloseTooLarge, "message over "+strconv.Itoa(c.maxMessage())+" bytes")
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, c.hdr[:4]); err != nil {
			return false, 0, nil, c.lost(err)
		}
		mask = [4]byte(c.hdr[:4])
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, c.lost(err)
	}
	if masked {
		unmask(payload, mask)
	}
	return fin, op, payload, nil
}

// peerClosed answers the peer's close frame with one of our own and turns it
// into the error Read returns.
func (c *Conn) peerClosed(payload []byte) error {
	code, reason := CloseNoStatus, ""
	switch {
	case len(payload) == 1:
		return c.fail(CloseProtocolError, "a close frame with one byte of status")
	case len(payload) >= 2:
		code = binary.BigEndian.Uint16(payload)
		reason = string(payload[2:])
		if !validCloseCode(code) {
			return c.fail(CloseProtocolError, "close code "+strconv.Itoa(int(code))+" is not one")
		}
		if !utf8.ValidString(reason) {
			return c.fail(CloseBadPayload, "a close reason that is not UTF-8")
		}
	}
	echo := CloseNormal
	if len(payload) >= 2 {
		echo = code
	}
	c.wmu.Lock()
	if !c.sentClose {
		c.sentClose = true
		// Best effort: the socket closes either way, and the peer that sent
		// this close frame may already be gone.
		_ = c.writeFrameLocked(opClose, closePayload(echo, ""))
	}
	c.closed.Store(true)
	c.nc.Close()
	c.wmu.Unlock()
	return &CloseError{Code: code, Reason: reason}
}

// validCloseCode reports whether a peer may send code, per section 7.4.
func validCloseCode(code uint16) bool {
	switch {
	// 1007 to 1014, not 1011: 1012 restart, 1013 try again and 1014 bad
	// gateway were registered with IANA after RFC 6455 named the first
	// eleven, and a peer may send all three. Refusing one is how a close
	// that means "come back" turns into a protocol error, which is a
	// reconnect loop rather than a reconnect.
	case code >= 1000 && code <= 1003, code >= 1007 && code <= 1014, code >= 3000 && code <= 4999:
		return true
	}
	return false
}

// fail closes the connection because the peer broke a rule: a close frame
// with the code, best effort, then the socket. The error names the rule.
func (c *Conn) fail(code uint16, reason string) error {
	c.close(code, reason)
	return &CloseError{Code: code, Reason: reason}
}

// lost turns a read that ended early into the error Read returns. A timeout
// or a hang-up is not a protocol violation, so no close frame is owed; the
// socket is closed so nothing else waits on it.
func (c *Conn) lost(err error) error {
	if c.closed.Load() {
		return ErrClosed
	}
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	c.wmu.Lock()
	c.closed.Store(true)
	c.nc.Close()
	c.wmu.Unlock()
	return err
}

// Write sends p as one binary message.
func (c *Conn) Write(p []byte) error { return c.writeFrame(opBinary, p) }

// Ping sends a ping carrying p. A peer that is still there answers with a
// pong, which Read swallows; the point is the read deadline that arriving
// refreshes. A payload too long for a control frame is refused rather than
// written, because the frame it would make is one the peer must close on.
func (c *Conn) Ping(p []byte) error {
	if len(p) > controlMax {
		return fmt.Errorf("ws: a ping of %d bytes: send at most %d, which is all a control frame carries", len(p), controlMax)
	}
	return c.writeFrame(opPing, p)
}

// Close sends a close frame with code and reason, if none has been sent yet,
// and closes the socket. It does not wait for the peer's own close frame: the
// peer has the code, which is what a close frame is for, and the socket
// closing behind it ends anything the peer was still sending. Safe to call
// more than once and from any goroutine; a Read in progress returns
// [ErrClosed].
func (c *Conn) Close(code uint16, reason string) error {
	c.close(code, reason)
	return nil
}

func (c *Conn) close(code uint16, reason string) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if !c.sentClose && !c.closed.Load() {
		c.sentClose = true
		// Best effort: a peer that has already hung up gets no close frame,
		// and there is nothing left to tell it.
		_ = c.writeFrameLocked(opClose, closePayload(code, reason))
	}
	c.closed.Store(true)
	c.nc.Close()
}

// closePayload is a close frame's body: the code, then the reason cut to fit
// a control frame on a rune boundary, since a reason that is not UTF-8 is one
// the peer must close on.
func closePayload(code uint16, reason string) []byte {
	if len(reason) > controlMax-2 {
		n := controlMax - 2
		for n > 0 && !utf8.RuneStart(reason[n]) {
			n--
		}
		reason = reason[:n]
	}
	return append(binary.BigEndian.AppendUint16(nil, code), reason...)
}

func (c *Conn) writeFrame(op byte, p []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed.Load() || c.sentClose {
		return ErrClosed
	}
	return c.writeFrameLocked(op, p)
}

// writeFrameLocked writes one whole frame: the header, the mask when this is
// the client, and the payload, in one write from a buffer the Conn keeps.
func (c *Conn) writeFrameLocked(op byte, p []byte) error {
	c.wbuf = c.appendFrame(c.wbuf[:0], op, p)
	return c.writeLocked(c.wbuf)
}

// writeLocked is one write of b to the socket, under the write deadline.
func (c *Conn) writeLocked(b []byte) error {
	if c.WriteTimeout > 0 {
		if err := c.nc.SetWriteDeadline(time.Now().Add(c.WriteTimeout)); err != nil {
			return err
		}
	}
	_, err := c.nc.Write(b)
	return err
}

// appendFrame appends one frame to b: the header, the mask when this is the
// client, and the payload, masked eight bytes at a time.
func (c *Conn) appendFrame(b []byte, op byte, p []byte) []byte {
	b = append(b, 0x80|op)
	maskBit := byte(0)
	if !c.server {
		maskBit = 0x80
	}
	switch n := len(p); {
	case n <= 125:
		b = append(b, maskBit|byte(n))
	case n <= 1<<16-1:
		b = append(b, maskBit|126)
		b = binary.BigEndian.AppendUint16(b, uint16(n))
	default:
		b = append(b, maskBit|127)
		b = binary.BigEndian.AppendUint64(b, uint64(n))
	}
	if c.server {
		return append(b, p...)
	}
	var mask [4]byte
	rand.Read(mask[:])
	b = append(b, mask[:]...)
	start := len(b)
	b = append(b, p...)
	unmask(b[start:], mask)
	return b
}

// unmask applies the RFC's XOR mask to p in place, eight bytes at a time and
// then the rest. It is its own inverse, so the writer masks with it too.
func unmask(p []byte, mask [4]byte) {
	key := binary.LittleEndian.Uint32(mask[:])
	key64 := uint64(key)<<32 | uint64(key)
	for len(p) >= 8 {
		binary.LittleEndian.PutUint64(p, binary.LittleEndian.Uint64(p)^key64)
		p = p[8:]
	}
	for i := range p {
		p[i] ^= byte(key >> (8 * (i & 3)))
	}
}

func (c *Conn) maxMessage() int {
	if c.MaxMessage <= 0 {
		return DefaultMaxMessage
	}
	return c.MaxMessage
}
