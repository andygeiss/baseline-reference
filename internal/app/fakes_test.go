package app

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/andygeiss/baseline-reference/v6/internal/domain"
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

	// resets is where a new password deletes the user's reset links, as the
	// store does in the same transaction. Set by newFakeResets.
	resets *fakeResets

	// afterByName, when set, runs once a ByName has read its row and before
	// it returns: the gap in which another request's write lands. afterByID
	// is the same gap after the read the credential middleware makes.
	afterByName func(domain.User)
	afterByID   func(domain.User)
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
	var found *domain.User
	for _, u := range f.byID {
		if strings.EqualFold(u.Name, name) {
			found = &u
			break
		}
	}
	after := f.afterByName
	f.mu.Unlock()
	if found == nil {
		return domain.User{}, domain.ErrNotFound
	}
	if after != nil {
		after(*found)
	}
	return *found, nil
}

func (f *fakeUsers) ByID(_ context.Context, id string) (domain.User, error) {
	f.mu.Lock()
	u, ok := f.byID[id]
	after := f.afterByID
	f.mu.Unlock()
	if !ok {
		return domain.User{}, domain.ErrNotFound
	}
	if after != nil {
		after(u)
	}
	return u, nil
}

// Delete names the password counter, like the store: a fake that deleted by id
// alone would let a delete held open across a reset go through, and still
// pass.
func (f *fakeUsers) Delete(_ context.Context, id string, version int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return domain.ErrNotFound
	}
	if u.PasswordVersion != version {
		return domain.ErrStale
	}
	delete(f.byID, id)
	return nil
}

// Rehash stores a hash and leaves the counter where it is, and refuses a write
// that names a counter the row no longer holds, like the store — a fake that
// moved the counter would sign a user's other sessions out at a sign-in, and
// one that skipped the refusal would let a late write land over a reset, and
// both would still pass.
func (f *fakeUsers) Rehash(_ context.Context, id, hash string, version int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok || u.PasswordVersion != version {
		return domain.ErrStale
	}
	u.PasswordHash = hash
	f.byID[id] = u
	return nil
}

// reset is a password set from somewhere else, as a redeemed link makes one:
// the hash, the counter moved, and the user's reset links and machine tokens
// gone, under one lock as the store does it in one transaction.
func (f *fakeUsers) reset(id, hash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetPassword(id, hash)
	if f.resets != nil {
		f.resets.forget(id)
		f.resets.tokens.forget(id)
	}
}

// resetPassword is the write itself, with f.mu held: the hash, and the counter
// moved by one.
func (f *fakeUsers) resetPassword(id, hash string) domain.User {
	u := f.byID[id]
	u.PasswordHash = hash
	u.PasswordVersion++
	f.byID[id] = u
	return u
}

// SetEmail names the password counter, like the store: a fake that wrote by
// user id alone would let an address write held open across a reset go
// through, and still pass.
func (f *fakeUsers) SetEmail(_ context.Context, id, email string, version int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok || u.PasswordVersion != version {
		return domain.ErrStale
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
	mu    sync.Mutex
	users *fakeUsers
	// tokens is the store a reset deletes from, as the real one does in its
	// transaction (patterns/go-auth-sessions.md, Machine tokens rule 5).
	tokens *fakeTokens
	links  map[string]domain.Reset
	sent   []domain.Mail
	addErr error
}

// newFakeResets ties the two fakes together, because the store ties the two
// tables together: a redeemed link sets a password, and setting one deletes
// the user's links.
func newFakeResets(users *fakeUsers, tokens *fakeTokens) *fakeResets {
	f := &fakeResets{users: users, tokens: tokens, links: make(map[string]domain.Reset)}
	users.resets = f
	return f
}

// forget deletes every link a user has, as setting a password does.
func (f *fakeResets) forget(userID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for hash, res := range f.links {
		if res.UserID == userID {
			delete(f.links, hash)
		}
	}
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

// Redeem spends a link, sets the password and moves the counter, and deletes
// the user's other links, as the store does in one transaction.
func (f *fakeResets) Redeem(_ context.Context, hash, passwordHash string, now time.Time) (domain.User, error) {
	f.mu.Lock()
	res, ok := f.links[hash]
	if ok && res.Expired(now) {
		delete(f.links, hash) // spent, and refused like one that never was
		ok = false
	}
	f.mu.Unlock()
	if !ok {
		return domain.User{}, domain.ErrNotFound
	}

	// One lock across the password, the links and the tokens, as the store's
	// one transaction is: a token insert checks the counter under the same lock.
	f.users.mu.Lock()
	defer f.users.mu.Unlock()
	user := f.users.resetPassword(res.UserID, passwordHash)
	f.forget(res.UserID)
	f.tokens.forget(res.UserID)
	return user, nil
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
	// down, once set, is what every lookup returns: the store failing to
	// answer, which is not the same as answering "no such token".
	down error
	// hung, once set, makes every lookup wait for its context to end: a
	// store that takes the call and never answers.
	hung bool
}

// goDown makes every later lookup fail with err.
func (f *fakeTokens) goDown(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = err
}

// hang makes every later lookup block until its caller gives up.
func (f *fakeTokens) hang() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hung = true
}

func newFakeTokens(users *fakeUsers) *fakeTokens { return &fakeTokens{users: users} }

// forget deletes every token a user has, as a password reset does.
func (f *fakeTokens) forget(userID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = slices.DeleteFunc(f.tokens, func(t domain.Token) bool { return t.UserID == userID })
}

// Add names the owner's password counter, like the store: a fake that inserted
// by user id alone would let a token creation held open across a reset go
// through, and still pass.
func (f *fakeTokens) Add(_ context.Context, t *domain.Token, version int64) error {
	// The check and the insert under one lock, as the store's one statement
	// is: released in between, a reset could land there and leave this token.
	f.users.mu.Lock()
	defer f.users.mu.Unlock()
	owner, ok := f.users.byID[t.UserID]
	if !ok || owner.PasswordVersion != version {
		return domain.ErrStale
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	f.tokens = append(f.tokens, *t)
	return nil
}

func (f *fakeTokens) UserByHash(ctx context.Context, hash string) (domain.User, error) {
	return f.lookup(ctx, hash, true)
}

func (f *fakeTokens) Owner(ctx context.Context, hash string) (domain.User, error) {
	return f.lookup(ctx, hash, false)
}

// lookup is both reads; touch says whether this one marks the token used.
func (f *fakeTokens) lookup(ctx context.Context, hash string, touch bool) (domain.User, error) {
	f.mu.Lock()
	if f.down != nil {
		defer f.mu.Unlock()
		return domain.User{}, f.down
	}
	if f.hung {
		f.mu.Unlock()
		<-ctx.Done()
		return domain.User{}, ctx.Err()
	}
	var userID string
	for i, t := range f.tokens {
		if t.Hash == hash {
			userID = t.UserID
			if touch {
				f.tokens[i].LastUsedAt = time.Now().UTC().Format(time.RFC3339)
			}
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
