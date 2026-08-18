package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
	"github.com/gorilla/websocket"
)

// maxMessageBytes caps the size of a single WebSocket message.
const maxMessageBytes = 1 << 20 // 1 MiB

// errChatCanceled is returned by collectAssistantTurn when the stream was
// canceled (e.g. the user pressed Stop). It lets runChatLoop distinguish a
// user-initiated stop from a genuine error so it can emit chat_stopped instead
// of chat_error.
var errChatCanceled = errors.New("chat stream canceled")

// upgrader upgrades HTTP connections to WebSockets. CheckOrigin is permissive
// because the server is bound to localhost and is the origin for the SPA.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// client represents a single connected WebSocket peer.
type client struct {
	conn   *websocket.Conn
	send   chan []byte
	server *Server

	// cancelMu guards the active chat cancel function so a "stop" message can
	// cancel an in-progress stream from the read pump while the chat loop runs
	// in its own goroutine.
	cancelMu sync.Mutex
	cancel   *streamCancel
}

// streamCancel wraps a context cancel function so it can be stored as a
// pointer and compared by identity (Go functions cannot be compared directly).
type streamCancel struct {
	cancel context.CancelFunc
}

// setCancel records the cancel function for the currently active chat stream
// and returns a handle the chat loop can use to clear exactly its own
// registration when it finishes.
func (c *client) setCancel(fn context.CancelFunc) *streamCancel {
	sc := &streamCancel{cancel: fn}
	c.cancelMu.Lock()
	defer c.cancelMu.Unlock()
	// Defensively cancel any prior stream before replacing it.
	if c.cancel != nil {
		c.cancel.cancel()
	}
	c.cancel = sc
	return sc
}

// clearCancel removes the stored cancel function if it is still the given one.
func (c *client) clearCancel(sc *streamCancel) {
	c.cancelMu.Lock()
	defer c.cancelMu.Unlock()
	if c.cancel == sc {
		c.cancel = nil
	}
}

// cancelActive cancels the currently active chat stream, if any.
func (c *client) cancelActive() {
	c.cancelMu.Lock()
	defer c.cancelMu.Unlock()
	if c.cancel != nil {
		c.cancel.cancel()
		c.cancel = nil
	}
}

// sendJSON marshals v and queues it to the client's outbound channel. It is
// safe to call from any goroutine; if the client's buffer is full the message
// is dropped (the write pump will close the connection if it cannot keep up).
func (c *client) sendJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		logf("client send marshal: %v", err)
		return
	}
	select {
	case c.send <- data:
	default:
	}
}

// Hub manages all connected WebSocket clients and broadcasts events to them.
type Hub struct {
	mu         sync.RWMutex
	clients    map[*client]struct{}
	register   chan *client
	unregister chan *client
	shutdown   chan struct{}
	done       chan struct{}
}

// NewHub creates an empty Hub and starts its event loop.
func NewHub() *Hub {
	h := &Hub{
		clients:    make(map[*client]struct{}),
		register:   make(chan *client),
		unregister: make(chan *client),
		shutdown:   make(chan struct{}),
		done:       make(chan struct{}),
	}
	go h.run()
	return h
}

// run is the Hub's event loop. It processes registrations, unregistrations,
// and broadcasts until Shutdown is called.
func (h *Hub) run() {
	defer close(h.done)
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = struct{}{}
			h.mu.Unlock()
			// Queue the welcome event directly to the newly registered client.
			// Doing this here (after the map insert) avoids a race where the
			// handler broadcasts before registration completes.
			c.send <- welcomeEvent(h.ClientCount())
		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()
		case <-h.shutdown:
			h.mu.Lock()
			for c := range h.clients {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()
			return
		}
	}
}

// welcomeEvent builds the JSON welcome message sent to a client on connect.
func welcomeEvent(clientCount int) []byte {
	data, err := json.Marshal(map[string]any{
		"type":    "welcome",
		"clients": clientCount,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		logf("welcome marshal: %v", err)
		return []byte(`{"type":"welcome"}`)
	}
	return data
}

// ClientCount returns the number of currently connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Shutdown stops the Hub's event loop and closes all client connections.
func (h *Hub) Shutdown() {
	select {
	case <-h.shutdown:
		return
	default:
		close(h.shutdown)
	}
	<-h.done
}

// Broadcast marshals event to JSON and sends it to every connected client.
// It is safe to call from any goroutine.
func (h *Hub) Broadcast(event any) {
	data, err := json.Marshal(event)
	if err != nil {
		logf("broadcast marshal: %v", err)
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.send <- data:
		default:
			// Client's send buffer is full; drop it. The write pump will
			// eventually close the connection if it cannot keep up.
		}
	}
}

