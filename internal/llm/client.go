package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Client communicates with an OpenAI-compatible chat completions endpoint.
type Client struct {
	settings   Settings
	endpoint   string
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger

	nextID                 atomic.Uint64
	streamUsageUnsupported atomic.Bool
	mu                     sync.Mutex
	activeStreams          map[string]context.CancelFunc
	conversations          map[string][]Message
}

type ClientOption func(*Client)

type streamIdleWatchdog struct {
	mu       sync.Mutex
	timer    *time.Timer
	timeout  time.Duration
	cancel   context.CancelFunc
	stopped  bool
	timedOut atomic.Bool
}

func newStreamIdleWatchdog(timeout time.Duration, cancel context.CancelFunc) *streamIdleWatchdog {
	if timeout <= 0 || cancel == nil {
		return nil
	}
	watchdog := &streamIdleWatchdog{timeout: timeout, cancel: cancel}
	watchdog.timer = time.AfterFunc(timeout, watchdog.expire)
	return watchdog
}

func (w *streamIdleWatchdog) Activity() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.timedOut.Load() {
		return
	}
	w.timer.Stop()
	w.timer.Reset(w.timeout)
}

func (w *streamIdleWatchdog) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.stopped = true
	w.timer.Stop()
}

func (w *streamIdleWatchdog) TimedOut() bool {
	return w != nil && w.timedOut.Load()
}

func (w *streamIdleWatchdog) expire() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.timedOut.Store(true)
	cancel := w.cancel
	w.mu.Unlock()
	cancel()
}

// Stream is a live chat completion stream. Read from Events until it closes;
// the channel is closed when the stream finishes, errors, or is canceled.
type Stream struct {
	ID     string
	Events <-chan StreamEvent
	Usage  *Usage

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
		logger:        slog.Default(),
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

func WithLogger(logger *slog.Logger) ClientOption {
	return func(client *Client) {
		if logger != nil {
			client.logger = logger
		}
	}
}

// Complete performs a single non-streaming chat completion request.
func (c *Client) Complete(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	request.Stream = false
	request.StreamOptions = nil

	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, time.Duration(c.settings.TimeoutSeconds)*time.Second)
	defer cancel()

	body, err := json.Marshal(request)
	if err != nil {
		c.logger.Error("llm_error", "model", request.Model, "error", err.Error())
		return ChatResponse{}, fmt.Errorf("marshal chat request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		c.logger.Error("llm_error", "error", err.Error())
		return ChatResponse{}, fmt.Errorf("create chat request: %w", err)
	}
	clientRequestID := c.applyHeaders(httpRequest)

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		c.logger.Error("llm_error", "client_request_id", clientRequestID, "error", err.Error())
		return ChatResponse{}, fmt.Errorf("send chat request: %w", err)
	}
	defer response.Body.Close()
	serverRequestID := response.Header.Get("x-request-id")

	data, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		c.logger.Error("llm_error", "error", readErr.Error())
		return ChatResponse{}, fmt.Errorf("read chat response: %w", readErr)
	}
	compressionRequest := isContextCompressionRequest(request)
	if !compressionRequest {
		c.logger.Debug("llm_response", "status", response.StatusCode, "request_id", serverRequestID, "client_request_id", clientRequestID, "payload", string(data))
	}

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ChatResponse{}, responseErrorData(response.StatusCode, response.Status, data)
	}
	var chatResponse ChatResponse
	if err := json.Unmarshal(data, &chatResponse); err != nil {
		c.logger.Error("llm_error", "error", err.Error())
		return ChatResponse{}, fmt.Errorf("decode chat response: %w", err)
	}
	if compressionRequest {
		c.logger.Debug("llm_response", "status", response.StatusCode, "request_id", serverRequestID, "client_request_id", clientRequestID, "payload_bytes", len(data), "usage", chatResponse.Usage)
	}
	return chatResponse, nil
}

func isContextCompressionRequest(request ChatRequest) bool {
	for _, message := range request.Messages {
		if message.Name == "echo-context-summary" {
			return true
		}
	}
	return false
}

