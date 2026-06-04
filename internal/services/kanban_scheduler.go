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

const (
	kanbanEventName       = "echo:kanban:event"
	defaultAgentLimit     = 2
	maxAgentLimit         = 8
	agentCancellationText = "Agent execution was canceled."
)

type KanbanEvent struct {
	WorkspaceID string               `json:"workspaceId"`
	CardID      string               `json:"cardId,omitempty"`
	Type        string               `json:"type"`
	Board       KanbanBoard          `json:"board"`
	Entry       *KanbanProgressEntry `json:"entry,omitempty"`
}

type kanbanAgentRun struct {
	id     uint64
	cancel context.CancelFunc
}

type kanbanAgentResult struct {
	cardID string
}

func (s *SystemService) StartKanbanExecution(workspaceID string, concurrency int) (KanbanBoard, error) {
	workspace, settings, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return KanbanBoard{}, err
	}
	concurrency = normalizeAgentLimit(concurrency)

	s.mu.Lock()
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()

	s.chatMu.Lock()
	if _, running := s.kanbanRuns[workspaceID]; running {
		s.chatMu.Unlock()
		return board, fmt.Errorf("kanban execution is already running")
	}
	runContext, cancel := context.WithCancel(context.Background())
	s.kanbanRuns[workspaceID] = cancel
	s.chatMu.Unlock()

	go s.runKanbanScheduler(runContext, workspace, settings, concurrency)
	return board, nil
}

func (s *SystemService) StopKanbanExecution(workspaceID string) (KanbanBoard, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	s.chatMu.Lock()
	cancelRun := s.kanbanRuns[workspaceID]
	agentCancels := make([]context.CancelFunc, 0)
	for key, agent := range s.kanbanAgents {
		cardWorkspaceID, _ := splitKanbanAgentKey(key)
		if cardWorkspaceID == workspaceID {
			agentCancels = append(agentCancels, agent.cancel)
		}
	}
	s.chatMu.Unlock()

	if cancelRun != nil {
		cancelRun()
	}
	for _, cancel := range agentCancels {
		cancel()
	}
	return s.LoadKanbanBoard(workspaceID)
}

func (s *SystemService) StopKanbanCard(workspaceID string, cardID string) (KanbanBoard, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	key := kanbanAgentKey(workspaceID, cardID)
	s.chatMu.Lock()
	agent := s.kanbanAgents[key]
	s.chatMu.Unlock()
	if agent != nil {
		s.blockKanbanCard(workspaceID, cardID, agent.id, "Canceled", "User stopped the card agent.")
		agent.cancel()
		return s.LoadKanbanBoard(workspaceID)
	}

	s.mu.Lock()
	if err := s.moveKanbanCardLocked(workspaceID, cardID, KanbanLaneBlocked, KanbanProgressEntry{
		Type:    "error",
		Title:   "Canceled",
		Content: "User stopped the card.",
		Status:  KanbanLaneBlocked,
	}); err != nil {
		s.mu.Unlock()
		return KanbanBoard{}, err
	}
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return KanbanBoard{}, err
	}
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()
	s.emitKanbanEvent(KanbanEvent{WorkspaceID: workspaceID, CardID: cardID, Type: "card_updated", Board: board})
	return board, nil
}

func (s *SystemService) AddKanbanCardMessage(workspaceID string, cardID string, content string) (KanbanBoard, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return KanbanBoard{}, fmt.Errorf("message is required")
	}
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	s.mu.Lock()
	if err := s.moveKanbanCardLocked(workspaceID, cardID, KanbanLaneReady, KanbanProgressEntry{
		Type:    "message",
		Title:   "User message",
		Content: content,
		Status:  KanbanLaneReady,
	}); err != nil {
		s.mu.Unlock()
		return KanbanBoard{}, err
	}
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return KanbanBoard{}, err
	}
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()
	s.emitKanbanEvent(KanbanEvent{WorkspaceID: workspaceID, CardID: cardID, Type: "card_updated", Board: board})
	return board, nil
}

