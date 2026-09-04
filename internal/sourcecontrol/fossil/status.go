package fossil

import (
	"context"
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

func (p *Provider) Status(ctx context.Context, workspaceID, repositoryID string) (sourcecontrol.StatusSnapshot, error) {
	state, err := p.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.StatusSnapshot{}, err
	}
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

	groups := map[string][]sourcecontrol.Change{"conflicts": {}, "working": {}, "untracked": {}}
	hidden := 0
	seen := make(map[string]bool)
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
		ref, _ := state.refForPath(record.path)
		change := sourcecontrol.Change{
			Path: record.path, OldPath: record.oldPath, Ref: ref, Status: statusLabel(record.code),
			StatusCode: record.code, Kind: record.kind, GroupID: record.group,
		}
		groups[record.group] = append(groups[record.group], change)
		if record.group == "conflicts" || strings.Contains(record.code, "MERGE") || strings.Contains(record.code, "INTEGRATE") {
			merge = true
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
		for _, id := range []string{"conflicts", "working", "untracked"} {
			if len(groups[id]) > remaining {
				groups[id] = groups[id][:remaining]
			}
			remaining -= len(groups[id])
			if remaining < 0 {
				remaining = 0
			}
		}
	}
	return sourcecontrol.StatusSnapshot{
		WorkspaceID: state.workspaceID, RepositoryID: sourcecontrol.RepositoryID(state.workspaceID, ID, state.root), ProviderID: ID,
		Revision: state.revision.Load(), Branch: info.Branch, Head: info.Checkout,
		Groups: []sourcecontrol.ChangeGroup{
			{ID: "conflicts", Label: "Merge Changes", Role: "conflicts", Changes: groups["conflicts"], Actions: []string{"discard"}},
			{ID: "working", Label: "Changes", Role: "working", Changes: groups["working"], Actions: []string{"discard", "commit_selected", "untrack"}},
			{ID: "untracked", Label: "Untracked Files", Role: "untracked", Changes: groups["untracked"], Actions: []string{"track", "discard"}},
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
			if oldPath, newPath, ok := strings.Cut(pathValue, " -> "); ok {
				record.oldPath = filepathClean(oldPath)
				record.path = filepathClean(newPath)
			}
		case "UPDATED_BY_MERGE", "UPDATED_BY_INTEGRATE", "UPDATED_BY_CHERRY_PICK", "UPDATED_BY_BACKOUT":
			record.kind = "modified"
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
