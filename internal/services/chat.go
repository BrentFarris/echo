package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const chatStreamEventName = "echo:chat:event"

type ChatSession struct {
	WorkspaceID string        `json:"workspaceId"`
	Messages    []ChatMessage `json:"messages"`
	Busy        bool          `json:"busy"`
	StreamID    string        `json:"streamId,omitempty"`
}

type ChatMessage struct {
	ID        string             `json:"id"`
	Role      string             `json:"role"`
	Content   string             `json:"content,omitempty"`
	Reasoning string             `json:"reasoning,omitempty"`
	ToolCalls []ChatToolActivity `json:"toolCalls,omitempty"`
	Status    string             `json:"status"`
	Error     string             `json:"error,omitempty"`
}

type ChatToolActivity struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Status    string `json:"status"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}

type ChatStreamEvent struct {
	WorkspaceID  string            `json:"workspaceId"`
	StreamID     string            `json:"streamId"`
	MessageID    string            `json:"messageId"`
	Type         string            `json:"type"`
	Content      string            `json:"content,omitempty"`
	Reasoning    string            `json:"reasoning,omitempty"`
	ToolCall     *ChatToolActivity `json:"toolCall,omitempty"`
	Error        string            `json:"error,omitempty"`
	FinishReason string            `json:"finishReason,omitempty"`
}

type chatSessionState struct {
	WorkspaceID string
	Messages    []ChatMessage
	History     []llm.Message
	Busy        bool
	StreamID    string
}

func (s *SystemService) LoadChatSession(workspaceID string) (ChatSession, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return ChatSession{}, err
	}

	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	return cloneChatSession(s.ensureChatSessionLocked(workspaceID)), nil
}

func (s *SystemService) SendChatMessage(workspaceID string, content string) (ChatSession, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return ChatSession{}, fmt.Errorf("message is required")
	}

	workspace, settings, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return ChatSession{}, err
	}

	runContext, cancel := context.WithCancel(context.Background())

	s.chatMu.Lock()
	session := s.ensureChatSessionLocked(workspaceID)
	if session.Busy {
		s.chatMu.Unlock()
		cancel()
		return ChatSession{}, fmt.Errorf("chat is already busy")
	}

	userMessage := ChatMessage{
		ID:      s.nextChatIDLocked("msg"),
		Role:    llm.RoleUser,
		Content: content,
		Status:  "complete",
	}
	assistantMessage := ChatMessage{
		ID:     s.nextChatIDLocked("msg"),
		Role:   llm.RoleAssistant,
		Status: "streaming",
	}
	streamID := s.nextChatIDLocked("stream")
	session.Messages = append(session.Messages, userMessage, assistantMessage)
	session.History = append(session.History, llm.Message{Role: llm.RoleUser, Content: content})
	session.Busy = true
	session.StreamID = streamID
	s.chatStreams[workspaceID] = cancel
	clone := cloneChatSession(session)
	s.chatMu.Unlock()

	s.emitChatEvent(ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   assistantMessage.ID,
		Type:        "started",
	})

	go s.runChatTurn(runContext, cancel, workspace, settings, streamID, assistantMessage.ID)

	return clone, nil
}

func (s *SystemService) StopChatStream(workspaceID string) (ChatSession, error) {
	s.chatMu.Lock()
	cancel, ok := s.chatStreams[workspaceID]
	s.chatMu.Unlock()
	if !ok {
		return s.LoadChatSession(workspaceID)
	}
	cancel()
	return s.LoadChatSession(workspaceID)
}

func (s *SystemService) ClearChat(workspaceID string) (ChatSession, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return ChatSession{}, err
	}

	s.chatMu.Lock()
	if cancel, ok := s.chatStreams[workspaceID]; ok {
		cancel()
		delete(s.chatStreams, workspaceID)
	}
	session := &chatSessionState{WorkspaceID: workspaceID}
	s.chatSessions[workspaceID] = session
	clone := cloneChatSession(session)
	s.chatMu.Unlock()
	return clone, nil
}

func (s *SystemService) runChatTurn(ctx context.Context, cancel context.CancelFunc, workspace Workspace, settings llm.Settings, streamID string, messageID string) {
	defer cancel()
	defer s.finishChatStream(workspace.ID, streamID)

	client, err := llm.NewClient(settings)
	if err != nil {
		s.failChatMessage(workspace.ID, streamID, messageID, err.Error())
		return
	}

	messages := append([]llm.Message{chatSystemMessage(workspace)}, s.chatHistory(workspace.ID)...)
	for {
		if err := ctx.Err(); err != nil {
			s.cancelChatMessage(workspace.ID, streamID, messageID)
			return
		}

		request, err := llm.NewChatRequest(settings, messages, llm.WithTools(tools.LLMSchema()), llm.WithToolChoice("auto"))
		if err != nil {
			s.failChatMessage(workspace.ID, streamID, messageID, err.Error())
			return
		}

		content, toolCalls, finished, finishReason, err := s.streamAssistantResponse(ctx, client, request, workspace.ID, streamID, messageID)
		if err != nil {
			if ctx.Err() != nil {
				s.cancelChatMessage(workspace.ID, streamID, messageID)
				return
			}
			s.failChatMessage(workspace.ID, streamID, messageID, userFacingLLMError(err))
			return
		}
		toolCalls = s.normalizeToolCalls(toolCalls)
		if !finished {
			s.cancelChatMessage(workspace.ID, streamID, messageID)
			return
		}
		if err := finishReasonError(finishReason, len(toolCalls) > 0); err != nil {
			s.failChatMessage(workspace.ID, streamID, messageID, err.Error())
			return
		}

		assistantHistory := llm.Message{Role: llm.RoleAssistant, Content: content, ToolCalls: toolCalls}
		messages = append(messages, assistantHistory)
		s.appendChatHistory(workspace.ID, assistantHistory)
		if len(toolCalls) == 0 {
			s.completeChatMessage(workspace.ID, streamID, messageID, finishReason)
			return
		}

		for _, call := range toolCalls {
			if err := ctx.Err(); err != nil {
				s.cancelChatMessage(workspace.ID, streamID, messageID)
				return
			}
			resultMessage := s.executeToolCall(ctx, workspace, streamID, messageID, call)
			messages = append(messages, resultMessage)
			s.appendChatHistory(workspace.ID, resultMessage)
		}
	}
}

func (s *SystemService) streamAssistantResponse(ctx context.Context, client *llm.Client, request llm.ChatRequest, workspaceID string, streamID string, messageID string) (string, []llm.ToolCall, bool, string, error) {
	request.Messages = append([]llm.Message(nil), request.Messages...)
	totalContent := strings.Builder{}
	var lastLoop streamLoopDetection

	for attempt := 0; ; attempt++ {
		result := s.streamAssistantResponseAttempt(ctx, client, request, workspaceID, streamID, messageID)
		totalContent.WriteString(result.content)
		if result.loop != nil {
			lastLoop = *result.loop
			if attempt >= maxStreamLoopRetries {
				return totalContent.String(), result.toolCalls, false, result.finishReason, streamLoopExceededError(lastLoop)
			}
			s.retryChatMessage(workspaceID, streamID, messageID)
			request.Messages = appendStreamLoopRetryMessages(request.Messages, result.content, lastLoop)
			continue
		}
		return totalContent.String(), result.toolCalls, result.finished, result.finishReason, result.err
	}
}

type chatStreamAttemptResult struct {
	content      string
	toolCalls    []llm.ToolCall
	finished     bool
	finishReason string
	loop         *streamLoopDetection
	err          error
}

func (s *SystemService) streamAssistantResponseAttempt(ctx context.Context, client *llm.Client, request llm.ChatRequest, workspaceID string, streamID string, messageID string) chatStreamAttemptResult {
	stream := client.StreamChat(ctx, request)
	content := strings.Builder{}
	contentInlineParser := inlineToolCallStreamParser{}
	reasoningInlineParser := inlineToolCallStreamParser{}
	toolCalls := make(map[int]llm.ToolCall)
	loopDetector := streamLoopDetector{}
	finished := false
	finishReason := ""
	nextInlineToolIndex := inlineToolCallIndexBase

	recordInlineToolCalls := func(calls []llm.ToolCall) {
		for _, call := range calls {
			call = s.normalizeToolCall(call)
			toolCalls[nextInlineToolIndex] = call
			nextInlineToolIndex++
			s.updateToolActivity(workspaceID, streamID, messageID, call, "streaming", "", "")
		}
	}
	appendContent := func(text string) *streamLoopDetection {
		if text == "" {
			return nil
		}
		content.WriteString(text)
		s.appendChatContent(workspaceID, streamID, messageID, text)
		if detection, ok := loopDetector.observe(streamLoopContent, text); ok {
			return &detection
		}
		return nil
	}
	appendReasoning := func(text string) *streamLoopDetection {
		if text == "" {
			return nil
		}
		s.appendChatReasoning(workspaceID, streamID, messageID, text)
		if detection, ok := loopDetector.observe(streamLoopReasoning, text); ok {
			return &detection
		}
		return nil
	}
	flushInlineParsers := func() {
		parsedContent := contentInlineParser.Flush()
		recordInlineToolCalls(parsedContent.ToolCalls)
		_ = appendContent(parsedContent.Text)

		parsedReasoning := reasoningInlineParser.Flush()
		recordInlineToolCalls(parsedReasoning.ToolCalls)
		_ = appendReasoning(parsedReasoning.Text)
	}

	for event := range stream.Events {
		switch event.Type {
		case llm.EventToken:
			parsed := contentInlineParser.Consume(event.Content)
			recordInlineToolCalls(parsed.ToolCalls)
			if detection := appendContent(parsed.Text); detection != nil {
				stream.Cancel()
				return chatStreamAttemptResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), finished: false, finishReason: finishReason, loop: detection}
			}
		case llm.EventReasoning:
			parsed := reasoningInlineParser.Consume(event.Content)
			recordInlineToolCalls(parsed.ToolCalls)
			if detection := appendReasoning(parsed.Text); detection != nil {
				stream.Cancel()
				return chatStreamAttemptResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), finished: false, finishReason: finishReason, loop: detection}
			}
		case llm.EventToolCall:
			if event.ToolCall != nil {
				call := mergeToolDelta(toolCalls[event.ToolCall.Index], *event.ToolCall)
				toolCalls[event.ToolCall.Index] = call
				s.updateToolActivity(workspaceID, streamID, messageID, call, "streaming", "", "")
			}
		case llm.EventComplete:
			finished = true
			finishReason = event.FinishReason
		case llm.EventCanceled:
			return chatStreamAttemptResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), finished: false, finishReason: finishReason}
		case llm.EventError:
			return chatStreamAttemptResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), finished: false, finishReason: finishReason, err: errors.New(event.Error)}
		}
	}

	if err := ctx.Err(); err != nil {
		return chatStreamAttemptResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), finished: false, finishReason: finishReason}
	}
	flushInlineParsers()
	return chatStreamAttemptResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), finished: finished, finishReason: finishReason}
}

func (s *SystemService) executeToolCall(ctx context.Context, workspace Workspace, streamID string, messageID string, call llm.ToolCall) llm.Message {
	if call.ID == "" {
		call.ID = s.nextChatID("call")
	}
	s.updateToolActivity(workspace.ID, streamID, messageID, call, "running", "", "")

	events := func(event tools.Event) {
		if event.Message != "" {
			s.emitChatEvent(ChatStreamEvent{
				WorkspaceID: workspace.ID,
				StreamID:    streamID,
				MessageID:   messageID,
				Type:        "tool_event",
				ToolCall: &ChatToolActivity{
					ID:     call.ID,
					Name:   call.Function.Name,
					Status: event.Type,
					Result: event.Message,
				},
			})
		}
	}
	result := tools.Execute(tools.ExecutionContext{
		Context:       ctx,
		WorkspacePath: workspace.FolderPath,
		Emit:          events,
	}, call.Function.Name, json.RawMessage(call.Function.Arguments))

	data, err := json.Marshal(result)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"tool":%q,"success":false,"error":{"code":"marshal_error","message":%q}}`, call.Function.Name, err.Error()))
	}
	status := "complete"
	errorText := ""
	if !result.Success {
		status = "error"
		if result.Error != nil {
			errorText = result.Error.Message
		}
	}
	s.updateToolActivity(workspace.ID, streamID, messageID, call, status, string(data), errorText)

	return llm.Message{
		Role:       llm.RoleTool,
		ToolCallID: call.ID,
		Content:    string(data),
	}
}

