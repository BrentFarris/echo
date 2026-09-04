package fossil

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/checkpoint"
)

func (p *Provider) commitProtected(ctx context.Context, state *repositoryState, request sourcecontrol.ActionRequest) ([]string, []string, error) {
	message := strings.TrimSpace(strings.ReplaceAll(request.Message, "\r\n", "\n"))
	if message == "" {
		return nil, nil, &sourcecontrol.Error{Code: "commit_message_required", Message: "commit message is required"}
	}
	manifest, err := p.loadCheckpoint(state)
	if err != nil {
		return nil, nil, err
	}
	if manifest == nil || len(manifest.Entries) == 0 {
		return nil, nil, &sourcecontrol.Error{Code: "no_protected_changes", Message: "there are no Protected Changes to commit"}
	}
	info, err := p.checkoutInfo(ctx, state.workspaceID, state.root)
	if err != nil {
		return nil, nil, err
	}
	if diagnostic := checkpointStale(manifest, state, info.Checkout); diagnostic != "" {
		return nil, nil, &sourcecontrol.Error{Code: "protected_changes_stale", Message: diagnostic}
	}
	if records, statusErr := p.rawStatus(ctx, state); statusErr != nil {
		return nil, nil, statusErr
	} else {
		for _, record := range records {
			if record.group == "conflicts" {
				return nil, nil, &sourcecontrol.Error{Code: "protected_changes_conflict", Message: "resolve merge conflicts before committing Protected Changes"}
			}
		}
	}

	journal, blobs, err := p.captureRecoveryJournal(ctx, state, manifest)
	if err != nil {
		return nil, nil, err
	}
	if err := p.checkpoints.WriteJournal(journal, blobs); err != nil {
		return nil, nil, &sourcecontrol.Error{Code: "protected_changes_recovery_write_failed", Message: "the protected commit was not started because its recovery journal could not be saved", Cause: err}
	}
	// Recheck after the durable journal write. This closes the normal Echo
	// concurrency window and catches an external baseline change before any
	// checkout file is materialized.
	if currentInfo, infoErr := p.checkoutInfo(ctx, state.workspaceID, state.root); infoErr != nil || currentInfo.Checkout != manifest.Baseline {
		clearErr := p.checkpoints.ClearJournal(state.workspaceID, ID, state.repositoryID())
		if clearErr != nil {
			return nil, nil, &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "the protected commit was stopped before changing files, but its temporary recovery journal could not be removed", Cause: errors.Join(infoErr, clearErr)}
		}
		if infoErr != nil {
			return nil, nil, infoErr
		}
		return nil, nil, &sourcecontrol.Error{Code: "protected_changes_stale", Message: "the Fossil checkout changed while the protected commit was starting; no files were changed"}
	}
	if faultErr := p.injectProtectedCommitFault("before_materialization"); faultErr != nil {
		return nil, nil, interruptedProtectedCommit(faultErr)
	}

	rollback := func(stageErr error) ([]string, []string, error) {
		restoreErr := p.restoreJournal(ctx, state, journal, manifest.Baseline, nil)
		if restoreErr == nil {
			_ = p.checkpoints.ClearJournal(state.workspaceID, ID, state.repositoryID())
			return nil, nil, stageErr
		}
		return nil, nil, &sourcecontrol.Error{
			Code:    "protected_changes_recovery_required",
			Message: "the protected commit did not complete and Echo could not fully restore the working files; retry Source Control recovery before editing these paths",
			Cause:   errors.Join(stageErr, restoreErr),
		}
	}

	if err := p.normalizeAffectedToBaseline(ctx, state, manifest.Baseline, journal.Current); err != nil {
		return rollback(err)
	}
	if err := p.materializeProtectedEntries(ctx, state, manifest.Entries, true); err != nil {
		return rollback(err)
	}
	if err := p.verifyMaterialized(state, manifest.Entries); err != nil {
		return rollback(err)
	}
	if faultErr := p.injectProtectedCommitFault("before_commit"); faultErr != nil {
		return nil, nil, interruptedProtectedCommit(faultErr)
	}

	paths := protectedCommitPaths(manifest.Entries)
	args := []string{"commit", "--nosync", "--no-prompt", "--no-warnings", "-m", message}
	args = append(args, fossilPaths(paths)...)
	_, commitErr := p.run(ctx, state.workspaceID, state.root, false, args...)
	newInfo, err := p.checkoutInfo(ctx, state.workspaceID, state.root)
	if err != nil {
		if commitErr != nil {
			return nil, nil, &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "Fossil returned an error and Echo could not determine whether Protected Changes were committed; recovery data was retained", Cause: errors.Join(commitErr, err)}
		}
		return nil, nil, &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "Protected Changes were committed, but Echo could not verify the new check-in; recovery data was retained", Cause: err}
	}
	if newInfo.Checkout == "" || newInfo.Checkout == manifest.Baseline {
		if commitErr != nil {
			return rollback(commitErr)
		}
		return rollback(&sourcecontrol.Error{Code: "protected_commit_not_created", Message: "Fossil did not create a new check-in for Protected Changes"})
	}
	if newInfo.Parent != manifest.Baseline {
		return nil, nil, &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "the new Fossil check-in does not descend directly from the protected baseline; recovery data was retained", Cause: commitErr}
	}
	if err := p.verifyProtectedRevision(ctx, state, manifest, newInfo.Checkout); err != nil {
		return nil, nil, &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "Fossil created a check-in, but its protected contents could not be verified; recovery data was retained", Cause: err}
	}
	if faultErr := p.injectProtectedCommitFault("after_commit"); faultErr != nil {
		return nil, nil, interruptedProtectedCommit(faultErr)
	}
	journal.Phase = journalCommitted
	journal.NewBaseline = newInfo.Checkout
	if err := p.checkpoints.WriteJournal(journal, nil); err != nil {
		return nil, nil, &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "Protected Changes were committed, but the restoration journal could not be advanced; recovery will resume on the next Source Control access", Cause: err}
	}
	if faultErr := p.injectProtectedCommitFault("before_restoration"); faultErr != nil {
		return nil, nil, interruptedProtectedCommit(faultErr)
	}
	if err := p.restoreJournal(ctx, state, journal, newInfo.Checkout, manifest); err != nil {
		return nil, nil, &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "Protected Changes were committed, but later working versions could not be fully restored; recovery will resume on the next Source Control access", Cause: err}
	}
	if err := p.checkpoints.Clear(state.workspaceID, ID, state.repositoryID()); err != nil {
		return nil, nil, &sourcecontrol.Error{Code: "protected_changes_cleanup_failed", Message: "Protected Changes were committed and working files were restored, but recovery metadata could not be removed", Cause: err}
	}
	return paths, nil, nil
}

