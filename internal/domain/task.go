// Package domain holds the task rules. It imports nothing from the application
// and knows nothing about HTTP or storage.
package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrEmptyTitle   = errors.New("title is empty")
	ErrTitleTooLong = errors.New("title is too long")
)

// MaxTitleLen caps a title at 120 runes — one line on a phone, not a note.
const MaxTitleLen = 120

// Task is one thing to do. It is either open or done; there is no third state.
type Task struct {
	ID    string
	Title string
	Done  bool
}

// NewTask returns an open task with a normalized title, or an error when the
// title breaks a rule. The HTTP edge checks the same rules first, so it can
// name the field and word the message; this is where they are true whoever
// calls.
func NewTask(id, title string) (*Task, error) {
	title = NormalizeTitle(title)
	if err := ValidateTitle(title); err != nil {
		return nil, err
	}
	return &Task{ID: id, Title: title}, nil
}

// NormalizeTitle trims a title and collapses every run of whitespace to one
// space. Text pasted from a document arrives with tabs and line breaks in it,
// and a task is one line.
func NormalizeTitle(title string) string {
	return strings.Join(strings.Fields(title), " ")
}

// ValidateTitle reports why a normalized title cannot be stored, or nil when it
// can. Normalize first: this function reads "   " as a title of three spaces.
func ValidateTitle(title string) error {
	switch {
	case title == "":
		return ErrEmptyTitle
	// Runes, not bytes: len("ü") is 2, so counting bytes would cut off people
	// who write with accents before the advertised limit.
	case utf8.RuneCountInString(title) > MaxTitleLen:
		return ErrTitleTooLong
	}
	return nil
}

// Toggle flips the task between open and done.
func (t *Task) Toggle() { t.Done = !t.Done }

// Open counts the tasks still to do.
func Open(tasks []Task) int {
	n := 0
	for _, t := range tasks {
		if !t.Done {
			n++
		}
	}
	return n
}
