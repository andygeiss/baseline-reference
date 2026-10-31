package app

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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
	header := http.Header{}
	for _, c := range client.Jar.Cookies(u) {
		header.Add("Cookie", c.Name+"="+c.Value)
	}
	if origin != "" {
		header.Set("Origin", origin)
	}
	return dialSocket(t, ta.server, slug, header)
}

// dialSocket opens GET /ws?room=slug on server with exactly the headers given,
// and an Origin of the server's own host unless they name one.
func dialSocket(t *testing.T, server *httptest.Server, slug string, header http.Header) (*ws.Conn, error) {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := server.Client().Transport.(*http.Transport).DialContext(t.Context(), "tcp", u.Host)
	if err != nil {
		t.Fatalf("dialing the test server: %v", err)
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

// Rule 7: past the frames-a-second cap, the server closes with 1008.
//
// This drives readSeat alone, under the cap the handler sets, to count exactly
// what moved the seat; the same flood through the whole handler is
// TestAFloodClosesWith1008, and the cap counting pings and empty fragments is
// ws's TestTheFrameCapCountsEveryFrame.
//
// Inside the bubble the clock stands still, so the bucket never refills
// mid-flood and exactly socketPerSecond one-frame messages get through.
func TestTooManyMessagesCloseTheSocket(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		srv, cli := socketPair(t)
		srv.AllowFrame = newSocketLimiter().Allow
		s := &seat{name: "Ada", conn: srv}
		s.vouch(time.Now().Add(time.Hour))
		moved := 0
		read := make(chan uint16, 1)
		go func() { read <- readSeat(s, func() { moved++ }) }()
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

// A message that is not one position is not acted on: it is malformed, 1002.
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
	if got := closedWith(t, c); got != ws.CloseProtocolError {
		t.Errorf("closed with %d, want 1002", got)
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
		h.broadcast("room", nil) // never blocks, or this test hangs
	}
	select {
	case <-slow.kick:
	default:
		t.Error("a full queue did not kick the seat")
	}
}

// A seat's own moves never fill its own queue: however many, they owe it one
// fresh map, and that map supersedes whatever was still queued. Without this
// a flooding client filled its queue with its own echoes and was closed 1013,
// a slow reader, before the rate cap could close it 1008.
func TestASeatsOwnMovesDoNotFillItsQueue(t *testing.T) {
	t.Parallel()
	h := newHub()
	ada := &seat{name: "Ada", out: make(chan []byte, socketQueue), echo: make(chan struct{}, 1), kick: make(chan struct{}, 1)}
	grace := &seat{name: "Grace", out: make(chan []byte, socketQueue), echo: make(chan struct{}, 1), kick: make(chan struct{}, 1)}
	h.join("room", ada)
	h.join("room", grace)

	grace.move(5, 5)
	h.broadcast("room", grace) // queued toward Ada
	for i := range 4 * socketQueue {
		ada.move(uint16(i), 7)
		h.broadcast("room", ada)
	}
	select {
	case <-ada.kick:
		t.Fatal("the sender's own moves kicked it")
	default:
	}
	if len(ada.echo) != 1 {
		t.Fatalf("%d fresh maps owed, want 1", len(ada.echo))
	}
	// The same traffic still kicks a reader who is not the sender: nobody
	// drains Grace's queue here, so she fell behind.
	select {
	case <-grace.kick:
	default:
		t.Error("the reader that fell behind was not kicked")
	}

	msg := h.latest("room", ada)
	if len(ada.out) != 0 {
		t.Errorf("%d older maps still queued behind the latest", len(ada.out))
	}
	seats, ok := presence.DecodeDown(msg)
	if !ok {
		t.Fatal("latest is not a map")
	}
	for _, st := range seats {
		if st.Name == "Ada" && st.X != 4*socketQueue-1 {
			t.Errorf("the latest map has Ada at %d, want her last move", st.X)
		}
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

// links is every <a> start tag in html, as written.
func links(html string) []string {
	var tags []string
	for _, rest := range strings.Split(html, "<a ")[1:] {
		tag, _, _ := strings.Cut(rest, ">")
		tags = append(tags, "<a "+tag+">")
	}
	return tags
}

// The two scripts live in the map page's <head>, and a boosted link swaps only
// the body, so the link in carries hx-boost="false": arriving is a full load.
// Leaving needs no opt-out — the module stops itself once its canvas is gone,
// and Back or Forward is a full load under the layout's "history":"reload" —
// so every link out, like every link on every other page, keeps its boost.
// And htmx stays last in <head> (stack/htmx.md), after the page's scripts.
func TestTheMapIsEnteredByAFullLoad(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")

	_, room := ta.do(t, http.MethodGet, "/rooms/"+slug, nil, nil)
	in := 0
	for _, a := range links(room) {
		if strings.Contains(a, `href="/rooms/`+slug+`/map"`) {
			in++
			if !strings.Contains(a, `hx-boost="false"`) {
				t.Errorf("the link into the map is boosted: %s", a)
			}
		}
	}
	if in == 0 {
		t.Error("the room page has no link to its map")
	}

	_, page := ta.do(t, http.MethodGet, "/rooms/"+slug+"/map", nil, nil)
	head, body, _ := strings.Cut(page, "</head>")
	if !strings.Contains(head, `"history":"reload"`) {
		t.Error(`the layout's htmx-config lost "history":"reload", so Back to a boosted page would swap only its body`)
	}
	last := strings.TrimSpace(head)
	last = last[strings.LastIndex(last, "\n")+1:]
	if !strings.Contains(last, "/static/js/htmx.min.js") {
		t.Errorf("htmx is not last in <head>; the last line is %s", last)
	}
	out := links(body)
	if len(out) < 4 { // the header, the two nav entries, back to the room
		t.Fatalf("the map page has %d links, want the header's, the nav's and the way back", len(out))
	}
	for _, a := range out {
		if strings.Contains(a, "hx-boost") {
			t.Errorf("a link out of the map opts out of the boost: %s", a)
		}
	}

	_, rooms := ta.do(t, http.MethodGet, "/rooms", nil, nil)
	for _, a := range links(rooms) {
		if strings.Contains(a, "hx-boost") {
			t.Errorf("a page that is not the map opts out of the boost: %s", a)
		}
	}
}

// Rule 8, tier 1: the handler returns right after the handshake, and the
// socket lives on a goroutine the app tracks. Served inside its handler, the
// socket would let LoadAndSave commit the session it opened under once it
// closed — after a logout had deleted it. The routes are wrapped here so the
// test sees the handler return, and the socket must still carry a move after.
func TestTheSocketHandlerReturnsAtTheHandshake(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.signUp(t, "Ada", "correct-horse")
	slug := ta.makeRoom(t, "General")

	returned := make(chan struct{}, 1)
	routes := ta.Routes()
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes.ServeHTTP(w, r)
		if r.URL.Path == "/ws" {
			returned <- struct{}{}
		}
	}))
	u, err := url.Parse(ta.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	for _, c := range ta.client.Jar.Cookies(u) {
		header.Add("Cookie", c.Name+"="+c.Value)
	}
	c, err := dialSocket(t, server, slug, header)
	if err != nil {
		t.Fatalf("opening the socket: %v", err)
	}
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler still holds the socket 5s after the handshake")
	}
	if err := c.Write(presence.EncodeUp(7, 8)); err != nil {
		t.Fatalf("the socket died with its handler: %v", err)
	}
	for {
		msg, err := c.Read()
		if err != nil {
			t.Fatalf("the socket died with its handler: %v", err)
		}
		seats, _ := presence.DecodeDown(msg)
		for _, s := range seats {
			if s.Name == "Ada" && s.X == 7 && s.Y == 8 {
				return
			}
		}
	}
}

// socketBubble runs f with a test app whose server lives inside a
// testing/synctest bubble, so socketRecheck passes on synctest's clock. The
// options are made outside it, with a stopping context that never cancels
// (TestLoginRateLimit says why).
func socketBubble(t *testing.T, f func(t *testing.T, ta *testApp)) {
	t.Helper()
	o := newTestOptions(t, withStopping(context.Background()))
	synctest.Test(t, func(t *testing.T) { f(t, newTestAppWith(t, o)) })
}

// quietSocket is openSocket with no read deadline of its own, so a test can
// wait out several rechecks without the client giving up first.
func quietSocket(t *testing.T, ta *testApp, client *http.Client, slug string) *ws.Conn {
	t.Helper()
	c, err := openSocket(t, ta, client, slug, "")
	if err != nil {
		t.Fatalf("opening the socket: %v", err)
	}
	c.ReadTimeout = 0
	return c
}

// Rule 6, tier 1: what the cookie holds is re-resolved, not remembered. Each
// way a session stops naming its person closes an open socket with 1008
// within socketRecheck — the socket does not outlive the sign-in it opened
// under.
func TestAnEndedSessionClosesTheSocket(t *testing.T) {
	t.Parallel()
	cases := map[string]func(t *testing.T, ta *testApp){
		"signing out": func(t *testing.T, ta *testApp) {
			if res, _ := ta.do(t, http.MethodPost, "/logout", url.Values{}, nil); res.StatusCode != http.StatusSeeOther {
				t.Fatalf("signing out: %d", res.StatusCode)
			}
		},
		// From another browser, the way go-data-deletion.md's "signed out
		// everywhere" means it: this socket's session is still in the store,
		// and only the missing user row can end it.
		"deleting the account elsewhere": func(t *testing.T, ta *testApp) {
			mine := ta.client
			elsewhere := *mine
			elsewhere.Jar = emptyJar(t)
			ta.client = &elsewhere
			defer func() { ta.client = mine }()
			if res, _ := ta.do(t, http.MethodPost, "/login", url.Values{
				"name": {"Ada"}, "password": {"correct-horse"},
			}, nil); res.StatusCode != http.StatusSeeOther {
				t.Fatalf("signing in elsewhere: %d", res.StatusCode)
			}
			if res, _ := ta.do(t, http.MethodPost, "/account/delete", url.Values{
				"name": {"Ada"}, "password": {"correct-horse"},
			}, nil); res.StatusCode != http.StatusSeeOther {
				t.Fatalf("deleting the account: %d", res.StatusCode)
			}
		},
		// A reset moves the password stamp, which ends every older session
		// (go-auth-sessions.md) — this socket's included.
		"a password reset": func(t *testing.T, ta *testApp) {
			user, err := ta.users.ByName(t.Context(), "Ada")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ta.users.SetPassword(t.Context(), user.ID, "new-hash"); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, end := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			socketBubble(t, func(t *testing.T, ta *testApp) {
				ta.signUp(t, "Ada", "correct-horse")
				slug := ta.makeRoom(t, "General")
				c := quietSocket(t, ta, ta.client, slug)
				// The stamp is a clock reading, and the bubble's clock has
				// not moved since sign-up.
				time.Sleep(time.Millisecond)

				end(t, ta)
				// Bounded by the bubble's clock, so a socket that is never
				// closed fails here rather than hanging the run.
				closed := make(chan uint16, 1)
				go func() {
					for {
						_, err := c.Read()
						if err == nil {
							continue
						}
						ce, _ := errors.AsType[*ws.CloseError](err)
						if ce == nil {
							closed <- 0
							return
						}
						closed <- ce.Code
						return
					}
				}()
				select {
				case got := <-closed:
					if got != ws.ClosePolicy {
						t.Errorf("closed with %d, want 1008", got)
					}
				case <-time.After(socketRecheck):
					t.Errorf("still open %v after the session ended", socketRecheck)
					c.Close(ws.CloseNormal, "")
					<-closed
				}
			})
		})
	}
}

