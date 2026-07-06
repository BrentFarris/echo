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
	ID        string                `json:"id"`
	Role      string                `json:"role"`
	Content   string                `json:"content,omitempty"`
	Images    []ChatImageAttachment `json:"images,omitempty"`
	Videos    []ChatVideoAttachment `json:"videos,omitempty"`
	Reasoning string                `json:"reasoning,omitempty"`
	ToolCalls []ChatToolActivity    `json:"toolCalls,omitempty"`
	Status    string                `json:"status"`
	Error     string                `json:"error,omitempty"`
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
	return s.SendChatMessageWithAttachments(workspaceID, ChatMessageRequest{Content: content})
}

func (s *SystemService) SendChatMessageWithPlanMode(workspaceID string, content string, planMode bool) (ChatSession, error) {
	return s.SendChatMessageWithAttachments(workspaceID, ChatMessageRequest{Content: content, PlanMode: planMode})
}

func (s *SystemService) SendChatMessageWithAttachments(workspaceID string, request ChatMessageRequest) (ChatSession, error) {
	return s.sendChatMessage(workspaceID, request)
}

func (s *SystemService) sendChatMessage(workspaceID string, request ChatMessageRequest) (ChatSession, error) {
	content := strings.TrimSpace(request.Content)
	workspace, settings, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return ChatSession{}, err
	}
	images, err := s.prepareChatImages(workspace, content, request.Images)
	if err != nil {
		return ChatSession{}, err
	}
	videos, err := s.prepareChatVideos(workspace, content, request.Videos)
	if err != nil {
		return ChatSession{}, err
	}
	content = chatMediaTextContent(content, images, videos)
	if content == "" {
		return ChatSession{}, fmt.Errorf("message is required")
	}
	userHistory := llm.Message{Role: llm.RoleUser, Content: content}
	if len(images) > 0 || len(videos) > 0 {
		userHistory.ContentParts = chatMediaContentParts(request.Content, images, videos)
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
		Images:  images,
		Videos:  videos,
		Status:  "complete",
	}
	assistantMessage := ChatMessage{
		ID:     s.nextChatIDLocked("msg"),
		Role:   llm.RoleAssistant,
		Status: "streaming",
	}
	streamID := s.nextChatIDLocked("stream")
	session.Messages = append(session.Messages, userMessage, assistantMessage)
	session.History = append(session.History, userHistory)
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

	go s.runChatTurn(runContext, cancel, workspace, settings, streamID, assistantMessage.ID, request.PlanMode)

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

func (s *SystemService) PruneChatMessage(workspaceID string, messageID string) (ChatSession, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return ChatSession{}, err
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return ChatSession{}, fmt.Errorf("message id is required")
	}

	s.chatMu.Lock()
	session := s.ensureChatSessionLocked(workspaceID)
	if session.Busy {
		s.chatMu.Unlock()
		return ChatSession{}, fmt.Errorf("wait for the current chat response to finish before pruning messages")
	}

	messageIndex := -1
	for i := range session.Messages {
		if session.Messages[i].ID == messageID {
			messageIndex = i
			break
		}
	}
	if messageIndex < 0 {
		s.chatMu.Unlock()
		return ChatSession{}, fmt.Errorf("message was not found")
	}

	messages := make([]ChatMessage, 0, len(session.Messages)-1)
	messages = append(messages, session.Messages[:messageIndex]...)
	messages = append(messages, session.Messages[messageIndex+1:]...)
	session.Messages = messages
	session.History = visibleChatHistory(messages)
	clone := cloneChatSession(session)
	s.chatMu.Unlock()

	return clone, nil
}

func visibleChatHistory(messages []ChatMessage) []llm.Message {
	history := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		switch message.Role {
		case llm.RoleUser:
			entry := llm.Message{Role: llm.RoleUser, Content: content}
			if len(message.Images) > 0 {
				entry.ContentParts = []llm.MessageContentPart{llm.TextContentPart(content)}
				for _, image := range message.Images {
					if image.DataURL != "" {
						entry.ContentParts = append(entry.ContentParts, llm.ImageURLContentPart(image.DataURL))
					}
				}
			}
			history = append(history, entry)
		case llm.RoleAssistant:
			if message.Status == "complete" {
				history = append(history, llm.Message{Role: llm.RoleAssistant, Content: content})
			}
		}
	}
	return history
}

