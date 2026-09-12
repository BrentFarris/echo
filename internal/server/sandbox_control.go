package server

import (
	"context"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/workspaces"
)

const sandboxResumedGuidance = "The user took control of the shared desktop since your previous observation. Pending actions from that observation were interrupted and must not be replayed. Read affected files again before relying on previous contents. Obtain a fresh ui_observe (or browser_snapshot/desktop snapshot in a legacy custom mode) before further GUI actions. Continue the user's task using the current state."

func sandboxResumeMessage() llm.Message {
	return llm.Message{Role: llm.RoleUser, Name: "echo-desktop-control", Content: sandboxResumedGuidance}
}

func sandboxSystemGuidance(workspace workspaces.Workspace) string {
	if !workspace.Sandbox.Enabled {
		return ""
	}
	return "\n\nWorkspace sandbox is enabled. Shell commands and integrated terminals run in Linux Bash in the SAME runtime container, user home, process space, localhost, and desktop session as the visible Xfce desktop. Use Linux syntax, not Windows PowerShell. DISPLAY, XDG_RUNTIME_DIR and the session D-Bus are already configured. Use labeled workspace paths for tool arguments; they map to /workspace/<root-id> inside Linux. Use echo-browser URL to open the managed visible Chromium browser from the shell, or the available browser tools. Desktop tools are available only when permitted by this agent mode. Use an explicitly backgrounded command with redirected output for applications that should outlive a shell tool call. Take Control pauses new AI actions while existing applications and jobs can keep running; refresh affected files and GUI context after Return Control. Host file and skill tools remain confined to the registered workspace paths." + sandboxUIGuidance
}

const sandboxUIGuidance = "\nFor GUI work, use ui_observe then ui_act on an observed target. Prefer browser accessibility for Chromium and desktop accessibility for native apps. Identify controls by their label and ancestor context; search, expand scopeRef, or paginate when needed. Provide explicit expected results to ui_act or ui_verify. A completed click alone does not establish task success. Fill/check/select/focus have intrinsic verification. On stale or ambiguous targets, observe again. Never repeat an unknown execution without inspecting current state. Use ui_observe with screenshot=true and ui_locate only when semantics cannot identify a control; the Vision endpoint receives a separate image request and does not replace your reasoning model. ui_verify with visual=true returns model-assessed evidence, not deterministic proof. If the runtime reports ui_capability_unavailable, explain that the sandbox image needs refreshing. Explicit custom modes may instead expose legacy browser or desktop tools."

func (s *chatSession) waitForSandboxControl(ctx context.Context) (uint64, error) {
	manager := s.manager.server.sandbox
	if manager == nil {
		return 0, ctx.Err()
	}
	if !manager.IsEnabled(s.workspace.ID) {
		generation, _ := manager.AIControl(s.workspace.ID)
		return generation, ctx.Err()
	}
	_, held := manager.AIControl(s.workspace.ID)
	s.setSandboxHold(held)
	generation, err := manager.WaitForAI(ctx, s.workspace.ID)
	if err == nil {
		s.setSandboxHold(false)
	}
	return generation, err
}

func (s *chatSession) setSandboxHold(held bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.executionHeld == held {
		return
	}
	s.executionHeld = held
	if goal := s.goalByIDLocked(s.active.GoalID); goal != nil && goal.Status == sessions.GoalStatusActive {
		now := time.Now().UTC()
		if held {
			closeGoalActivePeriod(goal, now)
		} else if goal.ActiveSince == nil {
			goal.ActiveSince = &now
		}
	}
	s.emitLocked(map[string]any{"type": "execution_hold", "turnId": s.active.ID, "held": held})
}

func (m *chatSessionManager) sandboxControlChanged(workspaceID string) {
	if m == nil || m.server.sandbox == nil {
		return
	}
	_, held := m.server.sandbox.AIControl(workspaceID)
	m.mu.Lock()
	parent := m.sessions[workspaceID]
	m.mu.Unlock()
	if parent == nil {
		return
	}
	parent.mu.Lock()
	tabs := make([]*chatSession, 0, len(parent.tabs)+1)
	for _, tab := range parent.tabs {
		tabs = append(tabs, tab)
	}
	if parent.codeChat != nil {
		tabs = append(tabs, parent.codeChat)
	}
	parent.mu.Unlock()
	for _, tab := range tabs {
		tab.setSandboxHold(held)
	}
}

// Propagate admission to compression requests, including retries and research.
type sandboxRequestGateKey struct{}

func (s *chatSession) sandboxRequestContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, sandboxRequestGateKey{}, func(ctx context.Context) error {
		_, err := s.waitForSandboxControl(ctx)
		return err
	})
}
func admitSandboxModelRequest(ctx context.Context) error {
	if gate, ok := ctx.Value(sandboxRequestGateKey{}).(func(context.Context) error); ok {
		return gate(ctx)
	}
	return ctx.Err()
}
