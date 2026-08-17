// Package logmail is the mailer that needs nothing: it writes the message to
// the log instead of sending it.
//
// It is the default so the whole app runs with an empty environment — the same
// job internal/echo does for the assistant. A developer can walk the entire
// password-reset flow with no relay, no account, and no network.
package logmail

import (
	"context"
	"log/slog"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

type Sender struct {
	logger *slog.Logger
}

func New(logger *slog.Logger) *Sender {
	return &Sender{logger: logger}
}

// Send writes the whole message, body included.
//
// Everywhere else in this app the body of a mail is the one thing never
// logged, because it carries a reset token. Here the log *is* the delivery: a
// message whose body did not arrive was not delivered, and a developer would
// have nothing to click. That is exactly why parseConfig refuses this adapter
// when ENV=prod — the trade is only safe on a machine where the log and the
// inbox are the same person.
func (s *Sender) Send(_ context.Context, m domain.Mail) error {
	if err := m.Validate(); err != nil {
		return err
	}
	s.logger.Info("mail (not sent — logmail)", "to", m.To, "subject", m.Subject, "body", m.Text)
	return nil
}