func (s *SystemService) appendChatContent(workspaceID string, streamID string, messageID string, content string) {
	s.mutateChatMessage(workspaceID, messageID, func(message *ChatMessage) {
		message.Content += content
	}, ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   messageID,
		Type:        "token",
		Content:     content,
	})
}

func (s *SystemService) appendChatReasoning(workspaceID string, streamID string, messageID string, reasoning string) {
	s.mutateChatMessage(workspaceID, messageID, func(message *ChatMessage) {
		message.Reasoning += reasoning
	}, ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   messageID,
		Type:        "reasoning",
		Reasoning:   reasoning,
	})
}

func (s *SystemService) updateToolActivity(workspaceID string, streamID string, messageID string, call llm.ToolCall, status string, result string, errorText string) {
	activity := ChatToolActivity{
		ID:        call.ID,
		Name:      call.Function.Name,
		Arguments: call.Function.Arguments,
		Status:    status,
		Result:    result,
		Error:     errorText,
	}
	s.mutateChatMessage(workspaceID, messageID, func(message *ChatMessage) {
		for i := range message.ToolCalls {
			if message.ToolCalls[i].ID != "" && message.ToolCalls[i].ID == activity.ID {
				message.ToolCalls[i] = activity
				return
			}
			if message.ToolCalls[i].ID == "" && message.ToolCalls[i].Name == activity.Name {
				message.ToolCalls[i] = activity
				return
			}
		}
		message.ToolCalls = append(message.ToolCalls, activity)
	}, ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   messageID,
		Type:        "tool_call",
		ToolCall:    &activity,
	})
}

