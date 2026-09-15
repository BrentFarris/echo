package p4

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/brent/echo/internal/sourcecontrol"
)

const fileMetadataFields = "depotFile,clientFile,haveRev,workRev,action,actionOwner,change,movedFile,movedRev,type,headType,charset,unresolved"

func groupID(change string) string {
	if change == "" || change == "0" || change == "default" {
		return "default"
	}
	return change
}
func (p *Provider) Status(ctx context.Context, workspace, id string) (sourcecontrol.StatusSnapshot, error) {
	r, err := p.repo(ctx, workspace, id)
	if err != nil {
		return sourcecontrol.StatusSnapshot{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return p.status(ctx, r), nil
}

func (p *Provider) status(ctx context.Context, r *repository) sourcecontrol.StatusSnapshot {
	settings := p.Settings(r.workspace).Repositories[r.id]
	active := groupID(settings.Active)
	snapshot := sourcecontrol.StatusSnapshot{WorkspaceID: r.workspace, RepositoryID: r.id, ProviderID: ID, Branch: r.connection.Client, ActiveGroupID: active, TrackingEnabled: settings.Tracking, DetectionIncomplete: r.incomplete, Groups: []sourcecontrol.ChangeGroup{}, Revision: r.revision}
	fail := func(err error) sourcecontrol.StatusSnapshot {
		if r.last.RepositoryID != "" {
			snapshot = r.last
		}
		if len(snapshot.Groups) == 0 {
			snapshot.Groups = []sourcecontrol.ChangeGroup{{ID: "default", Label: "Default", Role: "pending", KeepEmpty: true, Changes: []sourcecontrol.Change{}}}
		}
		snapshot.TrackingEnabled = settings.Tracking && !r.detached
		snapshot.ActiveGroupID = active
		snapshot.Stale, snapshot.Diagnostic = true, "P4 status is stale: "+err.Error()
		r.revision++
		snapshot.Revision = r.revision
		return p.withLocalChanges(r, snapshot)
	}
	changes, err := p.run(ctx, r.connection, "changes", "-s", "pending", "-c", r.connection.Client, "-u", r.connection.User, "-l", "-m", strconv.Itoa(sourcecontrol.StatusLimit+1))
	if err != nil {
		return fail(err)
	}
	groups := map[string]*sourcecontrol.ChangeGroup{}
	makeGroup := func(id, description string) {
		label := "Default"
		if id != "default" {
			label = "Change " + id
		}
		groups[id] = &sourcecontrol.ChangeGroup{ID: id, Label: label, Role: "pending", Description: strings.TrimSpace(description), KeepEmpty: true, Changes: []sourcecontrol.Change{}, Actions: []string{"reopen", "revert", "revert_unchanged"}}
	}
	makeGroup("default", "")
	for _, change := range changes {
		if change["change"] != "" {
			makeGroup(change["change"], change["desc"])
		}
	}
	if len(changes) > sourcecontrol.StatusLimit {
		snapshot.Truncated = true
	}
	opened, err := p.run(ctx, r.connection, "opened", "-C", r.connection.Client, "-u", r.connection.User, "-m", strconv.Itoa(sourcecontrol.StatusLimit+1))
	if err != nil {
		return fail(err)
	}
	if len(opened) > sourcecontrol.StatusLimit {
		snapshot.Truncated = true
		opened = opened[:sourcecontrol.StatusLimit]
	}
	// Only request fstat for files already known to be open, never //... here.
	metadata := map[string]record{}
	for start := 0; start < len(opened); start += 64 {
		args := []string{"fstat", "-T", fileMetadataFields}
		for _, file := range opened[start:min(start+64, len(opened))] {
			if file["depotFile"] != "" {
				args = append(args, file["depotFile"])
			}
		}
		if len(args) == 3 {
			continue
		}
		rows, statErr := p.runInput(ctx, r.connection, args[:3], args[3:])
		if statErr != nil {
			return fail(statErr)
		}
		for _, row := range rows {
			metadata[row["depotFile"]] = row
		}
	}
	for _, file := range opened {
		if file["depotFile"] == "" {
			continue
		}
		id := groupID(file["change"])
		if groups[id] == nil {
			makeGroup(id, "")
		}
		group := groups[id]
		meta := metadata[file["depotFile"]]
		local := meta["clientFile"]
		ref := r.ref(local)
		if ref == nil || local == "" {
			group.HiddenChangeCount++
			snapshot.HiddenChangeCount++
			continue
		}
		if _, err := p.resolve(r, r.relative(local)); err != nil {
			group.HiddenChangeCount++
			snapshot.HiddenChangeCount++
			continue
		}
		if file["action"] == "move/delete" {
			continue
		}
		change := nativeChange(r.relative(local), id, file["action"])
		change.Ref, change.NeedsResolve = ref, meta["unresolved"] != "" && meta["unresolved"] != "0"
		if file["action"] == "move/add" {
			source := metadata[meta["movedFile"]]["clientFile"]
			_, sourceErr := p.resolve(r, r.relative(source))
			if source == "" || r.ref(source) == nil || sourceErr != nil {
				change.Diagnostic = "Move source is outside this workspace; whole-changelist actions are disabled"
				group.HiddenChangeCount++
				snapshot.HiddenChangeCount++
			} else {
				change.OldPath = r.relative(source)
			}
		}
		group.Changes = append(group.Changes, change)
	}
	snapshot.Groups = append(snapshot.Groups, *groups["default"])
	if groups[active] == nil {
		snapshot.Diagnostic = "The active changelist is no longer pending in this client. Choose another active changelist."
	}
	if r.detached {
		snapshot.TrackingEnabled = false
		snapshot.Diagnostic = "These queued operations belong to a previous P4 connection. Review them with their original client; automatic tracking is disabled here."
	}
	ids := []string{}
	for id := range groups {
		if id != "default" {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		left, _ := strconv.Atoi(ids[i])
		right, _ := strconv.Atoi(ids[j])
		return left > right
	})
	for _, id := range ids {
		snapshot.Groups = append(snapshot.Groups, *groups[id])
	}
	snapshot = p.withLocalChanges(r, snapshot)
	if snapshot.Truncated {
		snapshot.Diagnostic = "P4 results are incomplete; whole-changelist actions are disabled"
	}
	r.revision++
	snapshot.Revision = r.revision
	r.last = snapshot
	return snapshot
}

// The local journal is available even when the server is offline. Always merge
// it into a retained snapshot so a successful offline write stays visible.
func (p *Provider) withLocalChanges(r *repository, snapshot sourcecontrol.StatusSnapshot) sourcecontrol.StatusSnapshot {
	groups := []sourcecontrol.ChangeGroup{}
	visible := map[string]bool{}
	for _, group := range snapshot.Groups {
		if group.ID == "local" {
			continue
		}
		groups = append(groups, group)
		for _, change := range group.Changes {
			visible[r.absolute(change.Path)] = true
			if change.OldPath != "" {
				visible[r.absolute(change.OldPath)] = true
			}
		}
	}
	snapshot.Groups = groups
	snapshot.TotalChangeCount = 0
	snapshot.HiddenChangeCount = 0
	snapshot.DetectionIncomplete = r.incomplete
	local := sourcecontrol.ChangeGroup{ID: "local", Label: "Local changes awaiting P4", Role: "local", Changes: []sourcecontrol.Change{}, Actions: []string{"reconcile_preview"}}
	for path, diagnostic := range r.candidates {
		if visible[path] && diagnostic == "" {
			continue
		}
		if ref := r.ref(path); ref != nil {
			local.Changes = append(local.Changes, sourcecontrol.Change{Path: r.relative(path), Ref: ref, GroupID: "local", Status: "untracked", StatusCode: "?", Diagnostic: diagnostic})
		}
	}
	for _, pending := range p.pending(r) {
		for _, file := range pending.Files {
			if file.Registered {
				continue
			}
			path := file.Path
			if file.Destination != "" {
				path = file.Destination
			}
			if ref := r.ref(path); ref != nil {
				found := false
				for i := range local.Changes {
					if local.Changes[i].Path == r.relative(path) {
						local.Changes[i].Diagnostic = pending.Diagnostic
						found = true
					}
				}
				if !found {
					change := sourcecontrol.Change{Path: r.relative(path), Ref: ref, GroupID: "local", Status: "untracked", StatusCode: "!", Diagnostic: pending.Diagnostic}
					if file.Destination != "" {
						change.OldPath = r.relative(file.Path)
					}
					local.Changes = append(local.Changes, change)
				}
			} else {
				local.HiddenChangeCount++
				local.Diagnostic = "Pending registration includes paths outside the currently registered workspace roots"
			}
		}
	}
	if r.incomplete {
		local.KeepEmpty = true
		local.Diagnostic = strings.TrimSpace(local.Diagnostic + " Local change detection is incomplete. Scan a selected folder to review existing or missed changes.")
	}
	p.mu.Lock()
	journalDiagnostic := p.journalDiagnostics[r.id]
	p.mu.Unlock()
	if journalDiagnostic != "" {
		local.KeepEmpty = true
		local.Diagnostic = strings.TrimSpace(local.Diagnostic + " " + journalDiagnostic)
		snapshot.DetectionIncomplete = true
	}
	snapshot.Groups = append(snapshot.Groups, local)
	for i := range snapshot.Groups {
		group := &snapshot.Groups[i]
		sort.Slice(group.Changes, func(i, j int) bool { return group.Changes[i].Path < group.Changes[j].Path })
		snapshot.TotalChangeCount += len(group.Changes)
		snapshot.HiddenChangeCount += group.HiddenChangeCount
		if group.HiddenChangeCount > 0 && group.ID != "local" {
			group.Diagnostic = fmt.Sprintf("%d opened file(s) outside the visible workspace", group.HiddenChangeCount)
		}
	}
	snapshot.TotalChangeCount += snapshot.HiddenChangeCount
	return snapshot
}

func nativeChange(path, group, action string) sourcecontrol.Change {
	status, code := "modified", "M"
	switch action {
	case "add", "branch":
		status, code = "added", "A"
	case "delete", "move/delete":
		status, code = "deleted", "D"
	case "move/add":
		status, code = "renamed", "R"
	}
	return sourcecontrol.Change{Path: filepath.ToSlash(path), GroupID: group, Status: status, StatusCode: code, Kind: action}
}

func (p *Provider) stat(ctx context.Context, r *repository, path string) (record, error) {
	rows, err := p.files(ctx, r.fileConnection(path), []string{"fstat", "-T", fileMetadataFields}, []string{path})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row["depotFile"] != "" {
			return row, nil
		}
	}
	return record{}, nil
}
