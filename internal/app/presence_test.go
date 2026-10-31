package app

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/andygeiss/baseline-reference/v5/internal/presence"
	"github.com/andygeiss/baseline-reference/v5/internal/ws"
)

// openSocket opens GET /ws?room=slug through the whole middleware chain, as the
// client's own cookie holder, with the Origin header given (empty means the
// page's own). It returns the handshake's error rather than failing, because
// half these tests want the refusal.
func openSocket(t *testing.T, ta *testApp, client *http.Client, slug, origin string) (*ws.Conn, error) {
	t.Helper()
	u, err := url.Parse(ta.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := client.Transport.(*http.Transport).DialContext(t.Context(), "tcp", u.Host)
	if err != nil {
		t.Fatalf("dialing the test server: %v", err)
	}
	header := http.Header{}
	for _, c := range client.Jar.Cookies(u) {
		header.Add("Cookie", c.Name+"="+c.Value)
	}
	if origin != "" {
		header.Set("Origin", origin)
	}
	conn, err := ws.Dial(nc, u.Host, "/ws?room="+url.QueryEscape(slug), header)
	if err != nil {
		nc.Close()
		return nil, err
	}
	conn.WriteTimeout = 5 * time.Second
	conn.ReadTimeout = 5 * time.Second
	t.Cleanup(func() { conn.Close(ws.CloseNormal, "") })
	return conn, nil
}

// secondPerson signs somebody else in on the same app, with a jar of their own.
func secondPerson(t *testing.T, ta *testApp, name string) *http.Client {
	t.Helper()
	c := *ta.client
	c.Jar = emptyJar(t)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ta.server.URL+"/register",
		strings.NewReader(url.Values{"name": {name}, "password": {"correct-horse"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("registering %s: %d", name, res.StatusCode)
	}
	return &c
}

// The whole loop: one person points, and the other sees them there, under the
// name their session holds — the wire carries no name up (rule 6).
func TestAPointerReachesEveryoneInTheRoom(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")
	grace := secondPerson(t, ta, "Grace")

	ada, err := openSocket(t, ta, ta.client, slug, "")
	if err != nil {
		t.Fatalf("Ada's socket: %v", err)
	}
	theirs, err := openSocket(t, ta, grace, slug, "")
	if err != nil {
		t.Fatalf("Grace's socket: %v", err)
	}
	if err := theirs.Write(presence.EncodeUp(100, 200)); err != nil {
		t.Fatalf("Grace points: %v", err)
	}
	for {
		msg, err := ada.Read()
		if err != nil {
			t.Fatalf("Ada never saw Grace point: %v", err)
		}
		seats, ok := presence.DecodeDown(msg)
		if !ok {
			t.Fatalf("Ada got a message that is not a map: %v", msg)
		}
		for _, s := range seats {
			if s.Name == "Grace" && s.X == 100 && s.Y == 200 {
				return
			}
		}
	}
}

// Rule 5, tier 1, through the real middleware chain: the CSRF protection lets
// every GET by, so the handshake's own check is the only one there is.
func TestTheSocketRefusesAnotherOrigin(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")
	_, err := openSocket(t, ta, ta.client, slug, "https://evil.example")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("a foreign origin opened the socket: %v", err)
	}
}

// Rule 6: the socket creates nothing. A room that no form made is a 404,
// before any upgrade.
func TestTheSocketMakesNoRoom(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	_, err := openSocket(t, ta, ta.client, "no-such-room", "")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("a socket opened for a room nobody made: %v", err)
	}
	if rooms, _ := ta.rooms.All(t.Context()); len(rooms) != 0 {
		t.Errorf("the socket made %d rooms", len(rooms))
	}
}

// closedWith reads until the socket ends and returns the close code.
func closedWith(t *testing.T, c *ws.Conn) uint16 {
	t.Helper()
	for {
		_, err := c.Read()
		if err == nil {
			continue
		}
		if ce, ok := errors.AsType[*ws.CloseError](err); ok {
			return ce.Code
		}
		t.Fatalf("the socket ended without a close frame: %v", err)
	}
}

// Rule 7: past the messages-a-second cap, the server closes with 1008.
//
// This drives readSeat alone, not the whole handler. Through the handler every
// message the cap lets through is also broadcast back to the sender, and the
// burst (socketPerSecond) is bigger than the queue (socketQueue): whether the
// writer drains the queue before the cap is reached is the scheduler's call,
// so the same flood closes with 1013 now and then. The queue's own cap has its
// own test, TestAFullQueueKicksTheSeat, and the handler's path into readSeat
// is TestAMessageThatIsNotAPositionClosesTheSocket's.
//
// Inside the bubble the clock stands still, so the bucket never refills
// mid-flood and exactly socketPerSecond messages get through.
func TestTooManyMessagesCloseTheSocket(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		srv, cli := socketPair(t)
		s := &seat{name: "Ada", conn: srv}
		moved := 0
		read := make(chan uint16, 1)
		go func() { read <- readSeat(s, newSocketLimiter(), func() { moved++ }) }()
		closed := make(chan uint16, 1)
		go func() { closed <- closedWith(t, cli) }()

		for range 2 * socketPerSecond {
			if cli.Write(presence.EncodeUp(1, 1)) != nil {
				break // closed under us, which is the point
			}
		}
		if got := <-read; got != ws.ClosePolicy {
			t.Errorf("readSeat returned %d, want 1008", got)
		}
		if got := <-closed; got != ws.ClosePolicy {
			t.Errorf("closed with %d, want 1008", got)
		}
		if moved != socketPerSecond {
			t.Errorf("%d messages moved the seat, want the burst of %d", moved, socketPerSecond)
		}
	})
}

