package app

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	reference "github.com/andygeiss/baseline-reference"
	"github.com/andygeiss/baseline-reference/internal/domain"
	"github.com/andygeiss/baseline-reference/internal/store"
)

type testApp struct {
	*App
	tasks *store.Tasks
	srv   *httptest.Server
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	templatesFS, err := fs.Sub(reference.TemplatesFS, "web/templates")
	if err != nil {
		t.Fatalf("templates fs: %v", err)
	}
	staticFS, err := fs.Sub(reference.StaticFS, "web")
	if err != nil {
		t.Fatalf("static fs: %v", err)
	}
	tasks := store.NewTasks(db)
	a, err := New(slog.New(slog.DiscardHandler), tasks, templatesFS, staticFS, "test")
	if err != nil {
		t.Fatalf("building app: %v", err)
	}

	srv := httptest.NewServer(a.Routes())
	t.Cleanup(srv.Close)
	srv.Client().CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse // assert on redirects instead of following them
	}
	return &testApp{App: a, tasks: tasks, srv: srv}
}

// do issues a request against the full middleware chain and returns the
// response with its body read.
func (ta *testApp) do(t *testing.T, method, path string, form url.Values, headers map[string]string) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, ta.srv.URL+path, body)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := ta.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return res, string(b)
}

// seed puts a task on the list directly through the store.
func (ta *testApp) seed(t *testing.T, id, title string) *domain.Task {
	t.Helper()
	task, err := domain.NewTask(id, title)
	if err != nil {
		t.Fatalf("building task %s: %v", id, err)
	}
	if err := ta.tasks.Add(t.Context(), task); err != nil {
		t.Fatalf("seeding task %s: %v", id, err)
	}
	return task
}

// list reads the stored tasks, to check what a request actually changed.
func (ta *testApp) list(t *testing.T) []domain.Task {
	t.Helper()
	tasks, err := ta.tasks.All(t.Context())
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	return tasks
}

var htmxHeaders = map[string]string{"HX-Request": "true"}

func TestTasksShow_EmptyList(t *testing.T) {
	t.Parallel()
	res, body := newTestApp(t).do(t, "GET", "/", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(body, "<html") || !strings.Contains(body, `id="todo"`) {
		t.Error("the page is missing its document or the add form")
	}
	if !strings.Contains(body, "Your list is empty") {
		t.Error("an empty list says nothing to the reader")
	}
	// The security headers are asserted once, in TestSecureHeaders — the same
	// single-owner rule the baseline applies to the policy itself.
}

func TestTasksShow_ListsTasksWithTheirState(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.seed(t, "t1", "Buy milk")
	ta.seed(t, "t2", "Call Ada")
	if err := ta.tasks.Update(t.Context(), "t2", func(task *domain.Task) error {
		task.Toggle()
		return nil
	}); err != nil {
		t.Fatalf("toggling t2: %v", err)
	}

	_, body := ta.do(t, "GET", "/", nil, nil)
	if !strings.Contains(body, "Buy milk") || !strings.Contains(body, "Call Ada") {
		t.Error("the page does not list both tasks")
	}
	if !strings.Contains(body, `aria-pressed="false"`) || !strings.Contains(body, `aria-pressed="true"`) {
		t.Error("done and open rows are not told apart in words")
	}
	if !strings.Contains(body, "1 of 2 still open") {
		t.Error("the status line does not count the open tasks")
	}
}

func TestTaskAdd_PlainFormRedirects(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	res, _ := ta.do(t, "POST", "/tasks", url.Values{"title": {"Buy milk"}}, nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (POST-redirect-GET without htmx)", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /", loc)
	}
	tasks := ta.list(t)
	if len(tasks) != 1 || tasks[0].Title != "Buy milk" {
		t.Errorf("the task was not stored: %+v", tasks)
	}
}