func (p *Provider) injectProtectedCommitFault(stage string) error {
	if p.protectedCommitFault == nil {
		return nil
	}
	return p.protectedCommitFault(stage)
}

func interruptedProtectedCommit(cause error) error {
	return &sourcecontrol.Error{
		Code: "protected_changes_recovery_required", Message: "the protected commit was interrupted; recovery will resume on the next Source Control access", Cause: cause,
	}
}

func (p *Provider) recoverProtectedCommit(ctx context.Context, state *repositoryState) error {
	journal, err := p.checkpoints.LoadJournal(state.workspaceID, ID, state.repositoryID())
	if err != nil {
		return &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "Protected Changes recovery metadata could not be read", Cause: err}
	}
	if journal == nil {
		return nil
	}
	if journal.Phase != journalPrepared && journal.Phase != journalCommitted {
		return &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "Protected Changes recovery metadata has an unknown transaction phase; recovery data was preserved"}
	}
	if journal.Phase == journalCommitted && strings.TrimSpace(journal.NewBaseline) == "" {
		return &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "Protected Changes recovery metadata is missing its committed check-in; recovery data was preserved"}
	}
	manifest, err := p.loadCheckpoint(state)
	if err != nil {
		return err
	}
	if manifest == nil || journal.CheckoutFingerprint != state.checkoutFingerprint() || manifest.CheckoutFingerprint != state.checkoutFingerprint() {
		return &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "Protected Changes recovery belongs to a different checkout; recovery data was preserved"}
	}
	info, err := p.checkoutInfo(ctx, state.workspaceID, state.root)
	if err != nil {
		return err
	}
	if info.Checkout == journal.Baseline {
		if journal.Phase == journalCommitted {
			return &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "the checkout moved away from the interrupted protected commit; return to its committed check-in before retrying recovery"}
		}
		if err := p.restoreJournal(ctx, state, *journal, journal.Baseline, nil); err != nil {
			return &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "Echo could not restore an interrupted protected commit; recovery data was preserved", Cause: err}
		}
		if err := p.checkpoints.ClearJournal(state.workspaceID, ID, state.repositoryID()); err != nil {
			return err
		}
		return nil
	}
	if journal.NewBaseline != "" && info.Checkout != journal.NewBaseline {
		return &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "the checkout moved again after a protected commit; recovery data was preserved"}
	}
	if info.Parent != journal.Baseline {
		return &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "the current check-in is not the interrupted protected commit; recovery data was preserved"}
	}
	if err := p.verifyProtectedRevision(ctx, state, manifest, info.Checkout); err != nil {
		return &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "the current check-in does not match the interrupted protected commit; recovery data was preserved", Cause: err}
	}
	if err := p.restoreJournal(ctx, state, *journal, info.Checkout, manifest); err != nil {
		return &sourcecontrol.Error{Code: "protected_changes_recovery_required", Message: "Protected Changes were committed, but later working versions still need recovery", Cause: err}
	}
	if err := p.checkpoints.Clear(state.workspaceID, ID, state.repositoryID()); err != nil {
		return err
	}
	return nil
}

