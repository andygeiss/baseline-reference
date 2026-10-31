package app

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/andygeiss/baseline-reference/v5/internal/auth"
	"github.com/andygeiss/baseline-reference/v5/internal/domain"
	"github.com/andygeiss/baseline-reference/v5/internal/presence"
	"github.com/andygeiss/baseline-reference/v5/internal/ws"
)

// A room's map is the one page here that is a program: a canvas showing who
// has the map open and where each of them is pointing, redrawn as they move.
// htmx cannot draw that, so the page carries a WebAssembly module and the
// module talks to GET /ws (patterns/wasm-client.md). Everything around it — the
// room, the sign-in, the room list — stays hypermedia.

// The socket's caps, rule 7: it is capped like a body.
const (
	// socketMaxMessage is the largest message read. An up message is four
	// bytes; a kibibyte is plenty and anything bigger is not this client.
	socketMaxMessage = 1 << 10
	// socketPerSecond is how many messages a second one socket may send, with
	// a burst of the same. The module sends at most twenty.
	socketPerSecond = 30
	// socketQueue is how many messages wait for a slow reader. Past it the
	// server closes the socket with 1013 rather than buffering.
	socketQueue = 8
	// socketPing is how often an idle socket is pinged; a peer silent for two
	// of these is gone.
	socketPing = 20 * time.Second
	// socketWrite bounds every write, so a reader that stopped reading costs
	// one timeout rather than a goroutine for ever.
	socketWrite = 10 * time.Second
	// socketRecheck is how often an open socket asks again whether the
	// credential it opened under still names its person (rule 6): signing
	// out, a password reset, a revoked token, or a deleted account closes it
	// with 1008 within this long. Five seconds, because what a socket does
	// dies with it — a pointer, and a view of the room's pointers — so a few
	// seconds' grace costs nothing that outlives the connection, while two
	// reads a socket every five seconds cost the store next to nothing. Per
	// message would be up to socketPerSecond lookups a second each, and
	// would never recheck a socket that only watches.
	socketRecheck = 5 * time.Second
)

type mapPage struct {
	base
	Room domain.Room
}

// handleRoomMap is the page the module runs on. It is reached by a plain link
// that opts out of the boost: the two scripts are in this page's <head>, and a
// boosted swap would bring the body without them.
func (a *App) handleRoomMap(w http.ResponseWriter, r *http.Request) {
	room, err := a.rooms.BySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, domain.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, "roommap.html", "", mapPage{base: a.newBase(r, "room"), Room: room})
}

