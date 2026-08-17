package app

import (
	"context"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// Assistant answers the conversation so far.
//
// Reply returns domain.ErrRefused when the model declines to answer. Every
// other error is transient: the caller logs it and the room carries on without
// an answer.
//
// The port is in the app's words, not a vendor's: no model, no token ceiling,
// no *http.Response. Every one of those knobs lives inside the adapter, so
// swapping which model answers is a change to one package and a flag.
type Assistant interface {
	Reply(ctx context.Context, history []domain.Message) (string, error)
}
