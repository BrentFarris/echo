package fossil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/checkpoint"
)

const (
	protectedGroupID = "protected"
	journalPrepared  = "prepared"
	journalCommitted = "committed"
)

func (p *Provider) loadCheckpoint(state *repositoryState) (*checkpoint.Manifest, error) {
	manifest, err := p.checkpoints.LoadManifest(state.workspaceID, ID, state.repositoryID())
	if err != nil {
		return nil, &sourcecontrol.Error{Code: "protected_changes_unavailable", Message: "Protected Changes could not be loaded", Cause: err}
	}
	return manifest, nil
}

func checkpointEntries(manifest *checkpoint.Manifest) map[string]checkpoint.FileState {
	entries := make(map[string]checkpoint.FileState)
	if manifest == nil {
		return entries
	}
	for _, entry := range manifest.Entries {
		entries[pathIdentity(entry.Path)] = entry
	}
	return entries
}

// protectedEntryForRecord associates a Fossil status record with the frozen
// file version it extends. Direct path matches cover ordinary edits. Rename
// records also match their old endpoint so a rename made after protection is
// presented as the later version of that protected file instead of as an
// unrelated add/delete pair.
func protectedEntryForRecord(entries map[string]checkpoint.FileState, record statusRecord) (checkpoint.FileState, bool) {
	if entry, ok := entries[pathIdentity(record.path)]; ok {
		return entry, true
	}
	if record.kind != "renamed" || record.oldPath == "" {
		return checkpoint.FileState{}, false
	}
	oldKey := pathIdentity(record.oldPath)
	ordered := make([]checkpoint.FileState, 0, len(entries))
	for _, entry := range entries {
		ordered = append(ordered, entry)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return strings.ToLower(ordered[i].Path) < strings.ToLower(ordered[j].Path) })
	for _, entry := range ordered {
		if oldKey == pathIdentity(entry.Path) || (entry.OldPath != "" && oldKey == pathIdentity(entry.OldPath)) {
			return entry, true
		}
	}
	return checkpoint.FileState{}, false
}

func validateCheckpointEntries(entries []checkpoint.FileState) error {
	endpoints := make(map[string]string)
	for _, entry := range entries {
		for _, pathValue := range []string{entry.Path, entry.OldPath} {
			if pathValue == "" {
				continue
			}
			key := pathIdentity(pathValue)
			if owner, exists := endpoints[key]; exists && owner != pathIdentity(entry.Path) {
				return &sourcecontrol.Error{
					Code: "protected_changes_overlap", Message: "overlapping rename endpoints cannot be protected separately",
					Details: map[string]any{"path": pathValue},
				}
			}
			endpoints[key] = pathIdentity(entry.Path)
		}
	}
	return nil
}

func checkpointStale(manifest *checkpoint.Manifest, state *repositoryState, checkout string) string {
	if manifest == nil {
		return ""
	}
	if manifest.CheckoutFingerprint != state.checkoutFingerprint() {
		return "Protected Changes belong to a different Fossil checkout. Clear protection or reopen the original checkout."
	}
	if manifest.Baseline != checkout {
		return "The Fossil checkout changed after these files were protected. Return to the original check-in or clear protection."
	}
	return ""
}

