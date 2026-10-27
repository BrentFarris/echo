package server

import (
	"sync"
	"time"

	"github.com/brent/echo/internal/agentmodes"
	"github.com/brent/echo/internal/workspacegroups"
)

// groupOrchestrator dispatches ready pending tasks from workspace groups to
// the first workspace in the group that is ready. A workspace is ready when it
// has no in-flight chat turns at all, and it can host at most one Group Worker
// chat at a time (enforced again inside the chat send path).
type groupOrchestrator struct {
	server *Server

	mu      sync.Mutex
	started bool
	kickCh  chan struct{}
	done    chan struct{}
}

// dispatchInterval is the fallback poll cadence; kicks from task changes and
// turn completions trigger an immediate pass.
const dispatchInterval = 5 * time.Second

func newGroupOrchestrator(server *Server) *groupOrchestrator {
	return &groupOrchestrator{
		server: server,
		kickCh: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

// start launches the dispatch loop. It is idempotent and safe to call
// repeatedly.
func (o *groupOrchestrator) start() {
	o.mu.Lock()
	if o.started {
		o.mu.Unlock()
		return
	}
	o.started = true
	o.mu.Unlock()
	go o.loop()
}

// stop terminates the dispatch loop. It is safe to call repeatedly and is
// used on server shutdown so background passes never outlive the server.
func (o *groupOrchestrator) stop() {
	o.mu.Lock()
	if !o.started {
		o.started = true // prevent a later start from launching a dead loop
		o.mu.Unlock()
		close(o.done)
		return
	}
	o.mu.Unlock()
	select {
	case <-o.done:
	default:
		close(o.done)
	}
}

// kick requests a dispatch pass without blocking the caller.
func (o *groupOrchestrator) kick() {
	o.mu.Lock()
	start := !o.started
	o.mu.Unlock()
	if start {
		o.start()
		return
	}
	select {
	case o.kickCh <- struct{}{}:
	default:
	}
}

func (o *groupOrchestrator) loop() {
	ticker := time.NewTicker(dispatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-o.done:
			return
		case <-o.kickCh:
		case <-ticker.C:
		}
		select {
		case <-o.done:
			return
		default:
		}
		o.dispatchReadyTasks()
	}
}

// dispatchReadyTasks walks every group's pending queue in creation order and
// dispatches each ready task to the first workspace in its group that is
// ready. At most one task is dispatched per workspace per pass.
func (o *groupOrchestrator) dispatchReadyTasks() {
	if o.server.llm == nil || o.server.workspaceGroups == nil {
		return
	}
	groups, err := o.server.workspaceGroups.List()
	if err != nil {
		logf("orchestrator list groups: %v", err)
		return
	}
	for _, group := range groups {
		if len(group.PendingTasks) == 0 {
			continue
		}
		available := make([]string, 0, len(group.WorkspaceIDs))
		for _, workspaceID := range group.WorkspaceIDs {
			if !o.server.sessions.workspaceBusy(workspaceID) &&
				!o.server.sessions.groupWorkerBusy(workspaceID, "") {
				available = append(available, workspaceID)
			}
		}
		if len(available) == 0 {
			continue
		}
		for _, task := range group.PendingTasks {
			if !o.taskDispatchable(task) {
				continue
			}
			if len(available) == 0 {
				break
			}
			workspaceID := available[0]
			available = available[1:]
			chatID, err := o.server.sessions.sendOrchestrated(workspaceID, inboundMessage{
				WorkspaceID:     workspaceID,
				Message:         task.Prompt,
				Model:           task.Model,
				AgentModeID:     agentmodes.GroupWorkerID,
				ReasoningEffort: task.ReasoningEffort,
				Temperature:     task.Temperature,
			})
			if err != nil {
				logf("orchestrator dispatch task %s to workspace %s: %v", task.ID, workspaceID, err)
				continue
			}
			if err := o.server.workspaceGroups.MarkDispatched(group.ID, task.ID, workspaceID, chatID); err != nil {
				logf("orchestrator mark dispatched task %s: %v", task.ID, err)
			}
			logf("orchestrator dispatched task %s from group %s to workspace %s (chat %s)", task.ID, group.ID, workspaceID, chatID)
			o.server.broadcastWorkspaceGroupsChanged()
		}
	}
}

// taskDispatchable reports whether a pending task is eligible for pickup.
func (o *groupOrchestrator) taskDispatchable(task workspacegroups.PendingTask) bool {
	return task.Ready && task.DispatchedWorkspaceID == ""
}
