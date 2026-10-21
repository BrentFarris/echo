package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultModel is used when settings do not specify a model name.
	DefaultModel = "clef-flash"
	// DefaultTimeout bounds a single decision-model call. Decision models are
	// expected to answer in well under a second; callers that need a different
	// budget should pass their own context deadline, which always wins.
	DefaultTimeout = 5 * time.Second
	// classifyPath is appended to the configured base URL unless the URL
	// already targets it.
	classifyPath = "/systemone"
)

// ErrDisabled is returned by calls on a client without a configured endpoint.
// Callers should treat it as "feature off" and fall back to their normal
// behavior rather than surfacing an error.
var ErrDisabled = errors.New("decision model is not configured")

// ErrNoQuestions is returned when a builder query finishes without any
// questions, which is always a caller bug.
var ErrNoQuestions = errors.New("decision model query has no questions")

// Client calls a decision-model endpoint. The zero value and nil clients are
// disabled: Enabled reports false and every call returns ErrDisabled, so call
// sites can hold a nil *Client and simply check Enabled.
type Client struct {
	baseURL string
	model   string
	http    *http.Client
}

// New creates a Client for a decision-model base URL (for example
// "http://192.168.50.231:11434/v1"). The /systemone classify path is appended
// automatically. An empty baseURL yields a disabled client.
func New(baseURL, model string) *Client {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return &Client{}
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = DefaultModel
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http:    &http.Client{Timeout: DefaultTimeout},
	}
}

// Enabled reports whether the client has a configured endpoint.
func (c *Client) Enabled() bool {
	return c != nil && c.baseURL != ""
}

// Model returns the model name sent with every request.
func (c *Client) Model() string {
	if c == nil || c.model == "" {
		return DefaultModel
	}
	return c.model
}

// Classify sends a request to the decision model and returns the parsed
// response. A nil request or a disabled client returns ErrDisabled (nil
// request is treated as a programming error but still fails soft). The
// context controls cancellation; when it carries no deadline the client's
// DefaultTimeout applies.
func (c *Client) Classify(ctx context.Context, request *Request) (*Response, error) {
	if !c.Enabled() || request == nil {
		return nil, ErrDisabled
	}
	if request.Model == "" {
		request.Model = c.Model()
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("decision model: encode request: %w", err)
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		// Bound the call even when the caller passed context.Background().
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.classifyURL(), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("decision model: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("decision model: %w", err)
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("decision model: read response: %w", err)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("decision model: endpoint returned %s: %s", httpResp.Status, truncateForError(body))
	}
	var response Response
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decision model: decode response: %w", err)
	}
	return &response, nil
}

// classifyURL joins the configured base URL with the classify path, tolerating
// base URLs that already include it (with or without a trailing slash).
func (c *Client) classifyURL() string {
	if strings.HasSuffix(c.baseURL, classifyPath) {
		return c.baseURL
	}
	return c.baseURL + classifyPath
}

func truncateForError(body []byte) string {
	const max = 256
	text := strings.TrimSpace(string(body))
	if len(text) > max {
		text = text[:max] + "…"
	}
	if text == "" {
		return "(empty response)"
	}
	return text
}