func (p *Provider) protect(ctx context.Context, state *repositoryState, request sourcecontrol.ActionRequest) ([]string, error) {
	paths, err := state.validatePaths(request.Paths)
	if err != nil {
		return nil, err
	}
	info, err := p.checkoutInfo(ctx, state.workspaceID, state.root)
	if err != nil {
		return nil, err
	}
	manifest, err := p.loadCheckpoint(state)
	if err != nil {
		return nil, err
	}
	if diagnostic := checkpointStale(manifest, state, info.Checkout); diagnostic != "" {
		return nil, &sourcecontrol.Error{Code: "protected_changes_stale", Message: diagnostic}
	}
	records, err := p.rawStatus(ctx, state)
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]statusRecord)
	for _, record := range records {
		byPath[pathIdentity(record.path)] = record
		if record.kind == "renamed" && record.oldPath != "" {
			// This alias lets a generated later-change row which still names the
			// frozen endpoint update protection to the current rename target.
			oldKey := pathIdentity(record.oldPath)
			if _, exists := byPath[oldKey]; !exists {
				byPath[oldKey] = record
			}
		}
	}
	entries := checkpointEntries(manifest)
	blobs := make(map[string][]byte)
	for _, pathValue := range paths {
		record, changed := byPath[pathIdentity(pathValue)]
		existing, alreadyProtected := protectedEntryForRecord(entries, record)
		if !changed {
			existing, alreadyProtected = entries[pathIdentity(pathValue)]
		}
		if changed && record.group == "conflicts" {
			return nil, &sourcecontrol.Error{Code: "protected_changes_conflict", Message: "resolve merge conflicts before protecting files", Details: map[string]any{"path": pathValue}}
		}
		if record.oldPath != "" && !state.pathAllowed(record.oldPath) {
			return nil, &sourcecontrol.Error{Code: "path_outside_workspace", Message: "both sides of a rename must be inside this workspace", Cause: sourcecontrol.ErrInvalidPath}
		}
		if !changed {
			if alreadyProtected {
				// The later working version is the checkout baseline. Updating the
				// frozen version to it is equivalent to removing protection.
				delete(entries, pathIdentity(existing.Path))
				continue
			}
			return nil, &sourcecontrol.Error{Code: "invalid_fossil_path_state", Message: "only changed or untracked files can be protected", Details: map[string]any{"path": pathValue}}
		}
		entry, content, captureErr := p.captureFileState(state, record.path, record.oldPath, record.code, protectedKind(record))
		if captureErr != nil {
			return nil, captureErr
		}
		if alreadyProtected {
			delete(entries, pathIdentity(existing.Path))
		}
		entries[pathIdentity(entry.Path)] = entry
		for id, data := range content {
			blobs[id] = data
		}
	}
	if len(entries) == 0 {
		if err := p.checkpoints.Clear(state.workspaceID, ID, state.repositoryID()); err != nil {
			return nil, &sourcecontrol.Error{Code: "protected_changes_write_failed", Message: "Protected Changes could not be cleared", Cause: err}
		}
		return paths, nil
	}
	ordered := make([]checkpoint.FileState, 0, len(entries))
	for _, entry := range entries {
		ordered = append(ordered, entry)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return strings.ToLower(ordered[i].Path) < strings.ToLower(ordered[j].Path) })
	if err := validateCheckpointEntries(ordered); err != nil {
		return nil, err
	}
	generation := uint64(1)
	if manifest != nil {
		generation = manifest.Generation + 1
	}
	next := checkpoint.Manifest{
		Version: checkpoint.Version, WorkspaceID: state.workspaceID, ProviderID: ID, RepositoryID: state.repositoryID(),
		CheckoutFingerprint: state.checkoutFingerprint(), Baseline: info.Checkout, Generation: generation, Entries: ordered,
	}
	if err := p.checkpoints.ReplaceManifest(next, blobs); err != nil {
		return nil, &sourcecontrol.Error{Code: "protected_changes_write_failed", Message: "Protected Changes could not be saved safely; refresh Source Control before retrying", Cause: err}
	}
	return paths, nil
}

func protectedKind(record statusRecord) string {
	if record.group == "untracked" || record.kind == "added" {
		return "added"
	}
	return record.kind
}

func (p *Provider) unprotect(state *repositoryState, request sourcecontrol.ActionRequest) ([]string, error) {
	manifest, err := p.loadCheckpoint(state)
	if err != nil || manifest == nil {
		return nil, err
	}
	if request.Action == "unprotect_all" {
		if !request.Confirmed {
			return nil, &sourcecontrol.Error{Code: "confirmation_required", Message: "clearing Protected Changes requires confirmation"}
		}
		paths := make([]string, 0, len(manifest.Entries))
		for _, entry := range manifest.Entries {
			paths = append(paths, entry.Path)
		}
		if err := p.checkpoints.Clear(state.workspaceID, ID, state.repositoryID()); err != nil {
			return nil, &sourcecontrol.Error{Code: "protected_changes_write_failed", Message: "Protected Changes could not be cleared", Cause: err}
		}
		return paths, nil
	}
	paths, err := state.validatePaths(request.Paths)
	if err != nil {
		return nil, err
	}
	remove := make(map[string]bool)
	for _, pathValue := range paths {
		remove[pathIdentity(pathValue)] = true
	}
	kept := make([]checkpoint.FileState, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if !remove[pathIdentity(entry.Path)] {
			kept = append(kept, entry)
		}
	}
	if len(kept) == len(manifest.Entries) {
		return nil, &sourcecontrol.Error{Code: "invalid_fossil_path_state", Message: "only protected files can have protection removed"}
	}
	if len(kept) == 0 {
		err = p.checkpoints.Clear(state.workspaceID, ID, state.repositoryID())
	} else {
		manifest.Entries = kept
		manifest.Generation++
		err = p.checkpoints.ReplaceManifest(*manifest, nil)
	}
	if err != nil {
		return nil, &sourcecontrol.Error{Code: "protected_changes_write_failed", Message: "Protected Changes could not be updated", Cause: err}
	}
	return paths, nil
}

