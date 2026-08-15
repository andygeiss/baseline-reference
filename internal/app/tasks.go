package app

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// tasksView is the template data for tasks.html and every fragment in it.
type tasksView struct {
	Tasks       []domain.Task
	Open        int
	MaxTitleLen int // the client-side cap, so one number reaches both sides
	Form        taskForm
	Message     string
}

func newTasksView(tasks []domain.Task, form taskForm) tasksView {
	return tasksView{
		Tasks:       tasks,
		Open:        domain.Open(tasks),
		MaxTitleLen: domain.MaxTitleLen,
		Form:        form,
	}
}

func (a *App) handleTasksShow(w http.ResponseWriter, r *http.Request) {
	a.renderTasks(w, r, http.StatusOK, "", taskForm{}, "")
}

func (a *App) handleTaskAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		status := http.StatusBadRequest // malformed body — not a validation failure
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge // the 1 MiB cap
		}
		a.clientError(w, r, status)
		return
	}

	// Normalize before checking, so a title of three spaces is empty and a
	// pasted line break cannot reach the length check as a character.
	form := taskForm{Title: domain.NormalizeTitle(r.PostFormValue("title"))}
	form.Check(form.Title != "", "title", "Write down what you need to do.")
	form.Check(utf8.RuneCountInString(form.Title) <= domain.MaxTitleLen, "title",
		fmt.Sprintf("Use at most %d characters.", domain.MaxTitleLen))
	if !form.Valid() {
		a.renderInvalid(w, r, form)
		return
	}

	// rand.Text: 128 unguessable bits, URL-safe, no encoding step.
	task, err := domain.NewTask(rand.Text(), form.Title)
	if err != nil {
		// Unreachable while the checks above state the same rules. It stays
		// because the domain, not this handler, decides what a task may be.
		a.serverError(w, r, err)
		return
	}
	a.answerChange(w, r, a.tasks.Add(r.Context(), task))
}

func (a *App) handleTaskToggle(w http.ResponseWriter, r *http.Request) {
	a.answerChange(w, r, a.tasks.Update(r.Context(), r.PathValue("id"),
		func(t *domain.Task) error { t.Toggle(); return nil }))
}

func (a *App) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	a.answerChange(w, r, a.tasks.Delete(r.Context(), r.PathValue("id")))
}

// answerChange answers one change to the list. A missing task becomes a word
// for the reader instead of a 404: tapping a row that another tab already
// deleted is not bad input, it is a stale list, and the answer is the current
// one with a note about what happened.
func (a *App) answerChange(w http.ResponseWriter, r *http.Request, err error) {
	var message string
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrNotFound):
		message = "That task is gone."
	default:
		a.serverError(w, r, err)
		return
	}

	// htmx gets the fresh region with the status line swapped in out of band;
	// everything else follows POST-redirect-GET back to the list.
	if isHTMX(r) {
		a.renderTasks(w, r, http.StatusOK, "todo-swap", taskForm{}, message)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// renderInvalid re-renders the form and the list with the field errors at 422.
// The counts did not change, so this fragment leaves the live region alone.
func (a *App) renderInvalid(w http.ResponseWriter, r *http.Request, form taskForm) {
	if r.Header.Get("HX-Boosted") == "true" {
		// A boosted swap otherwise pushes the POST URL into history, and a
		// refresh then GETs a route that only answers POST.
		w.Header().Set("HX-Push-Url", "false")
	}
	a.renderTasks(w, r, http.StatusUnprocessableEntity, "todo", form, "")
}

// renderTasks reads the current list and renders it — the whole page, or the
// named block when the request came from htmx.
func (a *App) renderTasks(w http.ResponseWriter, r *http.Request, status int, block string, form taskForm, message string) {
	tasks, err := a.tasks.All(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	view := newTasksView(tasks, form)
	view.Message = message
	a.render(w, r, status, "tasks.html", block, view)
}
