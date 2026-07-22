package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

const (
	maxResearchAgentsPerTurn     = 8
	maxResearchAgentFollowUps    = 6
	maxResearchAgentRounds       = 32
	researchContextTriggerPct    = 70
	researchToolArgumentMaxBytes = 2 * 1024
	researchToolResultMaxBytes   = 4 * 1024
	maxResearchAgentReasoning    = 128 * 1024
)

const researchReasoningTruncatedMarker = "[Earlier agent thinking truncated]\n\n"

const researchAgentSystemPrompt = `You are a focused, read-only research sub-agent working for a parent chat orchestrator.
Investigate only the assigned question. Use the available inspection and research tools aggressively enough to establish facts.
Do not edit files, execute shell commands, mutate external systems, create tasks, or spawn other agents.
Keep raw tool output in this private research thread. Your final response is a concise handoff to the parent model.
Include: findings; evidence with exact URLs or workspace paths and line numbers when available; uncertainties or conflicting evidence; and useful follow-up questions.
Do not claim a source was checked unless you actually inspected it.`

const researchOrchestratorSystemGuidance = "Act as the main orchestrator for deep research. When the request has two or more independent research branches or needs broad evidence gathering, proactively use research_agents_spawn with distinct non-overlapping assignments. Broad inspection of a workspace directory, subsystem, component, repository, or codebase must be delegated before you perform direct exploratory reads, including when the final deliverable is a skill, document, plan, or code change. When an automatic Research Scout report is present, use its layout to identify the major independent aspects and spawn multiple focused specialist agents in one call before final synthesis. Keep final synthesis and user intent in this main thread. Collect every needed report with research_agents_wait, and use research_agent_send for focused follow-up questions. Do not reproduce raw sub-agent transcripts, and do not finish while required agents are still running or uncollected. Use direct tools instead only for a single narrow lookup."

func shouldBootstrapResearch(message llm.Message, modeID string) bool {
	if modeID != AgentModeIDGeneral && modeID != AgentModeIDPlan {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(message.Content))
	if text == "" {
		return false
	}
	for _, phrase := range []string{
		"do not spawn", "don't spawn", "without research agents", "without sub-agents",
		"without subagents", "no research agents", "no sub-agents", "no subagents",
	} {
		if strings.Contains(text, phrase) {
			return false
		}
	}

	// These phrases express broad evidence gathering directly. More ambiguous
	// verbs below also require an explicit workspace-scale scope cue.
	for _, phrase := range []string{"look through", "deep research", "deep dive", "survey the code", "survey this code"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	if strings.HasPrefix(text, "research ") {
		return true
	}

	broadVerb := false
	for _, phrase := range []string{"research", "investigate", "analyze", "analyse", "audit", "review", "understand", "trace"} {
		if strings.Contains(text, phrase) {
			broadVerb = true
			break
		}
	}
	if !broadVerb {
		return false
	}
	for _, scope := range []string{"codebase", "repository", "entire workspace", "whole workspace", "the workspace", "workspace structure", "workspace code", "directory", "folder", "subsystem", "component", "module", "architecture", "skill file"} {
		if strings.Contains(text, scope) {
			return true
		}
	}
	return containsDirectoryLikePath(text)
}

func containsDirectoryLikePath(text string) bool {
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == '(' || r == ')' || r == '[' || r == ']' || r == ',' || r == ';'
	}) {
		field = strings.Trim(field, "`\"'.:")
		if !strings.ContainsAny(field, "/\\") {
			continue
		}
		for _, prefix := range []string{"image/", "video/", "audio/", "application/", "text/", "data:"} {
			if strings.HasPrefix(field, prefix) {
				field = ""
				break
			}
		}
		if field == "" || strings.Contains(field, "://") {
			continue
		}
		lastSlash := strings.LastIndexAny(field, "/\\")
		last := field[lastSlash+1:]
		if last != "" && !strings.Contains(last, ".") {
			return true
		}
	}
	return false
}

func automaticResearchTask(message llm.Message) string {
	return "Scout the overall workspace layout needed for this request. Stay read-only and do a fast structural survey rather than exhaustive research. Identify the major independent aspects that specialist agents should investigate in parallel, with the key entry-point paths and a short reason for each aspect. Do not perform requested writes.\n\nUser request:\n" + strings.TrimSpace(message.Content)
}

