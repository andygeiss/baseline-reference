// Package echo answers without a language model. It exists so the whole loop —
// mention, reply, storage, the poll that brings the reply back — can be
// exercised with no key, no local model, and no second machine.
//
// This is a product mode, not a test double. It lives in internal/ rather than
// in a _test.go file, and `-assistant=echo` is how you find out whether
// everything around the model works when the answer does not matter yet. It is
// also the default, which is what lets this application start with an empty
// environment (patterns/go-config.md rule 3).
package echo

import (
	"context"
	"fmt"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// Assistant replies by naming what it was asked, so a reader can see that the
// right text reached the adapter.
type Assistant struct{}

func New() *Assistant { return &Assistant{} }

// Reply answers the last thing anybody said.
func (a *Assistant) Reply(_ context.Context, history []domain.Message) (string, error) {
	turns := domain.Alternating(history)
	if len(turns) == 0 {
		return "", fmt.Errorf("nothing to reply to")
	}
	return "echo: " + turns[len(turns)-1].Text, nil
}
