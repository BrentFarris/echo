package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

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

type kanbanDependencyOutput struct {
	ID      string
	Title   string
	Content string
}

type kanbanToolCallExecution struct {
	Messages        []llm.Message
	ChangedPaths    []string
	SkillCheckpoint bool
}

func (s *SystemService) StartKanbanExecution(workspaceID string, concurrency int) (KanbanBoard, error) {
	workspace, settings, err := s.workspaceAndSettingsFor(workspaceID, llm.InteractionKanban)
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

// StartKanbanExecutionWithContext starts kanban execution and discards the returned board.
// It implements tools.KanbanExecutor for tool calls that do not need the board snapshot.
func (s *SystemService) StartKanbanExecutionWithContext(ctx context.Context, workspaceID string, concurrency int) error {
	_, err := s.StartKanbanExecution(workspaceID, concurrency)
	return err
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
	if s.debugger != nil {
		s.debugger.shutdown()
	}
	s.chatMu.Lock()
	runCancels := make([]context.CancelFunc, 0, len(s.kanbanRuns))
	for _, cancel := range s.kanbanRuns {
		runCancels = append(runCancels, cancel)
	}
	agentCancels := make([]context.CancelFunc, 0, len(s.kanbanAgents))
	for _, agent := range s.kanbanAgents {
		agentCancels = append(agentCancels, agent.cancel)
	}
	heartbeatCancels := make([]context.CancelFunc, 0, len(s.heartbeats))
	heartbeatTickers := make([]*time.Ticker, 0, len(s.heartbeats))
	for _, h := range s.heartbeats {
		heartbeatCancels = append(heartbeatCancels, h.cancel)
		heartbeatTickers = append(heartbeatTickers, h.ticker)
	}
	watchdogCancels := make([]context.CancelFunc, 0, len(s.watchdogs))
	watchdogTickers := make([]*time.Ticker, 0, len(s.watchdogs))
	for _, w := range s.watchdogs {
		watchdogCancels = append(watchdogCancels, w.cancel)
		watchdogTickers = append(watchdogTickers, w.ticker)
	}
	chatCancels := make([]context.CancelFunc, 0, len(s.chatStreams))
	for _, cancel := range s.chatStreams {
		chatCancels = append(chatCancels, cancel)
	}
	for _, session := range s.chatSessions {
		if session == nil || !session.Busy {
			continue
		}
		session.Busy = false
		session.StreamID = ""
		for i := range session.Messages {
			if session.Messages[i].Status == "streaming" || session.Messages[i].Status == "retrying" {
				session.Messages[i].Status = "canceled"
				if session.Messages[i].Error == "" {
					session.Messages[i].Error = "Interrupted when Echo closed."
				}
			}
		}
	}
	s.chatMu.Unlock()

	for _, cancel := range runCancels {
		cancel()
	}
	for _, cancel := range agentCancels {
		cancel()
	}
	for _, cancel := range heartbeatCancels {
		cancel()
	}
	for _, ticker := range heartbeatTickers {
		ticker.Stop()
	}
	for _, cancel := range watchdogCancels {
		cancel()
	}
	for _, ticker := range watchdogTickers {
		ticker.Stop()
	}
	for _, cancel := range chatCancels {
		cancel()
	}
	_ = s.persistAllWorkspaceAutosaves()
	s.closeAllLSPClients()
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
	card, dependencyOutputs, ok := s.agentCardSnapshot(workspace.ID, cardID)
	if !ok {
		return
	}

	contextBrief := s.kanbanWorkspaceContextBrief(ctx, workspace, card, dependencyOutputs, agentID)

	client, err := llm.NewClient(settings)
	if err != nil {
		s.blockKanbanCard(workspace.ID, cardID, agentID, "Agent error", err.Error())
		return
	}

	messages := []llm.Message{
		kanbanAgentSystemMessage(workspace, workspaceSkillCandidates(ctx, workspace, kanbanWorkspaceSkillTask(card))),
		kanbanAgentUserMessage(card, dependencyOutputs, contextBrief),
	}
	currentUser := messages[1]
	toolSchema := tools.LLMSchema()
	changedPaths := map[string]bool{}
	recoverableToolCalls := make(map[string]bool)
	forcedCompactions := 0
	verificationAttempts := 0
	noToolContinuationAttempts := 0
	hasProjectChanges := false
	verificationCurrent := false
	skillCheckpointPending := false
	skillCheckpointReminders := 0
	var totalUsage llm.Usage
	defer func() {
		if totalUsage.TotalTokens > 0 {
			_, _ = s.RecordTokenUsage(workspace.ID, int64(totalUsage.TotalTokens))
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			s.blockKanbanCard(workspace.ID, cardID, agentID, "Canceled", agentCancellationText)
			return
		}

		preflightPolicy := contextCompactionPolicy{CurrentUser: currentUser}
		if contextNeedsCompaction(settings, messages, toolSchema) &&
			contextHasCompressibleStale(settings, messages, preflightPolicy) {
			s.appendKanbanAgentProgress(workspace.ID, cardID, agentID, KanbanProgressEntry{
				Type:    "status",
				Title:   "Context compaction started",
				Content: "The agent is condensing stale context while preserving the original card and recent work.",
				Status:  KanbanLaneInProgress,
			})
			compaction, compactErr := compactContextIfNeeded(ctx, client, settings, messages, toolSchema, preflightPolicy)
			if compactErr != nil {
				if ctx.Err() != nil {
					s.blockKanbanCard(workspace.ID, cardID, agentID, "Canceled", agentCancellationText)
					return
				}
				s.appendKanbanAgentProgress(workspace.ID, cardID, agentID, KanbanProgressEntry{
					Type:    "status",
					Title:   "Context compaction deferred",
					Content: compactErr.Error(),
					Status:  KanbanLaneInProgress,
				})
			} else if compaction.Compacted {
				messages = compaction.Messages
				s.appendKanbanCompactionResult(workspace.ID, cardID, agentID, compaction)
			}
		}

		request, err := llm.NewChatRequest(settings, messages, llm.WithTools(toolSchema), llm.WithToolChoice("auto"))
		if err != nil {
			s.blockKanbanCard(workspace.ID, cardID, agentID, "Agent error", err.Error())
			return
		}

		content, _, toolCalls, finished, finishReason, usage, err := s.streamKanbanAgentResponse(ctx, client, request, workspace.ID, cardID, agentID)
		if err != nil {
			if ctx.Err() != nil {
				s.blockKanbanCard(workspace.ID, cardID, agentID, "Canceled", agentCancellationText)
				return
			}
			if llm.IsContextLengthExceeded(err) {
				if recovery, ok := recoverToolResultContext(messages, recoverableToolCalls); ok {
					messages = recovery.Messages
					s.appendKanbanAgentProgress(workspace.ID, cardID, agentID, KanbanProgressEntry{
						Type:    "tool_result",
						Title:   "Tool result too large: " + recovery.Call.Function.Name,
						Content: recovery.ResultMessage.Content,
					})
					continue
				}
				if forcedCompactions >= 2 {
					s.blockKanbanCard(workspace.ID, cardID, agentID, "Agent context exhausted", "Echo could not free enough context while preserving the system message, original card, and recent agent state.")
					return
				}
				var compaction contextCompactionResult
				var compactErr error
				for forcedCompactions < 2 {
					forcedCompactions++
					s.appendKanbanAgentProgress(workspace.ID, cardID, agentID, KanbanProgressEntry{
						Type:    "status",
						Title:   "Context compaction started",
						Content: "The provider rejected the request for context length, so Echo is compacting stale agent history.",
						Status:  KanbanLaneInProgress,
					})
					compaction, compactErr = compactContextIfNeeded(ctx, client, settings, messages, toolSchema, contextCompactionPolicy{
						CurrentUser:    currentUser,
						Force:          true,
						Aggressiveness: forcedCompactions,
					})
					if compactErr == nil {
						break
					}
					if ctx.Err() != nil {
						s.blockKanbanCard(workspace.ID, cardID, agentID, "Canceled", agentCancellationText)
						return
					}
				}
				if compactErr != nil {
					s.blockKanbanCard(workspace.ID, cardID, agentID, "Agent context exhausted", "Echo could not compact the context safely: "+compactErr.Error())
					return
				}
				messages = compaction.Messages
				s.appendKanbanCompactionResult(workspace.ID, cardID, agentID, compaction)
				continue
			}
			s.blockKanbanCard(workspace.ID, cardID, agentID, "Agent error", userFacingLLMError(err))
			return
		}
		if usage != nil {
			totalUsage.PromptTokens += usage.PromptTokens
			totalUsage.CompletionTokens += usage.CompletionTokens
			totalUsage.TotalTokens += usage.TotalTokens
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
		forcedCompactions = 0

		if len(toolCalls) == 0 && strings.TrimSpace(content) == "" {
			continue
		}

		assistantMessage := llm.Message{Role: llm.RoleAssistant, Content: content, ToolCalls: toolCalls}
		messages = append(messages, assistantMessage)
		if len(toolCalls) == 0 {
			if shouldContinueKanbanNoToolTurn(content, noToolContinuationAttempts) {
				noToolContinuationAttempts++
				s.appendKanbanAgentProgress(workspace.ID, cardID, agentID, KanbanProgressEntry{
					Type:    "status",
					Title:   "Agent continuing",
					Content: "The agent described its next step without calling a tool, so Echo asked it to continue with a real tool call or a final completion summary.",
					Status:  KanbanLaneInProgress,
				})
				messages = append(messages, llm.Message{
					Role:    llm.RoleUser,
					Content: kanbanNoToolContinuationPrompt(),
				})
				continue
			}
			if !verificationCurrent {
				s.appendKanbanAgentProgress(workspace.ID, cardID, agentID, KanbanProgressEntry{
					Type:    "verification",
					Title:   "Verification started",
					Content: "Checking changed files before marking the card Done.",
					Status:  KanbanLaneInProgress,
				})
				verificationAttempts++
				report, err := s.runKanbanVerification(ctx, workspace, sortedKanbanChangedPaths(changedPaths))
				if err != nil {
					if ctx.Err() != nil {
						s.blockKanbanCard(workspace.ID, cardID, agentID, "Canceled", agentCancellationText)
						return
					}
					s.blockKanbanCard(workspace.ID, cardID, agentID, "Verification error", err.Error())
					return
				}
				reportText := kanbanVerificationReportText(report)
				s.appendKanbanAgentProgress(workspace.ID, cardID, agentID, KanbanProgressEntry{
					Type:    "verification",
					Title:   kanbanVerificationProgressTitle(report, verificationAttempts),
					Content: reportText,
				})
				if !kanbanVerificationReportSucceeded(report) {
					if verificationAttempts < 2 {
						messages = append(messages, llm.Message{
							Role:    llm.RoleUser,
							Content: kanbanVerificationRepairPrompt(report),
						})
						continue
					}
					s.blockKanbanCard(workspace.ID, cardID, agentID, "Verification failed", reportText)
					return
				}
				verificationCurrent = true
				if hasProjectChanges {
					skillCheckpointPending = true
				}
			}
			if skillCheckpointPending {
				if skillCheckpointReminders < workspaceSkillMaxReminders {
					skillCheckpointReminders++
					s.appendKanbanAgentProgress(workspace.ID, cardID, agentID, KanbanProgressEntry{
						Type:    "status",
						Title:   "Skill learning checkpoint",
						Content: "Verification passed. Waiting for the agent to save durable workspace knowledge or explicitly skip.",
						Status:  KanbanLaneInProgress,
					})
					messages = append(messages, llm.Message{
						Role:    llm.RoleUser,
						Content: workspaceSkillCheckpointPrompt(true),
					})
					continue
				}
				s.appendKanbanAgentProgress(workspace.ID, cardID, agentID, KanbanProgressEntry{
					Type:    "status",
					Title:   "Skill learning checkpoint skipped",
					Content: workspaceSkillCheckpointWarning(),
				})
				skillCheckpointPending = false
			}
			s.finishKanbanCard(workspace.ID, cardID, agentID, content)
			return
		}
		noToolContinuationAttempts = 0

		for _, call := range toolCalls {
			if err := ctx.Err(); err != nil {
				s.blockKanbanCard(workspace.ID, cardID, agentID, "Canceled", agentCancellationText)
				return
			}
			execution := s.executeKanbanToolCall(ctx, workspace, settings, cardID, agentID, call)
			recoverableToolCalls[call.ID] = true
			messages = append(messages, execution.Messages...)
			for _, path := range execution.ChangedPaths {
				changedPaths[path] = true
			}
			if len(execution.ChangedPaths) > 0 {
				hasProjectChanges = true
				verificationCurrent = false
				skillCheckpointPending = true
				skillCheckpointReminders = 0
			}
			if execution.SkillCheckpoint && verificationCurrent {
				skillCheckpointPending = false
			}
		}
	}
}

func (s *SystemService) streamKanbanAgentResponse(ctx context.Context, client *llm.Client, request llm.ChatRequest, workspaceID string, cardID string, agentID uint64) (string, string, []llm.ToolCall, bool, string, *llm.Usage, error) {
	request.Messages = append([]llm.Message(nil), request.Messages...)
	totalContent := strings.Builder{}
	totalReasoning := strings.Builder{}
	var totalUsage llm.Usage
	var lastLoop streamLoopDetection

	for attempt := 0; ; attempt++ {
		result := s.streamKanbanAgentResponseAttempt(ctx, client, request, workspaceID, cardID, agentID)
		totalContent.WriteString(result.content)
		totalReasoning.WriteString(result.reasoning)
		if result.usage != nil {
			totalUsage.PromptTokens += result.usage.PromptTokens
			totalUsage.CompletionTokens += result.usage.CompletionTokens
			totalUsage.TotalTokens += result.usage.TotalTokens
		}
		if result.loop != nil {
			lastLoop = *result.loop
			if attempt >= maxStreamLoopRetries {
				return totalContent.String(), totalReasoning.String(), result.toolCalls, false, result.finishReason, &totalUsage, streamLoopExceededError(lastLoop)
			}
			s.appendKanbanAgentProgress(workspaceID, cardID, agentID, KanbanProgressEntry{
				Type:    "status",
				Title:   "Agent retrying",
				Content: fmt.Sprintf("Detected repeated %s while streaming; retrying from the latest useful point.", streamLoopTarget(lastLoop)),
			})
			request.Messages = appendStreamLoopRetryMessages(request.Messages, result.content, lastLoop)
			continue
		}
		return totalContent.String(), totalReasoning.String(), result.toolCalls, result.finished, result.finishReason, &totalUsage, result.err
	}
}

type kanbanStreamAttemptResult struct {
	content      string
	reasoning    string
	toolCalls    []llm.ToolCall
	finished     bool
	finishReason string
	usage        *llm.Usage
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
	return kanbanStreamAttemptResult{content: content.String(), reasoning: reasoning.String(), toolCalls: orderedToolCalls(toolCalls), finished: finished, finishReason: finishReason, usage: stream.Usage}
}

func (s *SystemService) executeKanbanToolCall(ctx context.Context, workspace Workspace, settings llm.Settings, cardID string, agentID uint64, call llm.ToolCall) kanbanToolCallExecution {
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
	}, nil)
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

	return kanbanToolCallExecution{
		Messages:        toolResultMessages(call, result, data),
		ChangedPaths:    affectedPathsFromChanges(execution.Changes),
		SkillCheckpoint: workspaceSkillCheckpointCompleted(call, result),
	}
}