func (p *Provider) captureFileState(state *repositoryState, pathValue, oldPath, statusCode, kind string) (checkpoint.FileState, map[string][]byte, error) {
	entry := checkpoint.FileState{Path: pathValue, OldPath: oldPath, StatusCode: statusCode, Kind: kind}
	ref, ok := state.refForPath(pathValue)
	if !ok || ref == nil {
		return entry, nil, &sourcecontrol.Error{Code: "path_outside_workspace", Message: "source control path is outside this workspace", Cause: sourcecontrol.ErrInvalidPath}
	}
	// Resolve the canonical parent without following the final component. A
	// Fossil symlink is content in its own right (its target string), not the
	// file it happens to point at.
	hostPath, err := p.fs.ResolveEntryHostPath(state.workspaceID, *ref)
	if err != nil {
		return entry, nil, &sourcecontrol.Error{Code: "protected_changes_read_failed", Message: "file could not be captured for protection", Cause: err, Details: map[string]any{"path": pathValue}}
	}
	info, err := os.Lstat(hostPath)
	if err != nil {
		if os.IsNotExist(err) {
			return entry, nil, nil
		}
		return entry, nil, err
	}
	if info.IsDir() {
		return entry, nil, &sourcecontrol.Error{Code: "protected_changes_directory", Message: "directories cannot be protected directly", Details: map[string]any{"path": pathValue}}
	}
	entry.Exists = true
	entry.Mode = uint32(info.Mode().Perm())
	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(hostPath)
		if readErr != nil {
			return entry, nil, &sourcecontrol.Error{Code: "protected_changes_read_failed", Message: "symbolic link could not be captured for protection", Cause: readErr}
		}
		entry.Symlink = true
		entry.SymlinkTarget = target
		entry.Hash = checkpoint.BlobID([]byte("symlink\x00" + target))
		return entry, nil, nil
	}
	if !info.Mode().IsRegular() {
		return entry, nil, &sourcecontrol.Error{Code: "protected_changes_special_file", Message: "only regular files and symbolic links can be protected", Details: map[string]any{"path": pathValue}}
	}
	data, err := os.ReadFile(hostPath)
	if err != nil {
		return entry, nil, &sourcecontrol.Error{Code: "protected_changes_read_failed", Message: "file could not be captured for protection", Cause: err}
	}
	entry.Blob = checkpoint.BlobID(data)
	entry.Hash = entry.Blob
	return entry, map[string][]byte{entry.Blob: data}, nil
}

func (p *Provider) checkpointContent(state *repositoryState, entry checkpoint.FileState) ([]byte, error) {
	if !entry.Exists || entry.Symlink {
		return nil, nil
	}
	data, err := p.checkpoints.ReadBlob(state.workspaceID, ID, state.repositoryID(), entry.Blob)
	if err != nil {
		return nil, &sourcecontrol.Error{Code: "protected_changes_corrupt", Message: "Protected Changes content is unavailable; the checkpoint was kept for recovery", Cause: err, Details: map[string]any{"path": entry.Path}}
	}
	return data, nil
}

