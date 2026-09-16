package fossil

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/checkpoint"
	"github.com/brent/echo/internal/sourcecontrol/hunk"
)

func fossilHunkSide(side sourcecontrol.DiffSide) hunk.Side {
	return hunk.Side{Content: side.Content, Exists: side.Exists, EOL: side.EOL, HasBOM: side.HasBOM}
}

func (p *Provider) describeHunks(ctx context.Context, state *repositoryState, document *sourcecontrol.DiffDocument, manifest *checkpoint.Manifest) {
	target := document.Target
	if document.Kind != "text" || (target.Kind != "change" && target.Kind != "") || state.isProtectedPath(target.Path) {
		return
	}
	if target.GroupID != "working" && target.GroupID != "untracked" && target.GroupID != protectedGroupID {
		return
	}
	info, err := p.checkoutInfo(ctx, state.workspaceID, state.root)
	if err != nil || checkpointStale(manifest, state, info.Checkout) != "" {
		return
	}
	records, err := p.rawStatus(ctx, state)
	if err != nil {
		return
	}
	for _, record := range records {
		if pathIdentity(record.path) == pathIdentity(target.Path) && record.group == "conflicts" {
			return
		}
	}
	current, _, err := p.captureFileState(state, target.Path, target.OldPath, "", "")
	if err != nil || current.Symlink {
		return
	}
	entries := checkpointEntries(manifest)
	entry, protected := entries[pathIdentity(target.Path)]
	if entry.Symlink {
		return
	}
	basePath := target.Path
	if protected && entry.OldPath != "" {
		basePath = entry.OldPath
	} else if target.OldPath != "" {
		basePath = target.OldPath
	}
	_, symlink, _, err := p.revisionPermissions(ctx, state, info.Checkout, basePath)
	if err != nil || symlink {
		return
	}
	if target.GroupID == protectedGroupID && !protected {
		return
	}
	document.HunkToken = hunk.Token(document.RepositoryID, target, document.Original, document.Modified, manifest, info.Checkout, current)
	if target.GroupID == protectedGroupID {
		document.HunkActions = []string{"unprotect_hunk"}
	} else {
		document.HunkActions = []string{"protect_hunk", "revert_hunk"}
	}
}