// handleWebSocket upgrades the request to a WebSocket and registers the client
// with the Hub. It blocks for the lifetime of the connection.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logf("websocket upgrade: %v", err)
		return
	}
	c := &client{
		conn:   conn,
		send:   make(chan []byte, 256),
		server: s,
	}
	go c.writePump()
	s.hub.register <- c
	c.readPump(s.hub)
}

// readPump reads messages from the client until the connection closes, then
// unregisters it. Inbound JSON messages are dispatched to handlers; currently
// the only supported message type is "chat".
func (c *client) readPump(h *Hub) {
	defer func() {
		h.unregister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageBytes)
	c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		var msg inboundMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			logf("ws decode inbound: %v", err)
			continue
		}
		switch msg.Type {
		case "chat":
			c.handleChatMessage(msg)
		case "stop":
			c.handleStopMessage()
		}
	}
}

// inboundMessage is the envelope the frontend sends over WebSocket.
type inboundMessage struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	// Model is the optional model the user selected for this prompt. When set,
	// the chat request is routed to the endpoint that owns that model instead
	// of the default chat endpoint.
	Model string `json:"model,omitempty"`
}

// handleChatMessage runs an LLM chat completion for the given message and
// streams the resulting events back to the sending client. It runs a
// tool-calling loop so the model can invoke registered tools (e.g.
// filesystem_list) and feed their results back until it produces a final
// answer.
func (c *client) handleChatMessage(msg inboundMessage) {
	if c.server == nil || c.server.llm == nil {
		c.sendJSON(map[string]any{"type": "chat_error", "error": "LLM client is not configured"})
		return
	}
	text := strings.TrimSpace(msg.Message)
	if text == "" {
		return
	}

	// Send an ack so the frontend knows the message was accepted.
	c.sendJSON(map[string]any{"type": "chat_start", "message": text})

	// Resolve the settings for this request. If the user selected a specific
	// model, route to the endpoint that owns that model; otherwise use the
	// default chat endpoint.
	settings := c.server.llmSettings
	if msg.Model != "" {
		if resolved, ok := c.server.settingsForModel(msg.Model); ok {
			settings = resolved
		}
	}

	request, err := llm.NewChatRequest(
		settings,
		[]llm.Message{{Role: llm.RoleUser, Content: text}},
		llm.WithStream(true),
		llm.WithTools(tools.LLMSchema()),
	)
	if err != nil {
		c.sendJSON(map[string]any{"type": "chat_error", "error": err.Error()})
		return
	}

	// Run the chat loop in a goroutine so the read pump stays live and can
	// receive a "stop" message while the reply is streaming. The cancellable
	// context lets a stop message cancel the in-progress LLM stream.
	ctx, cancel := context.WithCancel(context.Background())
	sc := c.setCancel(cancel)
	go func() {
		defer c.clearCancel(sc)
		c.runChatLoop(ctx, settings, request)
	}()
}

// handleStopMessage cancels the currently active chat stream (if any) and
// notifies the client that activity was stopped. It is invoked from the read
// pump so it can run while the chat loop streams in its own goroutine.
func (c *client) handleStopMessage() {
	c.cancelActive()
	c.sendJSON(map[string]any{"type": "chat_stopped"})
}

