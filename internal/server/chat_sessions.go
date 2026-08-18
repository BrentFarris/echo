package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/agentmodes"
	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
	"github.com/brent/echo/internal/workspaces"
)

var errChatCanceled = errors.New("chat stream canceled")

type sessionEvent struct {
	Type        string         `json:"type"`
	WorkspaceID string         `json:"workspaceId"`
	Sequence    uint64         `json:"sequence"`
	Event       map[string]any `json:"event"`
}

type sessionSnapshot struct {
	Type        string          `json:"type"`
	WorkspaceID string          `json:"workspaceId"`
	Sequence    uint64          `json:"sequence"`
	Revision    uint64          `json:"revision"`
	Turns       []sessions.Turn `json:"turns"`
	ActiveTurn  *sessions.Turn  `json:"activeTurn,omitempty"`
}

type chatSessionManager struct {
	server   *Server
	mu       sync.Mutex
	sessions map[string]*chatSession
	wg       sync.WaitGroup
}

type chatSession struct {
	manager     *chatSessionManager
	workspace   workspaces.Workspace
	store       *sessions.Store
	mu          sync.Mutex
	transcript  sessions.Transcript
	sequence    uint64
	active      *sessions.Turn
	cancel      context.CancelFunc
	subscribers map[*client]struct{}
	loadErr     error
}

func newChatSessionManager(server *Server) *chatSessionManager {
	return &chatSessionManager{server: server, sessions: make(map[string]*chatSession)}
}

func (m *chatSessionManager) get(workspaceID string) (*chatSession, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, fmt.Errorf("workspaceId is required")
	}
	m.mu.Lock()
	if session := m.sessions[workspaceID]; session != nil {
		m.mu.Unlock()
		return session, nil
	}
	m.mu.Unlock()

	workspace, ok, err := m.server.workspaces.Get(workspaceID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("workspace %q not found", workspaceID)
	}
	store := sessions.NewStore(workspace.MainPath)
	transcript, loadErr := store.Load(workspaceID)
	session := &chatSession{
		manager:     m,
		workspace:   workspace,
		store:       store,
		transcript:  transcript,
		sequence:    transcript.Revision,
		subscribers: make(map[*client]struct{}),
		loadErr:     loadErr,
	}
	if loadErr != nil {
		// Keep the corrupt file untouched and fail closed for this workspace.
		session.transcript = sessions.Transcript{Version: sessions.Version, WorkspaceID: workspaceID}
	}

	m.mu.Lock()
	if existing := m.sessions[workspaceID]; existing != nil {
		m.mu.Unlock()
		return existing, nil
	}
	m.sessions[workspaceID] = session
	m.mu.Unlock()
	return session, nil
}

func (m *chatSessionManager) subscribe(c *client, workspaceID string) {
	session, err := m.get(workspaceID)
	if err != nil {
		m.commandError(c, workspaceID, "invalid_workspace", err.Error(), "")
		return
	}
	m.unsubscribe(c)
	session.mu.Lock()
	session.subscribers[c] = struct{}{}
	session.sendSnapshotLocked(c)
	if session.loadErr != nil {
		m.commandError(c, workspaceID, "session_load_failed", session.loadErr.Error(), "")
	}
	session.mu.Unlock()
}

func (m *chatSessionManager) unsubscribe(c *client) {
	m.mu.Lock()
	all := make([]*chatSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		all = append(all, session)
	}
	m.mu.Unlock()
	for _, session := range all {
		session.mu.Lock()
		delete(session.subscribers, c)
		session.mu.Unlock()
	}
}

