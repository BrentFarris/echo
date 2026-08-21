package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/brent/echo/internal/agentmodes"
	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
)

const (
	maxResearchAgentFollowUps = 6
	maxResearchAgentRounds    = 32
	maxResearchReasoningBytes = 128 * 1024
	researchToolArgBytes      = 2 * 1024
	researchToolResultBytes   = 4 * 1024
)

const researchReasoningTruncatedMarker = "[Earlier agent thinking truncated]\n\n"

const researchAgentSystemPrompt = `You are a focused, read-only research agent working for a parent chat orchestrator.
Investigate only the assigned question. Use available inspection and research tools to establish concrete facts.
Do not edit files, run shell commands, mutate external systems, create agent modes, or spawn other agents.
Keep raw tool output in this private research thread. Your final response is a concise handoff to the parent model.
Include findings, evidence with exact URLs or workspace paths and line numbers when available, uncertainties, conflicts, and useful follow-up questions.
Do not claim that a source was checked unless you actually inspected it.`

const researchOrchestratorGuidance = `You can delegate independent read-only investigations to research agents. Use research_agents_spawn for focused, non-overlapping branches, research_agents_wait to collect every report needed for synthesis, research_agent_send for a focused follow-up, and research_agents_cancel for work that is no longer needed. Child transcripts remain private; rely on their bounded reports and do not finalize while required agents are still running or have uncollected reports.`

type chatResearchRun struct {
	session        *chatSession
	ctx            context.Context
	cancel         context.CancelFunc
	turnID         string
	settings       llm.Settings
	parentSettings llm.Settings
	streamer       chatStreamer
	toolScopes     *tools.ToolScopeChecker
	semaphore      chan struct{}

	mu      sync.Mutex
	agents  map[string]*chatResearchAgentRun
	order   []string
	updates chan struct{}
	closed  bool
	wg      sync.WaitGroup
}

type chatResearchAgentRun struct {
	id                string
	name              string
	task              string
	status            string
	phase             string
	report            string
	errText           string
	sequence          int
	deliveredSequence int
	followUps         int
	messages          []llm.Message
	checkpoint        *sessions.ContextCheckpoint
	pending           []string
	workerActive      bool
	canceled          bool
	currentCancel     context.CancelFunc
}

type researchStreamResult struct {
	content      string
	reasoning    string
	toolCalls    []llm.ToolCall
	usage        *llm.Usage
	completed    bool
	finishReason string
}

func newChatResearchRun(parent context.Context, session *chatSession, turnID string, settings, parentSettings llm.Settings, streamer chatStreamer, mode agentmodes.Mode) *chatResearchRun {
	ctx, cancel := context.WithCancel(parent)
	concurrency := settings.Normalized().ResearchAgentConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > tools.MaxResearchAgentsPerTurn {
		concurrency = tools.MaxResearchAgentsPerTurn
	}
	return &chatResearchRun{
		session: session, ctx: ctx, cancel: cancel, turnID: turnID,
		settings: settings, parentSettings: parentSettings, streamer: streamer,
		toolScopes: researchToolScopes(mode), semaphore: make(chan struct{}, concurrency),
		agents: make(map[string]*chatResearchAgentRun), updates: make(chan struct{}, 1),
	}
}

func researchToolScopes(mode agentmodes.Mode) *tools.ToolScopeChecker {
	permissions := make([]tools.ToolPermission, 0)
	for _, schema := range tools.ResearchLLMSchemaForScopes(nil) {
		name := schema.Function.Name
		if len(mode.Permissions) == 0 {
			permissions = append(permissions, tools.ToolPermission{Name: name})
			continue
		}
		if permission, ok := mode.Permissions[name]; ok {
			permission.Name = name
			permissions = append(permissions, permission)
		}
	}
	if len(permissions) == 0 {
		return tools.NewDenyAllToolScopeChecker()
	}
	return tools.NewToolScopeChecker(permissions)
}

func modeAllowsResearch(mode agentmodes.Mode) bool {
	if len(mode.Permissions) == 0 {
		return true
	}
	for _, name := range []string{
		tools.ResearchAgentsSpawnToolName, tools.ResearchAgentSendToolName,
		tools.ResearchAgentsWaitToolName, tools.ResearchAgentsCancelToolName,
	} {
		if _, ok := mode.Permissions[name]; ok {
			return true
		}
	}
	return false
}