// socketPair is both ends of one socket over a net.Pipe, with no server in
// between, so a test can drive one side's loop inside a synctest bubble.
func socketPair(t *testing.T) (server, client *ws.Conn) {
	t.Helper()
	sc, cc := net.Pipe()
	accepted := make(chan *ws.Conn, 1)
	go func() {
		br := bufio.NewReader(sc)
		req, err := http.ReadRequest(br)
		if err != nil {
			t.Errorf("reading the handshake: %v", err)
			sc.Close()
			accepted <- nil
			return
		}
		c, err := ws.Accept(&hijacker{conn: sc, br: br, header: http.Header{}}, req)
		if err != nil {
			t.Errorf("accepting the socket: %v", err)
			sc.Close()
		}
		accepted <- c
	}()
	client, err := ws.Dial(cc, "chat.example.com", "/ws", nil)
	server = <-accepted
	if err != nil || server == nil {
		t.Fatalf("opening the socket pair: %v", err)
	}
	t.Cleanup(func() {
		client.Close(ws.CloseNormal, "")
		server.Close(ws.CloseNormal, "")
	})
	return server, client
}

// hijacker is the least ResponseWriter ws.Accept takes: one that hands over
// its connection.
type hijacker struct {
	conn   net.Conn
	br     *bufio.Reader
	header http.Header
}

func (h *hijacker) Header() http.Header         { return h.header }
func (h *hijacker) Write(p []byte) (int, error) { return h.conn.Write(p) }
func (h *hijacker) WriteHeader(int)             {}
func (h *hijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, bufio.NewReadWriter(h.br, bufio.NewWriter(h.conn)), nil
}

// A message that is not one position is not acted on.
func TestAMessageThatIsNotAPositionClosesTheSocket(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")
	c, err := openSocket(t, ta, ta.client, slug, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Write([]byte("Grace")); err != nil {
		t.Fatal(err)
	}
	if got := closedWith(t, c); got != ws.ClosePolicy {
		t.Errorf("closed with %d, want 1008", got)
	}
}

// Rule 7's third cap: the queue toward a slow reader. broadcast never blocks;
// a full queue kicks the seat, and its writer closes it with 1013.
func TestAFullQueueKicksTheSeat(t *testing.T) {
	t.Parallel()
	h := newHub()
	slow := &seat{name: "Slow", out: make(chan []byte, socketQueue), kick: make(chan struct{}, 1)}
	if !h.join("room", slow) {
		t.Fatal("an empty room refused a seat")
	}
	for range socketQueue + 1 {
		h.broadcast("room") // never blocks, or this test hangs
	}
	select {
	case <-slow.kick:
	default:
		t.Error("a full queue did not kick the seat")
	}
}

// The room's map holds MaxSeats, and refuses the next one.
func TestAFullMapRefusesTheNextSeat(t *testing.T) {
	t.Parallel()
	h := newHub()
	for range presence.MaxSeats {
		if !h.join("room", &seat{}) {
			t.Fatal("refused a seat below the cap")
		}
	}
	if h.join("room", &seat{}) {
		t.Error("seated one past the cap")
	}
}

// Rule 8: srv.Shutdown does not track a hijacked connection, so the app closes
// every socket with 1001 when it is stopping, and Wait joins the goroutines.
func TestShutdownClosesEverySocketWith1001(t *testing.T) {
	t.Parallel()
	stopping, stop := context.WithCancel(t.Context())
	ta := newTestApp(t, func(o *Options) { o.Stopping = stopping })
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")
	c, err := openSocket(t, ta, ta.client, slug, "")
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if got := closedWith(t, c); got != ws.CloseGoingAway {
		t.Errorf("closed with %d, want 1001", got)
	}
	waited := make(chan struct{})
	go func() { ta.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return: a socket's goroutine outlived the shutdown")
	}
}

// The map page carries the module and its two scripts, in <head>.
func TestTheMapPageCarriesTheModule(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")
	res, body := ta.do(t, http.MethodGet, "/rooms/"+slug+"/map", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET the map: %d", res.StatusCode)
	}
	head, _, _ := strings.Cut(body, "</head>")
	for _, want := range []string{`src="/static/js/wasm_exec.js?v=test" defer`, `src="/static/js/wasm_app.js?v=test" defer`} {
		if !strings.Contains(head, want) {
			t.Errorf("<head> lacks %s", want)
		}
	}
	if !strings.Contains(body, `data-module="/static/roommap.wasm?v=test"`) {
		t.Error("no element names the module")
	}
}
