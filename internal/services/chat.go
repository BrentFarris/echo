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
	Revision    uint64        `json:"revision"`
}

type ChatMessage struct {
	ID                string                  `json:"id"`
	Role              string                  `json:"role"`
	Content           string                  `json:"content,omitempty"`
	Images            []ChatImageAttachment   `json:"images,omitempty"`
	Videos            []ChatVideoAttachment   `json:"videos,omitempty"`
	Reasoning         string                  `json:"reasoning,omitempty"`
	ResearchReasoning []ChatResearchReasoning `json:"researchReasoning,omitempty"`
	ToolCalls         []ChatToolActivity      `json:"toolCalls,omitempty"`
	ResearchAgents    []ChatResearchAgent     `json:"researchAgents,omitempty"`
	Status            string                  `json:"status"`
	Error             string                  `json:"error,omitempty"`
}

type ChatToolActivity struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Status    string `json:"status"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	AgentID   string `json:"agentId,omitempty"`
	AgentName string `json:"agentName,omitempty"`
}

type ChatResearchAgent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Phase     string `json:"phase,omitempty"`
	TaskLabel string `json:"taskLabel,omitempty"`
	Error     string `json:"error,omitempty"`
}

type ChatResearchReasoning struct {
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	Reasoning string `json:"reasoning"`
	Truncated bool   `json:"truncated,omitempty"`
	Replace   bool   `json:"replace,omitempty"`
}

type ChatStreamEvent struct {
	WorkspaceID       string                 `json:"workspaceId"`
	StreamID          string                 `json:"streamId"`
	MessageID         string                 `json:"messageId"`
	Type              string                 `json:"type"`
	Content           string                 `json:"content,omitempty"`
	Reasoning         string                 `json:"reasoning,omitempty"`
	ResearchReasoning *ChatResearchReasoning `json:"researchReasoning,omitempty"`
	ToolCall          *ChatToolActivity      `json:"toolCall,omitempty"`
	ResearchAgent     *ChatResearchAgent     `json:"researchAgent,omitempty"`
	Error             string                 `json:"error,omitempty"`
	FinishReason      string                 `json:"finishReason,omitempty"`
	Revision          uint64                 `json:"revision"`
	Session           *ChatSession           `json:"session,omitempty"`
}

type chatSessionState struct {
	WorkspaceID string
	Messages    []ChatMessage
	History     []llm.Message
	Busy        bool
	StreamID    string
	Revision    uint64
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

	agentModeID := request.AgentModeID
	// Backward compatibility: PlanMode true maps to plan mode ID.
	if agentModeID == "" && request.PlanMode {
		agentModeID = AgentModeIDPlan
	}
	if agentModeID == "" {
		agentModeID = AgentModeIDGeneral
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
	session.Revision++
	s.chatStreams[workspaceID] = cancel
	clone := cloneChatSession(session)
	s.chatMu.Unlock()

	s.emitChatEvent(ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   assistantMessage.ID,
		Type:        "started",
		Revision:    clone.Revision,
		Session:     &clone,
	})

	go s.runChatTurn(runContext, cancel, workspace, settings, streamID, assistantMessage.ID, agentModeID)

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
	streamID := ""
	if cancel, ok := s.chatStreams[workspaceID]; ok {
		if existing := s.chatSessions[workspaceID]; existing != nil {
			streamID = existing.StreamID
		}
		cancel()
		delete(s.chatStreams, workspaceID)
	}
	revision := uint64(1)
	if existing := s.chatSessions[workspaceID]; existing != nil {
		revision = existing.Revision + 1
	}
	session := &chatSessionState{WorkspaceID: workspaceID, Revision: revision}
	s.chatSessions[workspaceID] = session
	clone := cloneChatSession(session)
	s.chatMu.Unlock()
	if streamID != "" {
		s.closeChatResearchRun(workspaceID, streamID)
	}
	s.emitChatEvent(ChatStreamEvent{
		WorkspaceID: workspaceID,
		Type:        "session_updated",
		Revision:    clone.Revision,
		Session:     &clone,
	})
	_ = s.persistWorkspaceAutosave(workspaceID)
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
	session.Revision++
	clone := cloneChatSession(session)
	s.chatMu.Unlock()
	s.emitChatEvent(ChatStreamEvent{
		WorkspaceID: workspaceID,
		Type:        "session_updated",
		Revision:    clone.Revision,
		Session:     &clone,
	})
	_ = s.persistWorkspaceAutosave(workspaceID)

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

func (s *SystemService) RetryChatMessage(workspaceID string, messageID string, agentModeID string) (ChatSession, error) {
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

	if agentModeID == "" {
		agentModeID = AgentModeIDGeneral
	}

	assistantMessage := ChatMessage{
		ID:     s.nextChatIDLocked("msg"),
		Role:   llm.RoleAssistant,
		Status: "streaming",
	}
	streamID := s.nextChatIDLocked("stream")
	session.Messages = append(session.Messages, assistantMessage)
	session.Busy = true
	session.StreamID = streamID
	session.Revision++

	runContext, cancel := context.WithCancel(context.Background())
	s.chatStreams[workspaceID] = cancel

	clone := cloneChatSession(session)
	s.chatMu.Unlock()

	s.emitChatEvent(ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   assistantMessage.ID,
		Type:        "started",
		Revision:    clone.Revision,
		Session:     &clone,
	})

	go func() {
		workspace, settings, err := s.workspaceAndSettings(workspaceID)
		if err != nil {
			s.failChatMessage(workspaceID, streamID, assistantMessage.ID, err.Error())
			return
		}
		s.runChatTurnWithHistory(runContext, cancel, workspace, settings, streamID, assistantMessage.ID, history, agentModeID, func(wid string, u llm.Usage) {
			_, _ = s.RecordTokenUsage(wid, int64(u.TotalTokens))
		})
	}()

	return clone, nil
}

func (s *SystemService) EditChatMessage(workspaceID string, messageID string, content string, agentModeID string) (ChatSession, error) {
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
		session.Revision++
		clone := cloneChatSession(session)
		s.chatMu.Unlock()
		s.emitChatEvent(ChatStreamEvent{
			WorkspaceID: workspaceID,
			MessageID:   messageID,
			Type:        "session_updated",
			Revision:    clone.Revision,
			Session:     &clone,
		})
		_ = s.persistWorkspaceAutosave(workspaceID)
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

	if agentModeID == "" {
		agentModeID = AgentModeIDGeneral
	}

	assistantMessage := ChatMessage{
		ID:     s.nextChatIDLocked("msg"),
		Role:   llm.RoleAssistant,
		Status: "streaming",
	}
	streamID := s.nextChatIDLocked("stream")
	session.Messages = append(session.Messages, assistantMessage)
	session.Busy = true
	session.StreamID = streamID
	session.Revision++

	runContext, cancel := context.WithCancel(context.Background())
	s.chatStreams[workspaceID] = cancel

	clone := cloneChatSession(session)
	s.chatMu.Unlock()

	s.emitChatEvent(ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   assistantMessage.ID,
		Type:        "started",
		Revision:    clone.Revision,
		Session:     &clone,
	})

	go func() {
		workspace, settings, err := s.workspaceAndSettings(workspaceID)
		if err != nil {
			s.failChatMessage(workspaceID, streamID, assistantMessage.ID, err.Error())
			return
		}
		s.runChatTurnWithHistory(runContext, cancel, workspace, settings, streamID, assistantMessage.ID, history, agentModeID, func(wid string, u llm.Usage) {
			_, _ = s.RecordTokenUsage(wid, int64(u.TotalTokens))
		})
	}()

	return clone, nil
}

func (s *SystemService) runChatTurn(ctx context.Context, cancel context.CancelFunc, workspace Workspace, settings llm.Settings, streamID string, messageID string, agentModeID string) {
	s.runChatTurnWithHistory(ctx, cancel, workspace, settings, streamID, messageID, s.chatHistory(workspace.ID), agentModeID, func(wid string, u llm.Usage) {
		_, _ = s.RecordTokenUsage(wid, int64(u.TotalTokens))
	})
}

func (s *SystemService) runChatTurnWithHistory(ctx context.Context, cancel context.CancelFunc, workspace Workspace, settings llm.Settings, streamID string, messageID string, history []llm.Message, agentModeID string, onUsage func(workspaceID string, usage llm.Usage)) {
	defer cancel()
	defer s.finishChatStream(workspace.ID, streamID)

	client, err := llm.NewClient(settings)
	if err != nil {
		s.failChatMessage(workspace.ID, streamID, messageID, err.Error())
		return
	}

	mode, resolvedModeID := s.resolveAgentMode(agentModeID)
	isPlanMode := resolvedModeID == AgentModeIDPlan
	toolScopes := buildToolScopes(mode.Permissions)
	researchEnabled := settings.ResearchAgentConcurrency > 0
	var research *chatResearchRun
	if researchEnabled {
		_, researchSettings, researchSettingsErr := s.workspaceAndSettingsFor(workspace.ID, llm.InteractionResearch)
		if researchSettingsErr != nil {
			s.failChatMessage(workspace.ID, streamID, messageID, researchSettingsErr.Error())
			return
		}
		research = s.newChatResearchRun(ctx, workspace, researchSettings, settings, streamID, messageID, mode)
		defer research.Close()
	}

	candidates := workspaceSkillCandidates(ctx, workspace, latestWorkspaceSkillTask(history))
	currentUser := latestContextUserMessage(history)
	messages := append([]llm.Message{chatSystemMessage(workspace, mode, candidates, researchEnabled)}, history...)
	toolSchema := tools.LLMSchema()
	if researchEnabled {
		toolSchema = tools.ChatLLMSchema()
	}
	if isPlanMode {
		toolSchema = tools.PlanModeDirectLLMSchema()
		if researchEnabled {
			toolSchema = tools.PlanModeLLMSchema()
		}
	}
	recoverableToolCalls := make(map[string]bool)
	if researchEnabled && shouldBootstrapResearch(currentUser, resolvedModeID) {
		arguments, marshalErr := json.Marshal(map[string]any{
			"agents": []map[string]string{{
				"name": "Research Scout",
				"task": automaticResearchTask(currentUser),
			}},
		})
		if marshalErr == nil {
			call := llm.ToolCall{
				ID:   s.nextChatID("call"),
				Type: "function",
				Function: llm.FunctionCall{
					Name:      "research_agents_spawn",
					Arguments: string(arguments),
				},
			}
			assistant := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}
			messages = append(messages, assistant)
			s.appendChatHistory(workspace.ID, assistant)
			execution := s.executeToolCall(ctx, workspace, settings, streamID, messageID, call, isPlanMode, toolScopes, research)
			research.MarkAutomaticScout()
			recoverableToolCalls[call.ID] = true
			messages = append(messages, execution.Messages...)
			for _, resultMessage := range execution.Messages {
				s.appendChatHistory(workspace.ID, resultMessage)
			}
		}
	}
	forcedCompactions := 0
	mediaPayloadsDisabled := false
	skillCheckpointPending := false
	skillCheckpointReminders := 0
	emptyAssistantRetries := 0
	transientResponseRetries := 0
	researchFinalizeReminders := 0
	forceFinalNoTools := false
	orchestrationRounds := 0
	for {
		orchestrationRounds++
		if orchestrationRounds > 64 {
			if s.completeChatWithResearchFallback(workspace.ID, streamID, messageID, research, "The chat orchestration limit was reached before the model produced a final answer.") {
				return
			}
			s.failChatMessage(workspace.ID, streamID, messageID, "The chat exceeded 64 assistant/tool rounds without producing a final answer.")
			return
		}
		if err := ctx.Err(); err != nil {
			s.cancelChatMessage(workspace.ID, streamID, messageID)
			return
		}
		if research != nil {
			if instruction := research.TakeFanoutInstruction(); instruction != "" {
				messages = append(messages, llm.Message{Role: llm.RoleUser, Content: instruction})
			}
		}

		preflightPolicy := contextCompactionPolicy{CurrentUser: currentUser}
		researchHeadroomCompaction := research != nil && research.parentContextNeedsCompaction(messages, toolSchema)
		if researchHeadroomCompaction &&
			contextHasCompressibleStale(settings, messages, preflightPolicy) {
			if !contextNeedsCompaction(settings, messages, toolSchema) {
				preflightPolicy.Force = true
			}
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

		if mediaPayloadsDisabled {
			var changed bool
			messages, changed = chatMessagesWithoutMediaPayloads(messages)
			if changed {
				s.replaceChatHistory(workspace.ID, messages[1:])
				currentUser = latestContextUserMessage(messages)
			}
		}

		var request llm.ChatRequest
		var err error
		if forceFinalNoTools {
			request, err = llm.NewChatRequest(settings, messages)
		} else {
			request, err = llm.NewChatRequest(settings, messages, llm.WithTools(toolSchema), llm.WithToolChoice("auto"))
		}
		if err != nil {
			if s.completeChatWithResearchFallback(workspace.ID, streamID, messageID, research, err.Error()) {
				return
			}
			s.failChatMessage(workspace.ID, streamID, messageID, err.Error())
			return
		}

		publishResponse := !skillCheckpointPending && (forceFinalNoTools || research == nil || !research.HasOutstanding())
		content, toolCalls, finished, finishReason, usage, err := s.streamAssistantResponse(ctx, client, request, workspace.ID, streamID, messageID, publishResponse)
		if usage != nil && onUsage != nil {
			onUsage(workspace.ID, *usage)
		}
		if err != nil {
			if ctx.Err() != nil {
				s.cancelChatMessage(workspace.ID, streamID, messageID)
				return
			}
			if content == "" && len(toolCalls) == 0 && isUnsupportedChatMediaError(err) {
				var changed bool
				messages, changed = chatMessagesWithoutMediaPayloads(messages)
				if changed {
					mediaPayloadsDisabled = true
					s.replaceChatHistory(workspace.ID, messages[1:])
					currentUser = latestContextUserMessage(messages)
					continue
				}
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
					if s.completeChatWithResearchFallback(workspace.ID, streamID, messageID, research, "Echo could not free enough context for final synthesis.") {
						return
					}
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
					if s.completeChatWithResearchFallback(workspace.ID, streamID, messageID, research, "Echo could not compact the context safely: "+compactErr.Error()) {
						return
					}
					s.failChatMessage(workspace.ID, streamID, messageID, "Echo could not compact the context safely: "+compactErr.Error())
					return
				}
				messages = compaction.Messages
				s.replaceChatHistory(workspace.ID, messages[1:])
				s.emitChatCompactionResult(workspace.ID, streamID, messageID, compaction)
				s.retryChatMessage(workspace.ID, streamID, messageID)
				continue
			}
			if content == "" && len(toolCalls) == 0 && transientResponseRetries < 1 {
				transientResponseRetries++
				s.retryChatMessage(workspace.ID, streamID, messageID)
				messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "The previous model stream failed before returning usable content. Retry once from the current research state and produce a valid tool call or final answer."})
				continue
			}
			if s.completeChatWithResearchFallback(workspace.ID, streamID, messageID, research, userFacingLLMError(err)) {
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
			if s.completeChatWithResearchFallback(workspace.ID, streamID, messageID, research, err.Error()) {
				return
			}
			s.failChatMessage(workspace.ID, streamID, messageID, err.Error())
			return
		}
		forcedCompactions = 0
		transientResponseRetries = 0
		if isEmptyAssistantResponse(content, toolCalls) {
			if emptyAssistantRetries >= maxEmptyAssistantRetries {
				if s.completeChatWithResearchFallback(workspace.ID, streamID, messageID, research, emptyAssistantResponseError().Error()) {
					return
				}
				s.failChatMessage(workspace.ID, streamID, messageID, emptyAssistantResponseError().Error())
				return
			}
			emptyAssistantRetries++
			s.retryChatMessage(workspace.ID, streamID, messageID)
			messages = append(messages, emptyAssistantRetryMessage())
			continue
		}
		emptyAssistantRetries = 0

		assistantHistory := llm.Message{Role: llm.RoleAssistant, Content: content, ToolCalls: toolCalls}
		if len(toolCalls) == 0 && !forceFinalNoTools && research != nil && research.HasOutstanding() {
			messages = append(messages, assistantHistory)
			researchFinalizeReminders++
			if researchFinalizeReminders <= 3 {
				reminder := "Research agents are still running or have reports that have not been collected. Do not finalize yet. Call research_agents_wait, ask a focused follow-up with research_agent_send, or cancel unneeded agents."
				if research.NeedsFanout() {
					reminder = "The general scout report has been collected, but specialist fan-out has not happened. Do not finalize. Identify the major independent aspects from the scout report and call research_agents_spawn with at least 2 focused specialist agents, then wait for their reports."
				}
				messages = append(messages, llm.Message{Role: llm.RoleUser, Content: reminder})
				continue
			}
			_, _ = research.CancelResearchAgents(context.WithoutCancel(ctx), nil)
			handoff := research.FallbackMarkdown()
			if handoff == "" {
				handoff = "No usable research handoff was available; state the limitation clearly."
			}
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "The research coordination limit was reached. Produce the final answer now without tools, using the available partial handoffs below and explicitly identifying missing evidence.\n\n" + handoff})
			forceFinalNoTools = true
			continue
		}
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
			execution := s.executeToolCall(ctx, workspace, settings, streamID, messageID, call, isPlanMode, toolScopes, research)
			if tools.IsResearchAgentToolName(call.Function.Name) {
				researchFinalizeReminders = 0
			}
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

func (s *SystemService) streamAssistantResponse(ctx context.Context, client *llm.Client, request llm.ChatRequest, workspaceID string, streamID string, messageID string, publish bool) (string, []llm.ToolCall, bool, string, *llm.Usage, error) {
	request.Messages = append([]llm.Message(nil), request.Messages...)
	totalContent := strings.Builder{}
	var lastLoop streamLoopDetection

	for attempt := 0; ; attempt++ {
		result := s.streamAssistantResponseAttempt(ctx, client, request, workspaceID, streamID, messageID, publish)
		totalContent.WriteString(result.content)
		if result.loop != nil {
			lastLoop = *result.loop
			if attempt >= maxStreamLoopRetries {
				return totalContent.String(), result.toolCalls, false, result.finishReason, result.usage, streamLoopExceededError(lastLoop)
			}
			s.retryChatMessage(workspaceID, streamID, messageID)
			request.Messages = appendStreamLoopRetryMessages(request.Messages, result.content, lastLoop)
			continue
		}
		return totalContent.String(), result.toolCalls, result.finished, result.finishReason, result.usage, result.err
	}
}

type chatStreamAttemptResult struct {
	content      string
	toolCalls    []llm.ToolCall
	finished     bool
	finishReason string
	loop         *streamLoopDetection
	usage        *llm.Usage
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
	return chatStreamAttemptResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), finished: finished, finishReason: finishReason, usage: stream.Usage}
}

type chatToolCallExecution struct {
	Messages        []llm.Message
	Changes         []tools.FileChange
	SkillCheckpoint bool
}

func (s *SystemService) executeToolCall(ctx context.Context, workspace Workspace, settings llm.Settings, streamID string, messageID string, call llm.ToolCall, readOnlyOnly bool, toolScopes *tools.ToolScopeChecker, research *chatResearchRun) chatToolCallExecution {
	if call.ID == "" {
		call.ID = s.nextChatID("call")
	}
	if tools.IsResearchAgentToolName(call.Function.Name) && research == nil {
		data := fmt.Sprintf(`{"tool":%q,"success":false,"error":{"code":"research_agents_disabled","message":"research agents are disabled in settings"}}`, call.Function.Name)
		s.updateToolActivity(workspace.ID, streamID, messageID, call, "error", data, "research agents are disabled in settings")
		return chatToolCallExecution{
			Messages: []llm.Message{{
				Role:       llm.RoleTool,
				ToolCallID: call.ID,
				Content:    data,
			}},
		}
	}
	if readOnlyOnly && !tools.IsPlanModeToolName(call.Function.Name) {
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
		Type:           "chat",
		MessageID:      messageID,
		researchAgents: research,
	}, events, toolScopes)
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
	s.closeChatResearchRun(workspaceID, streamID)
	event := ChatStreamEvent{
		WorkspaceID:  workspaceID,
		StreamID:     streamID,
		MessageID:    messageID,
		Type:         "complete",
		FinishReason: finishReason,
	}
	// Update the durable message first, but keep the session busy until its
	// synchronous autosave finishes. Callers can therefore trust !Busy as a
	// true turn-settled boundary with no late persistence work still running.
	s.chatMu.Lock()
	if session := s.chatSessions[workspaceID]; session != nil && (session.StreamID == streamID || session.StreamID == "") {
		for i := range session.Messages {
			if session.Messages[i].ID == messageID {
				session.Messages[i].Status = "complete"
				session.Messages[i].Error = ""
				session.Revision++
				event.Revision = session.Revision
				break
			}
		}
	}
	s.chatMu.Unlock()
	_ = s.persistWorkspaceAutosave(workspaceID)
	if event.Revision > 0 {
		s.finishChatStream(workspaceID, streamID)
	}
	s.emitChatEvent(event)
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
	s.closeChatResearchRun(workspaceID, streamID)
	s.emitChatEvent(s.settleChatMessage(workspaceID, streamID, messageID, "canceled", ""))
}

func (s *SystemService) failChatMessage(workspaceID string, streamID string, messageID string, messageError string) {
	s.closeChatResearchRun(workspaceID, streamID)
	s.emitChatEvent(s.settleChatMessage(workspaceID, streamID, messageID, "error", messageError))
}

func (s *SystemService) settleChatMessage(workspaceID string, streamID string, messageID string, status string, messageError string) ChatStreamEvent {
	event := ChatStreamEvent{
		WorkspaceID: workspaceID,
		StreamID:    streamID,
		MessageID:   messageID,
		Type:        status,
		Error:       messageError,
	}
	s.chatMu.Lock()
	if session := s.chatSessions[workspaceID]; session != nil {
		messageFound := false
		for i := range session.Messages {
			if session.Messages[i].ID == messageID {
				messageFound = true
				if session.StreamID == streamID || session.StreamID == "" {
					session.Messages[i].Status = status
					session.Messages[i].Error = messageError
				}
				break
			}
		}
		if messageFound && (session.StreamID == streamID || session.StreamID == "") {
			session.Busy = false
			session.StreamID = ""
			session.Revision++
			event.Revision = session.Revision
			delete(s.chatStreams, workspaceID)
		}
	}
	s.chatMu.Unlock()
	return event
}

func (s *SystemService) mutateChatMessage(workspaceID string, messageID string, mutate func(*ChatMessage), event ChatStreamEvent) {
	s.chatMu.Lock()
	if session := s.chatSessions[workspaceID]; session != nil {
		for i := range session.Messages {
			if session.Messages[i].ID == messageID {
				mutate(&session.Messages[i])
				session.Revision++
				event.Revision = session.Revision
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
		delete(s.chatStreams, workspaceID)
	}
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

func chatSystemMessage(workspace Workspace, mode AgentMode, skillCandidates []tools.WorkspaceSkillSummary, researchEnabled bool) llm.Message {
	isPlanMode := mode.ID == AgentModeIDPlan
	instructions := "You are Echo, a personal AI assistant helping plan work inside the active workspace. " +
		contextCheckpointSystemGuidance + " " +
		"Use available tools when workspace facts are needed. " +
		"When the user mentions @path, treat it as a labeled workspace file or directory reference like <folder-label>/path. Read referenced files, and list or search within referenced directories before relying on their contents. " +
		"Use workspace_context for broad implementation planning when target files are unknown. " +
		"Use git_inspect when commit history, regressions, legacy behavior, ownership, or prior rationale would materially clarify the request; avoid routine history searches when the current code is sufficient. " +
		"When you need to find code but do not know the target file, prefer filesystem_search_workspace before shell commands. " +
		"When locating symbols, strings, or code blocks in a known file, prefer filesystem_search_text before reading the whole file. " +
		"When a search result gives a useful line number, read nearby code with filesystem_read_text aroundLine; copy the result's line value and avoid reading whole source files unless the entire file is genuinely needed. " +
		"Use lsp_query for definitions, references, hover info, document symbols, and member/completion candidates once you know the file and cursor position. " +
		"Keep plans concrete and concise."
	if researchEnabled {
		instructions += " " + researchOrchestratorSystemGuidance
	}
	if isPlanMode {
		instructions = "You are Echo, a personal AI assistant helping research and plan work inside the active workspace. " +
			contextCheckpointSystemGuidance + " " +
			"This chat is for planning changes only; do not make workspace changes, edit files, delete files, create project files, run system modifying shell commands, or otherwise execute the plan. " +
			"Use the available read-only tools to inspect files and gather the facts needed to answer the user. The sole mutation allowed in Plan Mode is workspace_task_create, which records future work in Echo's backlog when the user asks. " +
			"When the user mentions @path, treat it as a labeled workspace file or directory reference like <folder-label>/path. Read referenced files, and list or search within referenced directories before relying on their contents. " +
			"Use workspace_context for broad implementation planning when target files are unknown. " +
			"Use git_inspect when commit history, regressions, legacy behavior, ownership, or prior rationale would materially clarify the request; avoid routine history searches when the current code is sufficient. " +
			"When you need to find code but do not know the target file, prefer filesystem_search_workspace. " +
			"When locating symbols, strings, or code blocks in a known file, prefer filesystem_search_text before reading the whole file. " +
			"When a search result gives a useful line number, read nearby code with filesystem_read_text aroundLine; copy the result's line value and avoid reading whole source files unless the entire file is genuinely needed. " +
			"Use lsp_query for definitions, references, hover info, document symbols, and member/completion candidates once you know the file and cursor position. " +
			"Create a concrete, concise plan that follows the user's request and clearly describes the intended changes. " +
			"Even if the user asks you to modify files, tell them you are unable to because you are in planning mode."
		if researchEnabled {
			instructions += " " + researchOrchestratorSystemGuidance
		}
	}

	var prompt strings.Builder
	prompt.WriteString(instructions)
	if mode.Prompt != "" {
		prompt.WriteString("\n\n")
		prompt.WriteString(mode.Prompt)
	}
	learningEnabled := !isPlanMode

	// Append permission summary so the model knows its boundaries.
	permissionSummary := formatAgentModePermissionSummary(mode)
	if permissionSummary != "" {
		prompt.WriteString("\n\n")
		prompt.WriteString(permissionSummary)
	}

	return llm.Message{
		Role:    llm.RoleSystem,
		Content: workspaceSystemPrompt(workspaceSkillsPrompt(prompt.String(), skillCandidates, learningEnabled), workspace),
	}
}

// formatAgentModePermissionSummary returns a human-readable permission summary
// for the given mode. Returns empty string if no restrictions apply.
func formatAgentModePermissionSummary(mode AgentMode) string {
	permissions := mode.Permissions
	if len(permissions) == 0 && len(mode.ToolPermissions) == 0 && len(mode.PathPermissions) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Agent mode permissions:")

	// Derive tool list from Permissions map if available.
	var toolNames []string
	if len(permissions) > 0 {
		toolNames = permissionsMapToolNames(permissions)
	} else if len(mode.ToolPermissions) > 0 {
		toolNames = mode.ToolPermissions
	}

	if len(toolNames) > 0 {
		sb.WriteString("\n- Allowed tools: ")
		sb.WriteString(strings.Join(toolNames, ", "))
	} else {
		sb.WriteString("\n- Allowed tools: all")
	}

	// Derive per-tool path restrictions from Permissions map.
	if len(permissions) > 0 {
		// Check if any tool has path constraints.
		hasPathRestrictions := false
		for _, perm := range permissions {
			if len(perm.Paths) > 0 {
				hasPathRestrictions = true
				break
			}
		}
		if hasPathRestrictions {
			sb.WriteString("\n- Path restrictions per tool:")
			for _, name := range toolNames {
				perm, ok := permissions[name]
				if !ok {
					continue
				}
				if len(perm.Paths) > 0 {
					sb.WriteString(fmt.Sprintf("\n  - %s: %s", name, strings.Join(perm.Paths, ", ")))
				} else {
					sb.WriteString(fmt.Sprintf("\n  - %s: all paths", name))
				}
			}
		} else {
			sb.WriteString("\n- Allowed paths: all")
		}
	} else if len(mode.PathPermissions) > 0 {
		sb.WriteString("\n- Allowed paths: ")
		sb.WriteString(strings.Join(mode.PathPermissions, ", "))
	} else {
		sb.WriteString("\n- Allowed paths: all")
	}

	return sb.String()
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
		Revision:    session.Revision,
	}
	for i := range clone.Messages {
		clone.Messages[i].ToolCalls = append([]ChatToolActivity(nil), clone.Messages[i].ToolCalls...)
		clone.Messages[i].Images = append([]ChatImageAttachment(nil), clone.Messages[i].Images...)
		clone.Messages[i].Videos = append([]ChatVideoAttachment(nil), clone.Messages[i].Videos...)
	}
	return clone
}