func (r *chatResearchRun) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.cancel()
	for _, agent := range r.agents {
		agent.canceled = true
		agent.pending = nil
		if agent.currentCancel != nil {
			agent.currentCancel()
		}
	}
	r.mu.Unlock()

	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	r.clearAgentIndicators()
}

func (r *chatResearchRun) SpawnResearchAgents(ctx context.Context, specs []tools.ResearchAgentSpec) ([]tools.ResearchAgentSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("research turn is closed")
	}
	if len(specs) == 0 || len(r.agents)+len(specs) > tools.MaxResearchAgentsPerTurn {
		r.mu.Unlock()
		return nil, fmt.Errorf("a chat turn can own at most %d research agents", tools.MaxResearchAgentsPerTurn)
	}
	created := make([]*chatResearchAgentRun, 0, len(specs))
	for _, spec := range specs {
		id := fmt.Sprintf("agent-%d", len(r.order)+1)
		name := normalizeResearchAgentName(spec.Name, len(r.order)+1)
		task := strings.TrimSpace(spec.Task)
		agent := &chatResearchAgentRun{
			id: id, name: name, task: task, status: "queued", phase: "waiting for a research slot",
			pending: []string{task}, messages: []llm.Message{r.researchSystemMessage(task)},
		}
		r.agents[id] = agent
		r.order = append(r.order, id)
		created = append(created, agent)
	}
	snapshots := make([]tools.ResearchAgentSnapshot, 0, len(created))
	for _, agent := range created {
		snapshots = append(snapshots, r.snapshotLocked(agent, false))
	}
	r.mu.Unlock()

	for _, agent := range created {
		r.publishAgent(agent)
		r.startWorker(agent)
	}
	return snapshots, nil
}

func (r *chatResearchRun) SendResearchAgentMessage(ctx context.Context, agentID, message string) (tools.ResearchAgentSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return tools.ResearchAgentSnapshot{}, err
	}
	r.mu.Lock()
	agent := r.agents[strings.TrimSpace(agentID)]
	if agent == nil {
		r.mu.Unlock()
		return tools.ResearchAgentSnapshot{}, fmt.Errorf("research agent %q was not found", agentID)
	}
	if r.closed || agent.canceled {
		r.mu.Unlock()
		return tools.ResearchAgentSnapshot{}, fmt.Errorf("research agent %q is not available", agentID)
	}
	if agent.followUps >= maxResearchAgentFollowUps {
		r.mu.Unlock()
		return tools.ResearchAgentSnapshot{}, fmt.Errorf("research agent %q reached the follow-up limit", agentID)
	}
	agent.followUps++
	agent.pending = append(agent.pending, strings.TrimSpace(message))
	if !agent.workerActive {
		agent.status = "queued"
		agent.phase = "follow-up queued"
	}
	snapshot := r.snapshotLocked(agent, false)
	shouldStart := !agent.workerActive
	r.mu.Unlock()
	r.publishAgent(agent)
	if shouldStart {
		r.startWorker(agent)
	}
	return snapshot, nil
}

func (r *chatResearchRun) WaitResearchAgents(ctx context.Context, agentIDs []string, waitFor string, timeout time.Duration) (tools.ResearchAgentWaitResult, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		r.mu.Lock()
		selected, err := r.selectedAgentsLocked(agentIDs)
		if err != nil {
			r.mu.Unlock()
			return tools.ResearchAgentWaitResult{}, err
		}
		met := researchWaitCondition(selected, waitFor)
		if met {
			result := r.waitResultLocked(selected, true)
			r.mu.Unlock()
			return result, nil
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return tools.ResearchAgentWaitResult{}, ctx.Err()
		case <-r.ctx.Done():
			return tools.ResearchAgentWaitResult{}, r.ctx.Err()
		case <-r.updates:
		case <-timer.C:
			r.mu.Lock()
			selected, err := r.selectedAgentsLocked(agentIDs)
			if err != nil {
				r.mu.Unlock()
				return tools.ResearchAgentWaitResult{}, err
			}
			result := r.waitResultLocked(selected, researchWaitCondition(selected, waitFor))
			r.mu.Unlock()
			return result, nil
		}
	}
}