type chatResearchRun struct {
	service        *SystemService
	ctx            context.Context
	cancel         context.CancelFunc
	workspace      Workspace
	settings       llm.Settings
	parentSettings llm.Settings
	streamID       string
	messageID      string
	toolScopes     *tools.ToolScopeChecker
	semaphore      chan struct{}

	mu      sync.Mutex
	agents  map[string]*chatResearchAgentRun
	order   []string
	updates chan struct{}
	closed  bool
	wg      sync.WaitGroup

	stagedFanout    bool
	scoutID         string
	fanoutSatisfied bool
	fanoutPrompted  bool
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
	pending           []string
	workerActive      bool
	canceled          bool
	currentCancel     context.CancelFunc
}

type researchStreamResult struct {
	content      string
	toolCalls    []llm.ToolCall
	finished     bool
	finishReason string
	usage        *llm.Usage
	err          error
}

func (s *SystemService) newChatResearchRun(parent context.Context, workspace Workspace, settings llm.Settings, parentSettings llm.Settings, streamID string, messageID string, mode AgentMode) *chatResearchRun {
	ctx, cancel := context.WithCancel(parent)
	concurrency := settings.Normalized().ResearchAgentConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > maxResearchAgentsPerTurn {
		concurrency = maxResearchAgentsPerTurn
	}
	run := &chatResearchRun{
		service:        s,
		ctx:            ctx,
		cancel:         cancel,
		workspace:      workspace,
		settings:       settings,
		parentSettings: parentSettings,
		streamID:       streamID,
		messageID:      messageID,
		toolScopes:     researchToolScopes(mode),
		semaphore:      make(chan struct{}, concurrency),
		agents:         make(map[string]*chatResearchAgentRun),
		updates:        make(chan struct{}, 1),
	}
	key := researchRunKey(workspace.ID, streamID)
	s.researchMu.Lock()
	s.researchRuns[key] = run
	s.researchMu.Unlock()
	return run
}

func researchRunKey(workspaceID string, streamID string) string {
	return workspaceID + "\x00" + streamID
}

func (s *SystemService) closeChatResearchRun(workspaceID string, streamID string) {
	s.researchMu.Lock()
	run := s.researchRuns[researchRunKey(workspaceID, streamID)]
	s.researchMu.Unlock()
	if run != nil {
		run.Close()
	}
}

func (r *chatResearchRun) MarkAutomaticScout() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) == 0 {
		return
	}
	r.stagedFanout = true
	r.scoutID = r.order[0]
}

func (r *chatResearchRun) needsFanoutLocked() bool {
	if !r.stagedFanout || r.fanoutSatisfied {
		return false
	}
	scout := r.agents[r.scoutID]
	if scout == nil || scout.workerActive || len(scout.pending) > 0 {
		return false
	}
	switch scout.status {
	case "completed", "failed", "canceled":
		return scout.deliveredSequence >= scout.sequence
	default:
		return false
	}
}

func (r *chatResearchRun) NeedsFanout() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.needsFanoutLocked()
}

func (r *chatResearchRun) TakeFanoutInstruction() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.needsFanoutLocked() || r.fanoutPrompted {
		return ""
	}
	remaining := maxResearchAgentsPerTurn - len(r.agents)
	if remaining < 2 {
		r.fanoutSatisfied = true
		return ""
	}
	r.fanoutPrompted = true
	return fmt.Sprintf("The initial research scout has returned. Use its layout report to identify the major independent aspects of the request, then call research_agents_spawn once with between 2 and %d focused specialist agents (one aspect per agent). Do not repeat the general survey. After spawning, collect all specialist reports before final synthesis.", remaining)
}