// recoverProtectedCommitState records the fail-closed diagnostic while the
// caller holds the canonical checkout lock. A later successful retry clears
// the diagnostic without requiring an application restart.
func (p *Provider) recoverProtectedCommitState(ctx context.Context, state *repositoryState) error {
	err := p.recoverProtectedCommit(ctx, state)
	if err != nil {
		state.recoveryDiagnostic = providerDiagnostic(err)
	} else {
		state.recoveryDiagnostic = ""
	}
	return err
}

func (p *Provider) captureRecoveryJournal(ctx context.Context, state *repositoryState, manifest *checkpoint.Manifest) (checkpoint.Journal, map[string][]byte, error) {
	records, err := p.rawStatus(ctx, state)
	if err != nil {
		return checkpoint.Journal{}, nil, err
	}
	current, blobs, err := p.captureAffectedStates(state, manifest.Entries, records)
	if err != nil {
		return checkpoint.Journal{}, nil, err
	}
	return checkpoint.Journal{
		Version: checkpoint.Version, WorkspaceID: state.workspaceID, ProviderID: ID, RepositoryID: state.repositoryID(),
		CheckoutFingerprint: state.checkoutFingerprint(), Baseline: manifest.Baseline, Phase: journalPrepared, Current: current,
	}, blobs, nil
}

// captureAffectedStates closes over rename endpoints before touching the
// checkout. Fossil revert treats a rename as one operation even when only one
// endpoint is named, so every endpoint must be journaled. A rename that crosses
// the visible workspace boundary is rejected rather than mutating an unseen
// parent-checkout path.
func (p *Provider) captureAffectedStates(state *repositoryState, protected []checkpoint.FileState, records []statusRecord) ([]checkpoint.FileState, map[string][]byte, error) {
	byPath := make(map[string]statusRecord)
	for _, record := range records {
		byPath[pathIdentity(record.path)] = record
	}
	affected := make(map[string]string)
	for _, entry := range protected {
		affected[pathIdentity(entry.Path)] = entry.Path
		if entry.OldPath != "" {
			affected[pathIdentity(entry.OldPath)] = entry.OldPath
		}
	}
	for changed := true; changed; {
		changed = false
		for _, record := range records {
			_, pathAffected := affected[pathIdentity(record.path)]
			_, oldAffected := affected[pathIdentity(record.oldPath)]
			if !pathAffected && !oldAffected {
				continue
			}
			for _, pathValue := range []string{record.path, record.oldPath} {
				if pathValue == "" {
					continue
				}
				key := pathIdentity(pathValue)
				if _, exists := affected[key]; !exists {
					affected[key] = pathValue
					changed = true
				}
			}
		}
	}
	paths := make([]string, 0, len(affected))
	for _, pathValue := range affected {
		if !state.pathAllowed(pathValue) {
			return nil, nil, &sourcecontrol.Error{
				Code: "protected_changes_hidden_rename", Message: "a protected file was renamed across the visible workspace boundary; restore or clear that rename before continuing",
				Details: map[string]any{"path": pathValue},
			}
		}
		paths = append(paths, pathValue)
	}
	sort.SliceStable(paths, func(i, j int) bool { return strings.ToLower(paths[i]) < strings.ToLower(paths[j]) })
	current := make([]checkpoint.FileState, 0, len(paths))
	blobs := make(map[string][]byte)
	for _, pathValue := range paths {
		record := byPath[pathIdentity(pathValue)]
		entry, content, captureErr := p.captureFileState(state, pathValue, record.oldPath, record.code, record.kind)
		if captureErr != nil {
			return nil, nil, captureErr
		}
		current = append(current, entry)
		for id, data := range content {
			blobs[id] = data
		}
	}
	return current, blobs, nil
}