func (s *SystemService) RetryChatMessage(workspaceID string, messageID string, planMode bool) (ChatSession, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return ChatSession{}, err
	}

	s.chatMu.Lock()

	session := s.ensureChatSessionLocked(workspaceID)

	if session.Busy {
		s.chatMu.Unlock()
		return ChatSession{}, fmt.Errorf("chat is already busy")
	}

	msgIndex := -1
	for i := range session.Messages {
		if session.Messages[i].ID == messageID {
			msgIndex = i
			break
		}
	}
	if msgIndex < 0 {
		s.chatMu.Unlock()
		return ChatSession{}, fmt.Errorf("message was not found")
	}

	if session.Messages[msgIndex].Role != llm.RoleAssistant || session.Messages[msgIndex].Status != "complete" {
		s.chatMu.Unlock()
		return ChatSession{}, fmt.Errorf("can only retry complete assistant messages")
	}

	// A compacted model history no longer has a one-to-one index relationship
	// with visible messages. Rebuild from the visible prefix when the user
	// explicitly rewinds the transcript.
	history := visibleChatHistory(session.Messages[:msgIndex])
	session.Messages = session.Messages[:msgIndex]
	session.History = cloneLLMMessages(history)

	assistantMessage := ChatMessage{
		ID:     s.nextChatIDLocked("msg"),
		Role:   llm.RoleAssistant,
		Status: "streaming",
	}
	streamID := s.nextChatIDLocked("stream")
	session.Messages = append(session.Messages, assistantMessage)
	session.Busy = true
	session.StreamID = streamID

	runContext, cancel := context.WithCancel(context.Background())
	s.chatStreams[workspaceID] = cancel

	clone := cloneChatSession(session)
	s.chatMu.Unlock()

	s.emitChatEvent(ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   assistantMessage.ID,
		Type:        "started",
	})

	go func() {
		workspace, settings, err := s.workspaceAndSettings(workspaceID)
		if err != nil {
			s.failChatMessage(workspaceID, streamID, assistantMessage.ID, err.Error())
			return
		}
		s.runChatTurnWithHistory(runContext, cancel, workspace, settings, streamID, assistantMessage.ID, history, planMode)
	}()

	return clone, nil
}

func (s *SystemService) EditChatMessage(workspaceID string, messageID string, content string, planMode bool) (ChatSession, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return ChatSession{}, err
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return ChatSession{}, fmt.Errorf("message content is required")
	}

	s.chatMu.Lock()

	session := s.ensureChatSessionLocked(workspaceID)

	if session.Busy {
		s.chatMu.Unlock()
		return ChatSession{}, fmt.Errorf("chat is already busy")
	}

	msgIndex := -1
	for i := range session.Messages {
		if session.Messages[i].ID == messageID {
			msgIndex = i
			break
		}
	}
	if msgIndex < 0 {
		s.chatMu.Unlock()
		return ChatSession{}, fmt.Errorf("message was not found")
	}

	if session.Messages[msgIndex].Role == llm.RoleAssistant {
		if session.Messages[msgIndex].Status != "complete" {
			s.chatMu.Unlock()
			return ChatSession{}, fmt.Errorf("can only edit complete assistant messages")
		}

		session.Messages[msgIndex].Content = content
		session.History = visibleChatHistory(session.Messages)
		clone := cloneChatSession(session)
		s.chatMu.Unlock()
		return clone, nil
	}

	if session.Messages[msgIndex].Role != llm.RoleUser {
		s.chatMu.Unlock()
		return ChatSession{}, fmt.Errorf("can only edit user or assistant messages")
	}

	// Update the message content.
	session.Messages[msgIndex].Content = content

	// Rebuild the hidden context from the visible prefix. This intentionally
	// discards compacted summaries and hidden tool state that may contradict
	// the user's edited transcript.
	history := visibleChatHistory(session.Messages[:msgIndex+1])
	session.Messages = session.Messages[:msgIndex+1]
	session.History = cloneLLMMessages(history)

	assistantMessage := ChatMessage{
		ID:     s.nextChatIDLocked("msg"),
		Role:   llm.RoleAssistant,
		Status: "streaming",
	}
	streamID := s.nextChatIDLocked("stream")
	session.Messages = append(session.Messages, assistantMessage)
	session.Busy = true
	session.StreamID = streamID

	runContext, cancel := context.WithCancel(context.Background())
	s.chatStreams[workspaceID] = cancel

	clone := cloneChatSession(session)
	s.chatMu.Unlock()

	s.emitChatEvent(ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   assistantMessage.ID,
		Type:        "started",
	})

	go func() {
		workspace, settings, err := s.workspaceAndSettings(workspaceID)
		if err != nil {
			s.failChatMessage(workspaceID, streamID, assistantMessage.ID, err.Error())
			return
		}
		s.runChatTurnWithHistory(runContext, cancel, workspace, settings, streamID, assistantMessage.ID, history, planMode)
	}()

	return clone, nil
}

