package app

import (
	"bytes"
	"context"
	"io"
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

// SetPassword stamps the change as well as storing the hash, like the store —
// a fake that skipped the stamp would let every session survive a reset and
// still pass.
func (f *fakeUsers) SetPassword(_ context.Context, id, hash string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return "", domain.ErrNotFound
	}
	// Nanoseconds, not RFC 3339 seconds: a test resets a password within a
	// second of creating the account, and a stamp that did not move would not
	// end the old session.
	changedAt := time.Now().UTC().Format(time.RFC3339Nano)
	u.PasswordHash, u.PasswordChangedAt = hash, changedAt
	f.byID[id] = u
	return changedAt, nil
}

func (f *fakeUsers) SetEmail(_ context.Context, id, email string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return domain.ErrNotFound
	}
	u.Email = email
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
	users *fakeUsers       // the store joins the author's name on read; so does this
	files *fakeAttachments // the store writes both in one transaction; so does this
}

func newFakeMessages(users *fakeUsers, files *fakeAttachments) *fakeMessages {
	return &fakeMessages{users: users, files: files}
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

func (f *fakeMessages) Add(_ context.Context, m *domain.Message, blob []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++ // like AUTOINCREMENT: never reused, even after a delete
	m.Seq = f.seq
	m.CreatedAt = time.Now().UTC()
	if m.Attachment != nil {
		m.Attachment.MessageSeq = m.Seq
		m.Attachment.CreatedAt = m.CreatedAt
		if f.files != nil {
			f.files.put(*m.Attachment, blob)
		}
	}
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

// Page is the keyset read: the newest limit messages before the cursor, oldest
// first, with before == 0 meaning "from the newest".
func (f *fakeMessages) Page(_ context.Context, roomID string, before int64, limit int) ([]domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Message
	for _, m := range f.msgs {
		if m.RoomID == roomID && (before == 0 || m.Seq < before) {
			out = append(out, m)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return f.withAuthors(out), nil
}

// fakeAttachments keeps the bytes the message fake was handed. Delete carries
// the uploader test the store carries in its WHERE clause: a fake that let
// anybody delete anything would pass every two-user test written against it.
type fakeAttachments struct {
	mu    sync.Mutex
	files map[string]domain.Attachment
	bytes map[string][]byte
}

func newFakeAttachments() *fakeAttachments {
	return &fakeAttachments{
		files: make(map[string]domain.Attachment),
		bytes: make(map[string][]byte),
	}
}

func (f *fakeAttachments) put(a domain.Attachment, blob []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[a.ID] = a
	f.bytes[a.ID] = blob
}

func (f *fakeAttachments) Open(_ context.Context, id string) (domain.Attachment, io.ReadSeekCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.files[id]
	if !ok {
		return domain.Attachment{}, nil, domain.ErrNotFound
	}
	return a, nopCloser{bytes.NewReader(f.bytes[id])}, nil
}

func (f *fakeAttachments) Delete(_ context.Context, uploaderID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.files[id]
	if !ok || a.UploaderID != uploaderID {
		// Somebody else's file answers exactly like one that is not there.
		return domain.ErrNotFound
	}
	delete(f.files, id)
	delete(f.bytes, id)
	return nil
}

// all returns every stored attachment, so a test can find the id the server
// generated without scraping it out of the page.
func (f *fakeAttachments) all() []domain.Attachment {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Attachment, 0, len(f.files))
	for _, a := range f.files {
		out = append(out, a)
	}
	return out
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

// fakeResets holds outstanding links and the messages that carry them, in one
// place, because the store writes both in one transaction.
type fakeResets struct {
	mu     sync.Mutex
	links  map[string]domain.Reset
	sent   []domain.Mail
	addErr error
}

func newFakeResets() *fakeResets {
	return &fakeResets{links: make(map[string]domain.Reset)}
}

func (f *fakeResets) Add(_ context.Context, res *domain.Reset, mail domain.Mail) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return f.addErr
	}
	if err := mail.Validate(); err != nil {
		return err
	}
	f.links[res.Hash] = *res
	f.sent = append(f.sent, mail)
	return nil
}

func (f *fakeResets) Take(_ context.Context, hash string, now time.Time) (domain.Reset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, ok := f.links[hash]
	if !ok {
		return domain.Reset{}, domain.ErrNotFound
	}
	delete(f.links, hash) // single use, spent whether or not it had expired
	if res.Expired(now) {
		return domain.Reset{}, domain.ErrNotFound
	}
	return res, nil
}

// mails returns a copy of everything queued so far.
func (f *fakeResets) mails() []domain.Mail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Mail(nil), f.sent...)
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
