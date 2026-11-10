// Package chatapi talks to a Go Chat server over HTTP.
//
// It is the adapter: the only package in this repository that knows the server
// has a JSON surface at all. Everything else deals in domain types, so the
// command-line client can be built and tested without a server running.
//
// It imports internal/domain and nothing else of ours — `go list -deps` is how
// that is checked.
package chatapi

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/andygeiss/baseline-reference/v6/internal/domain"
)

// maxBody caps what one answer may be. A server that streams forever would
// otherwise fill this process's memory.
const maxBody = 4 << 20 // 4 MiB

// Client talks to one Go Chat server. It owns its http.Client so callers cannot
// hand it one without timeouts.
type Client struct {
	http    *http.Client
	baseURL string
	token   string
}

// New builds a client for the server at baseURL, signing every request with
// token.
func New(baseURL, token string) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// ResponseHeaderTimeout has no default. Without it, a server that accepts
	// the connection and then never answers is caught only by the whole-call
	// Timeout below.
	tr.ResponseHeaderTimeout = 5 * time.Second

	return &Client{
		// Timeout covers the whole exchange — dial, request, response, and
		// reading the body. It is the backstop; the caller's context is what
		// bounds the operation, which is why every method takes one.
		http:    &http.Client{Transport: tr, Timeout: 10 * time.Second},
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
	}
}

// Me returns the name the token belongs to.
func (c *Client) Me(ctx context.Context) (string, error) {
	var out struct {
		Name string `json:"name"`
	}
	if err := c.get(ctx, "/api/me", &out); err != nil {
		return "", err
	}
	return out.Name, nil
}

// Rooms lists every room on the server.
func (c *Client) Rooms(ctx context.Context) ([]domain.Room, error) {
	var out struct {
		Rooms []struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
		} `json:"rooms"`
	}
	if err := c.get(ctx, "/api/rooms", &out); err != nil {
		return nil, err
	}
	rooms := make([]domain.Room, 0, len(out.Rooms))
	for _, r := range out.Rooms {
		rooms = append(rooms, domain.Room{Slug: r.Slug, Name: r.Name})
	}
	return rooms, nil
}

// Messages returns what was said in a room after the cursor, oldest first, and
// the cursor to use next time.
func (c *Client) Messages(ctx context.Context, slug string, since int64) ([]domain.Message, int64, error) {
	path := "/api/rooms/" + url.PathEscape(slug) + "/messages"
	if since > 0 {
		path += "?since=" + strconv.FormatInt(since, 10)
	}
	var out struct {
		Messages []wireMessage `json:"messages"`
		Since    int64         `json:"since"`
	}
	if err := c.get(ctx, path, &out); err != nil {
		return nil, since, err
	}
	msgs := make([]domain.Message, 0, len(out.Messages))
	for _, m := range out.Messages {
		msg, err := m.domain()
		if err != nil {
			return nil, since, err
		}
		msgs = append(msgs, msg)
	}
	return msgs, out.Since, nil
}

// Post says something in a room and returns the message the server stored.
func (c *Client) Post(ctx context.Context, slug, body string) (domain.Message, error) {
	in := struct {
		Body string `json:"body"`
	}{Body: body}
	var out struct {
		Message wireMessage `json:"message"`
	}
	err := c.post(ctx, "/api/rooms/"+url.PathEscape(slug)+"/messages", in, &out)
	if err != nil {
		return domain.Message{}, err
	}
	return out.Message.domain()
}

// CreateRoom makes a new room and returns it.
func (c *Client) CreateRoom(ctx context.Context, name string) (domain.Room, error) {
	in := struct {
		Name string `json:"name"`
	}{Name: name}
	var out struct {
		Room struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
		} `json:"room"`
	}
	if err := c.post(ctx, "/api/rooms", in, &out); err != nil {
		return domain.Room{}, err
	}
	return domain.Room{Slug: out.Room.Slug, Name: out.Room.Name}, nil
}