func (s *SystemService) OpenKanbanCardDetail(workspaceID string, cardID string) (KanbanBoard, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	s.mu.Lock()
	found := false
	for _, card := range s.state.KanbanCards {
		if card.WorkspaceID == workspaceID && card.ID == cardID {
			found = true
			break
		}
	}
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()
	if !found {
		return KanbanBoard{}, fmt.Errorf("kanban card was not found")
	}

	s.chatMu.Lock()
	s.kanbanDetailViews[workspaceID] = cardID
	s.chatMu.Unlock()
	return board, nil
}

func (s *SystemService) CloseKanbanCardDetail(workspaceID string, cardID string) (KanbanBoard, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	s.chatMu.Lock()
	if activeCardID := s.kanbanDetailViews[workspaceID]; cardID == "" || activeCardID == cardID {
		delete(s.kanbanDetailViews, workspaceID)
	}
	s.chatMu.Unlock()
	return s.LoadKanbanBoard(workspaceID)
}

func (s *SystemService) Shutdown() {
	s.chatMu.Lock()
	runCancels := make([]context.CancelFunc, 0, len(s.kanbanRuns))
	for _, cancel := range s.kanbanRuns {
		runCancels = append(runCancels, cancel)
	}
	agentCancels := make([]context.CancelFunc, 0, len(s.kanbanAgents))
	for _, agent := range s.kanbanAgents {
		agentCancels = append(agentCancels, agent.cancel)
	}
	chatCancels := make([]context.CancelFunc, 0, len(s.chatStreams))
	for _, cancel := range s.chatStreams {
		chatCancels = append(chatCancels, cancel)
	}
	s.chatMu.Unlock()

	for _, cancel := range runCancels {
		cancel()
	}
	for _, cancel := range agentCancels {
		cancel()
	}
	for _, cancel := range chatCancels {
		cancel()
	}
}

func (s *SystemService) runKanbanScheduler(ctx context.Context, workspace Workspace, settings llm.Settings, concurrency int) {
	defer s.forgetKanbanRun(workspace.ID)

	done := make(chan kanbanAgentResult, maxAgentLimit)
	for {
		if ctx.Err() != nil {
			s.cancelWorkspaceAgents(workspace.ID)
			return
		}

		started := s.startEligibleKanbanAgents(ctx, workspace, settings, concurrency, done)
		active := s.activeKanbanAgentCount(workspace.ID)
		if active == 0 {
			blocked := s.blockUnstartableReadyCards(workspace.ID)
			if !started && !blocked && !s.workspaceHasReadyCards(workspace.ID) {
				s.emitKanbanSnapshot(workspace.ID, "scheduler_complete")
				return
			}
			if !started && !blocked {
				s.emitKanbanSnapshot(workspace.ID, "scheduler_complete")
				return
			}
			continue
		}

		select {
		case <-ctx.Done():
			s.cancelWorkspaceAgents(workspace.ID)
			return
		case <-done:
		}
	}
}

func (s *SystemService) startEligibleKanbanAgents(ctx context.Context, workspace Workspace, settings llm.Settings, concurrency int, done chan<- kanbanAgentResult) bool {
	capacity := concurrency - s.activeKanbanAgentCount(workspace.ID)
	if capacity <= 0 {
		return false
	}

	candidates := s.eligibleReadyCards(workspace.ID, capacity)
	started := false
	for _, card := range candidates {
		if ctx.Err() != nil {
			return started
		}
		if s.startKanbanAgent(ctx, workspace, settings, card.ID, done) {
			started = true
		}
	}
	return started
}