func (m *chatSessionManager) send(c *client, msg inboundMessage) {
	if m.server.llm == nil {
		m.commandError(c, msg.WorkspaceID, "llm_unavailable", "LLM client is not configured", msg.RequestID)
		return
	}
	session, err := m.get(msg.WorkspaceID)
	if err != nil {
		m.commandError(c, msg.WorkspaceID, "invalid_workspace", err.Error(), msg.RequestID)
		return
	}
	text := strings.TrimSpace(msg.Message)
	if text == "" {
		m.commandError(c, msg.WorkspaceID, "invalid_message", "message is required", msg.RequestID)
		return
	}
	requestID := strings.TrimSpace(msg.RequestID)
	if requestID == "" {
		requestID = newSessionID("request")
	}

	settings := m.server.llmSettings
	if msg.Model != "" {
		if resolved, ok := m.server.settingsForModel(msg.Model); ok {
			settings = resolved
		}
	}
	mode, err := m.server.modes.Resolve(session.workspace.MainPath, msg.AgentModeID)
	if err != nil {
		m.commandError(c, msg.WorkspaceID, "agent_mode_load_failed", err.Error(), requestID)
		return
	}
	scopes := tools.NewToolScopeChecker(agentmodes.PermissionList(mode))

	session.mu.Lock()
	if session.loadErr != nil {
		session.mu.Unlock()
		m.commandError(c, msg.WorkspaceID, "session_load_failed", session.loadErr.Error(), requestID)
		return
	}
	if session.hasRequestLocked(requestID) {
		session.sendSnapshotLocked(c)
		session.mu.Unlock()
		return
	}
	if session.active != nil {
		session.mu.Unlock()
		m.commandError(c, msg.WorkspaceID, "session_busy", "this workspace already has an active response", requestID)
		return
	}

	history := append([]llm.Message(nil), session.transcript.Messages...)
	history = append(history, llm.Message{Role: llm.RoleUser, Content: text})
	messages := append([]llm.Message{agentModeSystemMessage(session.workspace, mode)}, history...)
	request, err := llm.NewChatRequest(settings, messages, llm.WithStream(true), llm.WithTools(tools.LLMSchemaForScopes(scopes)))
	if err != nil {
		session.mu.Unlock()
		m.commandError(c, msg.WorkspaceID, "invalid_request", err.Error(), requestID)
		return
	}
	messages = append([]llm.Message(nil), request.Messages...)
	ctx, cancel := context.WithCancel(context.Background())
	session.cancel = cancel
	session.active = &sessions.Turn{
		ID:             newSessionID("turn"),
		RequestID:      requestID,
		UserContent:    text,
		Model:          request.Model,
		AgentModeID:    mode.ID,
		AgentModeName:  mode.Name,
		Status:         "streaming",
		StartedAt:      time.Now().UTC(),
		AssistantTurns: []sessions.AssistantTurn{},
	}
	turnID := session.active.ID
	session.emitLocked(map[string]any{
		"type": "turn_started", "turnId": turnID, "requestId": requestID,
		"message": text, "model": request.Model, "agentModeId": mode.ID, "agentModeName": mode.Name, "startedAt": session.active.StartedAt,
	})
	session.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		session.run(ctx, settings, request, messages, turnID, scopes)
	}()
}

func (m *chatSessionManager) stop(c *client, workspaceID string) {
	session, err := m.get(workspaceID)
	if err != nil {
		m.commandError(c, workspaceID, "invalid_workspace", err.Error(), "")
		return
	}
	session.mu.Lock()
	cancel := session.cancel
	session.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *chatSessionManager) commandError(c *client, workspaceID, code, message, requestID string) {
	c.sendJSON(map[string]any{
		"type": "command_error", "workspaceId": workspaceID, "code": code,
		"error": message, "requestId": requestID,
	})
}

