package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
	"github.com/andygeiss/baseline-reference/v3/internal/store"
)

// fakeMailer is the hand-written fake for the Mailer port, beside the code that
// declares it. It keeps what it was asked to send rather than counting calls, so
// the tests below assert an outcome — a message went out, or the row still says
// it did not (patterns/go-ports-adapters.md rule 6).
type fakeMailer struct {
	mu   sync.Mutex
	sent []domain.Mail
	err  error
}

func (f *fakeMailer) Send(_ context.Context, m domain.Mail) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeMailer) delivered() []domain.Mail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Mail(nil), f.sent...)
}

// outboxFixture opens a real database and queues one reset mail through the
// path a handler uses, so the test starts from a row that got there the way rows
// do.
func outboxFixture(t *testing.T) (*store.Outbox, domain.Mail) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	users := store.NewUsers(db)
	ada := &domain.User{ID: "ada", Name: "Ada", PasswordHash: "x"}
	if err := users.Add(t.Context(), ada); err != nil {
		t.Fatalf("adding a user: %v", err)
	}
	mail := domain.Mail{
		To:      "ada@example.com",
		Subject: "Reset your Go Chat password",
		Text:    "https://chat.example.com/reset/confirm?t=SECRET-TOKEN\n",
	}
	reset := &domain.Reset{Hash: "hash", UserID: ada.ID, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := store.NewResets(db).Add(t.Context(), reset, mail); err != nil {
		t.Fatalf("queueing a reset: %v", err)
	}
	return store.NewOutbox(db), mail
}

func TestDrainOutboxSendsAndMarks(t *testing.T) {
	t.Parallel()
	outbox, mail := outboxFixture(t)
	mailer := &fakeMailer{}

	drainOutbox(t.Context(), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), outbox, mailer)

	sent := mailer.delivered()
	if len(sent) != 1 || sent[0].To != mail.To {
		t.Fatalf("delivered %v, want the one queued message", sent)
	}
	if !strings.Contains(sent[0].Text, "SECRET-TOKEN") {
		t.Error("the message went out without its link")
	}
	// A second pass must not send it again: the row was marked, not deleted.
	drainOutbox(t.Context(), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), outbox, mailer)
	if again := mailer.delivered(); len(again) != 1 {
		t.Errorf("delivered %d messages over two passes, want 1", len(again))
	}
}

func TestDrainOutboxGivesUpAfterEnoughFailures(t *testing.T) {
	t.Parallel()
	outbox, _ := outboxFixture(t)
	mailer := &fakeMailer{err: errors.New("relay refused")}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	// A relay that has refused a message this many times will not take it on the
	// next one, and a row that retries forever is a log line every tick.
	for i := 0; i < store.MaxSendAttempts; i++ {
		drainOutbox(t.Context(), logger, outbox, mailer)
	}
	left, err := outbox.Unsent(t.Context(), 10)
	if err != nil {
		t.Fatalf("Unsent: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("%d messages still being retried after %d attempts, want none",
			len(left), store.MaxSendAttempts)
	}
}

// TestDrainOutboxNeverLogsTheBody is the rule that keeps a credential out of the
// logs: the body holds a reset link, so the sender names the recipient and the
// row and stops there (patterns/go-email.md).
func TestDrainOutboxNeverLogsTheBody(t *testing.T) {
	t.Parallel()
	outbox, _ := outboxFixture(t)
	mailer := &fakeMailer{err: errors.New("relay refused")} // the noisy path

	var log bytes.Buffer
	drainOutbox(t.Context(), slog.New(slog.NewTextHandler(&log, nil)), outbox, mailer)

	if strings.Contains(log.String(), "SECRET-TOKEN") {
		t.Errorf("the reset token reached the log:\n%s", log.String())
	}
	// The two things it may say, so this test fails if the line goes silent
	// rather than passing because nothing was logged at all.
	if !strings.Contains(log.String(), "ada@example.com") {
		t.Errorf("the failure names no recipient:\n%s", log.String())
	}
}
