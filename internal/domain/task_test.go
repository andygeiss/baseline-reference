package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{"a plain title is left alone", "Buy milk", "Buy milk"},
		{"surrounding spaces go", "  Buy milk  ", "Buy milk"},
		{"a run of spaces collapses", "Buy    milk", "Buy milk"},
		{"tabs and line breaks are whitespace too", "Buy\tmilk\nand bread", "Buy milk and bread"},
		{"whitespace only is empty", " \t\n ", ""},
		{"already empty stays empty", "", ""},
		{"accents survive", "Käse kaufen", "Käse kaufen"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeTitle(tt.title); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		title string
		want  error
	}{
		{"a plain title is fine", "Buy milk", nil},
		{"empty is refused", "", ErrEmptyTitle},
		{"exactly the cap is fine", strings.Repeat("a", MaxTitleLen), nil},
		{"one over the cap is refused", strings.Repeat("a", MaxTitleLen+1), ErrTitleTooLong},
		// len("ü") is 2, so a byte count would refuse this one at half the
		// advertised limit.
		{"the cap counts runes, not bytes", strings.Repeat("ü", MaxTitleLen), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateTitle(tt.title); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestNewTask(t *testing.T) {
	t.Parallel()

	got, err := NewTask("t1", "  Buy   milk ")
	if err != nil {
		t.Fatalf("new task: %v", err)
	}
	if got.ID != "t1" || got.Title != "Buy milk" || got.Done {
		t.Errorf("got %+v, want an open task titled %q", got, "Buy milk")
	}

	// Normalization runs first, so spaces alone are an empty title — not a
	// title of three characters.
	if _, err := NewTask("t2", "   "); !errors.Is(err, ErrEmptyTitle) {
		t.Errorf("blank title: got %v, want ErrEmptyTitle", err)
	}
	if _, err := NewTask("t3", strings.Repeat("a", MaxTitleLen+1)); !errors.Is(err, ErrTitleTooLong) {
		t.Errorf("long title: got %v, want ErrTitleTooLong", err)
	}
}

func TestTask_Toggle(t *testing.T) {
	t.Parallel()
	task := Task{ID: "t1", Title: "Buy milk"}

	task.Toggle()
	if !task.Done {
		t.Error("first toggle: task is still open")
	}
	task.Toggle()
	if task.Done {
		t.Error("second toggle: task did not come back open")
	}
}

func TestOpen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		tasks []Task
		want  int
	}{
		{"no tasks", nil, 0},
		{"all open", []Task{{Done: false}, {Done: false}}, 2},
		{"some done", []Task{{Done: true}, {Done: false}, {Done: true}}, 1},
		{"all done", []Task{{Done: true}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Open(tt.tasks); got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}