func researchToolScopes(mode AgentMode) *tools.ToolScopeChecker {
	permissions := make([]tools.ToolPermission, 0)
	for _, schema := range tools.ResearchLLMSchema() {
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

	r.service.researchMu.Lock()
	delete(r.service.researchRuns, researchRunKey(r.workspace.ID, r.streamID))
	r.service.researchMu.Unlock()
	r.service.clearResearchAgentIndicators(r.workspace.ID, r.streamID, r.messageID)
}

func (r *chatResearchRun) SpawnResearchAgents(ctx context.Context, specs []tools.ResearchAgentSpec) ([]tools.ResearchAgentSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	allowed, _, err := r.service.CheckTokenBudget(r.workspace.ID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("workspace token budget is paused")
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("research turn is closed")
	}
	if len(specs) == 0 || len(r.agents)+len(specs) > maxResearchAgentsPerTurn {
		r.mu.Unlock()
		return nil, fmt.Errorf("a chat turn can own at most %d research agents", maxResearchAgentsPerTurn)
	}
	created := make([]*chatResearchAgentRun, 0, len(specs))
	for _, spec := range specs {
		r.service.researchMu.Lock()
		r.service.researchAgentSeq++
		sequence := r.service.researchAgentSeq
		r.service.researchMu.Unlock()
		id := fmt.Sprintf("agent-%d", sequence)
		name := normalizeResearchAgentName(spec.Name, len(r.order)+1)
		task := strings.TrimSpace(spec.Task)
		agent := &chatResearchAgentRun{
			id:       id,
			name:     name,
			task:     task,
			status:   "queued",
			phase:    "waiting for a research slot",
			pending:  []string{task},
			messages: []llm.Message{researchAgentSystemMessage(r.workspace, workspaceSkillCandidates(r.ctx, r.workspace, task), r.reportTokenBudget())},
		}
		r.agents[id] = agent
		r.order = append(r.order, id)
		created = append(created, agent)
	}
	if r.stagedFanout && !r.fanoutSatisfied {
		specialists := 0
		for _, id := range r.order {
			if id != r.scoutID {
				specialists++
			}
		}
		if specialists >= 2 {
			r.fanoutSatisfied = true
		}
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

func normalizeResearchAgentName(name string, index int) string {
	name = strings.Join(strings.Fields(strings.TrimSpace(name)), " ")
	if name == "" {
		name = fmt.Sprintf("Researcher %d", index)
	}
	return truncateContextText(name, 48)
}

func researchAgentSystemMessage(workspace Workspace, candidates []tools.WorkspaceSkillSummary, reportTokens int) llm.Message {
	prompt := fmt.Sprintf("%s\nKeep the final handoff within approximately %d tokens.", researchAgentSystemPrompt, reportTokens)
	prompt = workspaceSkillsPrompt(prompt, candidates, false)
	return llm.Message{Role: llm.RoleSystem, Content: workspaceSystemPrompt(prompt, workspace)}
}

func (r *chatResearchRun) SendResearchAgentMessage(ctx context.Context, agentID string, message string) (tools.ResearchAgentSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return tools.ResearchAgentSnapshot{}, err
	}
	allowed, _, err := r.service.CheckTokenBudget(r.workspace.ID)
	if err != nil {
		return tools.ResearchAgentSnapshot{}, err
	}
	if !allowed {
		return tools.ResearchAgentSnapshot{}, fmt.Errorf("workspace token budget is paused")
	}
	r.mu.Lock()
	agent := r.agents[strings.TrimSpace(agentID)]
	if agent == nil {
		r.mu.Unlock()
		return tools.ResearchAgentSnapshot{}, fmt.Errorf("research agent %q was not found", agentID)
	}
	if agent.canceled || r.closed {
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
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
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
		case <-deadline.C:
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
	if r.stagedFanout {
		// Explicit cancellation is the parent model's escape hatch if the scout
		// proves that further specialist fan-out is unnecessary.
		r.fanoutSatisfied = true
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
		return nil, fmt.Errorf("no research agents exist in this chat turn")
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
	if len(agents) == 0 {
		return true
	}
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
		snapshot := r.snapshotLocked(agent, include)
		if include {
			agent.deliveredSequence = agent.sequence
			reports++
		}
		snapshots = append(snapshots, snapshot)
	}
	if reports > 0 {
		maxChars := r.aggregateReportTokenBudget() * contextCompactionCharsPerToken
		perReport := max(1, maxChars/reports)
		for i := range snapshots {
			if snapshots[i].Report != "" {
				snapshots[i].Report = truncateContextText(snapshots[i].Report, perReport)
			}
		}
	}
	return tools.ResearchAgentWaitResult{ConditionMet: met, Agents: snapshots}
}

func (r *chatResearchRun) snapshotLocked(agent *chatResearchAgentRun, includeReport bool) tools.ResearchAgentSnapshot {
	snapshot := tools.ResearchAgentSnapshot{ID: agent.id, Name: agent.name, Status: agent.status, Phase: agent.phase, Error: agent.errText, Sequence: agent.sequence}
	if includeReport && agent.report != "" {
		snapshot.Report = truncateContextText(agent.report, r.reportTokenBudget()*contextCompactionCharsPerToken)
	}
	return snapshot
}

func (r *chatResearchRun) startWorker(agent *chatResearchAgentRun) {
	r.mu.Lock()
	if r.closed || agent.workerActive || agent.canceled {
		r.mu.Unlock()
		return
	}
	agent.workerActive = true
	r.wg.Add(1)
	r.mu.Unlock()
	go r.runWorker(agent)
}

func (r *chatResearchRun) runWorker(agent *chatResearchAgentRun) {
	defer r.wg.Done()
	for {
		r.mu.Lock()
		if r.closed || agent.canceled || len(agent.pending) == 0 {
			agent.workerActive = false
			r.mu.Unlock()
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

		r.setAgentState(agent, "running", "researching", "")
		jobCtx, cancel := context.WithTimeout(r.ctx, researchJobDeadline(r.settings.TimeoutSeconds))
		r.mu.Lock()
		agent.currentCancel = cancel
		r.mu.Unlock()
		report, err := r.runAgentTurn(jobCtx, agent, prompt)
		cancel()
		<-r.semaphore

		r.mu.Lock()
		agent.currentCancel = nil
		if r.closed || agent.canceled {
			agent.workerActive = false
			agent.status = "canceled"
			agent.phase = ""
			r.mu.Unlock()
			r.publishAgent(agent)
			r.notify()
			return
		}
		agent.sequence++
		if err != nil {
			agent.status = "failed"
			agent.phase = ""
			agent.errText = userFacingLLMError(err)
			agent.report = ""
		} else {
			agent.status = "completed"
			agent.phase = ""
			agent.errText = ""
			agent.report = truncateContextText(report, r.reportTokenBudget()*contextCompactionCharsPerToken)
		}
		hasPending := len(agent.pending) > 0
		if hasPending {
			agent.status = "queued"
			agent.phase = "follow-up queued"
		}
		r.mu.Unlock()
		r.publishAgent(agent)
		r.notify()
	}
}

func researchJobDeadline(timeoutSeconds int) time.Duration {
	d := 2 * time.Duration(max(1, timeoutSeconds)) * time.Second
	if d < 2*time.Minute {
		d = 2 * time.Minute
	}
	if d > 15*time.Minute {
		d = 15 * time.Minute
	}
	return d
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

func (r *chatResearchRun) setAgentState(agent *chatResearchAgentRun, status string, phase string, errorText string) {
	r.mu.Lock()
	if !agent.canceled {
		agent.status, agent.phase, agent.errText = status, phase, errorText
	}
	r.mu.Unlock()
	r.publishAgent(agent)
}

func (r *chatResearchRun) publishAgent(agent *chatResearchAgentRun) {
	r.mu.Lock()
	public := ChatResearchAgent{ID: agent.id, Name: agent.name, Status: agent.status, Phase: agent.phase, TaskLabel: truncateContextText(agent.task, 120), Error: agent.errText}
	r.mu.Unlock()
	r.service.updateResearchAgentState(r.workspace.ID, r.streamID, r.messageID, public)
}

func (r *chatResearchRun) notify() {
	select {
	case r.updates <- struct{}{}:
	default:
	}
}

func (r *chatResearchRun) HasOutstanding() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, agent := range r.agents {
		if agent.workerActive || len(agent.pending) > 0 || agent.sequence > agent.deliveredSequence {
			return true
		}
	}
	return r.needsFanoutLocked()
}

func (r *chatResearchRun) HasReports() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, agent := range r.agents {
		if agent.report != "" {
			return true
		}
	}
	return false
}

func (r *chatResearchRun) FallbackMarkdown() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	maxChars := r.aggregateReportTokenBudget() * contextCompactionCharsPerToken
	perAgent := max(1, maxChars/max(1, len(r.order)))
	var sections []string
	for _, id := range r.order {
		agent := r.agents[id]
		if agent.report != "" {
			sections = append(sections, fmt.Sprintf("### %s (%s)\n\n%s", agent.name, agent.id, truncateContextText(agent.report, perAgent)))
		} else if agent.errText != "" {
			sections = append(sections, fmt.Sprintf("### %s (%s)\n\nResearch unavailable: %s", agent.name, agent.id, agent.errText))
		}
	}
	if len(sections) == 0 {
		return ""
	}
	return truncateContextText("I could not complete the final model synthesis, so here are the bounded research handoffs that were available.\n\n"+strings.Join(sections, "\n\n"), maxChars)
}

func (s *SystemService) completeChatWithResearchFallback(workspaceID string, streamID string, messageID string, run *chatResearchRun, reason string) bool {
	if run == nil || !run.HasReports() {
		return false
	}
	fallback := run.FallbackMarkdown()
	if strings.TrimSpace(fallback) == "" {
		return false
	}
	reason = strings.TrimSpace(reason)
	if reason != "" {
		fallback += "\n\nThe final synthesis model did not complete normally: " + reason
	}
	s.appendChatContent(workspaceID, streamID, messageID, fallback)
	s.appendChatHistory(workspaceID, llm.Message{Role: llm.RoleAssistant, Content: fallback})
	s.completeChatMessage(workspaceID, streamID, messageID, "fallback")
	return true
}

func (r *chatResearchRun) reportTokenBudget() int {
	budget := effectiveContextInputBudget(r.parentSettings) / 32
	return min(2048, max(256, budget))
}

func (r *chatResearchRun) aggregateReportTokenBudget() int {
	budget := effectiveContextInputBudget(r.parentSettings) / 8
	return min(8192, max(512, budget))
}

func (r *chatResearchRun) parentContextNeedsCompaction(messages []llm.Message, toolSchema []llm.Tool) bool {
	inputBudget := effectiveContextInputBudget(r.parentSettings)
	if r.HasOutstanding() {
		inputBudget = max(1, inputBudget-r.aggregateReportTokenBudget())
	}
	return estimateContextRequestTokens(messages, toolSchema) >= percentageOf(inputBudget, contextCompactionTriggerPercent)
}

func (r *chatResearchRun) runAgentTurn(ctx context.Context, agent *chatResearchAgentRun, prompt string) (string, error) {
	r.service.logAIEvent(slog.LevelInfo, "ai_operation_started", slog.String("surface", "research"), slog.Int("agent_sequence", agent.sequence))
	defer r.service.logAIEvent(slog.LevelInfo, "ai_operation_finished", slog.String("surface", "research"), slog.Int("agent_sequence", agent.sequence))
	client, err := r.service.newLLMClient(r.settings)
	if err != nil {
		return "", err
	}

	r.mu.Lock()
	messages := cloneLLMMessages(agent.messages)
	r.mu.Unlock()
	currentUser := llm.Message{Role: llm.RoleUser, Content: strings.TrimSpace(prompt)}
	messages = append(messages, currentUser)
	toolSchema := tools.ResearchLLMSchema()
	recoverableToolCalls := make(map[string]bool)
	emptyRetries := 0
	transientRetries := 0
	forcedCompactions := 0

	for round := 0; round < maxResearchAgentRounds; round++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if contextNeedsCompactionAt(r.settings, messages, toolSchema, researchContextTriggerPct) && contextHasCompressibleStale(r.settings, messages, contextCompactionPolicy{CurrentUser: currentUser}) {
			compaction, compactErr := compactContextIfNeeded(ctx, client, r.settings, messages, toolSchema, contextCompactionPolicy{CurrentUser: currentUser, Force: true})
			if compactErr == nil && compaction.Compacted {
				messages = compaction.Messages
			}
		}

		request, err := llm.NewChatRequest(r.settings, messages, llm.WithTools(toolSchema), llm.WithToolChoice("auto"))
		if err != nil {
			return "", err
		}
		result := streamResearchResponse(ctx, client, request, time.Duration(max(1, r.settings.TimeoutSeconds))*time.Second, func(reasoning string) {
			r.service.appendResearchAgentReasoning(r, agent, reasoning)
		})
		if result.usage != nil {
			_, _ = r.service.RecordTokenUsage(r.workspace.ID, int64(result.usage.TotalTokens))
		}
		if result.err != nil {
			if llm.IsContextLengthExceeded(result.err) {
				if recovery, ok := recoverToolResultContext(messages, recoverableToolCalls); ok {
					r.service.logAIEvent(slog.LevelWarn, "context_recovery", slog.String("surface", "research"), slog.String("tool_call_id", recovery.Call.ID))
					r.service.logModelFacingToolResult(recovery.Call, []llm.Message{recovery.ResultMessage})
					messages = recovery.Messages
					continue
				}
				if forcedCompactions < 2 {
					forcedCompactions++
					compaction, compactErr := compactContextIfNeeded(ctx, client, r.settings, messages, toolSchema, contextCompactionPolicy{CurrentUser: currentUser, Force: true, Aggressiveness: forcedCompactions})
					if compactErr == nil && compaction.Compacted {
						messages = compaction.Messages
						continue
					}
				}
			}
			if transientRetries < 1 && ctx.Err() == nil {
				r.service.logAIEvent(slog.LevelWarn, "ai_retry", slog.String("surface", "research"), slog.String("reason", "stream_error"))
				transientRetries++
				messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "The previous research response failed. Retry once from the existing evidence and return a usable answer or tool call."})
				continue
			}
			return "", result.err
		}
		forcedCompactions = 0
		toolCalls := r.service.normalizeToolCalls(result.toolCalls)
		if !result.finished {
			if transientRetries < 1 && ctx.Err() == nil {
				r.service.logAIEvent(slog.LevelWarn, "ai_retry", slog.String("surface", "research"), slog.String("reason", "incomplete_stream"))
				transientRetries++
				messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "The previous research stream ended before completion. Retry once from the existing evidence."})
				continue
			}
			return "", errors.New("research model stream ended before completion")
		}
		if err := finishReasonError(result.finishReason, len(toolCalls) > 0); err != nil {
			return "", err
		}
		if isEmptyAssistantResponse(result.content, toolCalls) {
			if emptyRetries >= 1 {
				return "", emptyAssistantResponseError()
			}
			emptyRetries++
			r.service.logAIEvent(slog.LevelWarn, "ai_retry", slog.String("surface", "research"), slog.String("reason", "empty_response"))
			messages = append(messages, emptyAssistantRetryMessage())
			continue
		}
		emptyRetries = 0
		assistant := llm.Message{Role: llm.RoleAssistant, Content: result.content, ToolCalls: toolCalls}
		messages = append(messages, assistant)
		if len(toolCalls) == 0 {
			r.mu.Lock()
			agent.messages = cloneLLMMessages(messages)
			r.mu.Unlock()
			return r.boundResearchReport(ctx, client, agent, strings.TrimSpace(result.content)), nil
		}

		for _, call := range toolCalls {
			r.setAgentState(agent, "running", "using "+call.Function.Name, "")
			r.service.updateResearchToolActivity(r, agent, call, "running", "", "")
			execution := tools.Execute(tools.ExecutionContext{
				Context:          ctx,
				FlowLog:          r.service.flowLog,
				ToolCallID:       call.ID,
				WorkspaceRoots:   workspaceToolRoots(r.workspace),
				SearxngURL:       r.settings.SearxngURL,
				CodeNavigator:    r.service.codeNavigator(r.workspace),
				WorkspaceContext: r.service.workspaceContextProvider(r.workspace),
				WorkspaceSkills:  r.service.workspaceSkillsProvider(r.workspace),
				WorkspaceTasks:   r.service.workspaceTasksProvider(r.workspace),
				ToolScopes:       r.toolScopes,
			}, call.Function.Name, json.RawMessage(call.Function.Arguments))
			data, marshalErr := json.Marshal(execution)
			if marshalErr != nil {
				data = []byte(fmt.Sprintf(`{"tool":%q,"success":false,"error":{"code":"marshal_error"}}`, call.Function.Name))
			}
			status, errorText := "complete", ""
			if !execution.Success {
				status = "error"
				if execution.Error != nil {
					errorText = execution.Error.Message
				}
			}
			r.service.updateResearchToolActivity(r, agent, call, status, string(data), errorText)
			recoverableToolCalls[call.ID] = true
			messages = append(messages, r.service.loggedToolResultMessages(call, execution, data)...)
		}
		r.setAgentState(agent, "running", "researching", "")
	}
	return "", fmt.Errorf("research agent exceeded %d assistant/tool rounds", maxResearchAgentRounds)
}

