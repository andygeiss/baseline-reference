package app

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// The JSON surface, for programs.
//
// This is a second surface, not a second representation of the first. The pages
// answer in HTML because a browser renders HTML; /api answers in JSON because
// the command-line client cannot render anything. Nothing here negotiates on
// Accept: one URL means one thing, and the two surfaces are free to differ —
// /api has no flash messages, no redirects, and no forms.
//
// Every field name below is a contract. Renaming one is a breaking change for
// every script anybody wrote against it, so it happens in a major release or
// not at all.

type apiRoom struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type apiMessage struct {
	Seq       int64  `json:"seq"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"` // RFC 3339, always UTC
}

// The response envelopes. Each one is an object rather than a bare array, so a
// later release can add a field beside the data instead of breaking the shape.
type (
	apiMe struct {
		Name string `json:"name"`
	}
	apiRooms struct {
		Rooms []apiRoom `json:"rooms"`
	}
	apiRoomBody struct {
		Room apiRoom `json:"room"`
	}
	apiMessages struct {
		Messages []apiMessage `json:"messages"`
		// Since is the cursor to send next time — the same idea the page's
		// poller carries, handed to a program instead of to htmx.
		Since int64 `json:"since"`
	}
	apiMessageBody struct {
		Message apiMessage `json:"message"`
		Since   int64      `json:"since"`
	}
)

// apiFailure is what every non-2xx answer on this surface looks like. Fields
// carries the per-field reasons a 422 has, and is absent otherwise.
type apiFailure struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields,omitempty"`
}

func (a *App) handleAPIMe(w http.ResponseWriter, r *http.Request) {
	a.apiJSON(w, r, http.StatusOK, apiMe{Name: userFrom(r.Context()).Name})
}

func (a *App) handleAPIRoomList(w http.ResponseWriter, r *http.Request) {
	rooms, err := a.rooms.All(r.Context())
	if err != nil {
		a.apiServerError(w, r, err)
		return
	}
	out := apiRooms{Rooms: make([]apiRoom, 0, len(rooms))} // never null in JSON
	for _, room := range rooms {
		out.Rooms = append(out.Rooms, apiRoom{Slug: room.Slug, Name: room.Name})
	}
	a.apiJSON(w, r, http.StatusOK, out)
}

func (a *App) handleAPIRoomCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !a.apiDecode(w, r, &in) {
		return
	}

	name := domain.NormalizeRoomName(in.Name)
	if err := domain.ValidateRoomName(name); err != nil {
		a.apiInvalid(w, r, "name", roomNameProblem(err))
		return
	}
	room, err := domain.NewRoom(rand.Text(), name)
	if err != nil {
		a.apiServerError(w, r, err)
		return
	}

	switch err := a.rooms.Add(r.Context(), room); {
	case err == nil:
	case errors.Is(err, domain.ErrSlugTaken):
		a.apiInvalid(w, r, "name", "A room already answers to that address.")
		return
	default:
		a.apiServerError(w, r, err)
		return
	}
	a.apiJSON(w, r, http.StatusCreated, apiRoomBody{Room: apiRoom{Slug: room.Slug, Name: room.Name}})
}

func (a *App) handleAPIMessageList(w http.ResponseWriter, r *http.Request) {
	room, ok := a.apiRoom(w, r)
	if !ok {
		return
	}
	// Unlike the page's poller, a program may leave this off and mean "from the
	// beginning" — a first call has no cursor to send.
	var since int64
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			a.apiError(w, r, http.StatusBadRequest, "since must be a whole number, zero or more.")
			return
		}
		since = parsed
	}

	msgs, err := a.messages.Since(r.Context(), room.ID, since)
	if err != nil {
		a.apiServerError(w, r, err)
		return
	}
	out := apiMessages{
		Messages: make([]apiMessage, 0, len(msgs)), // never null in JSON
		Since:    domain.LastSeq(msgs, since),
	}
	for _, m := range msgs {
		out.Messages = append(out.Messages, newAPIMessage(m))
	}
	a.apiJSON(w, r, http.StatusOK, out)
}