func (r *chatResearchRun) CancelResearchAgents(_ context.Context, agentIDs []string) ([]tools.ResearchAgentSnapshot, error) {
	r.mu.Lock()
	selected, err := r.selectedAgentsLocked(agentIDs)
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	for _, agent := range selected {
		agent.canceled = true
		agent.pending = nil
		agent.status = "canceled"
		agent.phase = ""
		agent.errText = "Canceled by the parent chat agent."
		agent.deliveredSequence = agent.sequence
		if agent.currentCancel != nil {
			agent.currentCancel()
		}
	}
	snapshots := make([]tools.ResearchAgentSnapshot, 0, len(selected))
	for _, agent := range selected {
		snapshots = append(snapshots, r.snapshotLocked(agent, false))
	}
	r.mu.Unlock()
	for _, agent := range selected {
		r.publishAgent(agent)
	}
	r.notify()
	return snapshots, nil
}

func (r *chatResearchRun) selectedAgentsLocked(ids []string) ([]*chatResearchAgentRun, error) {
	if len(r.agents) == 0 {
		return nil, errors.New("no research agents exist in this chat turn")
	}
	if len(ids) == 0 {
		selected := make([]*chatResearchAgentRun, 0, len(r.order))
		for _, id := range r.order {
			selected = append(selected, r.agents[id])
		}
		return selected, nil
	}
	selected := make([]*chatResearchAgentRun, 0, len(ids))
	seen := make(map[string]bool)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if seen[id] {
			continue
		}
		agent := r.agents[id]
		if agent == nil {
			return nil, fmt.Errorf("research agent %q was not found", id)
		}
		seen[id] = true
		selected = append(selected, agent)
	}
	return selected, nil
}

func researchWaitCondition(agents []*chatResearchAgentRun, waitFor string) bool {
	terminal := 0
	for _, agent := range agents {
		if !agent.workerActive && len(agent.pending) == 0 {
			switch agent.status {
			case "completed", "failed", "canceled":
				terminal++
			}
		}
	}
	if waitFor == "any" {
		return terminal > 0
	}
	return terminal == len(agents)
}

func (r *chatResearchRun) waitResultLocked(agents []*chatResearchAgentRun, met bool) tools.ResearchAgentWaitResult {
	snapshots := make([]tools.ResearchAgentSnapshot, 0, len(agents))
	reports := 0
	for _, agent := range agents {
		include := agent.sequence > agent.deliveredSequence
		snapshots = append(snapshots, r.snapshotLocked(agent, include))
		if include {
			agent.deliveredSequence = agent.sequence
			reports++
		}
	}
	if reports > 0 {
		perReport := max(1, r.aggregateReportMaxBytes()/reports)
		for index := range snapshots {
			if snapshots[index].Report != "" {
				snapshots[index].Report = limitResearchText(snapshots[index].Report, perReport)
			}
		}
	}
	return tools.ResearchAgentWaitResult{ConditionMet: met, Agents: snapshots}
}

func (r *chatResearchRun) snapshotLocked(agent *chatResearchAgentRun, includeReport bool) tools.ResearchAgentSnapshot {
	snapshot := tools.ResearchAgentSnapshot{
		ID: agent.id, Name: agent.name, Status: agent.status, Phase: agent.phase,
		Error: agent.errText, Sequence: agent.sequence,
	}
	if includeReport {
		snapshot.Report = limitResearchText(agent.report, r.reportMaxBytes())
	}
	return snapshot
}

func (r *chatResearchRun) HasOutstanding() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, agent := range r.agents {
		if agent.workerActive || len(agent.pending) > 0 || agent.sequence > agent.deliveredSequence {
			return true
		}
	}
	return false
}

func (r *chatResearchRun) FallbackMarkdown() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	perAgent := max(1, r.aggregateReportMaxBytes()/max(1, len(r.order)))
	sections := make([]string, 0, len(r.order))
	for _, id := range r.order {
		agent := r.agents[id]
		if agent.report != "" {
			sections = append(sections, fmt.Sprintf("### %s (%s)\n\n%s", agent.name, agent.id, limitResearchText(agent.report, perAgent)))
		} else if agent.errText != "" {
			sections = append(sections, fmt.Sprintf("### %s (%s)\n\nResearch unavailable: %s", agent.name, agent.id, agent.errText))
		}
	}
	return limitResearchText(strings.Join(sections, "\n\n"), r.aggregateReportMaxBytes())
}

func (r *chatResearchRun) reportMaxBytes() int {
	inputTokens := max(1, r.parentSettings.ContextLength-r.parentSettings.MaxTokens)
	return min(8192, max(1024, inputTokens/32*4))
}