func (r *chatResearchRun) boundResearchReport(ctx context.Context, client *llm.Client, agent *chatResearchAgentRun, report string) string {
	maxChars := r.reportTokenBudget() * contextCompactionCharsPerToken
	if len(report) <= maxChars {
		return report
	}

	r.setAgentState(agent, "summarizing", "summarizing", "")
	settings := r.settings
	settings.MaxTokens = r.reportTokenBudget()
	inputLimit := max(1024, effectiveContextInputBudget(settings)*contextCompactionCharsPerToken/2)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "Compress the research handoff to the requested budget. Preserve concrete findings, exact URLs or workspace paths and line numbers, uncertainties, and follow-up suggestions. Do not add facts."},
		{Role: llm.RoleUser, Content: fmt.Sprintf("Return at most approximately %d tokens.\n\n%s", r.reportTokenBudget(), truncateContextText(report, inputLimit))},
	}
	request, err := llm.NewChatRequest(settings, messages)
	if err == nil {
		result := streamResearchResponse(ctx, client, request, time.Duration(max(1, settings.TimeoutSeconds))*time.Second, func(reasoning string) {
			r.service.appendResearchAgentReasoning(r, agent, reasoning)
		})
		if result.usage != nil {
			_, _ = r.service.RecordTokenUsage(r.workspace.ID, int64(result.usage.TotalTokens))
		}
		if result.err == nil && result.finished && strings.TrimSpace(result.content) != "" {
			return truncateContextText(result.content, maxChars)
		}
	}
	return truncateContextText(report, maxChars)
}