func TestTaskAdd_HTMXReturnsFragment(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	res, body := ta.do(t, "POST", "/tasks", url.Values{"title": {"Buy milk"}}, htmxHeaders)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if strings.Contains(body, "<html") {
		t.Error("an htmx request got a full page, want the fragment only")
	}
	if !strings.Contains(body, `id="todo"`) || !strings.Contains(body, "Buy milk") {
		t.Error("the fragment is missing the swapped region or the new task")
	}
	if vary := strings.Join(res.Header.Values("Vary"), ","); !strings.Contains(vary, "HX-Request") ||
		!strings.Contains(vary, "HX-Boosted") {
		t.Errorf("Vary = %q, want HX-Request and HX-Boosted", vary)
	}
}

func TestTaskAdd_BoostedGetsRedirect(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	res, _ := ta.do(t, "POST", "/tasks", url.Values{"title": {"Buy milk"}},
		map[string]string{"HX-Request": "true", "HX-Boosted": "true"})
	if res.StatusCode != http.StatusSeeOther {
		t.Errorf("boosted: status = %d, want 303 (needs a full page, not a fragment)", res.StatusCode)
	}
}

func TestTaskAdd_NormalizesTheTitle(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	ta.do(t, "POST", "/tasks", url.Values{"title": {"  Buy \t milk\n"}}, htmxHeaders)
	tasks := ta.list(t)
	if len(tasks) != 1 || tasks[0].Title != "Buy milk" {
		t.Errorf("stored %+v, want the title trimmed and collapsed", tasks)
	}
}

func TestTaskAdd_InvalidTitleIs422WithTheValueKept(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, title, wantError string
	}{
		{"empty", "", "Write down what you need to do."},
		{"spaces only", "   ", "Write down what you need to do."},
		{"too long", strings.Repeat("a", domain.MaxTitleLen+1), "Use at most 120 characters."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ta := newTestApp(t)

			res, body := ta.do(t, "POST", "/tasks", url.Values{"title": {tt.title}}, htmxHeaders)
			if res.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", res.StatusCode)
			}
			if !strings.Contains(body, tt.wantError) {
				t.Errorf("the response does not say what is wrong: %q", body)
			}
			if len(ta.list(t)) != 0 {
				t.Error("an invalid task was stored")
			}
		})
	}

	// The submitted value comes back, so nothing has to be retyped.
	ta := newTestApp(t)
	long := strings.Repeat("b", domain.MaxTitleLen+1)
	_, body := ta.do(t, "POST", "/tasks", url.Values{"title": {long}}, htmxHeaders)
	if !strings.Contains(body, long) {
		t.Error("the 422 response dropped the submitted title")
	}
}

// A boosted 422 must tell htmx not to push the POST URL: a refresh would
// otherwise GET a route that only answers POST.
func TestTaskAdd_BoostedInvalidDoesNotPushTheURL(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	res, _ := ta.do(t, "POST", "/tasks", url.Values{"title": {""}},
		map[string]string{"HX-Request": "true", "HX-Boosted": "true"})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.StatusCode)
	}
	if got := res.Header.Get("HX-Push-Url"); got != "false" {
		t.Errorf("HX-Push-Url = %q, want false", got)
	}
}

func TestTaskToggle(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.seed(t, "t1", "Buy milk")

	_, body := ta.do(t, "POST", "/tasks/t1/toggle", url.Values{}, htmxHeaders)
	if !strings.Contains(body, `aria-pressed="true"`) {
		t.Error("the fragment does not show the task as done")
	}
	if tasks := ta.list(t); !tasks[0].Done {
		t.Error("the toggle was not persisted")
	}

	ta.do(t, "POST", "/tasks/t1/toggle", url.Values{}, htmxHeaders)
	if tasks := ta.list(t); tasks[0].Done {
		t.Error("the second toggle did not reopen the task")
	}
}

func TestTaskDelete(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.seed(t, "t1", "Buy milk")

	res, body := ta.do(t, "POST", "/tasks/t1/delete", url.Values{}, htmxHeaders)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if strings.Contains(body, "Buy milk") {
		t.Error("the deleted task is still in the fragment")
	}
	if len(ta.list(t)) != 0 {
		t.Error("the task was not deleted")
	}
}