// StreamChat starts a streaming chat completion request and returns a Stream.
// The stream runs in the background; consume Events until it closes.
func (c *Client) StreamChat(ctx context.Context, request ChatRequest) *Stream {
	streamID := c.newStreamID()
	streamContext, cancel := context.WithCancel(ctx)
	events := make(chan StreamEvent, 32)

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
		requestContext, cancelRequest := context.WithCancel(streamContext)
		defer cancelRequest()

		request.Stream = true
		if c.streamUsageUnsupported.Load() {
			request.StreamOptions = nil
		} else if request.StreamOptions == nil {
			request.StreamOptions = &StreamOptions{IncludeUsage: true}
		}

		var response *http.Response
		var clientRequestID string
		for attempt := 0; attempt < 2; attempt++ {
			body, err := json.Marshal(request)
			if err != nil {
				c.logger.Error("llm_error", "model", request.Model, "error", err.Error())
				emitLogged(streamContext, events, StreamEvent{Type: EventError, Error: fmt.Sprintf("marshal chat request: %v", err)})
				return
			}
			httpRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost, c.endpoint, bytes.NewReader(body))
			if err != nil {
				emitLogged(streamContext, events, StreamEvent{Type: EventError, Error: fmt.Sprintf("create chat request: %v", err)})
				return
			}
			clientRequestID = c.applyHeaders(httpRequest)
			httpRequest.Header.Set("Accept", "text/event-stream")
			response, err = c.httpClient.Do(httpRequest)
			if err != nil {
				if streamContext.Err() != nil {
					emitCanceledLogged(events)
					return
				}
				emitLogged(streamContext, events, StreamEvent{Type: EventError, Error: fmt.Sprintf("send chat request: %v", err)})
				return
			}
			if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
				break
			}
			data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			if attempt == 0 && request.StreamOptions != nil && unsupportedStreamUsage(response.StatusCode, data) {
				c.streamUsageUnsupported.Store(true)
				request.StreamOptions = nil
				continue
			}
			c.logger.Error("llm_response", "status", response.StatusCode, "request_id", response.Header.Get("x-request-id"), "client_request_id", clientRequestID, "payload", string(data))
			emitLogged(streamContext, events, StreamEvent{Type: EventError, Error: responseErrorData(response.StatusCode, response.Status, data).Error()})
			return
		}
		if response == nil || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return
		}
		defer response.Body.Close()
		c.logger.Debug("llm_response_started", "status", response.StatusCode, "request_id", response.Header.Get("x-request-id"), "client_request_id", clientRequestID)

		idleTimeout := time.Duration(c.settings.StreamIdleTimeoutSeconds) * time.Second
		watchdog := newStreamIdleWatchdog(idleTimeout, cancelRequest)
		if watchdog != nil {
			defer watchdog.Stop()
		}
		parsedEvents := make(chan StreamEvent, 32)
		go func() {
			defer close(parsedEvents)
			parseStreamWithActivity(requestContext, response.Body, parsedEvents, &stream.Usage, watchdog.Activity)
		}()
		for event := range parsedEvents {
			if watchdog.TimedOut() {
				continue
			}
			if event.Type == EventCanceled {
				emitCanceledLogged(events)
				continue
			}
			emitLogged(streamContext, events, event)
		}
		if watchdog.TimedOut() && streamContext.Err() == nil {
			emitLogged(streamContext, events, StreamEvent{
				Type:  EventError,
				Error: fmt.Sprintf("model stream was idle for %s", idleTimeout),
			})
		}
	}()

	return stream
}

func unsupportedStreamUsage(statusCode int, data []byte) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusUnprocessableEntity {
		return false
	}
	message := strings.ToLower(string(data))
	return strings.Contains(message, "stream_options") || strings.Contains(message, "include_usage")
}

// Cancel stops a live stream by ID. Returns false if no such stream is active.
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

// Cancel stops a stream from its handle.
func (s *Stream) Cancel() {
	if s != nil && s.cancel != nil {
		s.cancel()
	}
}

// ActiveStreamCount returns the number of currently running streams.
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

func (c *Client) applyHeaders(request *http.Request) string {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for key, value := range c.settings.Headers {
		request.Header.Set(key, value)
	}
	if strings.TrimSpace(request.Header.Get("X-Client-Request-Id")) == "" {
		request.Header.Set("X-Client-Request-Id", uuid.NewString())
	}
	return request.Header.Get("X-Client-Request-Id")
}

func defaultHTTPClient(timeoutSeconds int) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = time.Duration(timeoutSeconds) * time.Second
	return &http.Client{Transport: transport}
}

func responseErrorData(statusCode int, status string, data []byte) error {
	if len(data) > 4096 {
		data = data[:4096]
	}
	detail := strings.TrimSpace(string(data))
	if statusCode == http.StatusRequestEntityTooLarge {
		return fmt.Errorf("LLM endpoint rejected the request (413 Request Entity Too Large). This is usually caused by large image attachments or accumulated context. Try using smaller images")
	}
	if detail == "" {
		return fmt.Errorf("llm endpoint returned %s", status)
	}
	return fmt.Errorf("llm endpoint returned %s: %s", status, detail)
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