func (r *chatResearchRun) aggregateReportMaxBytes() int {
	inputTokens := max(1, r.parentSettings.ContextLength-r.parentSettings.MaxTokens)
	return min(32768, max(2048, inputTokens/8*4))
}

func (r *chatResearchRun) notify() {
	select {
	case r.updates <- struct{}{}:
	default:
	}
}

func normalizeResearchAgentName(name string, index int) string {
	name = strings.Join(strings.Fields(strings.TrimSpace(name)), " ")
	if name == "" {
		name = fmt.Sprintf("Researcher %d", index)
	}
	return limitResearchText(name, 48)
}

func limitResearchText(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for value != "" && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (r *chatResearchRun) researchSystemMessage(task string) llm.Message {
	var prompt strings.Builder
	prompt.WriteString(researchAgentSystemPrompt)
	roots := workspaceToolRoots(r.session.workspace)
	if len(roots) > 0 {
		prompt.WriteString("\n\nWorkspace paths must start with one of these folder labels: ")
		for index, root := range roots {
			if index > 0 {
				prompt.WriteString(", ")
			}
			prompt.WriteString(root.Label)
		}
		prompt.WriteString(".")
	}
	prompt.WriteString("\n\nAssigned investigation:\n")
	prompt.WriteString(strings.TrimSpace(task))
	return llm.Message{Role: llm.RoleSystem, Name: "echo-research-agent", Content: prompt.String()}
}

func (r *chatResearchRun) startWorker(agent *chatResearchAgentRun) {
	r.mu.Lock()
	if r.closed || agent.canceled || agent.workerActive || len(agent.pending) == 0 {
		r.mu.Unlock()
		return
	}
	agent.workerActive = true
	r.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.wg.Done()
		r.runWorker(agent)
	}()
}

func (r *chatResearchRun) runWorker(agent *chatResearchAgentRun) {
	for {
		r.mu.Lock()
		if r.closed || agent.canceled || len(agent.pending) == 0 {
			agent.workerActive = false
			if agent.canceled || r.closed {
				agent.status = "canceled"
				agent.phase = ""
			}
			r.mu.Unlock()
			r.publishAgent(agent)
			r.notify()
			return
		}
		prompt := agent.pending[0]
		agent.pending = agent.pending[1:]
		r.mu.Unlock()

		select {
		case r.semaphore <- struct{}{}:
		case <-r.ctx.Done():
			r.finishCanceledAgent(agent)
			return
		}

		r.mu.Lock()
		if r.closed || agent.canceled {
			r.mu.Unlock()
			<-r.semaphore
			r.finishCanceledAgent(agent)
			return
		}
		agent.status = "running"
		agent.phase = "investigating"
		jobCtx, cancel := context.WithTimeout(r.ctx, r.researchJobDeadline())
		agent.currentCancel = cancel
		r.mu.Unlock()
		r.publishAgent(agent)

		report, updatedMessages, err := r.runAgentTurn(jobCtx, agent, prompt)
		cancel()
		<-r.semaphore

		r.mu.Lock()
		agent.currentCancel = nil
		if len(updatedMessages) > 0 {
			agent.messages = updatedMessages
		}
		switch {
		case agent.canceled || r.closed || errors.Is(err, context.Canceled):
			agent.status = "canceled"
			agent.phase = ""
			if agent.errText == "" {
				agent.errText = "Research was canceled."
			}
		case err != nil:
			agent.status = "failed"
			agent.phase = ""
			agent.errText = err.Error()
			agent.sequence++
			agent.report = ""
		default:
			agent.status = "completed"
			agent.phase = ""
			agent.errText = ""
			agent.report = limitResearchText(strings.TrimSpace(report), r.reportMaxBytes())
			agent.sequence++
		}
		more := !agent.canceled && !r.closed && len(agent.pending) > 0
		if more {
			agent.status = "queued"
			agent.phase = "follow-up queued"
		} else {
			agent.workerActive = false
		}
		r.mu.Unlock()
		r.publishAgent(agent)
		r.notify()
		if !more {
			return
		}
	}
}

func (r *chatResearchRun) finishCanceledAgent(agent *chatResearchAgentRun) {
	r.mu.Lock()
	agent.workerActive = false
	agent.status = "canceled"
	agent.phase = ""
	if agent.errText == "" {
		agent.errText = "Research was canceled."
	}
	r.mu.Unlock()
	r.publishAgent(agent)
	r.notify()
}