// wireMessage is the server's shape. It is separate from domain.Message on
// purpose: the JSON field names are the server's contract, and the domain type
// should not have to change when they do.
type wireMessage struct {
	Seq       int64  `json:"seq"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

func (m wireMessage) domain() (domain.Message, error) {
	at, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil {
		// Neither the value nor time.Parse's error, which quotes it: what the
		// answer held stays out of the error.
		return domain.Message{}, errors.New("chatapi: a message's time is not RFC 3339")
	}
	return domain.Message{Seq: m.Seq, Author: m.Author, Body: m.Body, CreatedAt: at}, nil
}

// errBaseURL is all a request that could not be built says. The method is a
// constant and the context is never nil, so what failed is the URL, and
// NewRequestWithContext's own error quotes it whole: the address somebody
// configured, a password in it if they put one there, and the path and query
// built from this call's arguments. So the wrap carries no %w
// (patterns/go-http-client.md, One request, end to end).
var errBaseURL = errors.New("chatapi: build request: the base URL does not parse")

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return errBaseURL
	}
	return c.do(req, out, true)
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("chatapi: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return errBaseURL
	}
	req.Header.Set("Content-Type", "application/json")
	// Never retried: posting the same message twice is worse than failing.
	return c.do(req, out, false)
}

// do sends one request, retrying only when it is safe to.
//
// A GET may be repeated because repeating it changes nothing. A POST may not:
// the first attempt may have reached the server and only its answer got lost,
// and a retry would say the same thing twice in the room.
func (c *Client) do(req *http.Request, out any, retry bool) error {
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	attempts := 1
	if retry {
		attempts = 3
	}
	var lastErr error
	for attempt := range attempts {
		if attempt > 0 {
			select {
			case <-req.Context().Done():
				return req.Context().Err()
			case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
			}
		}
		res, err := c.http.Do(req)
		if err != nil {
			if cerr := req.Context().Err(); cerr != nil {
				return cerr // the context died, not the server
			}
			// The type, never the text: net/http's text can quote the URL
			// and the peer's bytes, and this error reaches stderr. The host,
			// which the person running the tool configured, and never the
			// path: the path is what was sent.
			lastErr = fmt.Errorf("chatapi: %s %s: no usable answer (%T)", req.Method, req.URL.Host, errors.Unwrap(err))
			continue // a connection that failed may succeed on the next try
		}
		err = decode(res, out)
		if errors.Is(err, errTransient) {
			lastErr = err
			continue
		}
		return err
	}
	return lastErr
}

// errTransient marks the answers worth trying again: the server is busy or
// broken, not the request.
var errTransient = errors.New("the server is busy or failing")

// decode reads one answer, turning the server's status into an error the rest
// of the program already understands.
//
// A failure carries the status code and nothing the server wrote
// (patterns/go-http-client.md, statusError). Its body is where an API quotes
// the request back — a name, an address, a query — and its reason phrase is
// the server's to write too; whatever this returns is printed, and may be
// logged by whoever runs the tool (patterns/go-errors-logging.md, Never log).
// The command says what to do about each kind in its own words.
func decode(res *http.Response, out any) error {
	defer res.Body.Close()

	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		if out == nil {
			return nil
		}
		// Capped: a server that answers forever would otherwise fill this
		// process. No %w: the decoder's text quotes the answer. The context's
		// error first: a cancel that lands while the body is read is the
		// caller giving up, not a bad answer.
		if err := json.UnmarshalRead(io.LimitReader(res.Body, maxBody), out); err != nil {
			return cmp.Or(res.Request.Context().Err(), errors.New("chatapi: the answer is not the JSON this client expects"))
		}
		return nil

	case res.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%w: %w", domain.ErrUnauthorized, statusError(res))
	case res.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %w", domain.ErrNotFound, statusError(res))
	case res.StatusCode == http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %w", domain.ErrRejected, statusError(res))
	case res.StatusCode == http.StatusTooManyRequests,
		res.StatusCode >= 500:
		return fmt.Errorf("chatapi: %w: %w", errTransient, statusError(res))
	default:
		return fmt.Errorf("chatapi: %w", statusError(res))
	}
}

// statusError reports the code and nothing the other side wrote.
func statusError(res *http.Response) error {
	return fmt.Errorf("status %d", res.StatusCode)
}