func (s *SystemService) startKanbanAgent(parent context.Context, workspace Workspace, settings llm.Settings, cardID string, done chan<- kanbanAgentResult) bool {
	agentContext, cancel := context.WithCancel(parent)

	s.chatMu.Lock()
	key := kanbanAgentKey(workspace.ID, cardID)
	if _, running := s.kanbanAgents[key]; running {
		s.chatMu.Unlock()
		cancel()
		return false
	}
	s.kanbanAgentSeq++
	agentID := s.kanbanAgentSeq
	s.kanbanAgents[key] = &kanbanAgentRun{id: agentID, cancel: cancel}

	s.mu.Lock()
	if err := s.moveKanbanCardLocked(workspace.ID, cardID, KanbanLaneInProgress, KanbanProgressEntry{
		Type:    "status",
		Title:   "Agent started",
		Content: "Card picked up by an AI agent.",
		Status:  KanbanLaneInProgress,
	}); err != nil {
		s.mu.Unlock()
		delete(s.kanbanAgents, key)
		s.chatMu.Unlock()
		cancel()
		return false
	}
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		delete(s.kanbanAgents, key)
		s.chatMu.Unlock()
		cancel()
		return false
	}
	board := boardForWorkspace(workspace.ID, s.state.KanbanCards)
	s.mu.Unlock()
	s.chatMu.Unlock()
	s.emitKanbanEvent(KanbanEvent{WorkspaceID: workspace.ID, CardID: cardID, Type: "card_started", Board: board})

	go func() {
		defer func() {
			s.forgetKanbanAgent(workspace.ID, cardID, agentID)
			select {
			case done <- kanbanAgentResult{cardID: cardID}:
			default:
			}
		}()
		s.runKanbanAgent(agentContext, workspace, settings, cardID, agentID)
	}()
	return true
}

func (s *SystemService) runKanbanAgent(ctx context.Context, workspace Workspace, settings llm.Settings, cardID string, agentID uint64) {
	card, ok := s.cardSnapshot(workspace.ID, cardID)
	if !ok {
		return
	}

	client, err := llm.NewClient(settings)
	if err != nil {
		s.blockKanbanCard(workspace.ID, cardID, agentID, "Agent error", err.Error())
		return
	}

	messages := []llm.Message{
		kanbanAgentSystemMessage(workspace),
		kanbanAgentUserMessage(card),
	}
	for {
		if err := ctx.Err(); err != nil {
			s.blockKanbanCard(workspace.ID, cardID, agentID, "Canceled", agentCancellationText)
			return
		}

		request, err := llm.NewChatRequest(settings, messages, llm.WithTools(tools.LLMSchema()), llm.WithToolChoice("auto"))
		if err != nil {
			s.blockKanbanCard(workspace.ID, cardID, agentID, "Agent error", err.Error())
			return
		}

		content, _, toolCalls, finished, finishReason, err := s.streamKanbanAgentResponse(ctx, client, request, workspace.ID, cardID, agentID)
		if err != nil {
			if ctx.Err() != nil {
				s.blockKanbanCard(workspace.ID, cardID, agentID, "Canceled", agentCancellationText)
				return
			}
			s.blockKanbanCard(workspace.ID, cardID, agentID, "Agent error", userFacingLLMError(err))
			return
		}
		toolCalls = s.normalizeToolCalls(toolCalls)
		if !finished {
			s.blockKanbanCard(workspace.ID, cardID, agentID, "Canceled", agentCancellationText)
			return
		}
		if err := finishReasonError(finishReason, len(toolCalls) > 0); err != nil {
			s.blockKanbanCard(workspace.ID, cardID, agentID, "Agent stopped early", err.Error())
			return
		}

		assistantMessage := llm.Message{Role: llm.RoleAssistant, Content: content, ToolCalls: toolCalls}
		messages = append(messages, assistantMessage)
		if len(toolCalls) == 0 {
			s.finishKanbanCard(workspace.ID, cardID, agentID, content)
			return
		}

		for _, call := range toolCalls {
			if err := ctx.Err(); err != nil {
				s.blockKanbanCard(workspace.ID, cardID, agentID, "Canceled", agentCancellationText)
				return
			}
			messages = append(messages, s.executeKanbanToolCall(ctx, workspace, settings, cardID, agentID, call))
		}
	}
}