func (r *chatResearchRun) researchJobDeadline() time.Duration {
	seconds := r.settings.Normalized().TimeoutSeconds
	duration := 2 * time.Duration(max(1, seconds)) * time.Second
	if duration < 2*time.Minute {
		duration = 2 * time.Minute
	}
	if duration > 15*time.Minute {
		duration = 15 * time.Minute
	}
	return duration
}

func (r *chatResearchRun) runAgentTurn(ctx context.Context, agent *chatResearchAgentRun, prompt string) (string, []llm.Message, error) {
	r.mu.Lock()
	canonical := cloneResearchMessages(agent.messages)
	checkpoint := cloneContextCheckpoint(agent.checkpoint)
	r.mu.Unlock()
	canonical = append(canonical, llm.Message{Role: llm.RoleUser, Content: strings.TrimSpace(prompt)})
	settings := r.settings
	streamer := r.streamer
	emptyAssistantRetries := 0
	transientStreamRetries := 0
	observedTokens := 0
	usageSource := "estimated"
	compressionCooldown := false

	for round := 0; round < maxResearchAgentRounds; round++ {
		toolSchema := r.session.manager.server.tools.ResearchLLMSchemaForScopes(r.toolScopes)
		if checkpoint != nil {
			toolSchema = append(toolSchema, contextHistorySearchToolSchema())
		}
		messages := buildCompressedModelHistory(canonical, checkpoint)
		currentTokens := contextRequestTokens(settings, messages, toolSchema)
		hardLimitPreflight := currentTokens+settings.MaxTokens > settings.ContextLength
		coolingDown := compressionCooldown
		compressionCooldown = false
		if hardLimitPreflight && !settings.CompressionEnabled() {
			return "", canonical, errors.New("research request would exceed the endpoint context window and automatic context compression is disabled")
		}
		if settings.CompressionEnabled() && (hardLimitPreflight || (currentTokens >= compressionThresholdTokens(settings) && !coolingDown)) {
			r.setAgentPhase(agent, "compressing context")
			updated, compressionErr := r.compressAgentContext(ctx, agent, settings, canonical, checkpoint, toolSchema, observedTokens, usageSource, round)
			compressionCooldown = true
			r.setAgentPhase(agent, "investigating")
			if updated != nil {
				checkpoint = updated
				messages = buildCompressedModelHistory(canonical, checkpoint)
				toolSchema = r.session.manager.server.tools.ResearchLLMSchemaForScopes(r.toolScopes)
				toolSchema = append(toolSchema, contextHistorySearchToolSchema())
			}
			if contextRequestTokens(settings, messages, toolSchema)+settings.MaxTokens > settings.ContextLength {
				if compressionErr != nil {
					return "", canonical, fmt.Errorf("research context compression failed before the next request: %w", compressionErr)
				}
				return "", canonical, errors.New("compressed research context still exceeds the endpoint context window")
			}
		}
		request, err := llm.NewChatRequest(settings, messages, llm.WithStream(true), llm.WithTools(toolSchema))
		if err != nil {
			return "", canonical, err
		}
		streamResult, err := r.collectResearchStream(ctx, streamer.StreamChat(ctx, request), agent)
		if err != nil {
			if isEmptyAssistantResponse(streamResult.content, streamResult.toolCalls) && transientStreamRetries < maxTransientStreamRetries && ctx.Err() == nil {
				transientStreamRetries++
				canonical = append(canonical, transientStreamRetryMessage())
				continue
			}
			return "", canonical, err
		}
		if finishErr := finishReasonError(streamResult.finishReason, len(streamResult.toolCalls) > 0); finishErr != nil {
			return "", canonical, finishErr
		}
		transientStreamRetries = 0
		if isEmptyAssistantResponse(streamResult.content, streamResult.toolCalls) {
			if emptyAssistantRetries >= maxEmptyAssistantRetries {
				return "", canonical, emptyAssistantResponseError()
			}
			emptyAssistantRetries++
			canonical = append(canonical, emptyAssistantRetryMessage())
			continue
		}
		emptyAssistantRetries = 0
		assistant := llm.Message{Role: llm.RoleAssistant, Content: streamResult.content, ToolCalls: streamResult.toolCalls}
		if streamResult.content != "" || len(streamResult.toolCalls) > 0 {
			canonical = append(canonical, assistant)
		}
		if streamResult.usage != nil {
			observedTokens = streamResult.usage.TotalTokens
			if observedTokens == 0 {
				observedTokens = streamResult.usage.PromptTokens + streamResult.usage.CompletionTokens
			}
			usageSource = "provider"
		}
		if len(streamResult.toolCalls) == 0 {
			r.mu.Lock()
			agent.checkpoint = cloneContextCheckpoint(checkpoint)
			r.mu.Unlock()
			return streamResult.content, canonical, nil
		}

		visualResult := false
		for callOrder, call := range streamResult.toolCalls {
			if err := ctx.Err(); err != nil {
				return "", canonical, err
			}
			callID := strings.TrimSpace(call.ID)
			if callID == "" {
				callID = fmt.Sprintf("research-%d-call-%d", round, callOrder)
			}
			r.setAgentPhase(agent, "using "+call.Function.Name)
			r.updateResearchToolActivity(agent, callID, callOrder, call.Function.Name, call.Function.Arguments, "", false, false)
			toolCtx := r.session.toolContext(ctx, r.toolScopes)
			toolCtx.ResearchAgents = nil
			toolCtx.AgentModes = nil
			var result tools.ExecutionResult
			if call.Function.Name == contextHistorySearchToolName {
				result = r.session.executeContextHistorySearch(canonical, checkpoint, json.RawMessage(call.Function.Arguments))
			} else {
				result = r.session.manager.server.tools.Execute(toolCtx, call.Function.Name, json.RawMessage(call.Function.Arguments))
			}
			data, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				data = []byte(fmt.Sprintf(`{"tool":%q,"success":false,"error":{"code":"marshal_error","message":%q}}`, call.Function.Name, marshalErr.Error()))
			}
			canonical = append(canonical, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Content: string(data)})
			if imageMessage, ok := toolResultImageMessage(call.Function.Name, result); ok {
				canonical = append(canonical, imageMessage)
				visualResult = true
			}
			if videoMessage, ok := toolResultVideoMessage(call.Function.Name, result); ok {
				canonical = append(canonical, videoMessage)
				visualResult = true
			}
			r.updateResearchToolActivity(agent, callID, callOrder, call.Function.Name, call.Function.Arguments, string(data), true, result.Success && marshalErr == nil)
		}
		r.setAgentPhase(agent, "investigating")
		if visualResult {
			settings, streamer = r.session.manager.server.routeMediaChat(settings, buildCompressedModelHistory(canonical, checkpoint), true)
		}
	}
	return "", canonical, fmt.Errorf("research agent exceeded %d model rounds", maxResearchAgentRounds)
}

