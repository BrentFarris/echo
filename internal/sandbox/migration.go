package sandbox

import (
	"context"
	"fmt"
	"time"
)

// MigrationEngine copies only sandbox-owned volumes. It never mounts the host
// workspace or changes the original containers' writable layers.
type MigrationEngine interface {
	CopyLegacyVolumes(context.Context, string, MachineState, MachineState) error
}

// BeginUpgrade reserves the workspace synchronously; duplicate requests reuse
// the same operation. The caller stops workspace services before Upgrade runs.
func (m *Manager) BeginUpgrade(workspaceID string) (bool, error) {
	if _, err := m.workspace(workspaceID); err != nil {
		return false, err
	}
	state, err := m.machineState(workspaceID)
	if err != nil {
		return false, err
	}
	if !state.NeedsUpgrade() {
		return false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime := m.runtimeFor(workspaceID)
	if runtime.upgrading {
		return false, nil
	}
	if _, exists := m.policy[workspaceID]; exists {
		return false, ErrPolicyTransition
	}
	select {
	case <-m.closed:
		return false, context.Canceled
	default:
	}
	runtime.upgrading = true
	runtime.upgradeDone = make(chan struct{})
	runtime.status.State = StateUpgrading
	runtime.status.Message = "Preparing sandbox upgrade"
	m.policy[workspaceID] = true
	return true, nil
}

// Upgrade is recoverable after process exit. Original resources are immutable
// during copying, and state.json changes only after the candidate is healthy.
func (m *Manager) Upgrade(ctx context.Context, workspaceID string) (resultErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.mu.Lock()
	m.runtimeFor(workspaceID).upgradeCancel = cancel
	select {
	case <-m.closed:
		cancel()
	default:
	}
	m.mu.Unlock()
	unlock := m.lock(workspaceID)
	defer unlock()
	defer func() {
		m.mu.Lock()
		runtime := m.runtimeFor(workspaceID)
		runtime.upgrading = false
		runtime.upgradeCancel = nil
		if runtime.upgradeDone != nil {
			close(runtime.upgradeDone)
			runtime.upgradeDone = nil
		}
		delete(m.policy, workspaceID)
		m.mu.Unlock()
		if resultErr != nil {
			if journal, exists, err := m.store.LoadMigration(workspaceID); err == nil && exists {
				journal.Status = MigrationStatus{Stage: "failed", Message: "Upgrade failed; original data is retained. Retry Upgrade sandbox. " + resultErr.Error()}
				_ = m.store.SaveMigration(journal)
			}
			m.transition(workspaceID, StateUpgradeRequired, ErrorCode(resultErr), "Upgrade failed; original data is retained. Retry Upgrade sandbox. "+resultErr.Error())
		}
	}()
	if err := m.awaitStartupReconcile(ctx); err != nil {
		return err
	}
	workspace, err := m.workspace(workspaceID)
	if err != nil {
		return err
	}
	state, err := m.machineState(workspaceID)
	if err != nil {
		return err
	}
	if !state.NeedsUpgrade() {
		m.transition(workspaceID, StateStopped, "", "Sandbox is already upgraded")
		return nil
	}
	copier, ok := m.engine.(MigrationEngine)
	if !ok {
		return fmt.Errorf("sandbox engine does not support data migration")
	}
	journal, exists, err := m.store.LoadMigration(workspaceID)
	if err != nil {
		return err
	}
	if !exists {
		candidate := DefaultMachineState(m.installation, workspaceID, m.images)
		candidate.NetworkGrants = append([]NetworkGrant(nil), state.NetworkGrants...)
		candidate.Recovery = append(candidate.Recovery, state)
		candidate.ApprovedSetupDigest = state.ApprovedSetupDigest
		candidate.LastSetup = state.LastSetup
		if state.ApprovedSetupDigest != "" {
			candidate.LastSetup.State = "approval_required"
			candidate.LastSetup.Message = "Review the existing setup script for ECHO_SANDBOX_ROLE=runtime before running it once in the unified environment"
		}
		journal = MigrationJournal{Original: state, Candidate: candidate, Status: MigrationStatus{Stage: "preflight"}}
		if err := m.store.SaveMigration(journal); err != nil {
			return err
		}
	}
	progress := func(stage, message string) error {
		journal.Status = MigrationStatus{Stage: stage, Message: message}
		if err := m.store.SaveMigration(journal); err != nil {
			return err
		}
		m.mu.Lock()
		m.runtimeFor(workspaceID).status.Migration = &MigrationStatus{Stage: stage, Message: message}
		m.mu.Unlock()
		m.transition(workspaceID, StateUpgrading, "", message)
		return nil
	}
	journal.Candidate.Images = m.images
	if err := progress("preflight", "Checking runtime images and workspace access"); err != nil {
		return err
	}
	// Pull before disrupting retained containers. A failed download is retryable.
	if err := m.engine.Pull(ctx, m.images, func(role, message string, percent int) {
		m.emit(Event{WorkspaceID: workspaceID, Event: "image_pull", Role: role, Message: message, Progress: percent})
	}); err != nil {
		return err
	}
	spec, err := m.spec(workspace)
	if err != nil {
		return err
	}
	if err := m.engine.ProbeWorkspace(ctx, spec); err != nil {
		return err
	}
	if preflight, ok := m.engine.(interface {
		PreflightLegacyVolumes(context.Context, string, MachineState, MachineState) error
	}); ok {
		if err := preflight.PreflightLegacyVolumes(ctx, m.installation, journal.Original, journal.Candidate); err != nil {
			return err
		}
	}
	if err := m.engine.Stop(ctx, journal.Original); err != nil {
		return err
	}
	if err := m.engine.Stop(ctx, journal.Candidate); err != nil {
		return err
	}
	if err := m.engine.Delete(ctx, journal.Candidate, DeleteScope{Containers: true, Network: true}); err != nil {
		return err
	}
	// Retry copying from the untouched sources. Partial target contents belong
	// exclusively to this journal and are replaced by the confined copy helper.
	if err := progress("copying", "Preserving homes, browser sessions, and exchange files"); err != nil {
		return err
	}
	if err := copier.CopyLegacyVolumes(ctx, m.installation, journal.Original, journal.Candidate); err != nil {
		return err
	}
	if err := progress("validating", "Starting and validating the unified environment"); err != nil {
		return err
	}
	secrets, err := m.credentials(workspaceID)
	if err != nil {
		return err
	}
	candidate := journal.Candidate
	defer func() {
		if resultErr != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = m.engine.Stop(cleanup, candidate)
		}
	}()
	ensured, err := m.engine.Ensure(ctx, spec, candidate, secrets)
	if err != nil {
		return err
	}
	candidate = ensured
	if err := m.engine.Start(ctx, candidate); err != nil {
		return err
	}
	if result, err := m.engine.Exec(ctx, candidate, ExecRequest{Command: []string{"/bin/bash", "-lc", "test -n \"$DISPLAY\" && test -S \"$XDG_RUNTIME_DIR/bus\" && command -v git && command -v go && command -v node"}}); err != nil {
		return err
	} else if result.ExitCode != 0 {
		return fmt.Errorf("candidate runtime validation exited with status %d", result.ExitCode)
	}
	if err := m.store.Save(candidate); err != nil {
		return err
	}
	// state.json is the commit record; an interrupted journal write after it
	// must never cause copying over the now-live runtime on retry.
	journal.Candidate = candidate
	journal.Status = MigrationStatus{Stage: "complete", Message: "Upgrade complete; original sandbox data is retained for recovery"}
	_ = m.store.SaveMigration(journal)
	m.mu.Lock()
	m.runtimeFor(workspaceID).status.Migration = &journal.Status
	m.mu.Unlock()
	m.transition(workspaceID, StateReady, "", journal.Status.Message)
	return nil
}