func streamResearchResponse(ctx context.Context, client *llm.Client, request llm.ChatRequest, idleTimeout time.Duration, onReasoning func(string)) researchStreamResult {
	stream := client.StreamChat(ctx, request)
	defer stream.Cancel()
	content := strings.Builder{}
	contentParser := inlineToolCallStreamParser{}
	reasoningParser := inlineToolCallStreamParser{}
	toolCalls := make(map[int]llm.ToolCall)
	nextInline := inlineToolCallIndexBase
	finished := false
	finishReason := ""

	record := func(calls []llm.ToolCall) {
		for _, call := range calls {
			toolCalls[nextInline] = call
			nextInline++
		}
	}
	emitReasoning := func(text string) {
		if onReasoning != nil && text != "" {
			onReasoning(text)
		}
	}
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(idleTimeout)
	}
	for {
		select {
		case <-ctx.Done():
			return researchStreamResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), err: ctx.Err()}
		case <-timer.C:
			stream.Cancel()
			return researchStreamResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), err: fmt.Errorf("research model stream was inactive for %s", idleTimeout)}
		case event, ok := <-stream.Events:
			if !ok {
				parsed := contentParser.Flush()
				content.WriteString(parsed.Text)
				record(parsed.ToolCalls)
				parsedReasoning := reasoningParser.Flush()
				record(parsedReasoning.ToolCalls)
				emitReasoning(parsedReasoning.Text)
				return researchStreamResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), finished: finished, finishReason: finishReason, usage: stream.Usage}
			}
			resetTimer()
			switch event.Type {
			case llm.EventToken:
				parsed := contentParser.Consume(event.Content)
				content.WriteString(parsed.Text)
				record(parsed.ToolCalls)
			case llm.EventReasoning:
				parsed := reasoningParser.Consume(event.Content)
				record(parsed.ToolCalls)
				emitReasoning(parsed.Text)
			case llm.EventToolCall:
				if event.ToolCall != nil {
					toolCalls[event.ToolCall.Index] = mergeToolDelta(toolCalls[event.ToolCall.Index], *event.ToolCall)
				}
			case llm.EventComplete:
				finished, finishReason = true, event.FinishReason
			case llm.EventCanceled:
				return researchStreamResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), err: context.Canceled}
			case llm.EventError:
				return researchStreamResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), err: errors.New(event.Error)}
			}
		}
	}
}