func (a *App) handleAPIMessageCreate(w http.ResponseWriter, r *http.Request) {
	room, ok := a.apiRoom(w, r)
	if !ok {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if !a.apiDecode(w, r, &in) {
		return
	}

	body := domain.NormalizeBody(in.Body)
	switch err := domain.ValidateBody(body); {
	case err == nil:
	case errors.Is(err, domain.ErrBodyEmpty):
		a.apiInvalid(w, r, "body", "Write something first.")
		return
	default:
		a.apiInvalid(w, r, "body", fmt.Sprintf("Use at most %d characters.", domain.MaxBodyLen))
		return
	}

	user := userFrom(r.Context())
	msg, err := domain.NewMessage(room.ID, user.ID, body)
	if err != nil {
		a.apiServerError(w, r, err)
		return
	}
	if err := a.messages.Add(r.Context(), msg); err != nil {
		a.apiServerError(w, r, err)
		return
	}
	// The store fills in the sequence but not the author's name, which it knows
	// only as an ID. The caller is the author, so it comes from the credential.
	msg.Author = user.Name
	a.apiJSON(w, r, http.StatusCreated, apiMessageBody{
		Message: newAPIMessage(*msg),
		Since:   msg.Seq,
	})
}

func newAPIMessage(m domain.Message) apiMessage {
	return apiMessage{
		Seq:       m.Seq,
		Author:    m.Author,
		Body:      m.Body,
		CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// apiRoom resolves the slug in the URL, answering 404 itself when no room does.
func (a *App) apiRoom(w http.ResponseWriter, r *http.Request) (domain.Room, bool) {
	room, err := a.rooms.BySlug(r.Context(), r.PathValue("slug"))
	switch {
	case err == nil:
		return room, true
	case errors.Is(err, domain.ErrNotFound):
		a.apiError(w, r, http.StatusNotFound, "No room answers to that name.")
	default:
		a.apiServerError(w, r, err)
	}
	return domain.Room{}, false
}

// apiDecode reads a JSON request body into v, answering the caller itself when
// it cannot.
//
// Unknown fields are refused. Both sides of this API ship in one repository, so
// a field the server does not know is a typo or a version mismatch — and
// silently dropping it would post a message the caller believes it sent.
func (a *App) apiDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body) // the body is already capped at 1 MiB by the middleware
	dec.DisallowUnknownFields()
	switch err := dec.Decode(v); {
	case err == nil:
	case errors.As(err, new(*http.MaxBytesError)):
		a.apiError(w, r, http.StatusRequestEntityTooLarge, "That request body is too big.")
		return false
	default:
		a.apiError(w, r, http.StatusBadRequest, "That is not a JSON object this endpoint takes.")
		return false
	}
	// One object per request, so trailing content is a malformed call rather
	// than something to ignore.
	if err := dec.Decode(new(struct{})); !errors.Is(err, io.EOF) {
		a.apiError(w, r, http.StatusBadRequest, "Send one JSON object and nothing after it.")
		return false
	}
	return true
}

func (a *App) apiJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		// Buffer first: a half-written body after WriteHeader cannot be undone.
		a.logger.Error("encoding an API answer", "path", r.URL.Path, "err", err)
		http.Error(w, `{"error":"Sorry, something went wrong."}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(body)
	w.Write([]byte("\n")) // a trailing newline keeps the output pipe-friendly
}

func (a *App) apiError(w http.ResponseWriter, r *http.Request, status int, message string) {
	a.logger.Debug("api client error", "method", r.Method, "path", r.URL.Path, "status", status)
	a.apiJSON(w, r, status, apiFailure{Error: message})
}

// apiInvalid is the 422 answer: which field, and why, in the same shape the
// pages put beside the input.
func (a *App) apiInvalid(w http.ResponseWriter, r *http.Request, field, message string) {
	a.apiJSON(w, r, http.StatusUnprocessableEntity, apiFailure{
		Error:  message,
		Fields: map[string]string{field: message},
	})
}

// apiServerError logs the cause and tells the caller nothing about it.
func (a *App) apiServerError(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.Error("server error", "method", r.Method, "path", r.URL.Path, "err", err)
	a.apiJSON(w, r, http.StatusInternalServerError, apiFailure{Error: "Sorry, something went wrong."})
}

// roomNameProblem words a room-name rule for whoever broke it.
func roomNameProblem(err error) string {
	switch {
	case errors.Is(err, domain.ErrRoomNameUnslugable):
		return "Use at least one letter or digit."
	case errors.Is(err, domain.ErrRoomNameReserved):
		return "That name is reserved. Pick another."
	default:
		return fmt.Sprintf("Use 1 to %d characters.", domain.MaxRoomNameLen)
	}
}