// handleSocket is GET /ws?room=<slug>, the one GET that mutates, because a
// browser opens a WebSocket with a GET.
//
// It acts only for the person the session already names (rule 6): the room
// was made by a POST form, the account by another, and an up message carries a
// position and nothing else — never a name, never a room. Who is pointing is
// whoever the cookie says, and the writer asks the store again every
// socketRecheck, so the cookie's say ends when the session or the account does.
func (a *App) handleSocket(w http.ResponseWriter, r *http.Request) {
	room, err := a.rooms.BySlug(r.Context(), r.URL.Query().Get("room"))
	if errors.Is(err, domain.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// Accept refuses an Origin that is not this host with 403 (rule 5, tier
	// 1), hijacks the connection and clears the server's deadlines (rule 8).
	conn, err := ws.Accept(w, r)
	if err != nil {
		a.logger.Debug("socket refused", "err", err)
		return
	}
	conn.MaxMessage = socketMaxMessage
	conn.ReadTimeout = 2 * socketPing
	conn.WriteTimeout = socketWrite

	user := userFrom(r.Context())
	s := &seat{
		name:   user.Name,
		userID: user.ID,
		cred:   a.credentialOf(r),
		conn:   conn,
		out:    make(chan []byte, socketQueue),
		echo:   make(chan struct{}, 1),
		kick:   make(chan struct{}, 1),
	}
	if !a.presence.join(room.ID, s) {
		conn.Close(ws.CloseTryAgain, "this room's map is full")
		return
	}
	a.logger.Info("socket opened", "room", room.Slug, "user", s.name)

	// srv.Shutdown does not track a hijacked connection, so this work is the
	// app's to count: main's a.Wait joins it after the listeners are down.
	a.running.Go(func() {
		code := a.serveSeat(room.ID, s)
		a.logger.Info("socket closed", "room", room.Slug, "user", s.name, "code", code)
	})
}

// serveSeat reads one socket until it ends, and returns the close code. Its
// writer runs beside it; the two end together.
func (a *App) serveSeat(roomID string, s *seat) uint16 {
	written := make(chan uint16, 1)
	done := make(chan struct{})
	go func() { written <- a.writeSeat(roomID, s, done) }()

	code := readSeat(s, newSocketLimiter(), func() { a.presence.broadcast(roomID, s) })

	a.presence.leave(roomID, s)
	a.presence.broadcast(roomID, nil)
	close(done)
	if w := <-written; w != 0 {
		code = w
	}
	s.conn.Close(ws.CloseNormal, "")
	return code
}

// newSocketLimiter is one socket's messages-a-second cap.
func newSocketLimiter() *rate.Limiter {
	return rate.NewLimiter(socketPerSecond, socketPerSecond)
}

// readSeat moves the seat as its messages say, until the socket ends or breaks
// a cap, and returns the code it closed with.
func readSeat(s *seat, limit *rate.Limiter, moved func()) uint16 {
	for {
		msg, err := s.conn.Read()
		if err != nil {
			if ce, ok := errors.AsType[*ws.CloseError](err); ok {
				return ce.Code
			}
			return ws.CloseGoingAway
		}
		if !limit.Allow() {
			s.conn.Close(ws.ClosePolicy, "too many messages")
			return ws.ClosePolicy
		}
		x, y, ok := presence.DecodeUp(msg)
		if !ok {
			s.conn.Close(ws.ClosePolicy, "a message is one position")
			return ws.ClosePolicy
		}
		s.move(x, y)
		moved()
	}
}

// writeSeat sends what the hub queues, pings an idle socket, and is the one
// place a socket is closed for a reason of the server's: a reader too slow
// for its queue (1013), a credential that no longer names its person (1008),
// a store that cannot say (1011), or a shutdown (1001). It returns that code,
// or 0 when the reader ended first.
func (a *App) writeSeat(roomID string, s *seat, done <-chan struct{}) uint16 {
	ping := time.NewTicker(socketPing)
	defer ping.Stop()
	recheck := time.NewTicker(socketRecheck)
	defer recheck.Stop()
	for {
		select {
		case <-done:
			return 0
		case <-a.stopping.Done():
			s.conn.Close(ws.CloseGoingAway, "server shutting down")
			return ws.CloseGoingAway
		case <-s.kick:
			s.conn.Close(ws.CloseTryAgain, "too slow to keep up")
			return ws.CloseTryAgain
		case <-recheck.C:
			ok, err := a.stillSignedIn(s)
			if err != nil {
				// Fail closed: a socket nobody can vouch for stops acting.
				a.logger.Error("socket recheck", "user", s.name, "err", err)
				s.conn.Close(ws.CloseInternalError, "")
				return ws.CloseInternalError
			}
			if !ok {
				s.conn.Close(ws.ClosePolicy, "signed out")
				return ws.ClosePolicy
			}
		case msg := <-s.out:
			if err := s.conn.Write(msg); err != nil {
				s.conn.Close(ws.CloseGoingAway, "")
				return 0
			}
		case <-s.echo:
			if err := s.conn.Write(a.presence.latest(roomID, s)); err != nil {
				s.conn.Close(ws.CloseGoingAway, "")
				return 0
			}
		case <-ping.C:
			if err := s.conn.Ping(nil); err != nil {
				s.conn.Close(ws.CloseGoingAway, "")
				return 0
			}
		}
	}
}

// credential is what the handshake presented, kept so the socket can ask the
// store again who it names. Exactly one is set.
type credential struct {
	tokenHash string // a machine token's hash
	session   string // the scs session token
}

func (a *App) credentialOf(r *http.Request) credential {
	if secret := auth.BearerToken(r.Header.Get("Authorization")); secret != "" {
		return credential{tokenHash: auth.HashToken(secret)}
	}
	return credential{session: a.sessions.Token(r.Context())}
}

// stillSignedIn resolves the seat's credential the way authenticate does, and
// reports whether it still names the seat's person. A session is not bound to
// this goroutine's context, so it is loaded afresh from the store by its
// token: a destroyed or expired one loads empty, and sessionUser then checks
// the user row and the password stamp exactly as a request would. An error is
// the store failing to answer, never a "no".
func (a *App) stillSignedIn(s *seat) (bool, error) {
	ctx, cancel := context.WithTimeout(a.stopping, socketWrite)
	defer cancel()
	var user domain.User
	var err error
	if s.cred.tokenHash != "" {
		user, err = a.tokenUser(ctx, s.cred.tokenHash)
	} else {
		var loaded context.Context
		if loaded, err = a.sessions.Load(ctx, s.cred.session); err == nil {
			user, err = a.sessionUser(loaded)
		}
	}
	switch {
	case err == nil:
		return user.ID == s.userID, nil
	case errors.Is(err, errNoCredential), errors.Is(err, errBadCredential):
		return false, nil
	default:
		return false, err
	}
}

// seat is one open map: whose it is, where they point, and the queue toward
// them.
type seat struct {
	name   string
	userID string
	cred   credential
	conn   *ws.Conn
	out    chan []byte
	kick   chan struct{}
	// echo says the seat's own move changed the map. It holds one: however
	// fast a seat moves, it waits for one fresh map, never a queue of them,
	// so a sender cannot fill its own queue and be closed 1013 for its own
	// speed. The move still comes back — the module draws its own dot from
	// the map it is sent, as it draws everyone's.
	echo chan struct{}

	mu   sync.Mutex
	x, y uint16
}

func (s *seat) move(x, y uint16) {
	s.mu.Lock()
	s.x, s.y = x, y
	s.mu.Unlock()
}

func (s *seat) at() presence.Seat {
	s.mu.Lock()
	defer s.mu.Unlock()
	return presence.Seat{Name: s.name, X: s.x, Y: s.y}
}

// hub is every open map, by room. It holds no history: a seat is somebody
// looking right now, and it goes with their socket.
type hub struct {
	mu    sync.Mutex
	rooms map[string]map[*seat]struct{}
}

func newHub() *hub { return &hub{rooms: make(map[string]map[*seat]struct{})} }

// join seats s in a room, or reports false when the room's map is full.
func (h *hub) join(roomID string, s *seat) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.rooms[roomID]) >= presence.MaxSeats {
		return false
	}
	if h.rooms[roomID] == nil {
		h.rooms[roomID] = make(map[*seat]struct{})
	}
	h.rooms[roomID][s] = struct{}{}
	return true
}

