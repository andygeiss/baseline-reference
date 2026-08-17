package domain

import (
	"errors"
	"strings"
)

// ErrRefused is what an assistant returns when the model declines to answer.
// It is a normal outcome the caller branches on, not a fault: the product says
// something out loud instead of showing a 500.
var ErrRefused = errors.New("the assistant declined to answer")

// AssistantID is the user row every assistant message is written as. It is
// seeded by a migration rather than registered, so the name can never be taken
// by a person and the foreign key on messages always resolves.
const AssistantID = "assistant"

// mention is how somebody addresses the assistant. Matching a literal word
// rather than "any message in the room" is what keeps a busy room from paying
// for a model call per line.
const mention = "@assistant"

// SystemPrompt is how the assistant speaks. It lives here, next to the types
// every adapter uses, because it is a product rule rather than a detail of any
// one vendor's API: put it in an adapter and the second adapter gets a copy,
// the two drift, and the product answers differently depending on a flag
// nobody connects to the symptom.
const SystemPrompt = `You are a participant in a group chat room, not an assistant in a private session.

Other people are reading along, so keep replies short — a sentence or two, and a
short paragraph at most. Plain text only: no headings, no bullet lists, no
tables, no markdown. Nobody can see anything you were not told; if a question
depends on something that is not in the conversation, say so and ask for it.`

// MentionsAssistant reports whether a message is addressed to the assistant.
func MentionsAssistant(body string) bool {
	return strings.Contains(strings.ToLower(body), mention)
}

// A Turn is one side of a conversation as a model has to receive it: the person
// who spoke, and what they said.
type Turn struct {
	FromAssistant bool
	Text          string
}

// Alternating returns a room's history the way a model has to be given it: the
// user speaks first, and from there the two take strict turns.
//
// A room reaches a worse shape than that on its own. Several people post before
// the assistant answers; a reply that fails after the question is stored leaves
// the question unanswered, so the next mention puts two user turns in a row.
// Anthropic answers that with a 400 and a local chat template silently builds a
// malformed prompt, so the normalising happens once, here, and every adapter
// calls it.
//
// Two turns by one side are one turn to the model, so they are joined, not
// dropped — the question somebody asked before the assistant caught up is still
// part of what it is answering.
func Alternating(history []Message) []Turn {
	var turns []Turn
	for _, m := range history {
		fromAssistant := m.AuthorID == AssistantID

		// Everything before the first user turn is dropped: a model cannot be
		// given an assistant turn to open with, and there is nothing yet for
		// that turn to have been a reply to.
		if len(turns) == 0 && fromAssistant {
			continue
		}

		// A person's name matters to the model here in a way it does not in a
		// two-party chat: without it, three people talking read as one.
		text := m.Body
		if !fromAssistant {
			text = m.Author + ": " + m.Body
		}

		if n := len(turns); n > 0 && turns[n-1].FromAssistant == fromAssistant {
			turns[n-1].Text += "\n" + text
			continue
		}
		turns = append(turns, Turn{FromAssistant: fromAssistant, Text: text})
	}

	// A history ending on the assistant has no question at the end. Asking for
	// a reply to it would be asking the model to talk to itself.
	if n := len(turns); n > 0 && turns[n-1].FromAssistant {
		turns = turns[:n-1]
	}
	return turns
}
