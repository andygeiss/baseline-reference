package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// The fakes: working in-memory stores, written by hand.
//
// They are not mocks. Nothing here counts calls or asserts an order — each one
// keeps real state, so a test can say "post two messages, then read the room"
// and check the outcome. A mock would let the handlers be rewritten wrongly and
// still pass, as long as they called the same methods.
//
// Every one guards its state with a mutex: the tests run with -race, and a
// handler test may fire concurrent requests.

type fakeUsers struct {
	mu   sync.Mutex
	byID map[string]domain.User
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{byID: make(map[string]domain.User)}
}

func (f *fakeUsers) Add(_ context.Context, u *domain.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.byID {
		// EqualFold, like the store's name_key column: two people may not
		// differ by capitals alone.
		if strings.EqualFold(existing.Name, u.Name) {
			return domain.ErrNameTaken
		}
	}
	f.byID[u.ID] = *u
	return nil
}

func (f *fakeUsers) ByName(_ context.Context, name string) (domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.byID {
		if strings.EqualFold(u.Name, name) {
			return u, nil
		}
	}
	return domain.User{}, domain.ErrNotFound
}

func (f *fakeUsers) ByID(_ context.Context, id string) (domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return domain.User{}, domain.ErrNotFound
}

func (f *fakeUsers) UpdatePasswordHash(_ context.Context, id, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return domain.ErrNotFound
	}
	u.PasswordHash = hash
	f.byID[id] = u
	return nil
}

type fakeRooms struct {
	mu    sync.Mutex
	rooms []domain.Room
}

func newFakeRooms() *fakeRooms { return &fakeRooms{} }

func (f *fakeRooms) Add(_ context.Context, r *domain.Room) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.rooms {
		if existing.Slug == r.Slug {
			return domain.ErrSlugTaken
		}
	}
	f.rooms = append(f.rooms, *r)
	return nil
}

func (f *fakeRooms) All(context.Context) ([]domain.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Room(nil), f.rooms...), nil
}

func (f *fakeRooms) BySlug(_ context.Context, slug string) (domain.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rooms {
		if r.Slug == slug {
			return r, nil
		}
	}
	return domain.Room{}, domain.ErrNotFound
}

type fakeMessages struct {
	mu    sync.Mutex
	seq   int64
	msgs  []domain.Message
	users *fakeUsers // the store joins the author's name on read; so does this
}

func newFakeMessages(users *fakeUsers) *fakeMessages {
	return &fakeMessages{users: users}
}

// withAuthors fills in the display names the real store gets from its JOIN. A
// fake that skipped this would let a handler attribute every message to nobody
// and still pass.
func (f *fakeMessages) withAuthors(msgs []domain.Message) []domain.Message {
	for i, m := range msgs {
		if u, err := f.users.ByID(context.Background(), m.AuthorID); err == nil {
			msgs[i].Author = u.Name
		}
	}
	return msgs
}

func (f *fakeMessages) Add(_ context.Context, m *domain.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++ // like AUTOINCREMENT: never reused, even after a delete
	m.Seq = f.seq
	m.CreatedAt = time.Now().UTC()
	f.msgs = append(f.msgs, *m)
	return nil
}

func (f *fakeMessages) Since(_ context.Context, roomID string, since int64) ([]domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Message
	for _, m := range f.msgs {
		if m.RoomID == roomID && m.Seq > since {
			out = append(out, m)
		}
		if len(out) == domain.MaxPageLen {
			break
		}
	}
	return f.withAuthors(out), nil
}

func (f *fakeMessages) Recent(_ context.Context, roomID string, limit int) ([]domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Message
	for _, m := range f.msgs {
		if m.RoomID == roomID {
			out = append(out, m)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return f.withAuthors(out), nil
}

type fakeTokens struct {
	mu     sync.Mutex
	tokens []domain.Token
	users  *fakeUsers
}

func newFakeTokens(users *fakeUsers) *fakeTokens { return &fakeTokens{users: users} }

func (f *fakeTokens) Add(_ context.Context, t *domain.Token) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	f.tokens = append(f.tokens, *t)
	return nil
}

func (f *fakeTokens) UserByHash(ctx context.Context, hash string) (domain.User, error) {
	f.mu.Lock()
	var userID string
	for i, t := range f.tokens {
		if t.Hash == hash {
			userID = t.UserID
			f.tokens[i].LastUsedAt = time.Now().UTC().Format(time.RFC3339)
			break
		}
	}
	f.mu.Unlock()
	if userID == "" {
		return domain.User{}, domain.ErrNotFound
	}
	return f.users.ByID(ctx, userID)
}

func (f *fakeTokens) ByUser(_ context.Context, userID string) ([]domain.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Token
	for _, t := range f.tokens {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeTokens) Delete(_ context.Context, userID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, t := range f.tokens {
		if t.ID == id && t.UserID == userID {
			f.tokens = append(f.tokens[:i], f.tokens[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}

// fakeAssistant answers from a script. Like the other fakes it keeps state
// rather than counting calls: a test sets what the model says, or which error
// it returns, and then asserts on what the room ends up holding.
type fakeAssistant struct {
	mu   sync.Mutex
	said string
	err  error
}

func newFakeAssistant() *fakeAssistant { return &fakeAssistant{said: "Sure — here you go."} }

// answer sets what the next Reply returns.
func (f *fakeAssistant) answer(said string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.said, f.err = said, err
}

func (f *fakeAssistant) Reply(_ context.Context, _ []domain.Message) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.said, f.err
}
