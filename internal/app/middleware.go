package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/auth"
	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

// middleware is the one canonical chain, outermost → innermost.
func (a *App) middleware(mux http.Handler) http.Handler {
	csrf := http.NewCrossOriginProtection()
	h := limitBody(mux)
	h = a.sessions.LoadAndSave(h)
	h = csrf.Handler(h)
	return a.logRequests(a.recoverPanic(a.secureHeaders(h)))
}

// uploadLimit is what the one route that takes a file may send: the file itself,
// plus a megabyte for the rest of the form around it.
const uploadLimit = domain.MaxAttachmentBytes + 1<<20

// limitBody caps request bodies at 1 MiB, and at uploadLimit for the route that
// accepts an attachment.
//
// The choice is made here because it cannot be made anywhere else. An outer cap
// cannot be raised further in: by the time a handler runs its body is already
// wrapped in the smaller reader, and nothing downstream can unwrap it
// (patterns/go-http-server.md rule 6, patterns/go-file-uploads.md).
//
// Matching on the path is what that costs. It is one route, and it fails the
// safe way: a second upload route added without a line here meets the 1 MiB cap
// and says so with a 413, rather than quietly accepting more than it should.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(1 << 20)
		if takesAFile(r) {
			limit = uploadLimit
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// takesAFile reports whether this is POST /rooms/{slug}/messages, the page form
// that carries an attachment. The JSON surface posts messages too, and it takes
// no files — the /rooms/ prefix is what keeps /api/rooms/… out.
func takesAFile(r *http.Request) bool {
	return r.Method == http.MethodPost &&
		strings.HasPrefix(r.URL.Path, "/rooms/") &&
		strings.HasSuffix(r.URL.Path, "/messages")
}

// contextKey keeps this package's context keys from colliding with anybody
// else's — a plain string would.
type contextKey string

const userKey contextKey = "user"

// userFrom returns the signed-in user, or the zero User when nobody is signed
// in. Handlers behind requireAuth always get a real one.
func userFrom(ctx context.Context) domain.User {
	u, _ := ctx.Value(userKey).(domain.User)
	return u
}

// The two ways a request can fail to name somebody, which are not the same
// thing. Neither is worth logging: both are normal ways to arrive.
var (
	// errNoCredential means nothing was presented. On the pages that is a
	// reader who is signed out, and the answer is the sign-in page.
	errNoCredential = errors.New("no credential")

	// errBadCredential means a token was presented and refused — revoked,
	// mistyped, or from another deployment. That is 401 on both surfaces: a
	// client whose token stopped working has to be told, and quietly treating
	// it as "signed out" hides the revocation behind a login page.
	errBadCredential = errors.New("bad credential")
)

// authenticate resolves whoever is making this request from either credential —
// a machine token or a browser session.
//
// It answers nothing itself. The two surfaces disagree about what a missing
// credential means: a browser should be sent to the sign-in page, and a program
// should be told 401 in a language it reads. So the middlewares below decide,
// and this decides only who is asking.
func (a *App) authenticate(r *http.Request) (domain.User, error) {
	if secret := auth.BearerToken(r.Header.Get("Authorization")); secret != "" {
		user, err := a.tokens.UserByHash(r.Context(), auth.HashToken(secret))
		if errors.Is(err, domain.ErrNotFound) {
			return domain.User{}, errBadCredential
		}
		if err != nil {
			return domain.User{}, err
		}
		return user, nil
	}

	id := a.sessions.GetString(r.Context(), "userID")
	if id == "" {
		return domain.User{}, errNoCredential
	}
	user, err := a.users.ByID(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) {
		// The account is gone but the cookie is not. Drop the session rather
		// than looping the reader through a sign-in they already did.
		_ = a.sessions.Destroy(r.Context())
		return domain.User{}, errNoCredential
	}
	if err != nil {
		return domain.User{}, err
	}
	// The session records what the password stamp was when it started. A reset
	// moves that stamp, so every session opened before it stops here — which is
	// the point of resetting a password somebody else may know. A machine token
	// is a separate credential and is revoked separately, on the profile page.
	if a.sessions.GetString(r.Context(), "pwAt") != user.PasswordChangedAt {
		_ = a.sessions.Destroy(r.Context())
		return domain.User{}, errNoCredential
	}
	return user, nil
}

// requireAuth guards the pages. A signed-out reader is sent to the sign-in page.
func (a *App) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := a.authenticate(r)
		switch {
		case err == nil:
			next.ServeHTTP(w, withUser(r, user))
		case errors.Is(err, errNoCredential):
			a.toLogin(w, r)
		case errors.Is(err, errBadCredential):
			// Not the sign-in page: whoever sent a token is a program, and a
			// 200 full of HTML would read as success to it.
			a.clientError(w, r, http.StatusUnauthorized)
		default:
			a.serverError(w, r, err)
		}
	})
}

// requireAPIAuth guards /api. A program gets 401 and a JSON body: a redirect to
// a sign-in page would be 200 of HTML it cannot read, and it would look like
// success to anything checking only the status.
func (a *App) requireAPIAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := a.authenticate(r)
		switch {
		case err == nil:
			next.ServeHTTP(w, withUser(r, user))
		case errors.Is(err, errNoCredential):
			a.apiError(w, r, http.StatusUnauthorized, "Send a token: Authorization: Bearer <token>.")
		case errors.Is(err, errBadCredential):
			a.apiError(w, r, http.StatusUnauthorized, "That token is not valid. It may have been revoked.")
		default:
			a.apiServerError(w, r, err)
		}
	})
}

func withUser(r *http.Request, user domain.User) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userKey, user))
}

// toLogin sends a signed-out reader to the login page.
//
// An htmx request gets 200 with HX-Redirect rather than a 303: the XHR would
// follow a redirect by itself and swap the whole login page into whatever
// fragment the reader was looking at. Both answers vary by HX-Request and
// neither goes through render, so they set the Vary header themselves.
func (a *App) toLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "HX-Request, HX-Boosted")
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (a *App) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		a.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
		)
	})
}

func (a *App) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				a.logger.Error("panic", "path", r.URL.Path, "panic", rec)
				http.Error(w, "Sorry, something went wrong.", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// csp is the whole policy, built once. A policy assembled per request is a
// policy that can differ per request, which is a bug.
//
// img-src allows data: so the mask icons in app.css can paint. A CSS image is
// an image request, and 'self' never matches data: — under default-src alone
// the browser refuses every mask. Such an image runs no script and reaches no
// third party, so allowing it gives an attacker nothing new.
//
// base-uri and form-action have no default-src fallback, so they are written
// out: the first stops an injected <base> from re-pointing every root-relative
// URL on the page, the second stops an injected form from posting off-origin.
const csp = "default-src 'self'; " +
	"img-src 'self' data:; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'"

func (a *App) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
