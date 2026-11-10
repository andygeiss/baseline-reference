// Package domain holds the chat rules. It imports nothing from the application
// and knows nothing about HTTP or storage.
package domain

import "errors"

var (
	ErrNotFound = errors.New("not found")

	// ErrCredentials covers both an unknown name and a wrong password. One
	// error, on purpose: a caller that could tell them apart would let anyone
	// find out which names exist.
	ErrCredentials = errors.New("wrong name or password")

	// ErrStale means a write named a value its request read earlier, and the
	// row no longer holds it: something else wrote in between. The write did
	// not happen, and must not be retried against the new value — what the
	// request checked before writing is no longer true.
	ErrStale = errors.New("changed in the meantime")

	ErrNameTaken = errors.New("name is taken")
	ErrSlugTaken = errors.New("room already exists")

	// The two conditions a client of somebody else's server meets and has to
	// tell apart. They live here because both sides speak them: the adapter
	// translates a status into one of these, and the command decides what to
	// print and which exit code to use.
	ErrUnauthorized = errors.New("not signed in")
	ErrRejected     = errors.New("refused")
)
