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
	trajectorylog "github.com/brent/echo/internal/trajectory"
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
	pending           []*chatResearchJob
	currentJob        *chatResearchJob
	lastJob           *chatResearchJob
	nextJobNumber     int
	workerActive      bool
	canceled          bool
	currentCancel     context.CancelFunc
	trajectoryStatus  string
}

type chatResearchJob struct {
	id               string
	number           int
	kind             string
	prompt           string
	queuedAt         time.Time
	startedAt        time.Time
	terminalRecorded bool
}

type researchStreamResult struct {
	content          string
	reasoning        string
	toolCalls        []llm.ToolCall
	usage            *llm.Usage
	completed        bool
	finishReason     string
	startedAt        time.Time
	firstTokenAt     *time.Time
	firstReasoningAt *time.Time
	completedAt      time.Time
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

func (r *chatResearchRun) newJobLocked(agent *chatResearchAgentRun, prompt, kind string) *chatResearchJob {
	agent.nextJobNumber++
	job := &chatResearchJob{
		id: fmt.Sprintf("%s-job-%d", agent.id, agent.nextJobNumber), number: agent.nextJobNumber,
		kind: kind, prompt: strings.TrimSpace(prompt), queuedAt: time.Now().UTC(),
	}
	agent.pending = append(agent.pending, job)
	return job
}

func researchJobData(agent *chatResearchAgentRun, job *chatResearchJob) map[string]any {
	data := map[string]any{"agentId": agent.id, "agentName": agent.name}
	if job != nil {
		data["jobId"] = job.id
		data["jobNumber"] = job.number
		data["jobKind"] = job.kind
	}
	return data
}

func researchRoundData(agent *chatResearchAgentRun, job *chatResearchJob, round int) map[string]any {
	data := researchJobData(agent, job)
	data["round"] = round
	return data
}

func (r *chatResearchRun) trajectoryRoundData(agent *chatResearchAgentRun, job *chatResearchJob, round int) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return researchRoundData(agent, job, round)
}

func cloneTrajectoryData(data map[string]any) map[string]any {
	cloned := make(map[string]any, len(data))
	for key, value := range data {
		cloned[key] = value
	}
	return cloned
}

func (r *chatResearchRun) appendTrajectoryAt(eventType string, at time.Time, data map[string]any, requireActive bool) {
	s := r.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if requireActive && !s.isActiveLocked(r.turnID) {
		return
	}
	if !requireActive && (s.trajectory == nil || !s.trajectory.Exists()) {
		return
	}
	s.appendTrajectoryBatchLocked([]trajectorylog.AppendEntry{{
		Timestamp: at, Type: eventType, TurnID: r.turnID, Data: data,
	}})
}

func (r *chatResearchRun) appendTrajectory(eventType string, data map[string]any) {
	r.appendTrajectoryAt(eventType, time.Now().UTC(), data, true)
}

func (r *chatResearchRun) appendJobQueued(agent *chatResearchAgentRun, job *chatResearchJob) {
	r.mu.Lock()
	data := researchJobData(agent, job)
	data["prompt"] = job.prompt
	data["status"] = "queued"
	data["queuedAt"] = job.queuedAt
	r.mu.Unlock()
	r.appendTrajectoryAt("research/job_queued", job.queuedAt, data, true)
}

func (r *chatResearchRun) markJobTerminalLocked(agent *chatResearchAgentRun, job *chatResearchJob, status, report, errText string, completedAt time.Time) map[string]any {
	if job == nil || job.terminalRecorded {
		return nil
	}
	job.terminalRecorded = true
	agent.lastJob = job
	startedAt := job.startedAt
	if startedAt.IsZero() {
		startedAt = job.queuedAt
	}
	data := researchJobData(agent, job)
	data["prompt"] = job.prompt
	data["status"] = status
	data["report"] = report
	data["error"] = errText
	data["queuedAt"] = job.queuedAt
	data["startedAt"] = startedAt
	data["completedAt"] = completedAt
	data["durationMs"] = completedAt.Sub(startedAt).Milliseconds()
	return data
}