func (s *SystemService) completeChatMessage(workspaceID string, streamID string, messageID string, finishReason string) {
	s.mutateChatMessage(workspaceID, messageID, func(message *ChatMessage) {
		message.Status = "complete"
	}, ChatStreamEvent{
		WorkspaceID:  workspaceID,
		StreamID:     streamID,
		MessageID:    messageID,
		Type:         "complete",
		FinishReason: finishReason,
	})
}

func (s *SystemService) retryChatMessage(workspaceID string, streamID string, messageID string) {
	s.mutateChatMessage(workspaceID, messageID, func(message *ChatMessage) {
		message.Status = "retrying"
	}, ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   messageID,
		Type:        "retrying",
	})
}

func (s *SystemService) cancelChatMessage(workspaceID string, streamID string, messageID string) {
	s.mutateChatMessage(workspaceID, messageID, func(message *ChatMessage) {
		message.Status = "canceled"
	}, ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   messageID,
		Type:        "canceled",
	})
}

func (s *SystemService) failChatMessage(workspaceID string, streamID string, messageID string, messageError string) {
	s.mutateChatMessage(workspaceID, messageID, func(message *ChatMessage) {
		message.Status = "error"
		message.Error = messageError
	}, ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   messageID,
		Type:        "error",
		Error:       messageError,
	})
}