// Acting on a task another tab already deleted is a stale list, not bad input:
// the answer is the current list plus a word about what happened.
func TestChangeOnAMissingTaskHealsTheList(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"toggle", "delete"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			ta := newTestApp(t)
			ta.seed(t, "t1", "Buy milk")

			res, body := ta.do(t, "POST", "/tasks/gone/"+action, url.Values{}, htmxHeaders)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.StatusCode)
			}
			if !strings.Contains(body, "That task is gone.") {
				t.Error("the response does not say what happened")
			}
			if !strings.Contains(body, "Buy milk") {
				t.Error("the response does not carry the current list")
			}
		})
	}
}

// The status line lives outside the swapped region, in a live region the swap
// leaves in place — so the fragment updates it out of band.
func TestFragmentUpdatesStatusOutOfBand(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	_, body := ta.do(t, "POST", "/tasks", url.Values{"title": {"Buy milk"}}, htmxHeaders)
	if !strings.Contains(body, `hx-swap-oob="innerHTML:#status"`) {
		t.Error("the fragment does not update the status region out of band")
	}

	_, page := ta.do(t, "GET", "/", nil, nil)
	if !strings.Contains(page, `id="status"`) || !strings.Contains(page, `aria-live="polite"`) {
		t.Error("the page is missing the live status region the fragment targets")
	}
	if strings.Contains(page, "hx-swap-oob") {
		t.Error("the full page renders an out-of-band status: it would show twice")
	}

	// A 422 changes no count, so it leaves the live region alone.
	_, invalid := ta.do(t, "POST", "/tasks", url.Values{"title": {""}}, htmxHeaders)
	if strings.Contains(invalid, "hx-swap-oob") {
		t.Error("an invalid submission re-announces a count that did not change")
	}
}

// failingStore fails every call, so the handlers' unexpected-error path runs.
// The message is a stand-in for the kind of internal detail a real driver error
// carries — it must never reach the browser.
type failingStore struct{}

const storeFailure = "table tasks is locked"

func (failingStore) Add(context.Context, *domain.Task) error { return errors.New(storeFailure) }

func (failingStore) All(context.Context) ([]domain.Task, error) {
	return nil, errors.New(storeFailure)
}

func (failingStore) Update(context.Context, string, func(*domain.Task) error) error {
	return errors.New(storeFailure)
}

func (failingStore) Delete(context.Context, string) error { return errors.New(storeFailure) }

func TestHandlers_StoreFailureIs500WithoutTheDetail(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)
	ta.App.tasks = failingStore{} // before the first request: no concurrent use

	tests := []struct {
		name, method, path string
		form               url.Values
	}{
		{"show", "GET", "/", nil},
		{"add", "POST", "/tasks", url.Values{"title": {"Buy milk"}}},
		{"toggle", "POST", "/tasks/t1/toggle", url.Values{}},
		{"delete", "POST", "/tasks/t1/delete", url.Values{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, body := ta.do(t, tt.method, tt.path, tt.form, nil)
			if res.StatusCode != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", res.StatusCode)
			}
			if strings.Contains(body, storeFailure) {
				t.Errorf("the response leaks the internal error: %q", body)
			}
		})
	}
}

func TestStaticAssets(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	res, body := ta.do(t, "GET", "/static/css/app.css", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "@layer") {
		t.Fatalf("app.css: status = %d", res.StatusCode)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", cc)
	}

	res, _ = ta.do(t, "GET", "/static/css/", nil, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("directory listing: status = %d, want 404", res.StatusCode)
	}
}

func TestCSRF_CrossOriginPostRejected(t *testing.T) {
	t.Parallel()
	ta := newTestApp(t)

	res, _ := ta.do(t, "POST", "/tasks", url.Values{"title": {"Buy milk"}},
		map[string]string{"Sec-Fetch-Site": "cross-site"})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST: status = %d, want 403", res.StatusCode)
	}
}