func (r *chatResearchRun) appendJobTerminal(data map[string]any, completedAt time.Time, requireActive bool) {
	if data != nil {
		r.appendTrajectoryAt("research/job_end", completedAt, data, requireActive)
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
	completedAt := time.Now().UTC()
	terminal := make([]map[string]any, 0)
	statuses := make([]map[string]any, 0)
	for _, agent := range r.agents {
		outstanding := agent.currentJob != nil || len(agent.pending) > 0 || agent.workerActive
		agent.canceled = true
		if outstanding {
			agent.status = "canceled"
			agent.phase = ""
			if agent.errText == "" {
				agent.errText = "Research was canceled."
			}
			if data := r.markJobTerminalLocked(agent, agent.currentJob, "canceled", "", agent.errText, completedAt); data != nil {
				terminal = append(terminal, data)
			}
			for _, job := range agent.pending {
				if data := r.markJobTerminalLocked(agent, job, "canceled", "", agent.errText, completedAt); data != nil {
					terminal = append(terminal, data)
				}
			}
			status := researchJobData(agent, agent.currentJob)
			status["status"] = agent.status
			status["error"] = agent.errText
			statuses = append(statuses, status)
		}
		agent.pending = nil
		if agent.currentCancel != nil {
			agent.currentCancel()
		}
	}
	r.mu.Unlock()
	for _, data := range terminal {
		r.appendJobTerminal(data, completedAt, false)
	}
	for _, data := range statuses {
		r.appendTrajectoryAt("research/status", completedAt, data, false)
	}

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
	createdJobs := make([]*chatResearchJob, 0, len(specs))
	createdEvents := make([]map[string]any, 0, len(specs))
	createdAt := make([]time.Time, 0, len(specs))
	for _, spec := range specs {
		id := fmt.Sprintf("agent-%d", len(r.order)+1)
		name := normalizeResearchAgentName(spec.Name, len(r.order)+1)
		task := strings.TrimSpace(spec.Task)
		agent := &chatResearchAgentRun{
			id: id, name: name, task: task, status: "queued", phase: "waiting for a research slot",
			messages: []llm.Message{r.researchSystemMessage(task)},
		}
		job := r.newJobLocked(agent, task, "initial")
		r.agents[id] = agent
		r.order = append(r.order, id)
		created = append(created, agent)
		createdJobs = append(createdJobs, job)
		data := researchJobData(agent, job)
		data["task"] = agent.task
		data["status"] = agent.status
		data["phase"] = agent.phase
		data["createdAt"] = job.queuedAt
		createdEvents = append(createdEvents, data)
		createdAt = append(createdAt, job.queuedAt)
	}
	snapshots := make([]tools.ResearchAgentSnapshot, 0, len(created))
	for _, agent := range created {
		snapshots = append(snapshots, r.snapshotLocked(agent, false))
	}
	r.mu.Unlock()

	for index, agent := range created {
		r.appendTrajectoryAt("research/agent_created", createdAt[index], createdEvents[index], true)
		r.appendJobQueued(agent, createdJobs[index])
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
	job := r.newJobLocked(agent, message, "follow_up")
	if !agent.workerActive {
		agent.status = "queued"
		agent.phase = "follow-up queued"
	}
	snapshot := r.snapshotLocked(agent, false)
	shouldStart := !agent.workerActive
	r.mu.Unlock()
	r.appendJobQueued(agent, job)
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
			result, deliveries := r.waitResultLocked(selected, true)
			r.mu.Unlock()
			r.appendReportDeliveries(deliveries)
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
			result, deliveries := r.waitResultLocked(selected, researchWaitCondition(selected, waitFor))
			r.mu.Unlock()
			r.appendReportDeliveries(deliveries)
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
	completedAt := time.Now().UTC()
	terminal := make([]map[string]any, 0)
	for _, agent := range selected {
		agent.canceled = true
		agent.status = "canceled"
		agent.phase = ""
		agent.errText = "Canceled by the parent chat agent."
		agent.deliveredSequence = agent.sequence
		if data := r.markJobTerminalLocked(agent, agent.currentJob, "canceled", "", agent.errText, completedAt); data != nil {
			terminal = append(terminal, data)
		}
		for _, job := range agent.pending {
			if data := r.markJobTerminalLocked(agent, job, "canceled", "", agent.errText, completedAt); data != nil {
				terminal = append(terminal, data)
			}
		}
		agent.pending = nil
		if agent.currentCancel != nil {
			agent.currentCancel()
		}
	}
	snapshots := make([]tools.ResearchAgentSnapshot, 0, len(selected))
	for _, agent := range selected {
		snapshots = append(snapshots, r.snapshotLocked(agent, false))
	}
	r.mu.Unlock()
	for _, data := range terminal {
		r.appendJobTerminal(data, completedAt, true)
	}
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

func (r *chatResearchRun) waitResultLocked(agents []*chatResearchAgentRun, met bool) (tools.ResearchAgentWaitResult, []map[string]any) {
	snapshots := make([]tools.ResearchAgentSnapshot, 0, len(agents))
	delivered := make([]bool, 0, len(agents))
	reports := 0
	for _, agent := range agents {
		include := agent.sequence > agent.deliveredSequence
		snapshots = append(snapshots, r.snapshotLocked(agent, include))
		delivered = append(delivered, include)
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
	deliveries := make([]map[string]any, 0, reports)
	for index, include := range delivered {
		if !include {
			continue
		}
		agent := agents[index]
		data := researchJobData(agent, agent.lastJob)
		data["status"] = snapshots[index].Status
		data["report"] = snapshots[index].Report
		data["error"] = snapshots[index].Error
		data["reportSequence"] = snapshots[index].Sequence
		data["conditionMet"] = met
		deliveries = append(deliveries, data)
	}
	return tools.ResearchAgentWaitResult{ConditionMet: met, Agents: snapshots}, deliveries
}

func (r *chatResearchRun) appendReportDeliveries(deliveries []map[string]any) {
	for _, data := range deliveries {
		r.appendTrajectory("research/report_delivered", data)
	}
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
		job := agent.pending[0]
		agent.pending = agent.pending[1:]
		agent.currentJob = job
		r.mu.Unlock()

		select {
		case r.semaphore <- struct{}{}:
		case <-r.ctx.Done():
			r.finishCanceledAgent(agent, job)
			return
		}

		r.mu.Lock()
		if r.closed || agent.canceled {
			r.mu.Unlock()
			<-r.semaphore
			r.finishCanceledAgent(agent, job)
			return
		}
		agent.status = "running"
		agent.phase = "investigating"
		job.startedAt = time.Now().UTC()
		jobCtx, cancel := context.WithTimeout(r.ctx, r.researchJobDeadline())
		agent.currentCancel = cancel
		jobStartedAt := job.startedAt
		jobStart := researchJobData(agent, job)
		jobStart["prompt"] = job.prompt
		jobStart["status"] = "running"
		jobStart["startedAt"] = jobStartedAt
		r.mu.Unlock()
		r.appendTrajectoryAt("research/job_start", jobStartedAt, jobStart, true)
		r.publishAgent(agent)

		report, updatedMessages, err := r.runAgentTurn(jobCtx, agent, job)
		cancel()
		<-r.semaphore

		r.mu.Lock()
		agent.currentCancel = nil
		if len(updatedMessages) > 0 {
			agent.messages = updatedMessages
		}
		terminalStatus := "completed"
		switch {
		case agent.canceled || r.closed || errors.Is(err, context.Canceled):
			terminalStatus = "canceled"
			agent.status = "canceled"
			agent.phase = ""
			if agent.errText == "" {
				agent.errText = "Research was canceled."
			}
		case err != nil:
			terminalStatus = "failed"
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
		completedAt := time.Now().UTC()
		terminalReport := ""
		if terminalStatus == "completed" {
			terminalReport = agent.report
		}
		terminalData := r.markJobTerminalLocked(agent, job, terminalStatus, terminalReport, agent.errText, completedAt)
		agent.currentJob = nil
		more := !agent.canceled && !r.closed && len(agent.pending) > 0
		if more {
			agent.status = "queued"
			agent.phase = "follow-up queued"
		} else {
			agent.workerActive = false
		}
		r.mu.Unlock()
		r.appendJobTerminal(terminalData, completedAt, true)
		r.publishAgent(agent)
		r.notify()
		if !more {
			return
		}
	}
}

func (r *chatResearchRun) finishCanceledAgent(agent *chatResearchAgentRun, job *chatResearchJob) {
	r.mu.Lock()
	agent.workerActive = false
	agent.status = "canceled"
	agent.phase = ""
	if agent.errText == "" {
		agent.errText = "Research was canceled."
	}
	completedAt := time.Now().UTC()
	terminal := make([]map[string]any, 0, len(agent.pending)+1)
	if data := r.markJobTerminalLocked(agent, job, "canceled", "", agent.errText, completedAt); data != nil {
		terminal = append(terminal, data)
	}
	for _, pending := range agent.pending {
		if data := r.markJobTerminalLocked(agent, pending, "canceled", "", agent.errText, completedAt); data != nil {
			terminal = append(terminal, data)
		}
	}
	agent.pending = nil
	agent.currentJob = nil
	r.mu.Unlock()
	for _, data := range terminal {
		r.appendJobTerminal(data, completedAt, true)
	}
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

func (r *chatResearchRun) runAgentTurn(ctx context.Context, agent *chatResearchAgentRun, job *chatResearchJob) (string, []llm.Message, error) {
	r.mu.Lock()
	canonical := cloneResearchMessages(agent.messages)
	checkpoint := cloneContextCheckpoint(agent.checkpoint)
	r.mu.Unlock()
	canonical = append(canonical, llm.Message{Role: llm.RoleUser, Content: strings.TrimSpace(job.prompt)})
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
			updated, compressionErr := r.compressAgentContext(ctx, agent, job, settings, canonical, checkpoint, toolSchema, observedTokens, usageSource, round)
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
		requestStartedAt := time.Now().UTC()
		requestData := r.trajectoryRoundData(agent, job, round)
		requestData["request"] = request
		requestData["model"] = request.Model
		requestData["startedAt"] = requestStartedAt
		r.appendTrajectoryAt("research/request_start", requestStartedAt, requestData, true)
		streamResult, err := r.collectResearchStream(ctx, streamer.StreamChat(ctx, request), agent, job, round, requestStartedAt)
		streamError := ""
		if err != nil {
			streamError = err.Error()
		}
		messageData := r.trajectoryRoundData(agent, job, round)
		messageData["content"] = streamResult.content
		messageData["reasoning"] = streamResult.reasoning
		messageData["toolCalls"] = streamResult.toolCalls
		messageData["finishReason"] = streamResult.finishReason
		messageData["usage"] = streamResult.usage
		messageData["streamError"] = streamError
		messageData["completed"] = streamResult.completed
		messageData["startedAt"] = streamResult.startedAt
		messageData["firstTokenAt"] = streamResult.firstTokenAt
		messageData["firstReasoningAt"] = streamResult.firstReasoningAt
		messageData["completedAt"] = streamResult.completedAt
		messageData["durationMs"] = streamResult.completedAt.Sub(streamResult.startedAt).Milliseconds()
		messageData["ttftMs"] = durationUntil(streamResult.startedAt, streamResult.firstTokenAt)
		r.appendTrajectoryAt("research/assistant_message", streamResult.completedAt, messageData, true)
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
			toolStartedAt := time.Now().UTC()
			toolCallData := r.trajectoryRoundData(agent, job, round)
			toolCallData["callId"] = callID
			toolCallData["callOrder"] = callOrder
			toolCallData["tool"] = call.Function.Name
			toolCallData["arguments"] = call.Function.Arguments
			toolCallData["status"] = "running"
			toolCallData["startedAt"] = toolStartedAt
			r.appendTrajectoryAt("research/tool_call", toolStartedAt, toolCallData, true)
			r.updateResearchToolActivity(agent, callID, callOrder, call.Function.Name, call.Function.Arguments, "", false, false)
			toolCtx := r.session.toolContext(ctx, r.toolScopes, nil, nil)
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
			toolCompletedAt := time.Now().UTC()
			resultSuccess := result.Success && marshalErr == nil
			toolResultData := r.trajectoryRoundData(agent, job, round)
			toolResultData["callId"] = callID
			toolResultData["callOrder"] = callOrder
			toolResultData["tool"] = call.Function.Name
			toolResultData["success"] = resultSuccess
			toolResultData["result"] = json.RawMessage(data)
			toolResultData["startedAt"] = toolStartedAt
			toolResultData["completedAt"] = toolCompletedAt
			toolResultData["durationMs"] = toolCompletedAt.Sub(toolStartedAt).Milliseconds()
			r.appendTrajectoryAt("research/tool_result", toolCompletedAt, toolResultData, true)
			r.updateResearchToolActivity(agent, callID, callOrder, call.Function.Name, call.Function.Arguments, string(data), true, resultSuccess)
		}
		r.setAgentPhase(agent, "investigating")
		if visualResult {
			settings, streamer = r.session.manager.server.routeMediaChat(settings, buildCompressedModelHistory(canonical, checkpoint), true)
		}
	}
	return "", canonical, fmt.Errorf("research agent exceeded %d model rounds", maxResearchAgentRounds)
}

func (r *chatResearchRun) compressAgentContext(ctx context.Context, agent *chatResearchAgentRun, job *chatResearchJob, settings llm.Settings, canonical []llm.Message, checkpoint *sessions.ContextCheckpoint, toolSchema []llm.Tool, observedTokens int, usageSource string, round int) (*sessions.ContextCheckpoint, error) {
	compressionID := newSessionID("compression")
	started := time.Now().UTC()
	identity := r.trajectoryRoundData(agent, job, round)
	startData := cloneTrajectoryData(identity)
	startData["compressionId"] = compressionID
	startData["trigger"] = "automatic"
	startData["phase"] = "research"
	startData["thresholdPercent"] = settings.ContextCompressionThresholdPercent
	startData["contextLength"] = settings.ContextLength
	startData["model"] = settings.Model
	startData["endpoint"] = settings.Endpoint
	startData["usageSource"] = usageSource
	startData["startedAt"] = started
	s := r.session
	s.mu.Lock()
	if !s.isActiveLocked(r.turnID) {
		s.mu.Unlock()
		return nil, errChatCanceled
	}
	s.appendTrajectoryLocked("context/compression_start", r.turnID, nil, startData)
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
		errorData := cloneTrajectoryData(identity)
		errorData["compressionId"] = compressionID
		errorData["trigger"] = "automatic"
		errorData["phase"] = "research"
		errorData["thresholdPercent"] = settings.ContextCompressionThresholdPercent
		errorData["contextLength"] = settings.ContextLength
		errorData["model"] = settings.Model
		errorData["endpoint"] = settings.Endpoint
		errorData["usageSource"] = resultUsageSource
		errorData["beforeTokens"] = result.BeforeTokens
		errorData["afterTokens"] = result.AfterTokens
		errorData["reclaimedTokens"] = result.BeforeTokens - result.AfterTokens
		errorData["summaryUsage"] = result.SummaryUsage
		errorData["chunkCount"] = result.ChunkCount
		errorData["recoveryAvailable"] = checkpoint != nil
		errorData["errorClass"] = classifyCompressionError(err)
		errorData["error"] = err.Error()
		errorData["durationMs"] = completed.Sub(started).Milliseconds()
		errorData["completedAt"] = completed
		s.appendTrajectoryLocked(eventType, r.turnID, nil, errorData)
		logf("context compression chat=%s trigger=automatic phase=research agent=%s status=failed before=%d after=%d duration_ms=%d error=%q", s.transcript.ChatID, agent.id, result.BeforeTokens, result.AfterTokens, completed.Sub(started).Milliseconds(), err.Error())
		return nil, err
	}
	result.Checkpoint.LastCompactedAt = completed
	result.Checkpoint.LastAssistantNumber = round
	completeData := cloneTrajectoryData(identity)
	completeData["compressionId"] = compressionID
	completeData["trigger"] = "automatic"
	completeData["phase"] = "research"
	completeData["thresholdPercent"] = settings.ContextCompressionThresholdPercent
	completeData["contextLength"] = settings.ContextLength
	completeData["model"] = settings.Model
	completeData["endpoint"] = settings.Endpoint
	completeData["usageSource"] = result.UsageSource
	completeData["beforeTokens"] = result.BeforeTokens
	completeData["afterTokens"] = result.AfterTokens
	completeData["reclaimedTokens"] = result.BeforeTokens - result.AfterTokens
	completeData["retiredMessages"] = result.RetiredMessages
	completeData["summaryUsage"] = result.SummaryUsage
	completeData["chunkCount"] = result.ChunkCount
	completeData["summary"] = result.Checkpoint.Summary
	completeData["recoveryAvailable"] = true
	completeData["durationMs"] = completed.Sub(started).Milliseconds()
	completeData["completedAt"] = completed
	s.appendTrajectoryLocked("context/compression_complete", r.turnID, nil, completeData)
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

func (r *chatResearchRun) collectResearchStream(ctx context.Context, stream *llm.Stream, agent *chatResearchAgentRun, job *chatResearchJob, round int, startedAt time.Time) (researchStreamResult, error) {
	var content strings.Builder
	var reasoning strings.Builder
	toolCalls := make(map[int]llm.ToolCall)
	result := researchStreamResult{startedAt: startedAt}
	var firstErr error
	trajectoryBuffer := streamTrajectoryBuffer{
		turnID: r.turnID, omitStep: true, eventType: "research/chunk",
		baseData: r.trajectoryRoundData(agent, job, round), chunk: make([]map[string]any, 0, trajectoryStreamChunkEvents),
	}
	flushTrajectory := func() {
		entries := trajectoryBuffer.drain()
		if len(entries) == 0 {
			return
		}
		s := r.session
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.isActiveLocked(r.turnID) {
			s.appendTrajectoryBatchLocked(entries)
		}
	}
	finalize := func(at time.Time) researchStreamResult {
		result.content = content.String()
		result.reasoning = reasoning.String()
		result.toolCalls = orderedToolCalls(toolCalls)
		result.completedAt = at
		return result
	}
	flushTicker := time.NewTicker(trajectoryStreamFlushInterval)
	defer flushTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			flushTrajectory()
			return finalize(time.Now().UTC()), ctx.Err()
		case event, ok := <-stream.Events:
			if !ok {
				flushTrajectory()
				result = finalize(time.Now().UTC())
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
			now := time.Now().UTC()
			if trajectoryBuffer.changesPhase(event.Type) {
				flushTrajectory()
			}
			if trajectoryBuffer.add(event, now) {
				flushTrajectory()
			}
			switch event.Type {
			case llm.EventToken:
				content.WriteString(event.Content)
				if result.firstTokenAt == nil {
					first := now
					result.firstTokenAt = &first
				}
			case llm.EventReasoning:
				reasoning.WriteString(event.Content)
				if result.firstReasoningAt == nil {
					first := now
					result.firstReasoningAt = &first
				}
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
				flushTrajectory()
				return finalize(now), context.Canceled
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
			if event.Type == llm.EventComplete || event.Type == llm.EventError {
				flushTrajectory()
			}
		case <-flushTicker.C:
			if trajectoryBuffer.hasData() {
				flushTrajectory()
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
	job := agent.currentJob
	if job == nil && public.Status == "queued" && len(agent.pending) > 0 {
		job = agent.pending[0]
	}
	if job == nil {
		job = agent.lastJob
	}
	jobID := ""
	if job != nil {
		jobID = job.id
	}
	statusKey := strings.Join([]string{public.Status, public.Phase, public.Error, jobID}, "\x00")
	var statusData map[string]any
	if statusKey != agent.trajectoryStatus {
		agent.trajectoryStatus = statusKey
		statusData = researchJobData(agent, job)
		statusData["status"] = public.Status
		statusData["phase"] = public.Phase
		statusData["error"] = public.Error
	}
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return
	}
	if statusData != nil {
		r.appendTrajectory("research/status", statusData)
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
