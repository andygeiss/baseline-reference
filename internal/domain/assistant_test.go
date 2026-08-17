package domain

import (
	"slices"
	"testing"
)

func TestMentionsAssistant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		body string
		want bool
	}{
		{"@assistant what is the answer?", true},
		{"@Assistant what is the answer?", true},    // capitals do not hide a mention
		{"ask @assistant about it", true},           // mid-sentence still counts
		{"morning all", false},                      //
		{"the assistant is quiet today", false},     // the word alone is not the mention
		{"email me at ada@assistant.example", true}, // known: any occurrence counts
	}

	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			t.Parallel()
			if got := MentionsAssistant(tt.body); got != tt.want {
				t.Errorf("MentionsAssistant(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

// TestAlternating pins the shape a model has to be given. The rows that matter
// are not the tidy transcript — they are the shapes a real room reaches on its
// own, which is why they are tested against the failure that produces them
// rather than only against a well-formed conversation.
func TestAlternating(t *testing.T) {
	t.Parallel()

	user := func(name, body string) Message {
		return Message{AuthorID: "u-" + name, Author: name, Body: body}
	}
	bot := func(body string) Message {
		return Message{AuthorID: AssistantID, Author: "Assistant", Body: body}
	}

	tests := []struct {
		name string
		in   []Message
		want []Turn
	}{
		{
			name: "a tidy transcript alternates already",
			in:   []Message{user("Ada", "hello"), bot("hi"), user("Ada", "@assistant why?")},
			want: []Turn{
				{Text: "Ada: hello"},
				{FromAssistant: true, Text: "hi"},
				{Text: "Ada: @assistant why?"},
			},
		},
		{
			name: "an unanswered question leaves two user turns, which are joined",
			// The failure that produces this: a reply that fails after the
			// question is stored. The next mention would otherwise send two
			// user turns in a row — a 400 from Anthropic, and a silently
			// malformed prompt from a local chat template.
			in: []Message{user("Ada", "@assistant why?"), user("Ada", "@assistant still there?")},
			want: []Turn{
				{Text: "Ada: @assistant why?\nAda: @assistant still there?"},
			},
		},
		{
			name: "several people talking are one turn, and keep their names",
			in:   []Message{user("Ada", "hello"), user("Bob", "hi Ada"), bot("hello both"), user("Ada", "@assistant ?")},
			want: []Turn{
				{Text: "Ada: hello\nBob: hi Ada"},
				{FromAssistant: true, Text: "hello both"},
				{Text: "Ada: @assistant ?"},
			},
		},
		{
			name: "a history opening on the assistant drops that turn",
			// A model cannot be given an assistant turn to open with, and there
			// is nothing yet for it to have been a reply to.
			in:   []Message{bot("anybody there?"), user("Ada", "@assistant yes")},
			want: []Turn{{Text: "Ada: @assistant yes"}},
		},
		{
			name: "a history ending on the assistant drops that turn",
			// Otherwise the model is asked to reply to itself.
			in:   []Message{user("Ada", "@assistant why?"), bot("because")},
			want: []Turn{{Text: "Ada: @assistant why?"}},
		},
		{
			name: "a room holding only assistant turns has nothing to reply to",
			in:   []Message{bot("one"), bot("two")},
			want: nil,
		},
		{
			name: "an empty room has nothing to reply to",
			in:   nil,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Alternating(tt.in)

			if !slices.Equal(got, tt.want) {
				t.Fatalf("Alternating() = %+v, want %+v", got, tt.want)
			}
			// The invariant the whole function exists for, asserted separately
			// so a future row cannot pass by matching a wrong expectation.
			for i, turn := range got {
				if i > 0 && turn.FromAssistant == got[i-1].FromAssistant {
					t.Errorf("turn %d repeats the previous speaker: %+v", i, got)
				}
			}
			if len(got) > 0 {
				if got[0].FromAssistant {
					t.Error("the history opens on the assistant")
				}
				if got[len(got)-1].FromAssistant {
					t.Error("the history ends on the assistant, so there is nothing to answer")
				}
			}
		})
	}
}
