package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	settings   Settings
	endpoint   string
	apiKey     string
	httpClient *http.Client

	nextID        atomic.Uint64
	mu            sync.Mutex
	activeStreams map[string]context.CancelFunc
	conversations map[string][]Message
}

type ClientOption func(*Client)

type Stream struct {
	ID     string
	Events <-chan StreamEvent

	cancel context.CancelFunc
}

func NewClient(settings Settings, options ...ClientOption) (*Client, error) {
	settings = settings.Normalized()
	if err := settings.Validate(); err != nil {
		return nil, err
	}

	client := &Client{
		settings:      settings,
		endpoint:      chatCompletionsURL(settings.Endpoint),
		httpClient:    defaultHTTPClient(settings.TimeoutSeconds),
		activeStreams: make(map[string]context.CancelFunc),
		conversations: make(map[string][]Message),
	}
	for _, option := range options {
		option(client)
	}
	return client, nil
}

func WithAPIKey(apiKey string) ClientOption {
	return func(client *Client) {
		client.apiKey = strings.TrimSpace(apiKey)
	}
}

func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(client *Client) {
		if httpClient != nil {
			client.httpClient = httpClient
		}
	}
}

func (c *Client) Complete(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	request.Stream = false

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, time.Duration(c.settings.TimeoutSeconds)*time.Second)
	defer cancel()

	body, err := json.Marshal(request)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal chat request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("create chat request: %w", err)
	}
	c.applyHeaders(httpRequest)

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("send chat request: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ChatResponse{}, responseError(response)
	}

	var chatResponse ChatResponse
	if err := json.NewDecoder(response.Body).Decode(&chatResponse); err != nil {
		return ChatResponse{}, fmt.Errorf("decode chat response: %w", err)
	}
	return chatResponse, nil
}

func (c *Client) StreamChat(ctx context.Context, request ChatRequest) *Stream {
	streamID := c.newStreamID()
	streamContext, cancel := context.WithCancel(ctx)
	events := make(chan StreamEvent, 32)
	streamLogger := newStreamLogger(streamID, c.endpoint)

	c.mu.Lock()
	c.activeStreams[streamID] = cancel
	c.mu.Unlock()

	stream := &Stream{
		ID:     streamID,
		Events: events,
		cancel: cancel,
	}

	go func() {
		defer close(events)
		defer c.forgetStream(streamID)

		request.Stream = true
		body, err := json.Marshal(request)
		if err != nil {
			emitLogged(streamContext, events, StreamEvent{Type: EventError, Error: fmt.Sprintf("marshal chat request: %v", err)}, streamLogger)
			return
		}

		httpRequest, err := http.NewRequestWithContext(streamContext, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			emitLogged(streamContext, events, StreamEvent{Type: EventError, Error: fmt.Sprintf("create chat request: %v", err)}, streamLogger)
			return
		}
		c.applyHeaders(httpRequest)
		httpRequest.Header.Set("Accept", "text/event-stream")

		response, err := c.httpClient.Do(httpRequest)
		if err != nil {
			if streamContext.Err() != nil {
				emitCanceledLogged(events, streamLogger)
				return
			}
			emitLogged(streamContext, events, StreamEvent{Type: EventError, Error: fmt.Sprintf("send chat request: %v", err)}, streamLogger)
			return
		}
		defer response.Body.Close()

		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			emitLogged(streamContext, events, StreamEvent{Type: EventError, Error: responseError(response).Error()}, streamLogger)
			return
		}

		parseStreamLogged(streamContext, response.Body, events, streamLogger)
	}()

	return stream
}

func (s *Stream) Cancel() {
	if s != nil && s.cancel != nil {
		s.cancel()
	}
}

func (c *Client) Cancel(streamID string) bool {
	c.mu.Lock()
	cancel, ok := c.activeStreams[streamID]
	c.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

func (c *Client) ActiveStreamCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.activeStreams)
}

func (c *Client) SetConversationMessages(conversationID string, messages []Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conversations[conversationID] = append([]Message(nil), messages...)
}

func (c *Client) AppendConversationMessage(conversationID string, message Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conversations[conversationID] = append(c.conversations[conversationID], message)
}

func (c *Client) ConversationMessages(conversationID string) []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Message(nil), c.conversations[conversationID]...)
}

func (c *Client) ClearConversation(conversationID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.conversations, conversationID)
}

func (c *Client) newStreamID() string {
	return fmt.Sprintf("stream-%d", c.nextID.Add(1))
}

func (c *Client) forgetStream(streamID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.activeStreams, streamID)
}

func (c *Client) applyHeaders(request *http.Request) {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

func defaultHTTPClient(timeoutSeconds int) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = time.Duration(timeoutSeconds) * time.Second
	return &http.Client{Transport: transport}
}

func responseError(response *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	detail := strings.TrimSpace(string(data))
	if detail == "" {
		return fmt.Errorf("llm endpoint returned %s", response.Status)
	}
	return fmt.Errorf("llm endpoint returned %s: %s", response.Status, detail)
}

// IsContextLengthExceeded reports whether an endpoint rejected a request because
// its prompt was larger than the model's available context window.
func IsContextLengthExceeded(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"exceed_context_size_error",
		"context_length_exceeded",
		"exceeds the available context size",
		"maximum context length",
		"context window is too small",
		"too many tokens",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func chatCompletionsURL(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if strings.HasSuffix(endpoint, "/chat/completions") {
		return endpoint
	}
	return endpoint + "/chat/completions"
}