func (s *SystemService) streamKanbanAgentResponse(ctx context.Context, client *llm.Client, request llm.ChatRequest, workspaceID string, cardID string, agentID uint64) (string, string, []llm.ToolCall, bool, string, error) {
	request.Messages = append([]llm.Message(nil), request.Messages...)
	totalContent := strings.Builder{}
	totalReasoning := strings.Builder{}
	var lastLoop streamLoopDetection

	for attempt := 0; ; attempt++ {
		result := s.streamKanbanAgentResponseAttempt(ctx, client, request, workspaceID, cardID, agentID)
		totalContent.WriteString(result.content)
		totalReasoning.WriteString(result.reasoning)
		if result.loop != nil {
			lastLoop = *result.loop
			if attempt >= maxStreamLoopRetries {
				return totalContent.String(), totalReasoning.String(), result.toolCalls, false, result.finishReason, streamLoopExceededError(lastLoop)
			}
			s.appendKanbanAgentProgress(workspaceID, cardID, agentID, KanbanProgressEntry{
				Type:    "status",
				Title:   "Agent retrying",
				Content: fmt.Sprintf("Detected repeated %s while streaming; retrying from the latest useful point.", streamLoopTarget(lastLoop)),
			})
			request.Messages = appendStreamLoopRetryMessages(request.Messages, result.content, lastLoop)
			continue
		}
		return totalContent.String(), totalReasoning.String(), result.toolCalls, result.finished, result.finishReason, result.err
	}
}

type kanbanStreamAttemptResult struct {
	content      string
	reasoning    string
	toolCalls    []llm.ToolCall
	finished     bool
	finishReason string
	loop         *streamLoopDetection
	err          error
}

func (s *SystemService) streamKanbanAgentResponseAttempt(ctx context.Context, client *llm.Client, request llm.ChatRequest, workspaceID string, cardID string, agentID uint64) kanbanStreamAttemptResult {
	stream := client.StreamChat(ctx, request)
	content := strings.Builder{}
	reasoning := strings.Builder{}
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
		}
	}
	appendContent := func(text string) *streamLoopDetection {
		if text == "" {
			return nil
		}
		content.WriteString(text)
		s.appendKanbanAgentProgress(workspaceID, cardID, agentID, KanbanProgressEntry{
			Type:    "message",
			Title:   "Agent message",
			Content: text,
		})
		if detection, ok := loopDetector.observe(streamLoopContent, text); ok {
			return &detection
		}
		return nil
	}
	appendReasoning := func(text string) *streamLoopDetection {
		if text == "" {
			return nil
		}
		reasoning.WriteString(text)
		s.appendKanbanAgentProgress(workspaceID, cardID, agentID, KanbanProgressEntry{
			Type:    "thinking",
			Title:   "Thinking",
			Content: text,
		})
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
				return kanbanStreamAttemptResult{content: content.String(), reasoning: reasoning.String(), toolCalls: orderedToolCalls(toolCalls), finished: false, finishReason: finishReason, loop: detection}
			}
		case llm.EventReasoning:
			parsed := reasoningInlineParser.Consume(event.Content)
			recordInlineToolCalls(parsed.ToolCalls)
			if detection := appendReasoning(parsed.Text); detection != nil {
				stream.Cancel()
				return kanbanStreamAttemptResult{content: content.String(), reasoning: reasoning.String(), toolCalls: orderedToolCalls(toolCalls), finished: false, finishReason: finishReason, loop: detection}
			}
		case llm.EventToolCall:
			if event.ToolCall != nil {
				call := mergeToolDelta(toolCalls[event.ToolCall.Index], *event.ToolCall)
				toolCalls[event.ToolCall.Index] = call
			}
		case llm.EventComplete:
			finished = true
			finishReason = event.FinishReason
		case llm.EventCanceled:
			return kanbanStreamAttemptResult{content: content.String(), reasoning: reasoning.String(), toolCalls: orderedToolCalls(toolCalls), finished: false, finishReason: finishReason}
		case llm.EventError:
			return kanbanStreamAttemptResult{content: content.String(), reasoning: reasoning.String(), toolCalls: orderedToolCalls(toolCalls), finished: false, finishReason: finishReason, err: errors.New(event.Error)}
		}
	}

	if ctx.Err() != nil {
		return kanbanStreamAttemptResult{content: content.String(), reasoning: reasoning.String(), toolCalls: orderedToolCalls(toolCalls), finished: false, finishReason: finishReason}
	}
	flushInlineParsers()
	return kanbanStreamAttemptResult{content: content.String(), reasoning: reasoning.String(), toolCalls: orderedToolCalls(toolCalls), finished: finished, finishReason: finishReason}
}