func (r *chatResearchRun) compressAgentContext(ctx context.Context, agent *chatResearchAgentRun, settings llm.Settings, canonical []llm.Message, checkpoint *sessions.ContextCheckpoint, toolSchema []llm.Tool, observedTokens int, usageSource string, round int) (*sessions.ContextCheckpoint, error) {
	compressionID := newSessionID("compression")
	started := time.Now().UTC()
	s := r.session
	s.mu.Lock()
	if !s.isActiveLocked(r.turnID) {
		s.mu.Unlock()
		return nil, errChatCanceled
	}
	s.appendTrajectoryLocked("context/compression_start", r.turnID, nil, map[string]any{
		"compressionId": compressionID, "trigger": "automatic", "phase": "research",
		"agentId": agent.id, "agentName": agent.name, "round": round,
		"thresholdPercent": settings.ContextCompressionThresholdPercent, "contextLength": settings.ContextLength,
		"model": settings.Model, "endpoint": settings.Endpoint, "usageSource": usageSource, "startedAt": started,
	})
	s.mu.Unlock()
	result, err := s.manager.server.compressContext(ctx, settings, canonical, checkpoint, nil, toolSchema, observedTokens, usageSource)
	completed := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isActiveLocked(r.turnID) {
		return nil, errChatCanceled
	}
	if err != nil {
		eventType := "context/compression_error"
		if errors.Is(err, errNothingToCompress) {
			eventType = "context/compression_skipped"
		}
		resultUsageSource := result.UsageSource
		if resultUsageSource == "" {
			resultUsageSource = usageSource
		}
		s.appendTrajectoryLocked(eventType, r.turnID, nil, map[string]any{
			"compressionId": compressionID, "trigger": "automatic", "phase": "research",
			"agentId": agent.id, "agentName": agent.name, "round": round,
			"thresholdPercent": settings.ContextCompressionThresholdPercent, "contextLength": settings.ContextLength,
			"model": settings.Model, "endpoint": settings.Endpoint, "usageSource": resultUsageSource,
			"beforeTokens": result.BeforeTokens, "afterTokens": result.AfterTokens,
			"reclaimedTokens": result.BeforeTokens - result.AfterTokens, "summaryUsage": result.SummaryUsage,
			"chunkCount": result.ChunkCount, "recoveryAvailable": checkpoint != nil,
			"errorClass": classifyCompressionError(err), "error": err.Error(), "durationMs": completed.Sub(started).Milliseconds(), "completedAt": completed,
		})
		logf("context compression chat=%s trigger=automatic phase=research agent=%s status=failed before=%d after=%d duration_ms=%d error=%q", s.transcript.ChatID, agent.id, result.BeforeTokens, result.AfterTokens, completed.Sub(started).Milliseconds(), err.Error())
		return nil, err
	}
	result.Checkpoint.LastCompactedAt = completed
	result.Checkpoint.LastAssistantNumber = round
	s.appendTrajectoryLocked("context/compression_complete", r.turnID, nil, map[string]any{
		"compressionId": compressionID, "trigger": "automatic", "phase": "research",
		"agentId": agent.id, "agentName": agent.name, "round": round,
		"thresholdPercent": settings.ContextCompressionThresholdPercent, "contextLength": settings.ContextLength,
		"model": settings.Model, "endpoint": settings.Endpoint, "usageSource": result.UsageSource,
		"beforeTokens": result.BeforeTokens, "afterTokens": result.AfterTokens,
		"reclaimedTokens": result.BeforeTokens - result.AfterTokens, "retiredMessages": result.RetiredMessages,
		"summaryUsage": result.SummaryUsage, "chunkCount": result.ChunkCount, "summary": result.Checkpoint.Summary,
		"recoveryAvailable": true, "durationMs": completed.Sub(started).Milliseconds(), "completedAt": completed,
	})
	logf("context compression chat=%s trigger=automatic phase=research agent=%s status=completed before=%d after=%d duration_ms=%d", s.transcript.ChatID, agent.id, result.BeforeTokens, result.AfterTokens, completed.Sub(started).Milliseconds())
	return cloneContextCheckpoint(result.Checkpoint), nil
}

