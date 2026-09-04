package fossil

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/brent/echo/internal/sourcecontrol"
)

type statusRecord struct {
	code    string
	path    string
	oldPath string
	group   string
	kind    string
}

var fossilRenameSeparator = regexp.MustCompile(`\s+->\s+`)

func (p *Provider) Status(ctx context.Context, workspaceID, repositoryID string) (sourcecontrol.StatusSnapshot, error) {
	state, err := p.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.StatusSnapshot{}, err
	}
	state.rootMu.Lock()
	if err := p.recoverProtectedCommitState(ctx, state); err != nil {
		state.rootMu.Unlock()
		return sourcecontrol.StatusSnapshot{}, err
	}
	state.rootMu.Unlock()
	state.rootMu.RLock()
	defer state.rootMu.RUnlock()
	return p.loadStatus(ctx, state)
}

func (p *Provider) loadStatus(ctx context.Context, state *repositoryState) (sourcecontrol.StatusSnapshot, error) {
	info, err := p.checkoutInfo(ctx, state.workspaceID, state.root)
	if err != nil {
		return sourcecontrol.StatusSnapshot{}, err
	}
	changesOutput, err := p.run(ctx, state.workspaceID, state.root, false, "changes", "--classify", "--differ", "--dotfiles", "--hash", "--rel-paths")
	if err != nil {
		return sourcecontrol.StatusSnapshot{}, err
	}
	records := parseClassifiedChanges(string(changesOutput))
	extrasOutput, extrasErr := p.run(ctx, state.workspaceID, state.root, false, "extras", "--dotfiles", "--rel-paths")
	if extrasErr == nil {
		records = append(records, parseExtras(string(extrasOutput))...)
	}
	manifest, err := p.loadCheckpoint(state)
	if err != nil {
		return sourcecontrol.StatusSnapshot{}, err
	}
	protectedEntries := checkpointEntries(manifest)
	protectedDiagnostic := checkpointStale(manifest, state, info.Checkout)

	groups := map[string][]sourcecontrol.Change{"conflicts": {}, protectedGroupID: {}, "working": {}, "untracked": {}}
	hidden := 0
	seen := make(map[string]bool)
	seenProtected := make(map[string]bool)
	merge := false
	for _, record := range records {
		key := record.group + "\x00" + pathIdentity(record.path)
		if seen[key] {
			continue
		}
		seen[key] = true
		if state.isProtectedPath(record.path) {
			continue
		}
		if !state.pathAllowed(record.path) {
			if record.group != "untracked" {
				hidden++
			}
			continue
		}
		protectedEntry, recordIsProtected := protectedEntryForRecord(protectedEntries, record)
		if recordIsProtected {
			seenProtected[pathIdentity(protectedEntry.Path)] = true
		}
		if record.group == "conflicts" || strings.Contains(record.code, "MERGE") || strings.Contains(record.code, "INTEGRATE") {
			merge = true
		}
		if record.group == "conflicts" {
			ref, _ := state.refForPath(record.path)
			groups["conflicts"] = append(groups["conflicts"], sourcecontrol.Change{
				Path: record.path, OldPath: record.oldPath, Ref: ref, Status: statusLabel(record.code),
				StatusCode: record.code, Kind: record.kind, GroupID: "conflicts",
			})
			continue
		}
		if recordIsProtected {
			matched, matchErr := p.fileStateMatchesWorking(state, protectedEntry)
			if matchErr != nil {
				return sourcecontrol.StatusSnapshot{}, matchErr
			}
			if !matched {
				copy := record
				groups["working"] = append(groups["working"], p.laterChange(state, protectedEntry, &copy))
			}
			continue
		}
		ref, _ := state.refForPath(record.path)
		change := sourcecontrol.Change{
			Path: record.path, OldPath: record.oldPath, Ref: ref, Status: statusLabel(record.code),
			StatusCode: record.code, Kind: record.kind, GroupID: record.group,
		}
		groups[record.group] = append(groups[record.group], change)
	}
	for _, entry := range protectedEntries {
		groups[protectedGroupID] = append(groups[protectedGroupID], protectedChange(entry, state))
		if seenProtected[pathIdentity(entry.Path)] {
			continue
		}
		matched, matchErr := p.fileStateMatchesWorking(state, entry)
		if matchErr != nil {
			return sourcecontrol.StatusSnapshot{}, matchErr
		}
		if !matched {
			groups["working"] = append(groups["working"], p.laterChange(state, entry, nil))
		}
	}
	for id := range groups {
		sort.SliceStable(groups[id], func(i, j int) bool { return strings.ToLower(groups[id][i].Path) < strings.ToLower(groups[id][j].Path) })
	}
	totalPaths := make(map[string]bool)
	for _, changes := range groups {
		for _, change := range changes {
			totalPaths[pathIdentity(change.Path)] = true
		}
	}
	total := len(totalPaths)
	truncated := total > sourcecontrol.StatusLimit
	if truncated {
		remaining := sourcecontrol.StatusLimit
		for _, id := range []string{"conflicts", protectedGroupID, "working", "untracked"} {
			if len(groups[id]) > remaining {
				groups[id] = groups[id][:remaining]
			}
			remaining -= len(groups[id])
			if remaining < 0 {
				remaining = 0
			}
		}
	}
	workingActions := []string{"discard", "protect", "untrack"}
	untrackedActions := []string{"protect", "track", "discard"}
	protectedActions := []string{}
	if manifest == nil {
		workingActions = append(workingActions, "commit_selected")
	} else {
		// The unprotect action also serves as the provider-neutral signal that
		// a checkpoint exists. Unlike the rendered change count, it is not lost
		// if an unusually large status is truncated before this group.
		protectedActions = append(protectedActions, "unprotect")
		if protectedDiagnostic == "" {
			protectedActions = append(protectedActions, "commit_protected")
		} else {
			workingActions = nil
			untrackedActions = nil
		}
	}
	conflictActions := []string{"discard"}
	if protectedDiagnostic != "" {
		conflictActions = nil
	}
	return sourcecontrol.StatusSnapshot{
		WorkspaceID: state.workspaceID, RepositoryID: sourcecontrol.RepositoryID(state.workspaceID, ID, state.root), ProviderID: ID,
		Revision: state.revision.Load(), Branch: info.Branch, Head: info.Checkout,
		Groups: []sourcecontrol.ChangeGroup{
			{ID: "conflicts", Label: "Merge Changes", Role: "conflicts", Changes: groups["conflicts"], Actions: conflictActions},
			{ID: protectedGroupID, Label: "Protected Changes", Role: "included", Changes: groups[protectedGroupID], Actions: protectedActions, Diagnostic: protectedDiagnostic},
			{ID: "working", Label: "Changes", Role: "working", Changes: groups["working"], Actions: workingActions},
			{ID: "untracked", Label: "Untracked Files", Role: "untracked", Changes: groups["untracked"], Actions: untrackedActions},
		},
		HiddenChangeCount: hidden, Truncated: truncated, TotalChangeCount: total,
		State: sourcecontrol.RepositoryState{MergeInProgress: merge},
	}, nil
}

