// Package anthropic is the only package that knows Anthropic's API. It
// translates in both directions: the app's history in, the app's answer or
// domain.ErrRefused out, and nothing vendor-shaped escapes either way.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/andygeiss/baseline-reference/v3/internal/domain"
)

const (
	endpoint = "https://api.anthropic.com/v1/messages"

	// apiVersion is Anthropic's dated API contract, not a model version. It
	// changes far more slowly than anything else here.
	apiVersion = "2023-06-01"

	// model, effort and maxTokens are the adapter's business and nobody else's
	// — the port names none of them, so changing which model answers is a
	// change to this file.
	//
	// Read from the claude-api skill on 2026-08-17; that skill, not this
	// repository and not the baseline, is the source of truth for anything on
	// the wire. Re-read it before changing any of these three.
	model = "claude-opus-5"

	// Adaptive thinking stays on and cost is controlled with effort instead.
	// Switching thinking off is the more expensive lever in every sense: on
	// current models a low effort level costs less than a disabled-thinking
	// request that rambles, and thinking-off can push the reasoning into the
	// visible answer — which in a chat room is the product bug, not a saving.
	effort = "low"

	// maxTokens is a hard cap on thinking *plus* the visible answer, so it is
	// sized for both. A ceiling sized for the two-sentence reply the prompt
	// asks for would truncate the reply the moment the model thinks.
	maxTokens = 4096

	// maxBody bounds what a compromised or confused server can make this
	// process allocate.
	maxBody = 1 << 20
)

// Assistant answers with a language model over Anthropic's API.
type Assistant struct {
	http *http.Client
	key  string

	// endpoint is the const above everywhere but in the wire-contract test,
	// which points it at an httptest server. Nothing here may ever reach the
	// live API: it is slow, non-deterministic, and somebody's bill.
	endpoint string
}

// New builds the adapter. It performs no request: boot may not wait on somebody
// else's system, so a missing or wrong key is discovered by the first reply,
// which is a broken feature rather than a crash loop
// (patterns/go-http-client.md).
func New(key string) *Assistant {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// Without this a server that accepts the connection and then says nothing
	// holds the request open until the client Timeout below.
	tr.ResponseHeaderTimeout = 10 * time.Second

	return &Assistant{
		key:      key,
		endpoint: endpoint,
		// At or above the handler's budget, never below it: the budget is what
		// gives up, so "this model is slow" and "we gave up on it" stay two
		// different events (patterns/go-http-server.md, the timeout ladder).
		http: &http.Client{Transport: tr, Timeout: 15 * time.Second},
	}
}

type (
	request struct {
		Model        string       `json:"model"`
		MaxTokens    int          `json:"max_tokens"`
		System       string       `json:"system"`
		Thinking     thinking     `json:"thinking"`
		OutputConfig outputConfig `json:"output_config"`
		Messages     []wireTurn   `json:"messages"`
	}
	thinking struct {
		Type string `json:"type"`
	}
	outputConfig struct {
		Effort string `json:"effort"`
	}
	wireTurn struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	response struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
)

// Reply answers the room. The vendor's words stop here.
func (a *Assistant) Reply(ctx context.Context, history []domain.Message) (string, error) {
	turns := domain.Alternating(history)
	if len(turns) == 0 {
		return "", fmt.Errorf("nothing to reply to")
	}
	wire := make([]wireTurn, 0, len(turns))
	for _, t := range turns {
		role := "user"
		if t.FromAssistant {
			role = "assistant"
		}
		wire = append(wire, wireTurn{Role: role, Content: t.Text})
	}

	body, err := json.Marshal(request{
		Model:        model,
		MaxTokens:    maxTokens,
		System:       domain.SystemPrompt,
		Thinking:     thinking{Type: "adaptive"},
		OutputConfig: outputConfig{Effort: effort},
		Messages:     wire,
	})
	if err != nil {
		return "", fmt.Errorf("encoding the request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("building the request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", apiVersion)
	req.Header.Set("x-api-key", a.key)

	res, err := a.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("asking the model: %w", err)
	}
	defer drainAndClose(res)

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("asking the model: %s", res.Status)
	}

	var out response
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBody)).Decode(&out); err != nil {
		return "", fmt.Errorf("decoding the reply: %w", err)
	}

	// The refusal check comes before reading the text: a declined request is a
	// successful 200 whose content is empty or partial, so indexing into it
	// first is the bug this ordering prevents — on the one path that is hardest
	// to reproduce by hand.
	if out.StopReason == "refusal" {
		return "", domain.ErrRefused
	}

	for _, c := range out.Content {
		if c.Type == "text" && c.Text != "" {
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("the model answered with no text")
}

// drainAndClose reads the rest of a response before closing it, so the
// connection goes back to the pool instead of being thrown away.
func drainAndClose(res *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxBody))
	_ = res.Body.Close()
}