func (r *chatResearchRun) setAgentPhase(agent *chatResearchAgentRun, phase string) {
	r.mu.Lock()
	if !agent.canceled && !r.closed {
		agent.status = "running"
		agent.phase = phase
	}
	r.mu.Unlock()
	r.publishAgent(agent)
}

func cloneResearchMessages(messages []llm.Message) []llm.Message {
	output := append([]llm.Message(nil), messages...)
	for index := range output {
		output[index].ContentParts = append([]llm.MessageContentPart(nil), output[index].ContentParts...)
		output[index].ToolCalls = append([]llm.ToolCall(nil), output[index].ToolCalls...)
	}
	return output
}

func (r *chatResearchRun) collectResearchStream(ctx context.Context, stream *llm.Stream, agent *chatResearchAgentRun) (researchStreamResult, error) {
	var content strings.Builder
	var reasoning strings.Builder
	toolCalls := make(map[int]llm.ToolCall)
	result := researchStreamResult{}
	var firstErr error
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case event, ok := <-stream.Events:
			if !ok {
				result.content = content.String()
				result.reasoning = reasoning.String()
				result.toolCalls = orderedToolCalls(toolCalls)
				if result.usage == nil && stream.Usage != nil {
					usage := *stream.Usage
					result.usage = &usage
				}
				if firstErr != nil {
					return result, firstErr
				}
				if !result.completed {
					return result, llm.ErrStreamEndedBeforeCompletion
				}
				return result, nil
			}
			switch event.Type {
			case llm.EventToken:
				content.WriteString(event.Content)
			case llm.EventReasoning:
				reasoning.WriteString(event.Content)
				r.appendResearchReasoning(agent, event.Content)
			case llm.EventToolCall:
				if event.ToolCall != nil {
					toolCalls[event.ToolCall.Index] = mergeToolDelta(toolCalls[event.ToolCall.Index], *event.ToolCall)
				}
			case llm.EventError:
				if firstErr == nil {
					firstErr = errors.New(event.Error)
				}
			case llm.EventCanceled:
				return result, context.Canceled
			case llm.EventUsage, llm.EventComplete:
				if event.Usage != nil {
					usage := *event.Usage
					result.usage = &usage
				}
				if event.Type == llm.EventComplete {
					result.completed = true
					result.finishReason = event.FinishReason
				}
			}
		}
	}
}

