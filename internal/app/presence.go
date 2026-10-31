package app

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

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
// whoever the cookie says.
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

	s := &seat{name: userFrom(r.Context()).Name, conn: conn, out: make(chan []byte, socketQueue), kick: make(chan struct{}, 1)}
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
	go func() { written <- a.writeSeat(s, done) }()

	limit := rate.NewLimiter(socketPerSecond, socketPerSecond)
	code := readSeat(s, limit, func() { a.presence.broadcast(roomID) })

	a.presence.leave(roomID, s)
	a.presence.broadcast(roomID)
	close(done)
	if w := <-written; w != 0 {
		code = w
	}
	s.conn.Close(ws.CloseNormal, "")
	return code
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
// for its queue (1013), or a shutdown (1001). It returns that code, or 0 when
// the reader ended first.
func (a *App) writeSeat(s *seat, done <-chan struct{}) uint16 {
	ping := time.NewTicker(socketPing)
	defer ping.Stop()
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
		case msg := <-s.out:
			if err := s.conn.Write(msg); err != nil {
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

// seat is one open map: whose it is, where they point, and the queue toward
// them.
type seat struct {
	name string
	conn *ws.Conn
	out  chan []byte
	kick chan struct{}

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

// broadcast queues the room's whole map to every seat in it. It never blocks:
// a seat whose queue is full is kicked, and its writer closes it.
func (h *hub) broadcast(roomID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	seats := make([]presence.Seat, 0, len(h.rooms[roomID]))
	for s := range h.rooms[roomID] {
		seats = append(seats, s.at())
	}
	msg := presence.EncodeDown(seats)
	for s := range h.rooms[roomID] {
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