func (s *SystemService) runChatTurn(ctx context.Context, cancel context.CancelFunc, workspace Workspace, settings llm.Settings, streamID string, messageID string, planMode bool) {
	s.runChatTurnWithHistory(ctx, cancel, workspace, settings, streamID, messageID, s.chatHistory(workspace.ID), planMode)
}

func (s *SystemService) runChatTurnWithHistory(ctx context.Context, cancel context.CancelFunc, workspace Workspace, settings llm.Settings, streamID string, messageID string, history []llm.Message, planMode bool) {
	defer cancel()
	defer s.finishChatStream(workspace.ID, streamID)

	client, err := llm.NewClient(settings)
	if err != nil {
		s.failChatMessage(workspace.ID, streamID, messageID, err.Error())
		return
	}

	candidates := workspaceSkillCandidates(ctx, workspace, latestWorkspaceSkillTask(history))
	currentUser := latestContextUserMessage(history)
	messages := append([]llm.Message{chatSystemMessage(workspace, planMode, candidates)}, history...)
	toolSchema := tools.LLMSchema()
	if planMode {
		toolSchema = tools.ReadOnlyLLMSchema()
	}
	recoverableToolCalls := make(map[string]bool)
	forcedCompactions := 0
	skillCheckpointPending := false
	skillCheckpointReminders := 0
	for {
		if err := ctx.Err(); err != nil {
			s.cancelChatMessage(workspace.ID, streamID, messageID)
			return
		}

		preflightPolicy := contextCompactionPolicy{CurrentUser: currentUser}
		if contextNeedsCompaction(settings, messages, toolSchema) &&
			contextHasCompressibleStale(settings, messages, preflightPolicy) {
			s.compactingChatMessage(workspace.ID, streamID, messageID)
			compaction, compactErr := compactContextIfNeeded(ctx, client, settings, messages, toolSchema, preflightPolicy)
			if compactErr != nil {
				if ctx.Err() != nil {
					s.cancelChatMessage(workspace.ID, streamID, messageID)
					return
				}
				s.retryChatMessage(workspace.ID, streamID, messageID)
			} else if compaction.Compacted {
				messages = compaction.Messages
				s.replaceChatHistory(workspace.ID, messages[1:])
				s.emitChatCompactionResult(workspace.ID, streamID, messageID, compaction)
				s.retryChatMessage(workspace.ID, streamID, messageID)
			}
		}

		request, err := llm.NewChatRequest(settings, messages, llm.WithTools(toolSchema), llm.WithToolChoice("auto"))
		if err != nil {
			s.failChatMessage(workspace.ID, streamID, messageID, err.Error())
			return
		}

		publishResponse := !skillCheckpointPending
		content, toolCalls, finished, finishReason, err := s.streamAssistantResponse(ctx, client, request, workspace.ID, streamID, messageID, publishResponse)
		if err != nil {
			if ctx.Err() != nil {
				s.cancelChatMessage(workspace.ID, streamID, messageID)
				return
			}
			if llm.IsContextLengthExceeded(err) {
				if recovery, ok := recoverToolResultContext(messages, recoverableToolCalls); ok {
					messages = recovery.Messages
					s.replaceChatHistory(workspace.ID, messages[1:])
					s.updateToolActivity(workspace.ID, streamID, messageID, recovery.Call, "error", recovery.ResultMessage.Content, toolResultContextErrorText)
					s.retryChatMessage(workspace.ID, streamID, messageID)
					continue
				}
				if forcedCompactions >= 2 {
					s.failChatMessage(workspace.ID, streamID, messageID, "Echo could not free enough context while preserving the system message, original prompt, and recent agent state.")
					return
				}
				var compaction contextCompactionResult
				var compactErr error
				for forcedCompactions < 2 {
					forcedCompactions++
					s.compactingChatMessage(workspace.ID, streamID, messageID)
					compaction, compactErr = compactContextIfNeeded(ctx, client, settings, messages, toolSchema, contextCompactionPolicy{
						CurrentUser:    currentUser,
						Force:          true,
						Aggressiveness: forcedCompactions,
					})
					if compactErr == nil {
						break
					}
					if ctx.Err() != nil {
						s.cancelChatMessage(workspace.ID, streamID, messageID)
						return
					}
				}
				if compactErr != nil {
					s.failChatMessage(workspace.ID, streamID, messageID, "Echo could not compact the context safely: "+compactErr.Error())
					return
				}
				messages = compaction.Messages
				s.replaceChatHistory(workspace.ID, messages[1:])
				s.emitChatCompactionResult(workspace.ID, streamID, messageID, compaction)
				s.retryChatMessage(workspace.ID, streamID, messageID)
				continue
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
		forcedCompactions = 0

		assistantHistory := llm.Message{Role: llm.RoleAssistant, Content: content, ToolCalls: toolCalls}
		messages = append(messages, assistantHistory)
		if publishResponse || len(toolCalls) > 0 {
			s.appendChatHistory(workspace.ID, assistantHistory)
		}
		if len(toolCalls) == 0 {
			if skillCheckpointPending {
				if skillCheckpointReminders < workspaceSkillMaxReminders {
					skillCheckpointReminders++
					messages = append(messages, llm.Message{
						Role:    llm.RoleUser,
						Content: workspaceSkillCheckpointPrompt(false),
					})
					continue
				}
				if content != "" {
					s.appendChatContent(workspace.ID, streamID, messageID, content)
				}
				s.appendChatContent(workspace.ID, streamID, messageID, "\n\n"+workspaceSkillCheckpointWarning())
				s.appendChatHistory(workspace.ID, assistantHistory)
			}
			s.completeChatMessage(workspace.ID, streamID, messageID, finishReason)
			return
		}

		for _, call := range toolCalls {
			if err := ctx.Err(); err != nil {
				s.cancelChatMessage(workspace.ID, streamID, messageID)
				return
			}
			execution := s.executeToolCall(ctx, workspace, settings, streamID, messageID, call, planMode)
			recoverableToolCalls[call.ID] = true
			messages = append(messages, execution.Messages...)
			for _, resultMessage := range execution.Messages {
				s.appendChatHistory(workspace.ID, resultMessage)
			}
			if len(execution.Changes) > 0 {
				skillCheckpointPending = true
				skillCheckpointReminders = 0
			}
			if execution.SkillCheckpoint {
				skillCheckpointPending = false
			}
		}
	}
}

func (s *SystemService) streamAssistantResponse(ctx context.Context, client *llm.Client, request llm.ChatRequest, workspaceID string, streamID string, messageID string, publish bool) (string, []llm.ToolCall, bool, string, error) {
	request.Messages = append([]llm.Message(nil), request.Messages...)
	totalContent := strings.Builder{}
	var lastLoop streamLoopDetection

	for attempt := 0; ; attempt++ {
		result := s.streamAssistantResponseAttempt(ctx, client, request, workspaceID, streamID, messageID, publish)
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

func (s *SystemService) streamAssistantResponseAttempt(ctx context.Context, client *llm.Client, request llm.ChatRequest, workspaceID string, streamID string, messageID string, publish bool) chatStreamAttemptResult {
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
		if publish {
			s.appendChatContent(workspaceID, streamID, messageID, text)
		}
		if detection, ok := loopDetector.observe(streamLoopContent, text); ok {
			return &detection
		}
		return nil
	}
	appendReasoning := func(text string) *streamLoopDetection {
		if text == "" {
			return nil
		}
		if publish {
			s.appendChatReasoning(workspaceID, streamID, messageID, text)
		}
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

type chatToolCallExecution struct {
	Messages        []llm.Message
	Changes         []tools.FileChange
	SkillCheckpoint bool
}

func (s *SystemService) executeToolCall(ctx context.Context, workspace Workspace, settings llm.Settings, streamID string, messageID string, call llm.ToolCall, readOnlyOnly bool) chatToolCallExecution {
	if call.ID == "" {
		call.ID = s.nextChatID("call")
	}
	if readOnlyOnly && !tools.IsReadOnlyToolName(call.Function.Name) {
		data := fmt.Sprintf(`{"tool":%q,"success":false,"error":{"code":"tool_not_allowed","message":"tool is not available in plan mode"}}`, call.Function.Name)
		s.updateToolActivity(workspace.ID, streamID, messageID, call, "error", data, "tool is not available in plan mode")
		return chatToolCallExecution{
			Messages: []llm.Message{{
				Role:       llm.RoleTool,
				ToolCallID: call.ID,
				Content:    data,
			}},
		}
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
	execution := s.executeTrackedToolCall(ctx, workspace, settings, call, WorkspaceChangeSource{
		Type:      "chat",
		MessageID: messageID,
	}, events)
	result := execution.Result

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

	return chatToolCallExecution{
		Messages:        toolResultMessages(call, result, data),
		Changes:         execution.Changes,
		SkillCheckpoint: workspaceSkillCheckpointCompleted(call, result),
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
	s.chatMu.Lock()
	if session := s.chatSessions[workspaceID]; session != nil {
		for i := range session.Messages {
			if session.Messages[i].ID == messageID {
				session.Messages[i].Status = "complete"
				break
			}
		}
	}
	s.chatMu.Unlock()
	_ = s.persistWorkspaceAutosave(workspaceID)
	s.emitChatEvent(ChatStreamEvent{
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

func (s *SystemService) compactingChatMessage(workspaceID string, streamID string, messageID string) {
	s.mutateChatMessage(workspaceID, messageID, func(message *ChatMessage) {
		message.Status = "compacting"
		message.Error = ""
	}, ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   messageID,
		Type:        "compacting",
	})
}

func (s *SystemService) emitChatCompactionResult(workspaceID string, streamID string, messageID string, result contextCompactionResult) {
	content := fmt.Sprintf(
		"Context compacted from approximately %d to %d tokens; %d stale messages were replaced.",
		result.BeforeTokens,
		result.AfterTokens,
		result.RemovedMessages,
	)
	if result.UsedFallback && result.Warning != "" {
		content += " " + result.Warning
		s.emitChatEvent(ChatStreamEvent{
			WorkspaceID: workspaceID,
			StreamID:    streamID,
			MessageID:   messageID,
			Type:        "compaction_warning",
			Content:     result.Warning,
		})
	}
	s.emitChatEvent(ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   messageID,
		Type:        "compacted",
		Content:     content,
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

func (s *SystemService) chatHistoryUpToLocked(workspaceID string, messageIndex int) []llm.Message {
	if session := s.chatSessions[workspaceID]; session != nil && messageIndex > 0 {
		if messageIndex > len(session.History) {
			messageIndex = len(session.History)
		}
		return append([]llm.Message(nil), session.History[:messageIndex]...)
	}
	return nil
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

func (s *SystemService) replaceChatHistory(workspaceID string, history []llm.Message) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	if session := s.chatSessions[workspaceID]; session != nil {
		session.History = cloneLLMMessages(history)
	}
}

func (s *SystemService) workspaceAndSettings(workspaceID string) (Workspace, llm.Settings, error) {
	return s.workspaceAndSettingsFor(workspaceID, llm.InteractionChat)
}

func (s *SystemService) workspaceAndSettingsFor(workspaceID string, interaction llm.Interaction) (Workspace, llm.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refreshWorkspaceStatusesLocked() {
		_ = s.saveLocked()
	}
	for _, workspace := range s.state.Workspaces {
		if workspace.ID == workspaceID {
			return workspace, s.state.Settings.ForInteraction(interaction), nil
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
	s.emitRuntimeEvent(chatStreamEventName, event)
	if s.ctx != nil {
		runtime.EventsEmit(s.ctx, chatStreamEventName, event)
	}
}

func chatSystemMessage(workspace Workspace, planMode bool, skillCandidates []tools.WorkspaceSkillSummary) llm.Message {
	instructions := "You are Echo, a personal AI assistant helping plan work inside the active workspace. " +
		contextCheckpointSystemGuidance + " " +
		"Use available tools when workspace facts are needed. " +
		"When the user mentions @path, treat it as a labeled workspace file reference like <folder-label>/path and read it before relying on its contents. " +
		"Use workspace_context for broad implementation planning when target files are unknown. " +
		"Use git_inspect when commit history, regressions, legacy behavior, ownership, or prior rationale would materially clarify the request; avoid routine history searches when the current code is sufficient. " +
		"When you need to find code but do not know the target file, prefer filesystem_search_workspace before shell commands. " +
		"When locating symbols, strings, or code blocks in a known file, prefer filesystem_search_text before reading the whole file. " +
		"When a search result gives a useful line number, read nearby code with filesystem_read_text aroundLine; copy the result's line value and avoid reading whole source files unless the entire file is genuinely needed. " +
		"Use lsp_query for definitions, references, hover info, document symbols, and member/completion candidates once you know the file and cursor position. " +
		"Keep plans concrete and concise."
	if planMode {
		instructions = "You are Echo, a personal AI assistant helping research and plan work inside the active workspace. " +
			contextCheckpointSystemGuidance + " " +
			"This chat is for planning changes only; do not make workspace changes, edit files, delete files, create files, run system modifying shell commands, or otherwise execute the plan. " +
			"Use the available read-only tools to inspect files and gather the facts needed to answer the user. " +
			"Use workspace_context for broad implementation planning when target files are unknown. " +
			"Use git_inspect when commit history, regressions, legacy behavior, ownership, or prior rationale would materially clarify the request; avoid routine history searches when the current code is sufficient. " +
			"When you need to find code but do not know the target file, prefer filesystem_search_workspace. " +
			"When locating symbols, strings, or code blocks in a known file, prefer filesystem_search_text before reading the whole file. " +
			"When a search result gives a useful line number, read nearby code with filesystem_read_text aroundLine; copy the result's line value and avoid reading whole source files unless the entire file is genuinely needed. " +
			"Use lsp_query for definitions, references, hover info, document symbols, and member/completion candidates once you know the file and cursor position. " +
			"Create a concrete, concise plan that follows the user's request and clearly describes the intended changes. " +
			"Even if the user asks you to modify files, tell them you are unable to because you are in planning mode."
	}
	return llm.Message{
		Role:    llm.RoleSystem,
		Content: workspaceSystemPrompt(workspaceSkillsPrompt(instructions, skillCandidates, !planMode), workspace),
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
	if repaired, ok := tools.RepairToolArgumentsJSON(json.RawMessage(call.Function.Arguments)); ok {
		call.Function.Arguments = string(repaired)
	}
	name, arguments := normalizeInlineToolNameAndRawArguments(call.Function.Name, json.RawMessage(call.Function.Arguments))
	call.Function.Name = name
	call.Function.Arguments = string(arguments)
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
		clone.Messages[i].Images = append([]ChatImageAttachment(nil), clone.Messages[i].Images...)
		clone.Messages[i].Videos = append([]ChatVideoAttachment(nil), clone.Messages[i].Videos...)
	}
	return clone
}
