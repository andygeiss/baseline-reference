package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
	"github.com/andygeiss/baseline-reference/v3/internal/store"
)

// Mailer sends one message, or says why it could not.
//
// The port is defined here because this is the only thing that consumes it: a
// handler never sends mail, it queues it. Two adapters satisfy it —
// internal/logmail, which needs nothing, and internal/smtpmail, which needs a
// relay.
type Mailer interface {
	Send(ctx context.Context, m domain.Mail) error
}

const (
	// sendInterval is how long a reset link may sit in the outbox before it
	// goes. The table is empty almost always, so the cost of looking is one
	// indexed read; the number anybody notices is how long the mail takes.
	sendInterval = 5 * time.Second

	// sendBatch bounds one pass, so a backlog drains over several ticks rather
	// than holding this goroutine through hundreds of connections.
	sendBatch = 20

	// sendTimeout bounds one message. A relay that accepts the connection and
	// then goes quiet must not outlast a shutdown.
	sendTimeout = 20 * time.Second
)

// sendMail drains the outbox on a ticker.
//
// Sending here rather than in the handler is the whole point of the outbox: SMTP
// takes seconds and can hang, and a handler waiting on it would hold a request
// open against WriteTimeout and hand the reader a 500 for somebody else's
// outage. It also keeps the answer to "reset my password" independent of
// whether the address exists, which is what stops that form from telling
// anybody which accounts are real (patterns/go-email.md).
func sendMail(ctx context.Context, logger *slog.Logger, outbox *store.Outbox, mailer Mailer) error {
	return every(ctx, sendInterval, func() { drainOutbox(ctx, logger, outbox, mailer) })
}

// drainOutbox is one pass. It is its own function so a test can run exactly one
// against a real outbox and a fake mailer, without a ticker in the way.
func drainOutbox(ctx context.Context, logger *slog.Logger, outbox *store.Outbox, mailer Mailer) {
	queued, err := outbox.Unsent(ctx, sendBatch)
	if err != nil {
		if !errors.Is(err, context.Canceled) { // shutdown mid-read is not a fault
			logger.Error("reading the outbox", "err", err)
		}
		return
	}
	for _, q := range queued {
		if ctx.Err() != nil {
			return // shutting down: the rest keep until the next start
		}
		sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		err := mailer.Send(sendCtx, q.Mail)
		cancel()

		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			// The recipient and the id, never the body: the body is where the
			// reset token is, and a log is not where a credential goes.
			logger.Warn("sending mail", "id", q.ID, "to", q.Mail.To, "err", err)
			// WithoutCancel so the attempt is still counted during a shutdown,
			// rather than being retried forever from a clean slate.
			if err := outbox.Failed(context.WithoutCancel(ctx), q.ID); err != nil {
				logger.Error("recording a failed send", "id", q.ID, "err", err)
			}
			continue
		}
		if err := outbox.Sent(context.WithoutCancel(ctx), q.ID); err != nil {
			// The message went out and the row still says it did not, so the
			// next tick sends it again. A duplicate reset mail is worth less
			// worry than a lost one, and this is an Error because it means the
			// two sides disagree.
			logger.Error("marking mail sent", "id", q.ID, "err", err)
		}
	}
}

// sweepResets deletes spent and expired reset rows. This only reclaims disk: an
// expired link stops working the moment it expires, because Take refuses it.
func sweepResets(ctx context.Context, logger *slog.Logger, resets *store.Resets) error {
	return every(ctx, time.Hour, func() {
		switch err := resets.DeleteExpired(ctx); {
		case errors.Is(err, context.Canceled):
		case err != nil:
			logger.Error("sweeping password resets", "err", err)
		}
	})
}
