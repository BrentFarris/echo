package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/brent/echo/internal/domain"
)

type Message struct {
	Role    string            `json:"role"`
	Content string            `json:"content"`
	Parts   []ContentPart     `json:"-"`
	Calls   []domain.ToolCall `json:"tool_calls,omitempty"`
	CallID  string            `json:"tool_call_id,omitempty"`
}
type Delta struct {
	Content   string
	Reasoning string
}
type Result struct {
	Content      string            `json:"content"`
	Reasoning    string            `json:"reasoning"`
	Calls        []domain.ToolCall `json:"toolCalls"`
	FinishReason string            `json:"finishReason"`
}
type Client struct{ HTTP *http.Client }
type localEndpointKey struct{}

func New() *Client {
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, e
		}
		if local, _ := ctx.Value(localEndpointKey{}).(bool); local {
			for _, ip := range ips {
				if !ip.IP.IsLoopback() && !ip.IP.IsPrivate() {
					return nil, errors.New("local model resolved to a public destination; enable Cloud Models")
				}
			}
		}
		var last error
		for _, ip := range ips {
			conn, e := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		if last == nil {
			last = errors.New("model destination has no addresses")
		}
		return nil, last
	}
	// Connections must not cross local/cloud policy boundaries through pooling:
	// every request rechecks the resolved address at the actual connection.
	return &Client{HTTP: &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: dial, DisableKeepAlives: true, ResponseHeaderTimeout: 60 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("model redirects are not allowed") }}}
}
func ValidateEndpoint(e domain.Endpoint) error {
	if err := ValidateSettings(e); err != nil {
		return err
	}
	u, err := url.Parse(e.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("provide an HTTP(S) base URL without credentials, query, or fragment")
	}
	if !e.Cloud {
		ips, err := net.LookupIP(u.Hostname())
		if err != nil {
			return err
		}
		for _, ip := range ips {
			if !ip.IsLoopback() && !ip.IsPrivate() {
				return errors.New("public endpoints require the Cloud Models plugin")
			}
		}
	}
	return nil
}
func URL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		base = strings.TrimSuffix(base, "/chat/completions")
	}
	return base + path
}
func (c *Client) Models(ctx context.Context, e domain.Endpoint, key string) (any, error) {
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, localEndpointKey{}, !e.Cloud), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, URL(e.URL, "/models"), nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("model server returned HTTP %d", res.StatusCode)
	}
	var value any
	err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&value)
	return value, err
}
func (c *Client) Complete(ctx context.Context, e domain.Endpoint, g domain.Generation, messages []Message, tools []domain.Tool, key string, emit func(Delta)) (out Result, err error) {
	ctx = context.WithValue(ctx, localEndpointKey{}, !e.Cloud)
	redact := func(v string) string {
		if key != "" {
			return strings.ReplaceAll(v, key, "[redacted]")
		}
		return v
	}
	var contentPending, reasoningPending string
	filter := func(pending *string, chunk string, final bool) string {
		*pending += chunk
		if key == "" {
			value := *pending
			*pending = ""
			return value
		}
		*pending = redact(*pending)
		keep := 0
		if !final {
			for n := 1; n < len(key) && n <= len(*pending); n++ {
				if strings.HasSuffix(*pending, key[:n]) {
					keep = n
				}
			}
		}
		value := (*pending)[:len(*pending)-keep]
		*pending = (*pending)[len(*pending)-keep:]
		return value
	}
	defer func() {
		delta := Delta{filter(&contentPending, "", true), filter(&reasoningPending, "", true)}
		if emit != nil && (delta.Content != "" || delta.Reasoning != "") {
			emit(delta)
		}
		out.Content = redact(out.Content)
		out.Reasoning = redact(out.Reasoning)
		for i := range out.Calls {
			out.Calls[i].Function.Arguments = redact(out.Calls[i].Function.Arguments)
		}
		if err != nil {
			err = errors.New(redact(err.Error()))
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if e.RequestTimeoutSeconds > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, time.Duration(e.RequestTimeoutSeconds)*time.Second)
		defer timeoutCancel()
	}
	body := generationParameters(e, g)
	body["messages"] = messages
	if len(tools) > 0 {
		defs := []any{}
		for _, t := range tools {
			defs = append(defs, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Parameters}})
		}
		body["tools"] = defs
	}
	data, err := json.Marshal(body)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, URL(e.URL, "/chat/completions"), bytes.NewReader(data))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return out, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		text := string(b)
		if key != "" {
			text = strings.ReplaceAll(text, key, "[redacted]")
		}
		return out, fmt.Errorf("model HTTP %d: %s", response.StatusCode, text)
	}
	idle := time.Duration(e.StreamIdleTimeoutSeconds) * time.Second
	if idle == 0 {
		idle = 120 * time.Second
	}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	if idle > 0 {
		timer = time.AfterFunc(idle, cancel)
	}
	defer timer.Stop()
	scanner := bufio.NewScanner(activityReader{response.Body, func() {
		if idle > 0 {
			timer.Reset(idle)
		}
	}})
	scanner.Buffer(make([]byte, 65536), 4<<20)
	calls := map[int]*domain.ToolCall{}
	done := false
	lines := []string{}
	flush := func() error {
		if len(lines) == 0 {
			return nil
		}
		raw := strings.Join(lines, "\n")
		lines = nil
		if raw == "[DONE]" {
			done = true
			return nil
		}
		var chunk struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Choices []struct {
				Delta struct {
					Content      string `json:"content"`
					Reasoning    string `json:"reasoning_content"`
					ReasoningAlt string `json:"reasoning"`
					Calls        []struct {
						Index    int                 `json:"index"`
						ID       string              `json:"id"`
						Function domain.FunctionCall `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			return fmt.Errorf("invalid model stream: %w", err)
		}
		if chunk.Error != nil {
			return errors.New(chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		choice := chunk.Choices[0]
		d := choice.Delta
		if d.Reasoning == "" {
			d.Reasoning = d.ReasoningAlt
		}
		out.Content += d.Content
		out.Reasoning += d.Reasoning
		if len(out.Content)+len(out.Reasoning) > 8<<20 {
			return errors.New("model response exceeded 8 MiB")
		}
		if emit != nil && (d.Content != "" || d.Reasoning != "") {
			emit(Delta{filter(&contentPending, d.Content, false), filter(&reasoningPending, d.Reasoning, false)})
		}
		for _, v := range d.Calls {
			if v.Index < 0 || v.Index > 127 {
				return errors.New("invalid tool-call index")
			}
			call := calls[v.Index]
			if call == nil {
				call = &domain.ToolCall{Type: "function"}
				calls[v.Index] = call
			}
			if v.ID != "" {
				call.ID = v.ID
			}
			call.Function.Name += v.Function.Name
			call.Function.Arguments += v.Function.Arguments
			if len(call.Function.Arguments) > 1<<20 {
				return errors.New("tool arguments exceeded 1 MiB")
			}
		}
		if choice.FinishReason != "" {
			out.FinishReason = choice.FinishReason
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err = flush(); err != nil {
				return out, err
			}
			if done {
				break
			}
		} else if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err = flush(); err != nil {
		return out, err
	}
	if err = scanner.Err(); err != nil {
		return out, err
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if !done && out.FinishReason == "" {
		return out, errors.New("model stream ended before completion")
	}
	indices := []int{}
	for i := range calls {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	for _, i := range indices {
		call := calls[i]
		if call.ID == "" {
			call.ID = domain.ID()
		}
		if !json.Valid([]byte(call.Function.Arguments)) {
			return out, errors.New("model returned invalid tool arguments")
		}
		out.Calls = append(out.Calls, *call)
	}
	return out, nil
}