func (r *chatResearchRun) publishAgent(agent *chatResearchAgentRun) {
	r.mu.Lock()
	public := sessions.ResearchAgent{
		ID: agent.id, Name: agent.name, Status: agent.status, Phase: agent.phase,
		TaskLabel: limitResearchText(agent.task, 160), Error: agent.errText,
	}
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return
	}

	s := r.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isActiveLocked(r.turnID) {
		return
	}
	found := false
	for index := range s.active.ResearchAgents {
		if s.active.ResearchAgents[index].ID == public.ID {
			if public.Status == "queued" || public.Status == "running" {
				s.active.ResearchAgents[index] = public
			} else {
				s.active.ResearchAgents = append(s.active.ResearchAgents[:index], s.active.ResearchAgents[index+1:]...)
			}
			found = true
			break
		}
	}
	if !found && (public.Status == "queued" || public.Status == "running") {
		s.active.ResearchAgents = append(s.active.ResearchAgents, public)
	}
	s.emitLocked(map[string]any{"type": "research_agent_status", "turnId": r.turnID, "researchAgent": public})
}

func (r *chatResearchRun) clearAgentIndicators() {
	s := r.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isActiveLocked(r.turnID) {
		return
	}
	s.active.ResearchAgents = nil
	s.emitLocked(map[string]any{"type": "research_agents_clear", "turnId": r.turnID})
}

func (r *chatResearchRun) appendResearchReasoning(agent *chatResearchAgentRun, delta string) {
	if delta == "" {
		return
	}
	s := r.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isActiveLocked(r.turnID) {
		return
	}
	index := -1
	for candidate := range s.active.ResearchReasoning {
		if s.active.ResearchReasoning[candidate].AgentID == agent.id {
			index = candidate
			break
		}
	}
	if index < 0 {
		s.active.ResearchReasoning = append(s.active.ResearchReasoning, sessions.ResearchReasoning{AgentID: agent.id, AgentName: agent.name})
		index = len(s.active.ResearchReasoning) - 1
	}
	entry := &s.active.ResearchReasoning[index]
	entry.Reasoning += delta
	replace := false
	if len(entry.Reasoning) > maxResearchReasoningBytes {
		keep := maxResearchReasoningBytes - len(researchReasoningTruncatedMarker)
		entry.Reasoning = researchReasoningTruncatedMarker + limitResearchTail(entry.Reasoning, keep)
		entry.Truncated = true
		replace = true
	}
	event := map[string]any{
		"type": "research_reasoning", "turnId": r.turnID, "agentId": agent.id,
		"agentName": agent.name, "content": delta,
	}
	if replace {
		event["content"] = entry.Reasoning
		event["replace"] = true
		event["truncated"] = true
	}
	s.emitLocked(event)
}

func limitResearchTail(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[len(value)-maxBytes:]
	for value != "" && !utf8.ValidString(value) {
		value = value[1:]
	}
	return value
}

func (r *chatResearchRun) updateResearchToolActivity(agent *chatResearchAgentRun, callID string, callOrder int, name, arguments, result string, complete, success bool) {
	s := r.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isActiveLocked(r.turnID) {
		return
	}
	publicCallID := agent.id + ":" + callID
	activity := sessions.ToolActivity{
		CallID: publicCallID, CallOrder: callOrder, Name: name,
		Arguments: limitResearchText(arguments, researchToolArgBytes), Status: "running",
		AgentID: agent.id, AgentName: agent.name,
	}
	if complete {
		activity.Status = "complete"
		activity.Success = success
		activity.Result = limitResearchText(result, researchToolResultBytes)
	}
	found := false
	for index := range s.active.ResearchTools {
		if s.active.ResearchTools[index].CallID == publicCallID {
			s.active.ResearchTools[index] = activity
			found = true
			break
		}
	}
	if !found {
		s.active.ResearchTools = append(s.active.ResearchTools, activity)
	}
	eventType := "tool_call"
	event := map[string]any{
		"type": eventType, "turnId": r.turnID, "callId": publicCallID,
		"callOrder": callOrder, "tool": name, "arguments": activity.Arguments,
		"status": activity.Status, "agentId": agent.id, "agentName": agent.name, "research": true,
	}
	if complete {
		event["type"] = "tool_result"
		event["success"] = success
		event["content"] = activity.Result
	}
	s.emitLocked(event)
}