func sortedKanbanChangedPaths(paths map[string]bool) []string {
	output := make([]string, 0, len(paths))
	for path := range paths {
		output = append(output, path)
	}
	return normalizedChangedPaths(output)
}

func (s *SystemService) eligibleReadyCards(workspaceID string, limit int) []KanbanCard {
	s.mu.Lock()
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()
	return FindEligibleCards(board, limit)
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

func (s *SystemService) agentCardSnapshot(workspaceID string, cardID string) (KanbanCard, []kanbanDependencyOutput, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cardsByID := kanbanCardsByID(s.state.KanbanCards)
	card, ok := cardsByID[cardID]
	if !ok || card.WorkspaceID != workspaceID {
		return KanbanCard{}, nil, false
	}

	outputs := make([]kanbanDependencyOutput, 0, len(card.Dependencies))
	for _, dependencyID := range card.Dependencies {
		dependency, ok := cardsByID[dependencyID]
		if !ok || dependency.WorkspaceID != workspaceID {
			continue
		}
		content := kanbanDependencyResultContent(dependency.ProgressTranscript)
		if content == "" {
			continue
		}
		title := strings.TrimSpace(dependency.Title)
		if title == "" {
			title = dependency.ID
		}
		outputs = append(outputs, kanbanDependencyOutput{
			ID:      dependency.ID,
			Title:   title,
			Content: content,
		})
	}

	return cloneKanbanCard(card), outputs, true
}

func (s *SystemService) appendKanbanProgress(workspaceID string, cardID string, entry KanbanProgressEntry) {
	s.mu.Lock()
	if s.appendKanbanProgressLocked(workspaceID, cardID, entry) {
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
		board := boardForWorkspace(workspaceID, s.state.KanbanCards)
		s.mu.Unlock()
		s.chatMu.Unlock()
		s.emitKanbanProgressEvent(KanbanEvent{WorkspaceID: workspaceID, CardID: cardID, Type: "card_progress", Board: board, Entry: &entry})
		return
	}
	s.mu.Unlock()
	s.chatMu.Unlock()
}

func (s *SystemService) appendKanbanCompactionResult(workspaceID string, cardID string, agentID uint64, result contextCompactionResult) {
	content := fmt.Sprintf(
		"Estimated context reduced from %d to %d tokens by replacing %d stale messages.",
		result.BeforeTokens,
		result.AfterTokens,
		result.RemovedMessages,
	)
	if result.UsedFallback && result.Warning != "" {
		content += " " + result.Warning
	}
	s.appendKanbanAgentProgress(workspaceID, cardID, agentID, KanbanProgressEntry{
		Type:    "status",
		Title:   "Context compressed",
		Content: content,
		Status:  KanbanLaneInProgress,
	})
}

func (s *SystemService) finishKanbanCard(workspaceID string, cardID string, agentID uint64, finalResult string) {
	content := strings.TrimSpace(finalResult)
	if content == "" {
		content = "Agent completed the card."
	}
	s.autosaveMu.Lock()
	s.chatMu.Lock()
	if !s.isActiveKanbanAgentLocked(workspaceID, cardID, agentID) {
		s.chatMu.Unlock()
		s.autosaveMu.Unlock()
		return
	}
	s.mu.Lock()
	if !s.kanbanCardInLaneLocked(workspaceID, cardID, KanbanLaneInProgress) {
		s.mu.Unlock()
		s.chatMu.Unlock()
		s.autosaveMu.Unlock()
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
		s.autosaveMu.Unlock()
		return
	}
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	if kanbanBoardComplete(board) {
		var workspace Workspace
		for _, candidate := range s.state.Workspaces {
			if candidate.ID == workspaceID {
				workspace = candidate
				break
			}
		}
		var chat *persistedChatSession
		if session := s.chatSessions[workspaceID]; session != nil && (len(session.Messages) > 0 || len(session.History) > 0) {
			snapshot := persistedChatSessionFrom(session)
			chat = &snapshot
		}
		cards := make([]KanbanCard, 0, len(board.Done))
		for _, card := range s.state.KanbanCards {
			if card.WorkspaceID == workspaceID {
				cards = append(cards, cloneKanbanCard(card))
			}
		}
		_ = writeWorkspaceAutosave(workspace, workspaceAutosave{
			Version:     workspaceAutosaveVersion,
			ChatSession: chat,
			KanbanCards: cards,
		})
	}
	s.mu.Unlock()
	s.chatMu.Unlock()
	s.autosaveMu.Unlock()
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
			entry.Timestamp = time.Now()
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
			entry.Timestamp = time.Now()
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
	if event.CardID == "" {
		return
	}
	s.emitKanbanEvent(event)
}

func (s *SystemService) emitKanbanEvent(event KanbanEvent) {
	s.emitRuntimeEvent(kanbanEventName, event)
	s.emitKanbanEventToWails(event)
}

func (s *SystemService) emitKanbanEventToWails(event KanbanEvent) {
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

func kanbanAgentSystemMessage(workspace Workspace, skillCandidates []tools.WorkspaceSkillSummary) llm.Message {
	return llm.Message{
		Role: llm.RoleSystem,
		Content: workspaceSystemPrompt(
			workspaceSkillsPrompt("You are Echo's autonomous Kanban agent. Complete the assigned card inside the active workspace. "+
				contextCheckpointSystemGuidance+" "+
				"Treat the provided Workspace Context Brief as your starting map, but validate important facts with targeted tools before editing. "+
				"Use workspace_context for broad repo context when the brief is missing or the target files remain unclear. "+
				"Use git_inspect when commit history, regressions, legacy behavior, ownership, or prior rationale would materially clarify the card; avoid routine history searches when the current code is sufficient. "+
				"Use available tools when you need workspace facts. Invoke tools through the tool-call API; do not print a function name or JSON arguments in the card transcript. "+
				"If you need to inspect or modify files, call the tool immediately instead of saying you will. "+
				"When you need to find code but do not know the target file, prefer filesystem_search_workspace before shell commands. "+
				"When locating symbols, strings, or code blocks in a known file, prefer filesystem_search_text before reading the whole file. "+
				"When a search result gives a useful line number, read nearby code with filesystem_read_text aroundLine; copy the result's line value and avoid reading whole source files unless the entire file is genuinely needed. "+
				"Use lsp_query for definitions, references, hover info, document symbols, and member/completion candidates once you know the file and cursor position. "+
				"Echo automatically runs detected verification commands before marking the card Done; if verification fails, repair the issue using the report. "+
				"When project files changed, first provide the completion summary to trigger verification; do not call workspace_skill_record until Echo reports that verification passed and requests the learning checkpoint. "+
				"Write the final message as a concise handoff summary for dependent cards, including what was done, important files or decisions, and how it was verified.",
				skillCandidates,
				true,
			),
			workspace,
		),
	}
}

func kanbanWorkspaceSkillTask(card KanbanCard) string {
	parts := []string{card.Title, card.Description}
	parts = append(parts, card.AcceptanceCriteria...)
	return strings.Join(parts, "\n")
}

func kanbanAgentUserMessage(card KanbanCard, dependencyOutputs []kanbanDependencyOutput, contextBrief string) llm.Message {
	criteria := "None recorded."
	if len(card.AcceptanceCriteria) > 0 {
		criteria = "- " + strings.Join(card.AcceptanceCriteria, "\n- ")
	}
	dependencies := kanbanDependencyOutputsPrompt(card, dependencyOutputs)
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
	description := card.Description
	if dir := strings.TrimSpace(card.Direction); dir != "" {
		description += "\n\nAdditional direction:\n" + dir
	}
	return llm.Message{
		Role: llm.RoleUser,
		Content: fmt.Sprintf("Complete this Kanban card.\n\nID: %s\nTitle: %s\nDescription: %s\nAcceptance criteria:\n%s\n\nCompleted dependency outputs:\n%s\n\nPrior card log:\n%s\n\nWorkspace context brief:\n%s",
			card.ID, card.Title, description, criteria, dependencies, progress, strings.TrimSpace(contextBrief)),
	}
}

func (s *SystemService) kanbanWorkspaceContextBrief(ctx context.Context, workspace Workspace, card KanbanCard, dependencyOutputs []kanbanDependencyOutput, agentID uint64) string {
	task := kanbanWorkspaceContextTask(card, dependencyOutputs)
	response, err := s.buildWorkspaceContext(ctx, workspace, tools.WorkspaceContextRequest{
		Task:     task,
		MaxFiles: tools.DefaultWorkspaceContextMaxFiles,
	})
	if err != nil {
		content := "Context brief unavailable: " + err.Error()
		s.appendKanbanAgentProgress(workspace.ID, card.ID, agentID, KanbanProgressEntry{
			Type:    "context",
			Title:   "Context brief warning",
			Content: content,
			Status:  KanbanLaneInProgress,
		})
		return content
	}
	brief := strings.TrimSpace(response.Brief)
	if brief == "" {
		brief = "No relevant workspace context was detected automatically."
	}
	s.appendKanbanAgentProgress(workspace.ID, card.ID, agentID, KanbanProgressEntry{
		Type:    "context",
		Title:   "Context brief",
		Content: brief,
		Status:  KanbanLaneInProgress,
	})
	return brief
}

func kanbanWorkspaceContextTask(card KanbanCard, dependencyOutputs []kanbanDependencyOutput) string {
	var builder strings.Builder
	builder.WriteString(card.Title)
	if strings.TrimSpace(card.Description) != "" {
		builder.WriteString("\n")
		builder.WriteString(card.Description)
	}
	if len(card.AcceptanceCriteria) > 0 {
		builder.WriteString("\nAcceptance criteria:\n- ")
		builder.WriteString(strings.Join(card.AcceptanceCriteria, "\n- "))
	}
	for _, output := range dependencyOutputs {
		if strings.TrimSpace(output.Content) == "" {
			continue
		}
		builder.WriteString("\nDependency ")
		builder.WriteString(output.ID)
		builder.WriteString(" ")
		builder.WriteString(output.Title)
		builder.WriteString(":\n")
		builder.WriteString(output.Content)
	}
	return builder.String()
}

func kanbanDependencyOutputsPrompt(card KanbanCard, outputs []kanbanDependencyOutput) string {
	if len(card.Dependencies) == 0 {
		return "No dependencies."
	}
	if len(outputs) == 0 {
		return "No completed dependency outputs were recorded."
	}

	lines := make([]string, 0, len(outputs)*3)
	for _, output := range outputs {
		title := strings.TrimSpace(output.Title)
		if title == "" {
			title = output.ID
		}
		lines = append(lines, fmt.Sprintf("- %s (%s):", output.ID, title))
		lines = append(lines, indentKanbanPromptBlock(output.Content))
	}
	return strings.Join(lines, "\n")
}

func kanbanDependencyResultContent(transcript []KanbanProgressEntry) string {
	for i := len(transcript) - 1; i >= 0; i-- {
		entry := transcript[i]
		if entry.Type != "result" && !strings.EqualFold(strings.TrimSpace(entry.Title), "Final result") {
			continue
		}
		if content := strings.TrimSpace(entry.Content); content != "" {
			return content
		}
	}
	for i := len(transcript) - 1; i >= 0; i-- {
		if content := strings.TrimSpace(transcript[i].Content); content != "" {
			return content
		}
	}
	return ""
}

func indentKanbanPromptBlock(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	for i := range lines {
		lines[i] = "  " + strings.TrimRight(lines[i], " \t")
	}
	return strings.Join(lines, "\n")
}

func shouldContinueKanbanNoToolTurn(content string, attempts int) bool {
	if attempts >= 2 {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(content))
	if normalized == "" {
		return true
	}
	preparatoryPrefixes := []string{
		"let me ",
		"i'll ",
		"i’ll ",
		"i will ",
		"i'm going to ",
		"i’m going to ",
		"i am going to ",
		"i need to ",
		"first, ",
		"first ",
	}
	for _, prefix := range preparatoryPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	for _, phrase := range []string{
		" start by ",
		" start with ",
		" going to inspect ",
		" going to read ",
		" need to inspect ",
		" need to read ",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

func kanbanNoToolContinuationPrompt() string {
	return "Continue the card. Your previous response described the next step but did not call a tool or finish the card. If you need workspace facts or file changes, invoke the appropriate tool through the tool-call API now. Do not print tool names or JSON arguments as normal text. If the card is already complete without tool use, reply with a concise final handoff summary."
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