// runChatLoop executes the tool-calling loop for a chat request. It streams
// the assistant's turns to the client, executes any requested tools, feeds the
// results back into the conversation, and repeats until the model stops
// requesting tools or the loop budget is exhausted.
func (c *client) runChatLoop(ctx context.Context, settings llm.Settings, request llm.ChatRequest) {
	messages := append([]llm.Message(nil), request.Messages...)

	for turn := 0; ; turn++ {
		// If the user asked to stop between turns, halt immediately without
		// starting another LLM request or executing further tools.
		if ctx.Err() != nil {
			c.sendJSON(map[string]any{"type": "chat_stopped"})
			return
		}

		turnRequest := request
		turnRequest.Messages = messages
		c.sendJSON(map[string]any{
			"type":      "chat_event",
			"eventType": "assistant_turn_start",
			"turn":      turn,
		})

		stream := c.server.llm.StreamChat(ctx, turnRequest)
		content, toolCalls, err := c.collectAssistantTurn(stream, turn)
		if err != nil {
			if errors.Is(err, errChatCanceled) {
				c.sendJSON(map[string]any{"type": "chat_stopped"})
			} else {
				c.sendJSON(map[string]any{"type": "chat_error", "error": err.Error()})
			}
			return
		}
		c.sendJSON(map[string]any{
			"type":         "chat_event",
			"eventType":    "assistant_turn_end",
			"turn":         turn,
			"hasToolCalls": len(toolCalls) > 0,
		})

		// Record the assistant turn in the conversation history.
		assistant := llm.Message{Role: llm.RoleAssistant, Content: content, ToolCalls: toolCalls}
		messages = append(messages, assistant)

		if len(toolCalls) == 0 {
			// Final answer produced; nothing more to execute.
			c.sendJSON(map[string]any{"type": "chat_done"})
			return
		}

		// Execute each requested tool and append its result.
		for callOrder, call := range toolCalls {
			callID := call.ID
			if callID == "" {
				callID = fmt.Sprintf("turn-%d-call-%d", turn, callOrder)
			}
			c.sendJSON(map[string]any{
				"type":      "chat_event",
				"eventType": "tool_call",
				"turn":      turn,
				"callId":    callID,
				"callOrder": callOrder,
				"tool":      call.Function.Name,
				"arguments": call.Function.Arguments,
			})
			result := tools.Execute(c.toolContext(ctx), call.Function.Name, json.RawMessage(call.Function.Arguments))
			data, marshalErr := json.Marshal(result)
			resultSuccess := result.Success
			if marshalErr != nil {
				data = []byte(fmt.Sprintf(`{"tool":%q,"success":false,"error":{"code":"marshal_error","message":%q}}`, call.Function.Name, marshalErr.Error()))
				resultSuccess = false
			}
			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: call.ID,
				Content:    string(data),
			})
			if imageMessage, ok := toolResultImageMessage(call.Function.Name, result); ok {
				messages = append(messages, imageMessage)
			}
			if videoMessage, ok := toolResultVideoMessage(call.Function.Name, result); ok {
				messages = append(messages, videoMessage)
			}
			c.sendJSON(map[string]any{
				"type":      "chat_event",
				"eventType": "tool_result",
				"turn":      turn,
				"callId":    callID,
				"callOrder": callOrder,
				"tool":      call.Function.Name,
				"success":   resultSuccess,
				"content":   string(data),
			})
		}
	}
}

// toolResultImageMessage builds a user message carrying the image a tool
// returned (e.g. filesystem_read_image) as an OpenAI image_url content part so
// the model can actually see it. It returns false when the tool produced no
// image content.
func toolResultImageMessage(toolName string, result tools.ExecutionResult) (llm.Message, bool) {
	if !result.Success || result.Output == nil {
		return llm.Message{}, false
	}
	provider, ok := result.Output.(tools.LLMImageContentProvider)
	if !ok {
		return llm.Message{}, false
	}
	image, ok := provider.LLMImageContent()
	if !ok || strings.TrimSpace(image.DataURL) == "" {
		return llm.Message{}, false
	}

	label := strings.TrimSpace(image.Path)
	if label == "" {
		label = strings.TrimSpace(image.Name)
	}
	if label == "" {
		label = "image"
	}
	text := fmt.Sprintf("Image returned by tool %s: %s (%s, %d bytes).", toolName, label, image.MediaType, image.Bytes)
	imagePart := llm.ImageURLContentPart(image.DataURL)
	if image.Detail != "" && imagePart.ImageURL != nil {
		imagePart.ImageURL.Detail = image.Detail
	}
	return llm.Message{
		Role:         llm.RoleUser,
		Content:      text,
		ContentParts: []llm.MessageContentPart{llm.TextContentPart(text), imagePart},
	}, true
}

// toolResultVideoMessage builds a user message carrying the video a tool
// returned (e.g. filesystem_read_video) as an OpenAI video_url content part so
// the model can actually see it. It returns false when the tool produced no
// video content.
func toolResultVideoMessage(toolName string, result tools.ExecutionResult) (llm.Message, bool) {
	if !result.Success || result.Output == nil {
		return llm.Message{}, false
	}
	provider, ok := result.Output.(tools.LLMVideoContentProvider)
	if !ok {
		return llm.Message{}, false
	}
	video, ok := provider.LLMVideoContent()
	if !ok || strings.TrimSpace(video.DataURL) == "" {
		return llm.Message{}, false
	}

	label := strings.TrimSpace(video.Path)
	if label == "" {
		label = strings.TrimSpace(video.Name)
	}
	if label == "" {
		label = "video"
	}
	text := fmt.Sprintf("Video returned by tool %s: %s (%s, %d bytes).", toolName, label, video.MediaType, video.Bytes)
	videoPart := llm.VideoURLContentPart(video.DataURL)
	if video.Detail != "" && videoPart.VideoURL != nil {
		videoPart.VideoURL.Detail = video.Detail
	}
	return llm.Message{
		Role:         llm.RoleUser,
		Content:      text,
		ContentParts: []llm.MessageContentPart{llm.TextContentPart(text), videoPart},
	}, true
}