func (p *Provider) applyHunk(ctx context.Context, state *repositoryState, request sourcecontrol.ActionRequest) ([]string, error) {
	block := request.Hunk
	if block == nil || block.Token == "" || (block.Target.Kind != "change" && block.Target.Kind != "") || block.Target.BaseRef != "" || block.Target.Ref != "" {
		return nil, &sourcecontrol.Error{Code: "invalid_hunk", Message: "a current change block is required"}
	}
	if _, err := state.validatePaths([]string{block.Target.Path}); err != nil {
		return nil, err
	}
	target := sourcecontrol.DiffTarget(block.Target)
	document, err := p.diffState(ctx, state, target)
	if err != nil {
		return nil, err
	}
	if document.HunkToken != block.Token || !slices.Contains(document.HunkActions, request.Action) {
		return nil, &sourcecontrol.Error{Code: "stale_diff", Message: "This diff changed. Refresh it and choose the change again."}
	}
	destination, source := fossilHunkSide(document.Original), fossilHunkSide(document.Modified)
	destRange, sourceRange := block.Original, block.Modified
	if request.Action == "unprotect_hunk" {
		destination, source = source, destination
		destRange, sourceRange = sourceRange, destRange
	}
	next, err := hunk.Apply(destination, source, destRange, sourceRange)
	if err != nil {
		return nil, &sourcecontrol.Error{Code: "invalid_hunk", Message: err.Error()}
	}
	if next == destination {
		return nil, &sourcecontrol.Error{Code: "invalid_hunk", Message: "this block has no protected changes"}
	}
	manifest, err := p.loadCheckpoint(state)
	if err != nil {
		return nil, err
	}
	info, err := p.checkoutInfo(ctx, state.workspaceID, state.root)
	if err != nil {
		return nil, err
	}
	entries := checkpointEntries(manifest)
	entry, protected := entries[pathIdentity(target.Path)]
	if !protected {
		records, statusErr := p.rawStatus(ctx, state)
		if statusErr != nil {
			return nil, statusErr
		}
		record := statusRecord{path: target.Path, oldPath: target.OldPath, code: "EDITED", kind: "modified"}
		for _, candidate := range records {
			if pathIdentity(candidate.path) == pathIdentity(target.Path) {
				record = candidate
				break
			}
		}
		entry, _, err = p.captureFileState(state, record.path, record.oldPath, record.code, protectedKind(record))
		if err != nil {
			return nil, err
		}
	}
	entry.Exists, entry.Symlink, entry.SymlinkTarget = next.Exists, false, ""
	if entry.OldPath != "" && !state.pathAllowed(entry.OldPath) {
		return nil, &sourcecontrol.Error{Code: "path_outside_workspace", Message: "both sides of a rename must be inside this workspace", Cause: sourcecontrol.ErrInvalidPath}
	}
	entry.Blob, entry.Hash = "", ""
	blobs := make(map[string][]byte)
	data := hunk.Bytes(next)
	if next.Exists {
		entry.Blob = checkpoint.BlobID(data)
		entry.Hash = entry.Blob
		blobs[entry.Blob] = data
		if entry.Mode == 0 {
			entry.Mode = 0o644
		}
		entry.MissingParents = nil
	}
	basePath := entry.Path
	if entry.OldPath != "" {
		basePath = entry.OldPath
	}
	baseline, baselineExists, err := p.revisionFile(ctx, state, info.Checkout, basePath)
	if err != nil {
		return nil, err
	}
	baselineMode, _, _, err := p.revisionPermissions(ctx, state, info.Checkout, basePath)
	if err != nil {
		return nil, err
	}
	if !protected && baselineExists {
		entry.Mode = (entry.Mode &^ 0o111) | (baselineMode & 0o111)
	}
	if protected && !destination.Exists && next.Exists {
		entry.Mode = baselineMode
	}
	// Retain rename metadata even when its content has returned to baseline.
	if entry.OldPath == "" && next.Exists == baselineExists && bytes.Equal(data, baseline) && (!next.Exists || entry.Mode&0o111 == baselineMode&0o111) {
		delete(entries, pathIdentity(entry.Path))
	} else {
		if entry.OldPath == "" {
			if !next.Exists {
				entry.Kind, entry.StatusCode = "deleted", "DELETED"
			} else if !baselineExists {
				entry.Kind = "added"
				if entry.StatusCode != "EXTRA" {
					entry.StatusCode = "ADDED"
				}
			} else {
				entry.Kind, entry.StatusCode = "modified", "EDITED"
			}
		}
		entries[pathIdentity(entry.Path)] = entry
	}
	fresh, err := p.diffState(ctx, state, target)
	if err != nil {
		return nil, err
	}
	if fresh.HunkToken != block.Token {
		return nil, &sourcecontrol.Error{Code: "stale_diff", Message: "This diff changed. Refresh it and choose the change again."}
	}
	if len(entries) == 0 {
		err = p.checkpoints.Clear(state.workspaceID, ID, state.repositoryID())
	} else {
		ordered := make([]checkpoint.FileState, 0, len(entries))
		for _, candidate := range entries {
			ordered = append(ordered, candidate)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
		if err := validateCheckpointEntries(ordered); err != nil {
			return nil, err
		}
		generation := uint64(1)
		if manifest != nil {
			generation = manifest.Generation + 1
		}
		err = p.checkpoints.ReplaceManifest(checkpoint.Manifest{Version: checkpoint.Version, WorkspaceID: state.workspaceID, ProviderID: ID, RepositoryID: state.repositoryID(), CheckoutFingerprint: state.checkoutFingerprint(), Baseline: info.Checkout, Generation: generation, Entries: ordered}, blobs)
	}
	if err != nil {
		return nil, &sourcecontrol.Error{Code: "protected_changes_write_failed", Message: "Protected Changes could not be saved safely", Cause: err}
	}
	return []string{target.Path}, nil
}

// F cards carry permissions that cannot be recovered from a deleted worktree
// file. A delta manifest may refer to one complete baseline via its B card.
func (p *Provider) revisionPermissions(ctx context.Context, state *repositoryState, revision, path string) (uint32, bool, bool, error) {
	for depth := 0; depth < 2; depth++ {
		artifact, err := p.run(ctx, state.workspaceID, state.root, false, "artifact", revision)
		if err != nil {
			return 0, false, false, err
		}
		baseline := ""
		for _, line := range strings.Split(string(artifact), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "B" {
				baseline = fields[1]
			}
			if len(fields) < 2 || fields[0] != "F" || pathIdentity(strings.ReplaceAll(fields[1], `\s`, " ")) != pathIdentity(path) {
				continue
			}
			if len(fields) == 2 {
				return 0, false, false, nil
			}
			mode, symlink := uint32(0o644), false
			if len(fields) > 3 {
				if strings.Contains(fields[3], "x") {
					mode = 0o755
				}
				symlink = strings.Contains(fields[3], "l")
			}
			return mode, symlink, true, nil
		}
		if baseline == "" {
			return 0, false, false, nil
		}
		revision = baseline
	}
	return 0, false, false, fmt.Errorf("Fossil manifest has an invalid baseline chain")
}