func (p *Provider) normalizeAffectedToBaseline(ctx context.Context, state *repositoryState, baseline string, entries []checkpoint.FileState) error {
	affected := make(map[string]string)
	for _, entry := range entries {
		affected[pathIdentity(entry.Path)] = entry.Path
		if entry.OldPath != "" {
			affected[pathIdentity(entry.OldPath)] = entry.OldPath
		}
	}
	revert := make(map[string]string)
	baselineExists := make(map[string]bool)
	for key, pathValue := range affected {
		_, exists, err := p.revisionFile(ctx, state, baseline, pathValue)
		if err != nil {
			return err
		}
		baselineExists[key] = exists
		if exists {
			revert[key] = pathValue
		}
	}
	records, err := p.rawStatus(ctx, state)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.group == "untracked" {
			continue
		}
		if _, ok := affected[pathIdentity(record.path)]; ok {
			revert[pathIdentity(record.path)] = record.path
		}
		if record.oldPath != "" {
			if _, ok := affected[pathIdentity(record.oldPath)]; ok {
				revert[pathIdentity(record.oldPath)] = record.oldPath
			}
		}
	}
	if len(revert) > 0 {
		paths := mapValues(revert)
		if _, err := p.run(ctx, state.workspaceID, state.root, false, append([]string{"revert", "--noundo", "-r", baseline}, fossilPaths(paths)...)...); err != nil {
			return err
		}
	}
	for key, pathValue := range affected {
		if baselineExists[key] {
			continue
		}
		entry := checkpoint.FileState{Path: pathValue}
		if err := p.materializeFileState(state, entry); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) materializeProtectedEntries(ctx context.Context, state *repositoryState, entries []checkpoint.FileState, forCommit bool) error {
	for _, entry := range entries {
		if entry.Kind == "renamed" && entry.OldPath != "" {
			old := checkpoint.FileState{Path: entry.OldPath}
			if err := p.materializeFileState(state, old); err != nil {
				return err
			}
		}
		if err := p.materializeFileState(state, entry); err != nil {
			return err
		}
		switch entry.Kind {
		case "added":
			if forCommit || entry.StatusCode != "EXTRA" {
				if _, err := p.run(ctx, state.workspaceID, state.root, false, "add", "--force", "./"+entry.Path); err != nil {
					return err
				}
			}
		case "deleted":
			if _, err := p.run(ctx, state.workspaceID, state.root, false, "rm", "--soft", "./"+entry.Path); err != nil {
				return err
			}
		case "renamed":
			if entry.OldPath == "" {
				return fmt.Errorf("protected rename %q has no original path", entry.Path)
			}
			if _, err := p.run(ctx, state.workspaceID, state.root, false, "mv", "--soft", "./"+entry.OldPath, "./"+entry.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Provider) verifyMaterialized(state *repositoryState, entries []checkpoint.FileState) error {
	for _, entry := range entries {
		matched, err := p.fileStateMatchesWorking(state, entry)
		if err != nil {
			return err
		}
		if !matched {
			return &sourcecontrol.Error{Code: "protected_changes_verify_failed", Message: "a protected file changed while the commit was being prepared", Details: map[string]any{"path": entry.Path}}
		}
	}
	return nil
}

func (p *Provider) verifyProtectedRevision(ctx context.Context, state *repositoryState, manifest *checkpoint.Manifest, revision string) error {
	for _, entry := range manifest.Entries {
		data, exists, err := p.revisionFile(ctx, state, revision, entry.Path)
		if err != nil {
			return err
		}
		if exists != entry.Exists {
			return fmt.Errorf("protected path %q has the wrong existence in check-in %s", entry.Path, shortRef(revision))
		}
		if entry.Exists {
			expected, contentErr := p.checkpointContent(state, entry)
			if contentErr != nil {
				return contentErr
			}
			if entry.Symlink {
				expected = []byte(entry.SymlinkTarget)
			}
			if checkpoint.BlobID(data) != checkpoint.BlobID(expected) {
				return fmt.Errorf("protected path %q has different content in check-in %s", entry.Path, shortRef(revision))
			}
		}
		if entry.OldPath != "" {
			if _, oldExists, oldErr := p.revisionFile(ctx, state, revision, entry.OldPath); oldErr != nil {
				return oldErr
			} else if oldExists {
				return fmt.Errorf("renamed path %q still exists in check-in %s", entry.OldPath, shortRef(revision))
			}
		}
	}
	return nil
}

func (p *Provider) restoreJournal(ctx context.Context, state *repositoryState, journal checkpoint.Journal, baseline string, committed *checkpoint.Manifest) error {
	if err := p.normalizeAffectedToBaseline(ctx, state, baseline, journal.Current); err != nil {
		return err
	}
	type baselineState struct{ exists bool }
	base := make(map[string]baselineState)
	for _, entry := range journal.Current {
		_, exists, err := p.revisionFile(ctx, state, baseline, entry.Path)
		if err != nil {
			return err
		}
		base[pathIdentity(entry.Path)] = baselineState{exists: exists}
	}
	for _, entry := range journal.Current {
		if err := p.materializeFileState(state, entry); err != nil {
			return err
		}
	}
	handled := make(map[string]bool)
	for _, entry := range journal.Current {
		if entry.Kind != "renamed" || entry.OldPath == "" {
			continue
		}
		oldPath := journalOldPathAfterCommit(entry.OldPath, committed)
		oldBase := base[pathIdentity(oldPath)]
		newBase := base[pathIdentity(entry.Path)]
		if oldBase.exists && !newBase.exists && entry.Exists {
			if _, err := p.run(ctx, state.workspaceID, state.root, false, "mv", "--soft", "./"+oldPath, "./"+entry.Path); err != nil {
				return err
			}
			handled[pathIdentity(oldPath)] = true
			handled[pathIdentity(entry.Path)] = true
		}
	}
	// Fossil reports a second rename relative to the original checkout. After
	// committing a protected rename, translate that ancestry to the new path.
	// Also recognize a user undoing the protected rename as a reverse rename
	// instead of degrading it to an add/delete pair.
	if committed != nil {
		desired := make(map[string]checkpoint.FileState, len(journal.Current))
		for _, entry := range journal.Current {
			desired[pathIdentity(entry.Path)] = entry
		}
		for _, protected := range committed.Entries {
			if protected.Kind != "renamed" || protected.OldPath == "" {
				continue
			}
			oldKey, newKey := pathIdentity(protected.OldPath), pathIdentity(protected.Path)
			if handled[oldKey] || handled[newKey] {
				continue
			}
			oldDesired, oldKnown := desired[oldKey]
			newDesired, newKnown := desired[newKey]
			if oldKnown && newKnown && oldDesired.Exists && !newDesired.Exists && base[newKey].exists && !base[oldKey].exists {
				if _, err := p.run(ctx, state.workspaceID, state.root, false, "mv", "--soft", "./"+protected.Path, "./"+protected.OldPath); err != nil {
					return err
				}
				handled[oldKey] = true
				handled[newKey] = true
			}
		}
	}
	for _, entry := range journal.Current {
		if handled[pathIdentity(entry.Path)] {
			continue
		}
		was := base[pathIdentity(entry.Path)].exists
		switch {
		case !was && entry.Exists && entry.Kind != "untracked" && entry.StatusCode != "EXTRA":
			if _, err := p.run(ctx, state.workspaceID, state.root, false, "add", "--force", "./"+entry.Path); err != nil {
				return err
			}
		case was && !entry.Exists:
			if _, err := p.run(ctx, state.workspaceID, state.root, false, "rm", "--soft", "./"+entry.Path); err != nil {
				return err
			}
		}
	}
	for _, entry := range journal.Current {
		matched, err := p.fileStateMatchesWorking(state, entry)
		if err != nil {
			return err
		}
		if !matched {
			return fmt.Errorf("working version of %q could not be restored", entry.Path)
		}
	}
	return nil
}

func journalOldPathAfterCommit(oldPath string, committed *checkpoint.Manifest) string {
	if committed == nil {
		return oldPath
	}
	key := pathIdentity(oldPath)
	for _, entry := range committed.Entries {
		if entry.Kind == "renamed" && entry.OldPath != "" && pathIdentity(entry.OldPath) == key {
			return entry.Path
		}
	}
	return oldPath
}

func protectedCommitPaths(entries []checkpoint.FileState) []string {
	values := make(map[string]string)
	for _, entry := range entries {
		values[pathIdentity(entry.Path)] = entry.Path
	}
	return mapValues(values)
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.SliceStable(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result
}

func (p *Provider) restoreProtectedEntries(ctx context.Context, state *repositoryState, manifest *checkpoint.Manifest, entries, current []checkpoint.FileState) error {
	if err := p.normalizeAffectedToBaseline(ctx, state, manifest.Baseline, current); err != nil {
		return err
	}
	if err := p.materializeProtectedEntries(ctx, state, entries, false); err != nil {
		return err
	}
	return p.verifyMaterialized(state, entries)
}