func (s *SystemService) mutateChatMessage(workspaceID string, messageID string, mutate func(*ChatMessage), event ChatStreamEvent) {
	s.chatMu.Lock()
	if session := s.chatSessions[workspaceID]; session != nil {
		for i := range session.Messages {
			if session.Messages[i].ID == messageID {
				mutate(&session.Messages[i])
				break
			}
		}
	}
	s.chatMu.Unlock()
	s.emitChatEvent(event)
}

func (s *SystemService) finishChatStream(workspaceID string, streamID string) {
	s.chatMu.Lock()
	if session := s.chatSessions[workspaceID]; session != nil && session.StreamID == streamID {
		session.Busy = false
		session.StreamID = ""
	}
	delete(s.chatStreams, workspaceID)
	s.chatMu.Unlock()
}

func (s *SystemService) dropChatSession(workspaceID string) {
	s.chatMu.Lock()
	if cancel, ok := s.chatStreams[workspaceID]; ok {
		cancel()
		delete(s.chatStreams, workspaceID)
	}
	delete(s.chatSessions, workspaceID)
	s.chatMu.Unlock()
}

func (s *SystemService) chatHistory(workspaceID string) []llm.Message {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	if session := s.chatSessions[workspaceID]; session != nil {
		return append([]llm.Message(nil), session.History...)
	}
	return nil
}