// The other half: a session that is still good keeps its socket across many
// rechecks, and the socket still works at the end.
func TestALiveSessionKeepsTheSocket(t *testing.T) {
	t.Parallel()
	socketBubble(t, func(t *testing.T, ta *testApp) {
		ta.signUp(t, "Ada", "correct-horse")
		slug := ta.makeRoom(t, "General")
		c := quietSocket(t, ta, ta.client, slug)

		// Under socketPing, so no ping goes unanswered while nobody reads.
		time.Sleep(3*socketRecheck + time.Second)

		if err := c.Write(presence.EncodeUp(42, 43)); err != nil {
			t.Fatalf("the socket closed under a live session: %v", err)
		}
		for {
			msg, err := c.Read()
			if err != nil {
				t.Fatalf("the socket closed under a live session: %v", err)
			}
			seats, _ := presence.DecodeDown(msg)
			for _, s := range seats {
				if s.Name == "Ada" && s.X == 42 && s.Y == 43 {
					return // her own move came back, as the module draws it
				}
			}
		}
	})
}

// Rule 7 through the whole handler: a flood is closed by the rate cap, 1008.
// Inside the bubble the writer happens to drain fast enough even with the old
// echo, so this alone would not have caught the 1013 race; the hub's half,
// TestASeatsOwnMovesDoNotFillItsQueue, is what pins that fix.
func TestAFloodClosesWith1008(t *testing.T) {
	t.Parallel()
	socketBubble(t, func(t *testing.T, ta *testApp) {
		ta.signUp(t, "Ada", "correct-horse")
		slug := ta.makeRoom(t, "General")
		c := quietSocket(t, ta, ta.client, slug)
		closed := make(chan uint16, 1)
		go func() { closed <- closedWith(t, c) }()
		for range 2 * socketPerSecond {
			if c.Write(presence.EncodeUp(1, 1)) != nil {
				break
			}
		}
		if got := <-closed; got != ws.ClosePolicy {
			t.Errorf("closed with %d, want 1008", got)
		}
	})
}