func (m *chatSessionManager) shutdown(ctx context.Context) {
	m.mu.Lock()
	all := make([]*chatSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		all = append(all, session)
	}
	m.mu.Unlock()
	for _, session := range all {
		session.mu.Lock()
		cancel := session.cancel
		session.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (s *chatSession) hasRequestLocked(requestID string) bool {
	if s.active != nil && s.active.RequestID == requestID {
		return true
	}
	for i := range s.transcript.Turns {
		if s.transcript.Turns[i].RequestID == requestID {
			return true
		}
	}
	return false
}

func (s *chatSession) sendSnapshotLocked(c *client) {
	snapshot := sessionSnapshot{
		Type: "session_snapshot", WorkspaceID: s.workspace.ID, Sequence: s.sequence,
		Revision: s.transcript.Revision, Turns: s.transcript.Turns, ActiveTurn: s.active,
	}
	c.sendJSON(snapshot)
}

func (s *chatSession) emitLocked(event map[string]any) {
	s.sequence++
	message := sessionEvent{Type: "session_event", WorkspaceID: s.workspace.ID, Sequence: s.sequence, Event: event}
	for subscriber := range s.subscribers {
		subscriber.sendJSON(message)
	}
}

func (s *chatSession) run(ctx context.Context, settings llm.Settings, request llm.ChatRequest, messages []llm.Message, turnID string, scopes *tools.ToolScopeChecker) {
	for assistantNumber := 0; ; assistantNumber++ {
		if ctx.Err() != nil {
			s.finish(turnID, "stopped", "", messages)
			return
		}
		turnRequest := request
		turnRequest.Messages = messages
		s.mu.Lock()
		if !s.isActiveLocked(turnID) {
			s.mu.Unlock()
			return
		}
		s.active.AssistantTurns = append(s.active.AssistantTurns, sessions.AssistantTurn{Number: assistantNumber})
		s.emitLocked(map[string]any{"type": "assistant_turn_start", "turnId": turnID, "turn": assistantNumber})
		s.mu.Unlock()

		stream := s.manager.server.llm.StreamChat(ctx, turnRequest)
		content, toolCalls, err := s.collectAssistantTurn(stream, turnID, assistantNumber)
		assistant := llm.Message{Role: llm.RoleAssistant, Content: content, ToolCalls: toolCalls}
		if content != "" || len(toolCalls) > 0 {
			messages = append(messages, assistant)
		}
		if err != nil {
			if errors.Is(err, errChatCanceled) {
				s.finish(turnID, "stopped", "", messages)
			} else {
				s.finish(turnID, "error", err.Error(), messages)
			}
			return
		}

		s.mu.Lock()
		if !s.isActiveLocked(turnID) {
			s.mu.Unlock()
			return
		}
		step := &s.active.AssistantTurns[len(s.active.AssistantTurns)-1]
		step.HasToolCalls = len(toolCalls) > 0
		s.emitLocked(map[string]any{
			"type": "assistant_turn_end", "turnId": turnID, "turn": assistantNumber,
			"hasToolCalls": len(toolCalls) > 0,
		})
		s.mu.Unlock()

		if len(toolCalls) == 0 {
			s.finish(turnID, "done", "", messages)
			return
		}

		for callOrder, call := range toolCalls {
			if ctx.Err() != nil {
				s.finish(turnID, "stopped", "", messages)
				return
			}
			callID := call.ID
			if callID == "" {
				callID = fmt.Sprintf("turn-%d-call-%d", assistantNumber, callOrder)
			}
			s.mu.Lock()
			if !s.isActiveLocked(turnID) {
				s.mu.Unlock()
				return
			}
			step := &s.active.AssistantTurns[len(s.active.AssistantTurns)-1]
			step.Tools = append(step.Tools, sessions.ToolActivity{
				CallID: callID, CallOrder: callOrder, Name: call.Function.Name,
				Arguments: call.Function.Arguments, Status: "running",
			})
			s.emitLocked(map[string]any{
				"type": "tool_call", "turnId": turnID, "turn": assistantNumber,
				"callId": callID, "callOrder": callOrder, "tool": call.Function.Name,
				"arguments": call.Function.Arguments,
			})
			s.mu.Unlock()

			result := tools.Execute(s.toolContext(ctx, scopes), call.Function.Name, json.RawMessage(call.Function.Arguments))
			data, marshalErr := json.Marshal(result)
			resultSuccess := result.Success
			if marshalErr != nil {
				data = []byte(fmt.Sprintf(`{"tool":%q,"success":false,"error":{"code":"marshal_error","message":%q}}`, call.Function.Name, marshalErr.Error()))
				resultSuccess = false
			}
			messages = append(messages, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Content: string(data)})
			if imageMessage, ok := toolResultImageMessage(call.Function.Name, result); ok {
				messages = append(messages, imageMessage)
			}
			if videoMessage, ok := toolResultVideoMessage(call.Function.Name, result); ok {
				messages = append(messages, videoMessage)
			}

			s.mu.Lock()
			if !s.isActiveLocked(turnID) {
				s.mu.Unlock()
				return
			}
			step = &s.active.AssistantTurns[len(s.active.AssistantTurns)-1]
			for i := range step.Tools {
				if step.Tools[i].CallID == callID {
					step.Tools[i].Status = "complete"
					step.Tools[i].Success = resultSuccess
					step.Tools[i].Result = string(data)
					break
				}
			}
			s.emitLocked(map[string]any{
				"type": "tool_result", "turnId": turnID, "turn": assistantNumber,
				"callId": callID, "callOrder": callOrder, "tool": call.Function.Name,
				"success": resultSuccess, "content": string(data),
			})
			s.mu.Unlock()
		}
	}
}

func (s *chatSession) collectAssistantTurn(stream *llm.Stream, turnID string, assistantNumber int) (string, []llm.ToolCall, error) {
	var content strings.Builder
	toolCalls := make(map[int]llm.ToolCall)
	var firstErr error
	for event := range stream.Events {
		switch event.Type {
		case llm.EventToken, llm.EventReasoning:
			if event.Type == llm.EventToken {
				content.WriteString(event.Content)
			}
			s.mu.Lock()
			if !s.isActiveLocked(turnID) {
				s.mu.Unlock()
				return content.String(), orderedToolCalls(toolCalls), errChatCanceled
			}
			step := &s.active.AssistantTurns[len(s.active.AssistantTurns)-1]
			if event.Type == llm.EventToken {
				step.Content += event.Content
			} else {
				step.Reasoning += event.Content
			}
			s.emitLocked(map[string]any{
				"type": string(event.Type), "turnId": turnID, "turn": assistantNumber,
				"content": event.Content, "finishReason": event.FinishReason, "error": event.Error,
			})
			s.mu.Unlock()
		case llm.EventToolCall:
			if event.ToolCall != nil {
				toolCalls[event.ToolCall.Index] = mergeToolDelta(toolCalls[event.ToolCall.Index], *event.ToolCall)
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

func (s *chatSession) finish(turnID, status, message string, messages []llm.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isActiveLocked(turnID) {
		return
	}
	now := time.Now().UTC()
	s.active.Status = status
	s.active.Error = message
	s.active.CompletedAt = &now
	completed := *s.active
	s.transcript.Turns = append(s.transcript.Turns, completed)
	s.transcript.Messages = sanitizeMessages(messages)
	s.transcript.Revision = s.sequence + 1
	persistErr := s.store.Save(s.transcript)
	s.active = nil
	s.cancel = nil
	event := map[string]any{"type": "turn_finished", "turnId": turnID, "status": status, "error": message, "completedAt": now}
	if persistErr != nil {
		event["persistenceError"] = persistErr.Error()
		logf("persist chat session %s: %v", s.workspace.ID, persistErr)
	}
	s.emitLocked(event)
}

func (s *chatSession) isActiveLocked(turnID string) bool {
	return s.active != nil && s.active.ID == turnID
}

func (s *chatSession) toolContext(ctx context.Context, scopes *tools.ToolScopeChecker) tools.ExecutionContext {
	settings := s.manager.server.settings
	roots := s.manager.server.confinedToolRoots(s.workspace)
	return tools.ExecutionContext{
		Context: ctx, WorkspaceRoots: roots, SearxngURL: settings.SearxngURL,
		ResolveWorkspacePath:      s.manager.server.toolPathResolver(s.workspace.ID, roots, false),
		ResolveWorkspaceChildPath: s.manager.server.toolPathResolver(s.workspace.ID, roots, true),
		ComfyuiURL:                settings.ComfyuiURL, ComfyuiDefaultCheckpoint: settings.ComfyuiDefaultCheckpoint,
		ComfyuiTxt2imgWorkflow: settings.ComfyuiTxt2imgWorkflow, ComfyuiImg2imgWorkflow: settings.ComfyuiImg2imgWorkflow,
		ToolScopes: scopes,
	}
}

func sanitizeMessages(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == llm.RoleSystem && message.Name == "echo-agent-mode" {
			continue
		}
		message.ContentParts = nil
		message.ToolCalls = append([]llm.ToolCall(nil), message.ToolCalls...)
		out = append(out, message)
	}
	return out
}

func agentModeSystemMessage(workspace workspaces.Workspace, mode agentmodes.Mode) llm.Message {
	var prompt strings.Builder
	prompt.WriteString("You are Echo, an AI assistant working inside the user's active workspace. Use the available tools when workspace facts or changes are needed. Carry out requested implementation work directly, verify meaningful changes, and keep the final response concrete and concise.")
	if len(workspace.Folders) > 0 {
		prompt.WriteString("\n\nWorkspace folders are addressed by their labels: ")
		roots := workspaceToolRoots(workspace)
		for i, root := range roots {
			if i > 0 {
				prompt.WriteString(", ")
			}
			prompt.WriteString(root.Label)
		}
		prompt.WriteString(". Start file paths with the appropriate label.")
	}
	if strings.TrimSpace(mode.Prompt) != "" {
		prompt.WriteString("\n\nAgent mode instructions (follow these for this turn):\n")
		prompt.WriteString(strings.TrimSpace(mode.Prompt))
	}
	if len(mode.Permissions) > 0 {
		prompt.WriteString("\n\nThis mode can only use its configured tool allowlist. Do not claim access to unavailable tools or paths.")
	}
	return llm.Message{Role: llm.RoleSystem, Name: "echo-agent-mode", Content: prompt.String()}
}

func newSessionID(prefix string) string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(value[:])
}

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
	part := llm.ImageURLContentPart(image.DataURL)
	if image.Detail != "" && part.ImageURL != nil {
		part.ImageURL.Detail = image.Detail
	}
	return llm.Message{Role: llm.RoleUser, Content: text, ContentParts: []llm.MessageContentPart{llm.TextContentPart(text), part}}, true
}

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
	part := llm.VideoURLContentPart(video.DataURL)
	if video.Detail != "" && part.VideoURL != nil {
		part.VideoURL.Detail = video.Detail
	}
	return llm.Message{Role: llm.RoleUser, Content: text, ContentParts: []llm.MessageContentPart{llm.TextContentPart(text), part}}, true
}