func (s *SystemService) executeKanbanToolCall(ctx context.Context, workspace Workspace, settings llm.Settings, cardID string, agentID uint64, call llm.ToolCall) llm.Message {
	if call.ID == "" {
		call.ID = s.nextChatID("call")
	}
	s.appendKanbanAgentProgress(workspace.ID, cardID, agentID, KanbanProgressEntry{
		Type:    "tool_call",
		Title:   "Tool call: " + call.Function.Name,
		Content: strings.TrimSpace(call.Function.Arguments),
	})

	cardTitle := ""
	if card, ok := s.cardSnapshot(workspace.ID, cardID); ok {
		cardTitle = card.Title
	}
	execution := s.executeTrackedToolCall(ctx, workspace, settings, call, WorkspaceChangeSource{
		Type:      "kanban",
		CardID:    cardID,
		CardTitle: cardTitle,
	}, func(event tools.Event) {
		if event.Message != "" {
			s.appendKanbanAgentProgress(workspace.ID, cardID, agentID, KanbanProgressEntry{
				Type:    "tool_event",
				Title:   "Tool event: " + event.Type,
				Content: event.Message,
			})
		}
	})
	result := execution.Result

	data, err := json.Marshal(result)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"tool":%q,"success":false,"error":{"code":"marshal_error","message":%q}}`, call.Function.Name, err.Error()))
	}
	title := "Tool result: " + call.Function.Name
	status := ""
	if !result.Success {
		status = KanbanLaneBlocked
	}
	s.appendKanbanAgentProgress(workspace.ID, cardID, agentID, KanbanProgressEntry{
		Type:    "tool_result",
		Title:   title,
		Content: string(data),
		Status:  status,
	})

	return llm.Message{
		Role:       llm.RoleTool,
		ToolCallID: call.ID,
		Content:    string(data),
	}
}

func (s *SystemService) eligibleReadyCards(workspaceID string, limit int) []KanbanCard {
	s.mu.Lock()
	defer s.mu.Unlock()

	cards := make([]KanbanCard, 0, limit)
	for _, card := range s.state.KanbanCards {
		if card.WorkspaceID != workspaceID || normalizeKanbanLane(card.Lane) != KanbanLaneReady {
			continue
		}
		if len(blockedDependenciesForCard(card, s.state.KanbanCards)) == 0 {
			cards = append(cards, cloneKanbanCard(card))
			if len(cards) == limit {
				break
			}
		}
	}
	return cards
}

func (s *SystemService) activeKanbanAgentCount(workspaceID string) int {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	count := 0
	for key := range s.kanbanAgents {
		cardWorkspaceID, _ := splitKanbanAgentKey(key)
		if cardWorkspaceID == workspaceID {
			count++
		}
	}
	return count
}

func (s *SystemService) workspaceHasReadyCards(workspaceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, card := range s.state.KanbanCards {
		if card.WorkspaceID == workspaceID && normalizeKanbanLane(card.Lane) == KanbanLaneReady {
			return true
		}
	}
	return false
}

func (s *SystemService) blockUnstartableReadyCards(workspaceID string) bool {
	s.mu.Lock()

	changed := false
	for i := range s.state.KanbanCards {
		card := &s.state.KanbanCards[i]
		if card.WorkspaceID != workspaceID || normalizeKanbanLane(card.Lane) != KanbanLaneReady {
			continue
		}
		blockedBy := blockedDependenciesForCard(*card, s.state.KanbanCards)
		if len(blockedBy) == 0 {
			continue
		}
		sort.Strings(blockedBy)
		card.Lane = KanbanLaneBlocked
		card.Status = KanbanLaneBlocked
		card.ProgressTranscript = append(card.ProgressTranscript, KanbanProgressEntry{
			Type:    "error",
			Title:   "Dependencies blocked",
			Content: fmt.Sprintf("Could not start because dependencies are not Done: %s.", strings.Join(blockedBy, ", ")),
			Status:  KanbanLaneBlocked,
		})
		changed = true
	}
	if !changed {
		s.mu.Unlock()
		return false
	}
	_ = s.saveLocked()
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()
	s.emitKanbanEvent(KanbanEvent{WorkspaceID: workspaceID, Type: "card_updated", Board: board})
	return true
}

func (s *SystemService) cardSnapshot(workspaceID string, cardID string) (KanbanCard, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, card := range s.state.KanbanCards {
		if card.WorkspaceID == workspaceID && card.ID == cardID {
			return cloneKanbanCard(card), true
		}
	}
	return KanbanCard{}, false
}

func (s *SystemService) appendKanbanProgress(workspaceID string, cardID string, entry KanbanProgressEntry) {
	s.mu.Lock()
	if s.appendKanbanProgressLocked(workspaceID, cardID, entry) {
		_ = s.saveLocked()
		board := boardForWorkspace(workspaceID, s.state.KanbanCards)
		s.mu.Unlock()
		s.emitKanbanProgressEvent(KanbanEvent{WorkspaceID: workspaceID, CardID: cardID, Type: "card_progress", Board: board, Entry: &entry})
		return
	}
	s.mu.Unlock()
}

func (s *SystemService) appendKanbanAgentProgress(workspaceID string, cardID string, agentID uint64, entry KanbanProgressEntry) {
	s.chatMu.Lock()
	if !s.isActiveKanbanAgentLocked(workspaceID, cardID, agentID) {
		s.chatMu.Unlock()
		return
	}
	s.mu.Lock()
	if s.appendKanbanProgressLocked(workspaceID, cardID, entry) {
		_ = s.saveLocked()
		board := boardForWorkspace(workspaceID, s.state.KanbanCards)
		s.mu.Unlock()
		s.chatMu.Unlock()
		s.emitKanbanProgressEvent(KanbanEvent{WorkspaceID: workspaceID, CardID: cardID, Type: "card_progress", Board: board, Entry: &entry})
		return
	}
	s.mu.Unlock()
	s.chatMu.Unlock()
}

func (s *SystemService) finishKanbanCard(workspaceID string, cardID string, agentID uint64, finalResult string) {
	content := strings.TrimSpace(finalResult)
	if content == "" {
		content = "Agent completed the card."
	}
	s.chatMu.Lock()
	if !s.isActiveKanbanAgentLocked(workspaceID, cardID, agentID) {
		s.chatMu.Unlock()
		return
	}
	s.mu.Lock()
	if !s.kanbanCardInLaneLocked(workspaceID, cardID, KanbanLaneInProgress) {
		s.mu.Unlock()
		s.chatMu.Unlock()
		return
	}
	if err := s.moveKanbanCardLocked(workspaceID, cardID, KanbanLaneDone, KanbanProgressEntry{
		Type:    "result",
		Title:   "Final result",
		Content: content,
		Status:  KanbanLaneDone,
	}); err != nil {
		s.mu.Unlock()
		s.chatMu.Unlock()
		return
	}
	_ = s.saveLocked()
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()
	s.chatMu.Unlock()
	s.emitKanbanEvent(KanbanEvent{WorkspaceID: workspaceID, CardID: cardID, Type: "card_done", Board: board})
}

func (s *SystemService) blockKanbanCard(workspaceID string, cardID string, agentID uint64, title string, reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "Agent could not complete the card."
	}
	s.chatMu.Lock()
	if !s.isActiveKanbanAgentLocked(workspaceID, cardID, agentID) {
		s.chatMu.Unlock()
		return
	}
	s.mu.Lock()
	if !s.kanbanCardInLaneLocked(workspaceID, cardID, KanbanLaneInProgress) {
		s.mu.Unlock()
		s.chatMu.Unlock()
		return
	}
	if err := s.moveKanbanCardLocked(workspaceID, cardID, KanbanLaneBlocked, KanbanProgressEntry{
		Type:    "error",
		Title:   title,
		Content: reason,
		Status:  KanbanLaneBlocked,
	}); err != nil {
		s.mu.Unlock()
		s.chatMu.Unlock()
		return
	}
	_ = s.saveLocked()
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()
	s.chatMu.Unlock()
	s.emitKanbanEvent(KanbanEvent{WorkspaceID: workspaceID, CardID: cardID, Type: "card_blocked", Board: board})
}

func (s *SystemService) moveKanbanCardLocked(workspaceID string, cardID string, lane string, entry KanbanProgressEntry) error {
	lane = normalizeKanbanLane(lane)
	if lane == "" {
		return fmt.Errorf("kanban lane is invalid")
	}
	for index := range s.state.KanbanCards {
		card := &s.state.KanbanCards[index]
		if card.WorkspaceID != workspaceID || card.ID != cardID {
			continue
		}
		if lane == KanbanLaneInProgress {
			blockedBy := blockedDependenciesForCard(*card, s.state.KanbanCards)
			if len(blockedBy) > 0 {
				return fmt.Errorf("kanban card is blocked by dependencies: %s", strings.Join(blockedBy, ", "))
			}
		}
		card.Lane = lane
		card.Status = lane
		if entry.Content != "" {
			if entry.Status == "" {
				entry.Status = lane
			}
			card.ProgressTranscript = append(card.ProgressTranscript, entry)
		}
		return nil
	}
	return fmt.Errorf("kanban card was not found")
}

func (s *SystemService) kanbanCardInLaneLocked(workspaceID string, cardID string, lane string) bool {
	lane = normalizeKanbanLane(lane)
	if lane == "" {
		return false
	}
	for _, card := range s.state.KanbanCards {
		if card.WorkspaceID == workspaceID && card.ID == cardID {
			return normalizeKanbanLane(card.Lane) == lane
		}
	}
	return false
}

func (s *SystemService) appendKanbanProgressLocked(workspaceID string, cardID string, entry KanbanProgressEntry) bool {
	if strings.TrimSpace(entry.Content) == "" {
		return false
	}
	for index := range s.state.KanbanCards {
		card := &s.state.KanbanCards[index]
		if card.WorkspaceID == workspaceID && card.ID == cardID {
			if canMergeKanbanProgress(card.ProgressTranscript, entry) {
				card.ProgressTranscript[len(card.ProgressTranscript)-1].Content += entry.Content
				return true
			}
			card.ProgressTranscript = append(card.ProgressTranscript, entry)
			return true
		}
	}
	return false
}

func canMergeKanbanProgress(transcript []KanbanProgressEntry, entry KanbanProgressEntry) bool {
	if len(transcript) == 0 || entry.Status != "" {
		return false
	}
	previous := transcript[len(transcript)-1]
	return previous.Status == "" && previous.Type == entry.Type && previous.Title == entry.Title
}

func (s *SystemService) forgetKanbanRun(workspaceID string) {
	s.chatMu.Lock()
	delete(s.kanbanRuns, workspaceID)
	s.chatMu.Unlock()
}

func (s *SystemService) forgetKanbanAgent(workspaceID string, cardID string, agentID uint64) {
	s.chatMu.Lock()
	if s.isActiveKanbanAgentLocked(workspaceID, cardID, agentID) {
		delete(s.kanbanAgents, kanbanAgentKey(workspaceID, cardID))
	}
	s.chatMu.Unlock()
}

func (s *SystemService) isActiveKanbanAgentLocked(workspaceID string, cardID string, agentID uint64) bool {
	agent := s.kanbanAgents[kanbanAgentKey(workspaceID, cardID)]
	return agent != nil && agent.id == agentID
}

func (s *SystemService) cancelWorkspaceAgents(workspaceID string) {
	s.chatMu.Lock()
	cancels := make([]context.CancelFunc, 0)
	for key, agent := range s.kanbanAgents {
		cardWorkspaceID, _ := splitKanbanAgentKey(key)
		if cardWorkspaceID == workspaceID {
			cancels = append(cancels, agent.cancel)
		}
	}
	s.chatMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *SystemService) emitKanbanSnapshot(workspaceID string, eventType string) {
	s.mu.Lock()
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()
	s.emitKanbanEvent(KanbanEvent{WorkspaceID: workspaceID, Type: eventType, Board: board})
}

func (s *SystemService) emitKanbanProgressEvent(event KanbanEvent) {
	if event.CardID == "" || !s.hasOpenKanbanCardDetail(event.WorkspaceID, event.CardID) {
		return
	}
	s.emitKanbanEvent(event)
}

func (s *SystemService) emitKanbanEvent(event KanbanEvent) {
	if s.kanbanEventSink != nil {
		s.kanbanEventSink(event)
	}
	if s.ctx != nil {
		runtime.EventsEmit(s.ctx, kanbanEventName, event)
	}
}

func (s *SystemService) hasOpenKanbanCardDetail(workspaceID string, cardID string) bool {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	return s.kanbanDetailViews[workspaceID] == cardID
}

func kanbanAgentSystemMessage(workspace Workspace) llm.Message {
	return llm.Message{
		Role: llm.RoleSystem,
		Content: workspaceSystemPrompt(
			"You are Echo's autonomous Kanban agent. Complete the assigned card inside the active workspace. "+
				"Use available tools when you need workspace facts. Keep the final message concise and include what changed and how it was verified.",
			workspace,
		),
	}
}

func kanbanAgentUserMessage(card KanbanCard) llm.Message {
	criteria := "None recorded."
	if len(card.AcceptanceCriteria) > 0 {
		criteria = "- " + strings.Join(card.AcceptanceCriteria, "\n- ")
	}
	progress := "No prior card messages or progress."
	if len(card.ProgressTranscript) > 0 {
		lines := make([]string, 0, len(card.ProgressTranscript))
		for _, entry := range card.ProgressTranscript {
			content := strings.TrimSpace(entry.Content)
			if content == "" {
				continue
			}
			title := strings.TrimSpace(entry.Title)
			if title == "" {
				title = strings.TrimSpace(entry.Type)
			}
			if title == "" {
				title = "Progress"
			}
			lines = append(lines, "- "+title+": "+content)
		}
		if len(lines) > 0 {
			progress = strings.Join(lines, "\n")
		}
	}
	return llm.Message{
		Role: llm.RoleUser,
		Content: fmt.Sprintf("Complete this Kanban card.\n\nID: %s\nTitle: %s\nDescription: %s\nAcceptance criteria:\n%s\n\nPrior card log:\n%s",
			card.ID, card.Title, card.Description, criteria, progress),
	}
}

func normalizeAgentLimit(concurrency int) int {
	if concurrency <= 0 {
		return defaultAgentLimit
	}
	if concurrency > maxAgentLimit {
		return maxAgentLimit
	}
	return concurrency
}

func kanbanAgentKey(workspaceID string, cardID string) string {
	return workspaceID + "\x00" + cardID
}

func splitKanbanAgentKey(key string) (string, string) {
	workspaceID, cardID, ok := strings.Cut(key, "\x00")
	if !ok {
		return key, ""
	}
	return workspaceID, cardID
}
