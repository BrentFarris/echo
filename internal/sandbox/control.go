package sandbox

import "context"

// AIControl is the admission state for all AI activity in one workspace.
// User terminals and running guest processes deliberately do not use this gate.
func (m *Manager) AIControl(workspaceID string) (uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime := m.runtimeFor(workspaceID)
	return runtime.controlGeneration, runtime.lease.Owner == LeaseUser
}

func (m *Manager) signalControlChangedLocked(runtime *runtimeState) {
	close(runtime.controlChanged)
	runtime.controlChanged = make(chan struct{})
}

// WaitForAI uses a broadcast channel, not polling, and can always be stopped.
func (m *Manager) WaitForAI(ctx context.Context, workspaceID string) (uint64, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		m.mu.Lock()
		runtime := m.runtimeFor(workspaceID)
		generation, held, changed := runtime.controlGeneration, runtime.lease.Owner == LeaseUser, runtime.controlChanged
		_, transitioning := m.policy[workspaceID]
		m.mu.Unlock()
		if transitioning {
			return 0, ErrPolicyTransition
		}
		if !held {
			return generation, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-m.closed:
			return 0, context.Canceled
		}
	}
}

// AdmitAIAction is the start boundary: actions admitted before takeover may
// finish, but a result produced before takeover can never admit a new action.
func (m *Manager) AdmitAIAction(workspaceID string, generation uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime := m.runtimeFor(workspaceID)
	if _, transitioning := m.policy[workspaceID]; transitioning {
		return ErrPolicyTransition
	}
	if runtime.lease.Owner == LeaseUser || runtime.controlGeneration != generation {
		return ErrAIActionInterrupted
	}
	return nil
}

func (m *Manager) RecordFileRead(workspaceID, turnID, path string, generation uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime := m.runtimeFor(workspaceID)
	if generation == 0 || generation != runtime.controlGeneration || runtime.lease.Owner == LeaseUser {
		return
	}
	if runtime.freshFiles == nil {
		runtime.freshFiles = make(map[string]uint64)
	}
	runtime.freshFiles[turnID+"\x00"+path] = generation
}
func (m *Manager) FileReadIsFresh(workspaceID, turnID, path string, generation uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime := m.runtimeFor(workspaceID)
	observed, found := runtime.freshFiles[turnID+"\x00"+path]
	return found && observed == generation && generation == runtime.controlGeneration && runtime.lease.Owner != LeaseUser
}

// Keep GUI admission tied to the originating observation even if lazy startup
// delays the call until after a complete take/return cycle.
type aiControlGenerationKey struct{}

func WithAIControlGeneration(ctx context.Context, generation uint64) context.Context {
	return context.WithValue(ctx, aiControlGenerationKey{}, generation)
}
func (m *Manager) checkAIControlContext(ctx context.Context, workspaceID string) error {
	if generation, ok := ctx.Value(aiControlGenerationKey{}).(uint64); ok {
		return m.AdmitAIAction(workspaceID, generation)
	}
	return ctx.Err()
}