func parseClassifiedChanges(output string) []statusRecord {
	result := []statusRecord{}
	for _, raw := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		code := strings.ToUpper(strings.TrimSuffix(fields[0], ":"))
		pathValue := strings.TrimSpace(line[len(fields[0]):])
		record := statusRecord{code: code, path: filepathClean(pathValue), group: "working", kind: "modified"}
		switch code {
		case "CONFLICT", "CONFLICT_1", "CONFLICT_2":
			record.group, record.kind = "conflicts", "conflict"
		case "EXTRA":
			record.group, record.kind = "untracked", "untracked"
		case "ADDED", "ADDED_BY_MERGE", "ADDED_BY_INTEGRATE":
			record.kind = "added"
		case "DELETED", "MISSING":
			record.kind = "deleted"
		case "RENAMED":
			record.kind = "renamed"
		case "UPDATED_BY_MERGE", "UPDATED_BY_INTEGRATE", "UPDATED_BY_CHERRY_PICK", "UPDATED_BY_BACKOUT":
			record.kind = "modified"
		}
		// Fossil 2.27 classifies a renamed file whose content also changed as
		// EDITED and prints "old  ->  new". Treat the arrow as the rename
		// signal independently of the leading classification token.
		if record.group != "conflicts" && record.group != "untracked" {
			if endpoints := fossilRenameSeparator.Split(pathValue, 2); len(endpoints) == 2 {
				record.code, record.kind = "RENAMED", "renamed"
				record.oldPath = filepathClean(endpoints[0])
				record.path = filepathClean(endpoints[1])
			}
		}
		if record.path != "" {
			result = append(result, record)
		}
	}
	return result
}

func parseExtras(output string) []statusRecord {
	result := []statusRecord{}
	for _, raw := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		pathValue := filepathClean(strings.TrimSpace(raw))
		if pathValue != "" {
			result = append(result, statusRecord{code: "EXTRA", path: pathValue, group: "untracked", kind: "untracked"})
		}
	}
	return result
}

func filepathClean(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimPrefix(value, "./")
	if clean, err := cleanPath(value); err == nil {
		return clean
	}
	return ""
}

func statusLabel(code string) string {
	labels := map[string]string{
		"EDITED": "Modified", "ADDED": "Added", "DELETED": "Deleted", "MISSING": "Missing",
		"RENAMED": "Renamed", "CONFLICT": "Conflict", "EXTRA": "Untracked",
		"UPDATED_BY_MERGE": "Updated by merge", "ADDED_BY_MERGE": "Added by merge",
		"UPDATED_BY_INTEGRATE": "Updated by integrate", "ADDED_BY_INTEGRATE": "Added by integrate",
	}
	if label := labels[code]; label != "" {
		return label
	}
	return strings.ToLower(strings.ReplaceAll(code, "_", " "))
}