// tokenSocket opens the map's socket the way a program does: a machine token
// in Authorization, no cookie, and the Origin naming the host.
func tokenSocket(t *testing.T, ta *testApp, secret, slug string) *ws.Conn {
	t.Helper()
	c, err := dialSocket(t, ta.server, slug, http.Header{"Authorization": {"Bearer " + secret}})
	if err != nil {
		t.Fatalf("opening the socket with a token: %v", err)
	}
	c.ReadTimeout = 0
	return c
}

// closedWithin reads c until it closes, and returns the code, or fails the
// test if the bubble's clock passes socketRecheck first. The millisecond is
// the tie: the socket's ticker and this timer would otherwise both fire at the
// same instant of the bubble's clock, in no promised order.
func closedWithin(t *testing.T, c *ws.Conn) uint16 {
	t.Helper()
	closed := make(chan uint16, 1)
	go func() {
		for {
			_, err := c.Read()
			if err == nil {
				continue
			}
			ce, _ := errors.AsType[*ws.CloseError](err)
			if ce == nil {
				closed <- 0
				return
			}
			closed <- ce.Code
			return
		}
	}()
	select {
	case got := <-closed:
		return got
	case <-time.After(socketRecheck + time.Millisecond):
		t.Errorf("still open %v later", socketRecheck)
		c.Close(ws.CloseNormal, "")
		<-closed
		return 0
	}
}