func (p *Provider) fileStateMatchesWorking(state *repositoryState, entry checkpoint.FileState) (bool, error) {
	current, _, err := p.captureFileState(state, entry.Path, entry.OldPath, entry.StatusCode, entry.Kind)
	if err != nil {
		return false, err
	}
	if current.Exists != entry.Exists || current.Symlink != entry.Symlink {
		return false, nil
	}
	if !entry.Exists {
		return true, nil
	}
	matched := false
	if current.Symlink {
		matched = current.SymlinkTarget == entry.SymlinkTarget
	} else {
		matched = current.Hash == entry.Hash && current.Mode == entry.Mode
	}
	if !matched || entry.OldPath == "" {
		return matched, nil
	}
	oldRef, ok := state.refForPath(entry.OldPath)
	if !ok || oldRef == nil {
		return false, nil
	}
	oldHostPath, oldErr := p.fs.ResolveEntryHostPath(state.workspaceID, *oldRef)
	if oldErr != nil {
		return false, oldErr
	}
	if _, oldErr = os.Lstat(oldHostPath); oldErr == nil {
		return false, nil
	}
	if os.IsNotExist(oldErr) {
		return true, nil
	}
	return false, oldErr
}

func (p *Provider) materializeFileState(state *repositoryState, entry checkpoint.FileState) error {
	ref, ok := state.refForPath(entry.Path)
	if !ok || ref == nil {
		return &sourcecontrol.Error{Code: "path_outside_workspace", Message: "source control path is outside this workspace", Cause: sourcecontrol.ErrInvalidPath}
	}
	hostPath, err := p.fs.ResolveEntryHostPath(state.workspaceID, *ref)
	if err != nil {
		return err
	}
	if !entry.Exists {
		if info, statErr := os.Lstat(hostPath); statErr == nil {
			if info.IsDir() {
				return fmt.Errorf("refusing to replace directory %q", entry.Path)
			}
			return os.Remove(hostPath)
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
		return err
	}
	if info, statErr := os.Lstat(hostPath); statErr == nil {
		if info.IsDir() {
			return fmt.Errorf("refusing to replace directory %q", entry.Path)
		}
		if err := os.Remove(hostPath); err != nil {
			return err
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if entry.Symlink {
		return os.Symlink(entry.SymlinkTarget, hostPath)
	}
	data, err := p.checkpointContent(state, entry)
	if err != nil {
		return err
	}
	mode := os.FileMode(entry.Mode)
	if mode == 0 {
		mode = 0o644
	}
	if err := os.WriteFile(hostPath, data, mode); err != nil {
		return err
	}
	return os.Chmod(hostPath, mode)
}

func protectedChange(entry checkpoint.FileState, state *repositoryState) sourcecontrol.Change {
	ref, _ := state.refForPath(entry.Path)
	code := entry.StatusCode
	if entry.Kind == "added" {
		code = "ADDED"
	}
	if code == "" {
		code = strings.ToUpper(entry.Kind)
	}
	return sourcecontrol.Change{
		Path: entry.Path, OldPath: entry.OldPath, Ref: ref, Status: statusLabel(code), StatusCode: code,
		Kind: entry.Kind, GroupID: protectedGroupID,
	}
}

func (p *Provider) laterChange(state *repositoryState, entry checkpoint.FileState, current *statusRecord) sourcecontrol.Change {
	record := statusRecord{code: "EDITED", path: entry.Path, group: "working", kind: "modified"}
	if current != nil {
		record = *current
		record.group = "working"
	}
	currentState, _, captureErr := p.captureFileState(state, record.path, record.oldPath, record.code, record.kind)
	if captureErr == nil {
		switch {
		case record.kind == "renamed" && pathIdentity(record.path) != pathIdentity(entry.Path):
			// Keep a rename made after protection as a rename in the later
			// working layer.
		case record.kind == "renamed":
			record.code, record.kind, record.oldPath = "EDITED", "modified", ""
		case entry.Exists && !currentState.Exists:
			record.code, record.kind = "MISSING", "deleted"
		case !entry.Exists && currentState.Exists:
			record.code, record.kind = "ADDED", "added"
		default:
			record.code, record.kind = "EDITED", "modified"
		}
	}
	ref, _ := state.refForPath(entry.Path)
	if record.path != "" {
		ref, _ = state.refForPath(record.path)
	}
	return sourcecontrol.Change{
		Path: record.path, OldPath: record.oldPath, Ref: ref, Status: statusLabel(record.code), StatusCode: record.code,
		Kind: record.kind, GroupID: "working",
	}
}