func (h *hub) leave(roomID string, s *seat) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.rooms[roomID], s)
	if len(h.rooms[roomID]) == 0 {
		delete(h.rooms, roomID)
	}
}

// broadcast queues the room's whole map to every seat in it but from, whose
// own move it is (nil when nobody's is): from is nudged instead, and its
// writer fetches the latest map. It never blocks: a seat whose queue is full is
// kicked, and its writer closes it.
func (h *hub) broadcast(roomID string, from *seat) {
	h.mu.Lock()
	defer h.mu.Unlock()
	msg := h.mapLocked(roomID)
	for s := range h.rooms[roomID] {
		if s == from {
			select {
			case s.echo <- struct{}{}:
			default: // a fresh map is already owed
			}
			continue
		}
		select {
		case s.out <- msg:
		default:
			select {
			case s.kick <- struct{}{}:
			default: // already kicked
			}
		}
	}
}

// latest is the room's map as it is now, for s, and drops every map still
// queued toward s: under the lock each of those is older than this one, and
// sent after it they would draw the room as it was.
func (h *hub) latest(roomID string, s *seat) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	msg := h.mapLocked(roomID)
	for range len(s.out) {
		<-s.out
	}
	return msg
}

// mapLocked encodes the room's whole map; h.mu is held.
func (h *hub) mapLocked(roomID string) []byte {
	seats := make([]presence.Seat, 0, len(h.rooms[roomID]))
	for s := range h.rooms[roomID] {
		seats = append(seats, s.at())
	}
	return presence.EncodeDown(seats)
}

// requireSocketAuth guards GET /ws. A socket has no sign-in page to be sent to
// and no JSON to read, so a missing or refused credential is a bare 401 before
// any upgrade.
func (a *App) requireSocketAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := a.authenticate(r)
		switch {
		case err == nil:
			next.ServeHTTP(w, withUser(r, user))
		case errors.Is(err, errNoCredential), errors.Is(err, errBadCredential):
			a.clientError(w, r, http.StatusUnauthorized)
		default:
			a.serverError(w, r, err)
		}
	})
}