// toolContext builds the tools.ExecutionContext for the active workspace so
// tools can resolve labeled workspace paths and reach configured services
// (e.g. the SearXNG endpoint used by web_search). It threads the chat's
// context through so tools observe cancellation when the user stops a reply.
func (c *client) toolContext(ctx context.Context) tools.ExecutionContext {
	execCtx := tools.ExecutionContext{
		Context:                  ctx,
		SearxngURL:               c.server.settings.SearxngURL,
		ComfyuiURL:               c.server.settings.ComfyuiURL,
		ComfyuiDefaultCheckpoint: c.server.settings.ComfyuiDefaultCheckpoint,
		ComfyuiTxt2imgWorkflow:   c.server.settings.ComfyuiTxt2imgWorkflow,
		ComfyuiImg2imgWorkflow:   c.server.settings.ComfyuiImg2imgWorkflow,
	}
	active, ok, err := c.server.workspaces.Active()
	if err != nil || !ok {
		return execCtx
	}
	execCtx.WorkspaceRoots = workspaceToolRoots(active)
	return execCtx
}

// collectAssistantTurn drains a stream, merging streamed tool-call deltas and
// forwarding token/reasoning events to the client. It returns the assembled
// assistant content, the complete tool calls (if any), and any fatal error.
func (c *client) collectAssistantTurn(stream *llm.Stream, turn int) (string, []llm.ToolCall, error) {
	var content strings.Builder
	toolCalls := make(map[int]llm.ToolCall)
	var firstErr error

	for event := range stream.Events {
		switch event.Type {
		case llm.EventToken:
			content.WriteString(event.Content)
			c.sendJSON(map[string]any{
				"type":         "chat_event",
				"eventType":    string(event.Type),
				"turn":         turn,
				"content":      event.Content,
				"finishReason": event.FinishReason,
				"error":        event.Error,
			})
		case llm.EventReasoning:
			c.sendJSON(map[string]any{
				"type":         "chat_event",
				"eventType":    string(event.Type),
				"turn":         turn,
				"content":      event.Content,
				"finishReason": event.FinishReason,
				"error":        event.Error,
			})
		case llm.EventToolCall:
			if event.ToolCall != nil {
				call := mergeToolDelta(toolCalls[event.ToolCall.Index], *event.ToolCall)
				toolCalls[event.ToolCall.Index] = call
			}
		case llm.EventError:
			if firstErr == nil {
				firstErr = fmt.Errorf("%s", event.Error)
			}
		case llm.EventCanceled:
			return content.String(), orderedToolCalls(toolCalls), errChatCanceled
		}
	}
	if firstErr != nil {
		return content.String(), orderedToolCalls(toolCalls), firstErr
	}
	return content.String(), orderedToolCalls(toolCalls), nil
}

// mergeToolDelta merges a streamed tool-call delta into an in-progress call.
func mergeToolDelta(existing llm.ToolCall, delta llm.ToolCallDelta) llm.ToolCall {
	if delta.ID != "" {
		existing.ID = delta.ID
	}
	if delta.Type != "" {
		existing.Type = delta.Type
	}
	if existing.Type == "" {
		existing.Type = "function"
	}
	if delta.Function.Name != "" {
		existing.Function.Name = delta.Function.Name
	}
	if delta.Function.Arguments != "" {
		existing.Function.Arguments += delta.Function.Arguments
	}
	return existing
}

// orderedToolCalls returns the tool calls sorted by their stream index.
func orderedToolCalls(calls map[int]llm.ToolCall) []llm.ToolCall {
	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	ordered := make([]llm.ToolCall, 0, len(indexes))
	for _, index := range indexes {
		call := calls[index]
		if call.Type == "" {
			call.Type = "function"
		}
		ordered = append(ordered, call)
	}
	return ordered
}

// writePump writes queued messages to the client. It sends periodic pings to
// keep the connection alive and detect dead peers.
func (c *client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// Hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