func contextNeedsCompactionAt(settings llm.Settings, messages []llm.Message, toolSchema []llm.Tool, percent int) bool {
	return estimateContextRequestTokens(messages, toolSchema) >= percentageOf(effectiveContextInputBudget(settings), percent)
}

func appendBoundedResearchReasoning(current string, delta string) (string, bool) {
	combined := current + delta
	if len(combined) <= maxResearchAgentReasoning {
		return combined, false
	}
	remaining := max(0, maxResearchAgentReasoning-len(researchReasoningTruncatedMarker))
	start := contextUTF8SuffixStart(combined, remaining)
	return researchReasoningTruncatedMarker + combined[start:], true
}

func (s *SystemService) appendResearchAgentReasoning(run *chatResearchRun, agent *chatResearchAgentRun, reasoning string) {
	if reasoning == "" {
		return
	}
	update := ChatResearchReasoning{
		AgentID:   agent.id,
		AgentName: agent.name,
		Reasoning: reasoning,
	}
	s.mutateChatMessage(run.workspace.ID, run.messageID, func(message *ChatMessage) {
		index := -1
		for i := range message.ResearchReasoning {
			if message.ResearchReasoning[i].AgentID == agent.id {
				index = i
				break
			}
		}
		if index < 0 {
			bounded, truncated := appendBoundedResearchReasoning("", reasoning)
			entry := ChatResearchReasoning{
				AgentID:   agent.id,
				AgentName: agent.name,
				Reasoning: bounded,
				Truncated: truncated,
			}
			message.ResearchReasoning = append(message.ResearchReasoning, entry)
			update.Truncated = truncated
			if truncated {
				update.Reasoning = bounded
				update.Replace = true
			}
			return
		}

		entry := &message.ResearchReasoning[index]
		wasTruncated := entry.Truncated
		bounded, truncated := appendBoundedResearchReasoning(entry.Reasoning, reasoning)
		entry.AgentName = agent.name
		entry.Reasoning = bounded
		entry.Truncated = entry.Truncated || truncated
		entry.Replace = false
		update.Truncated = entry.Truncated
		if truncated && !wasTruncated {
			update.Reasoning = bounded
			update.Replace = true
		}
	}, ChatStreamEvent{
		WorkspaceID:       run.workspace.ID,
		StreamID:          run.streamID,
		MessageID:         run.messageID,
		Type:              "agent_reasoning",
		ResearchReasoning: &update,
	})
}