// Rule 6, tier 1, for the other credential: a socket opened with a machine
// token closes with 1008 within socketRecheck of the token's revocation.
func TestARevokedTokenClosesTheSocket(t *testing.T) {
	t.Parallel()
	socketBubble(t, func(t *testing.T, ta *testApp) {
		ta.signUp(t, "Ada", "correct-horse")
		slug := ta.makeRoom(t, "General")
		secret := ta.makeToken(t, "map")
		c := tokenSocket(t, ta, secret, slug)

		tokens, err := ta.tokens.ByUser(t.Context(), ta.onlyUserID(t))
		if err != nil || len(tokens) != 1 {
			t.Fatalf("listing tokens: %v (%d tokens)", err, len(tokens))
		}
		if res, _ := ta.do(t, http.MethodPost, "/profile/tokens/"+tokens[0].ID+"/delete", url.Values{}, nil); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("revoking: %d", res.StatusCode)
		}
		if got := closedWithin(t, c); got != ws.ClosePolicy {
			t.Errorf("closed with %d, want 1008", got)
		}
	})
}

// Rule 6: a store that cannot answer is not a "yes". The socket fails closed,
// with 1011 rather than 1008: the credential may be fine, and the client may
// try again.
func TestAStoreErrorClosesTheSocketWith1011(t *testing.T) {
	t.Parallel()
	socketBubble(t, func(t *testing.T, ta *testApp) {
		ta.signUp(t, "Ada", "correct-horse")
		slug := ta.makeRoom(t, "General")
		secret := ta.makeToken(t, "map")
		c := tokenSocket(t, ta, secret, slug)

		ta.tokens.goDown(errors.New("the store is down"))
		if got := closedWithin(t, c); got != ws.CloseInternalError {
			t.Errorf("closed with %d, want 1011", got)
		}
	})
}