func (s *SystemService) appendChatHistory(workspaceID string, message llm.Message) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	if session := s.chatSessions[workspaceID]; session != nil {
		session.History = append(session.History, message)
	}
}

func (s *SystemService) workspaceAndSettings(workspaceID string) (Workspace, llm.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refreshWorkspaceStatusesLocked() {
		_ = s.saveLocked()
	}
	for _, workspace := range s.state.Workspaces {
		if workspace.ID == workspaceID {
			if workspace.Missing {
				return Workspace{}, llm.Settings{}, fmt.Errorf("workspace folder is unavailable")
			}
			return workspace, s.state.Settings, nil
		}
	}
	return Workspace{}, llm.Settings{}, fmt.Errorf("workspace was not found")
}

func (s *SystemService) validateWorkspaceAvailable(workspaceID string) error {
	_, _, err := s.workspaceAndSettings(workspaceID)
	return err
}

func (s *SystemService) ensureChatSessionLocked(workspaceID string) *chatSessionState {
	session := s.chatSessions[workspaceID]
	if session == nil {
		session = &chatSessionState{WorkspaceID: workspaceID}
		s.chatSessions[workspaceID] = session
	}
	return session
}

func (s *SystemService) nextChatID(prefix string) string {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	return s.nextChatIDLocked(prefix)
}

func (s *SystemService) nextChatIDLocked(prefix string) string {
	s.chatSeq++
	return fmt.Sprintf("%s-%d", prefix, s.chatSeq)
}

func (s *SystemService) emitChatEvent(event ChatStreamEvent) {
	if s.ctx != nil {
		runtime.EventsEmit(s.ctx, chatStreamEventName, event)
	}
}

func chatSystemMessage(workspace Workspace) llm.Message {
	return llm.Message{
		Role: llm.RoleSystem,
		Content: workspaceSystemPrompt(
			"You are Echo, a personal AI assistant helping plan work inside the active workspace. "+
				"Use available tools when workspace facts are needed. Keep plans concrete and concise.",
			workspace,
		),
	}
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

func (s *SystemService) normalizeToolCalls(calls []llm.ToolCall) []llm.ToolCall {
	normalized := append([]llm.ToolCall(nil), calls...)
	for i := range normalized {
		normalized[i] = s.normalizeToolCall(normalized[i])
	}
	return normalized
}

func (s *SystemService) normalizeToolCall(call llm.ToolCall) llm.ToolCall {
	if call.ID == "" {
		call.ID = s.nextChatID("call")
	}
	if call.Type == "" {
		call.Type = "function"
	}
	return call
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

func cloneChatSession(session *chatSessionState) ChatSession {
	clone := ChatSession{
		WorkspaceID: session.WorkspaceID,
		Messages:    append([]ChatMessage{}, session.Messages...),
		Busy:        session.Busy,
		StreamID:    session.StreamID,
	}
	for i := range clone.Messages {
		clone.Messages[i].ToolCalls = append([]ChatToolActivity(nil), clone.Messages[i].ToolCalls...)
	}
	return clone
}