func (s *SystemService) updateResearchAgentState(workspaceID string, streamID string, messageID string, agent ChatResearchAgent) {
	active := agent.Status == "queued" || agent.Status == "running" || agent.Status == "summarizing"
	s.mutateChatMessage(workspaceID, messageID, func(message *ChatMessage) {
		index := -1
		for i := range message.ResearchAgents {
			if message.ResearchAgents[i].ID == agent.ID {
				index = i
				break
			}
		}
		if active {
			if index >= 0 {
				message.ResearchAgents[index] = agent
			} else {
				message.ResearchAgents = append(message.ResearchAgents, agent)
			}
		} else if index >= 0 {
			message.ResearchAgents = append(message.ResearchAgents[:index], message.ResearchAgents[index+1:]...)
		}
	}, ChatStreamEvent{WorkspaceID: workspaceID, StreamID: streamID, MessageID: messageID, Type: "agent_status", ResearchAgent: &agent})
}

func (s *SystemService) clearResearchAgentIndicators(workspaceID string, streamID string, messageID string) {
	s.chatMu.Lock()
	changed := false
	var revision uint64
	if session := s.chatSessions[workspaceID]; session != nil {
		for i := range session.Messages {
			if session.Messages[i].ID == messageID && len(session.Messages[i].ResearchAgents) > 0 {
				session.Messages[i].ResearchAgents = nil
				session.Revision++
				revision = session.Revision
				changed = true
				break
			}
		}
	}
	s.chatMu.Unlock()
	if changed {
		s.emitChatEvent(ChatStreamEvent{WorkspaceID: workspaceID, StreamID: streamID, MessageID: messageID, Type: "agent_status", Revision: revision})
	}
}

func (s *SystemService) updateResearchToolActivity(run *chatResearchRun, agent *chatResearchAgentRun, call llm.ToolCall, status string, result string, errorText string) {
	activity := ChatToolActivity{
		ID:        agent.id + ":" + call.ID,
		Name:      call.Function.Name,
		Arguments: truncateContextText(call.Function.Arguments, researchToolArgumentMaxBytes),
		Status:    status,
		Result:    truncateContextText(result, researchToolResultMaxBytes),
		Error:     truncateContextText(errorText, 1024),
		AgentID:   agent.id,
		AgentName: agent.name,
	}
	s.mutateChatMessage(run.workspace.ID, run.messageID, func(message *ChatMessage) {
		for i := range message.ToolCalls {
			if message.ToolCalls[i].ID == activity.ID {
				message.ToolCalls[i] = activity
				return
			}
		}
		message.ToolCalls = append(message.ToolCalls, activity)
	}, ChatStreamEvent{WorkspaceID: run.workspace.ID, StreamID: run.streamID, MessageID: run.messageID, Type: "tool_call", ToolCall: &activity})
}