// Rule 6, tier 1: while a recheck has fallen due and not been answered, the
// socket acts on nothing, and an answer that misses its deadline is a 1011.
// Ada's socket runs on a token whose store takes the call and never answers;
// Grace watches on a session, whose store does. A move Ada sends before the
// check reaches Grace; one sent while it hangs never does; and the check gives
// up inside the interval, closing Ada's socket.
func TestAnUnansweredRecheckActsOnNothing(t *testing.T) {
	t.Parallel()
	socketBubble(t, func(t *testing.T, ta *testApp) {
		ta.signUp(t, "Ada", "correct-horse")
		slug := ta.makeRoom(t, "General")
		ada := tokenSocket(t, ta, ta.makeToken(t, "map"), slug)
		grace := quietSocket(t, ta, secondPerson(t, ta, "Grace"), slug)

		adaClosed := make(chan uint16, 1)
		go func() {
			for {
				_, err := ada.Read()
				if err == nil {
					continue
				}
				ce, _ := errors.AsType[*ws.CloseError](err)
				if ce == nil {
					adaClosed <- 0 // closed by this test, below
					return
				}
				adaClosed <- ce.Code
				return
			}
		}()
		var mu sync.Mutex
		var seen []presence.Seat // every place Grace saw Ada
		go func() {
			for {
				msg, err := grace.Read()
				if err != nil {
					return
				}
				seats, _ := presence.DecodeDown(msg)
				mu.Lock()
				for _, s := range seats {
					if s.Name == "Ada" {
						seen = append(seen, s)
					}
				}
				mu.Unlock()
			}
		}()
		sawAt := func(x, y uint16) bool {
			mu.Lock()
			defer mu.Unlock()
			for _, s := range seen {
				if s.X == x && s.Y == y {
					return true
				}
			}
			return false
		}

		if err := ada.Write(presence.EncodeUp(1, 2)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
		if !sawAt(1, 2) {
			t.Fatal("a move before any check did not reach Grace")
		}

		ta.tokens.hang()
		time.Sleep(socketRecheck) // Ada's check is due, and waiting on the store
		if err := ada.Write(presence.EncodeUp(9, 9)); err != nil {
			t.Fatal(err)
		}

		select {
		case got := <-adaClosed:
			if got != ws.CloseInternalError {
				t.Errorf("closed with %d, want 1011", got)
			}
		case <-time.After(socketRecheck):
			t.Errorf("still open %v after the check fell due: the deadline is not inside the interval", socketRecheck)
			ada.Close(ws.CloseNormal, "")
			<-adaClosed
		}
		time.Sleep(time.Millisecond)
		if sawAt(9, 9) {
			t.Error("a move sent while the check was unanswered reached Grace")
		}
		grace.Close(ws.CloseNormal, "")
	})
}
