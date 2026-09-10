package fossil

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/checkpoint"
)

var simpleName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@{}+-]*$`)

func (p *Provider) Action(ctx context.Context, workspaceID, repositoryID string, request sourcecontrol.ActionRequest) (result sourcecontrol.ActionResult, resultErr error) {
	state, err := p.repository(ctx, workspaceID, repositoryID)
	if err != nil {
		return sourcecontrol.ActionResult{}, err
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.Action = strings.TrimSpace(request.Action)
	if request.RequestID == "" {
		return sourcecontrol.ActionResult{}, &sourcecontrol.Error{Code: "request_id_required", Message: "requestId is required"}
	}
	state.rootMu.Lock()
	defer state.rootMu.Unlock()
	if err := p.recoverProtectedCommitState(ctx, state); err != nil {
		return sourcecontrol.ActionResult{}, err
	}
	if request.ExpectedRevision != 0 && request.ExpectedRevision != state.revision.Load() {
		return sourcecontrol.ActionResult{}, &sourcecontrol.Error{Code: "stale_source_control_revision", Message: "source control changed; refresh and try again", Details: map[string]any{"revision": state.revision.Load()}}
	}
	// Opening Fossil's local web UI does not change checkout state. Keep it out
	// of the mutating action path so it neither advances the revision token nor
	// applies Protected Changes' checkout-changing restrictions.
	if request.Action == "open_ui" {
		if err := p.openFossilUI(state); err != nil {
			return sourcecontrol.ActionResult{}, err
		}
		return sourcecontrol.ActionResult{RequestID: request.RequestID, RepositoryID: repositoryID, Revision: state.revision.Load()}, nil
	}
	// A failed VCS command may still have changed checkout state (for example,
	// a merge that stops on conflicts), so every accepted mutation attempt
	// advances the token. Stale requests return above without changing it.
	defer func() { result.Revision = state.revision.Add(1) }()
	paths, trashIDs, err := p.executeAction(ctx, state, request)
	if err != nil {
		return sourcecontrol.ActionResult{}, err
	}
	return sourcecontrol.ActionResult{RequestID: request.RequestID, RepositoryID: repositoryID, AffectedPaths: paths, TrashIDs: trashIDs}, nil
}

func (p *Provider) executeAction(ctx context.Context, state *repositoryState, request sourcecontrol.ActionRequest) ([]string, []string, error) {
	manifest, err := p.loadCheckpoint(state)
	if err != nil {
		return nil, nil, err
	}
	if manifest != nil && request.Action != "unprotect" && request.Action != "unprotect_all" && request.Action != "sync" && request.Action != "pull" && request.Action != "push" {
		info, infoErr := p.checkoutInfo(ctx, state.workspaceID, state.root)
		if infoErr != nil {
			return nil, nil, infoErr
		}
		if diagnostic := checkpointStale(manifest, state, info.Checkout); diagnostic != "" {
			return nil, nil, &sourcecontrol.Error{Code: "protected_changes_stale", Message: diagnostic}
		}
	}
	if manifest != nil && blockedWhileProtected(request.Action) {
		return nil, nil, &sourcecontrol.Error{
			Code:    "protected_changes_active",
			Message: "Protected Changes are active; commit them or clear protection before changing the checkout",
		}
	}
	switch request.Action {
	case "protect", "protect_all":
		paths, err := p.protect(ctx, state, request)
		return paths, nil, err
	case "unprotect", "unprotect_all":
		paths, err := p.unprotect(state, request)
		return paths, nil, err
	case "commit_protected":
		return p.commitProtected(ctx, state, request)
	case "track":
		paths, err := state.validatePaths(request.Paths)
		if err != nil {
			return nil, nil, err
		}
		records, err := p.rawStatus(ctx, state)
		if err != nil {
			return nil, nil, err
		}
		if err := requireFossilPathState(paths, records, func(record statusRecord) bool { return record.group == "untracked" }, "only untracked files can be tracked"); err != nil {
			return nil, nil, err
		}
		_, err = p.run(ctx, state.workspaceID, state.root, false, append([]string{"add", "--force"}, fossilPaths(paths)...)...)
		return paths, nil, err
	case "untrack":
		paths, err := state.validatePaths(request.Paths)
		if err != nil {
			return nil, nil, err
		}
		records, err := p.rawStatus(ctx, state)
		if err != nil {
			return nil, nil, err
		}
		if err := requireFossilPathState(paths, records, func(record statusRecord) bool { return record.code == "ADDED" }, "only files scheduled for addition can be untracked"); err != nil {
			return nil, nil, err
		}
		_, err = p.run(ctx, state.workspaceID, state.root, false, append([]string{"add", "--reset"}, fossilPaths(paths)...)...)
		return paths, nil, err
	case "discard", "discard_all":
		if !request.Confirmed {
			return nil, nil, &sourcecontrol.Error{Code: "confirmation_required", Message: "discarding changes requires confirmation"}
		}
		return p.discard(ctx, state, request)
	case "commit_all", "commit_selected":
		return p.commit(ctx, state, request)
	case "update", "checkout":
		args := []string{"update", "--nosync"}
		if ref := strings.TrimSpace(request.Ref); ref != "" {
			if err := requireRef(ref); err != nil {
				return nil, nil, err
			}
			args = append(args, ref)
		}
		_, err := p.run(ctx, state.workspaceID, state.root, false, args...)
		return nil, nil, err
	case "sync", "pull", "push":
		_, err := p.run(ctx, state.workspaceID, state.root, true, request.Action)
		return nil, nil, err
	case "merge":
		if err := requireRef(request.Ref); err != nil {
			return nil, nil, err
		}
		_, err := p.run(ctx, state.workspaceID, state.root, false, "merge", "--nosync", strings.TrimSpace(request.Ref))
		return nil, nil, err
	case "create_branch", "create_branch_from":
		name := strings.TrimSpace(request.Name)
		if err := requireName(name, "branch"); err != nil {
			return nil, nil, err
		}
		basis := "current"
		if request.Action == "create_branch_from" && strings.TrimSpace(request.StartPoint) != "" {
			basis = strings.TrimSpace(request.StartPoint)
			if err := requireRef(basis); err != nil {
				return nil, nil, err
			}
		}
		if _, err := p.run(ctx, state.workspaceID, state.root, false, "branch", "new", "--nosync", name, basis); err != nil {
			return nil, nil, err
		}
		_, err := p.run(ctx, state.workspaceID, state.root, false, "update", "--nosync", name)
		return nil, nil, err
	case "stash", "stash_untracked", "stash_snapshot":
		command := "save"
		if request.Action == "stash_snapshot" {
			command = "snapshot"
		}
		args := []string{"stash", command}
		if message := strings.TrimSpace(request.Message); message != "" {
			args = append(args, "-m", message)
		}
		if len(request.Paths) > 0 {
			paths, err := state.validatePaths(request.Paths)
			if err != nil {
				return nil, nil, err
			}
			args = append(args, fossilPaths(paths)...)
		}
		_, err := p.run(ctx, state.workspaceID, state.root, false, args...)
		return request.Paths, nil, err
	case "pop_stash":
		ref := strings.TrimSpace(request.Ref)
		if _, err := strconv.Atoi(ref); err != nil {
			return nil, nil, &sourcecontrol.Error{Code: "invalid_stash", Message: "Fossil stash ID is invalid"}
		}
		if _, err := p.run(ctx, state.workspaceID, state.root, false, "stash", "apply", ref); err != nil {
			return nil, nil, err
		}
		_, err := p.run(ctx, state.workspaceID, state.root, false, "stash", "drop", ref)
		return nil, nil, err
	case "apply_latest_stash", "apply_stash", "pop_latest_stash", "drop_stash":
		verb := "apply"
		if strings.HasPrefix(request.Action, "pop") {
			verb = "pop"
		} else if request.Action == "drop_stash" {
			verb = "drop"
		}
		args := []string{"stash", verb}
		if ref := strings.TrimSpace(request.Ref); ref != "" {
			if _, err := strconv.Atoi(ref); err != nil {
				return nil, nil, &sourcecontrol.Error{Code: "invalid_stash", Message: "Fossil stash ID is invalid"}
			}
			args = append(args, ref)
		}
		_, err := p.run(ctx, state.workspaceID, state.root, false, args...)
		return nil, nil, err
	case "drop_all_stashes":
		metadata, err := p.Metadata(ctx, state.workspaceID, sourcecontrol.RepositoryID(state.workspaceID, ID, state.root))
		if err != nil {
			return nil, nil, err
		}
		ids := make([]int, 0, len(metadata.Stashes))
		for _, stash := range metadata.Stashes {
			if id, parseErr := strconv.Atoi(stash.Ref); parseErr == nil {
				ids = append(ids, id)
			}
		}
		sort.Sort(sort.Reverse(sort.IntSlice(ids)))
		for _, id := range ids {
			if _, err := p.run(ctx, state.workspaceID, state.root, false, "stash", "drop", strconv.Itoa(id)); err != nil {
				return nil, nil, err
			}
		}
		return nil, nil, nil
	default:
		return nil, nil, &sourcecontrol.Error{Code: "unsupported_fossil_action", Message: "unsupported Fossil action"}
	}
}

func blockedWhileProtected(action string) bool {
	switch action {
	case "commit_all", "commit_selected", "update", "checkout", "merge", "create_branch", "create_branch_from",
		"stash", "stash_untracked", "stash_snapshot", "apply_latest_stash", "apply_stash", "pop_latest_stash", "pop_stash":
		return true
	default:
		return false
	}
}

func (p *Provider) discard(ctx context.Context, state *repositoryState, request sourcecontrol.ActionRequest) ([]string, []string, error) {
	records, err := p.rawStatus(ctx, state)
	if err != nil {
		return nil, nil, err
	}
	manifest, err := p.loadCheckpoint(state)
	if err != nil {
		return nil, nil, err
	}
	protected := checkpointEntries(manifest)
	requested := request.Paths
	if request.Action == "discard_all" {
		for _, record := range records {
			if state.pathAllowed(record.path) {
				requested = append(requested, record.path)
			}
		}
		for _, entry := range protected {
			requested = append(requested, entry.Path)
		}
	}
	paths, err := state.validatePaths(requested)
	if err != nil {
		if request.Action == "discard_all" && len(requested) == 0 {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	untracked := make(map[string]bool)
	recordByPath := make(map[string]statusRecord)
	for _, record := range records {
		recordByPath[pathIdentity(record.path)] = record
		if record.group == "untracked" {
			untracked[pathIdentity(record.path)] = true
		}
	}
	tracked := []string{}
	untrackedPaths := []string{}
	protectedToRestore := make(map[string]checkpoint.FileState)
	trashIDs := []string{}
	for _, pathValue := range paths {
		entry, isProtected := protected[pathIdentity(pathValue)]
		if !isProtected {
			if record, ok := recordByPath[pathIdentity(pathValue)]; ok {
				entry, isProtected = protectedEntryForRecord(protected, record)
			}
		}
		if isProtected {
			protectedToRestore[pathIdentity(entry.Path)] = entry
			continue
		}
		if !untracked[pathIdentity(pathValue)] {
			tracked = append(tracked, pathValue)
			continue
		}
		untrackedPaths = append(untrackedPaths, pathValue)
	}
	protectedEntries := make([]checkpoint.FileState, 0, len(protectedToRestore))
	for _, entry := range protectedToRestore {
		protectedEntries = append(protectedEntries, entry)
	}
	sort.SliceStable(protectedEntries, func(i, j int) bool {
		return strings.ToLower(protectedEntries[i].Path) < strings.ToLower(protectedEntries[j].Path)
	})
	var protectedCurrent []checkpoint.FileState
	if len(protectedEntries) > 0 {
		protectedCurrent, _, err = p.captureAffectedStates(state, protectedEntries, records)
		if err != nil {
			return paths, nil, err
		}
	}
	for _, pathValue := range untrackedPaths {
		ref, ok := state.refForPath(pathValue)
		if !ok || ref == nil {
			return paths, trashIDs, &sourcecontrol.Error{Code: "path_outside_workspace", Message: "untracked file is outside this workspace"}
		}
		item, trashErr := p.fs.Trash(state.workspaceID, *ref)
		if trashErr != nil {
			return paths, trashIDs, trashErr
		}
		trashIDs = append(trashIDs, item.ID)
	}
	if len(tracked) > 0 {
		args := append([]string{"revert"}, fossilPaths(tracked)...)
		if _, err := p.run(ctx, state.workspaceID, state.root, false, args...); err != nil {
			return paths, trashIDs, err
		}
	}
	if len(protectedEntries) > 0 {
		if err := p.restoreProtectedEntries(ctx, state, manifest, protectedEntries, protectedCurrent); err != nil {
			return paths, trashIDs, &sourcecontrol.Error{Code: "protected_changes_restore_failed", Message: "the protected files could not be restored", Cause: err}
		}
	}
	return paths, trashIDs, nil
}

func (p *Provider) commit(ctx context.Context, state *repositoryState, request sourcecontrol.ActionRequest) ([]string, []string, error) {
	message := strings.TrimSpace(strings.ReplaceAll(request.Message, "\r\n", "\n"))
	if message == "" {
		return nil, nil, &sourcecontrol.Error{Code: "commit_message_required", Message: "commit message is required"}
	}
	records, err := p.rawStatus(ctx, state)
	if err != nil {
		return nil, nil, err
	}
	hidden, protected := 0, 0
	visibleTracked := []string{}
	missing := make(map[string]string)
	for _, record := range records {
		if record.group == "untracked" {
			continue
		}
		if state.isProtectedPath(record.path) {
			protected++
			continue
		}
		if !state.pathAllowed(record.path) {
			hidden++
			continue
		}
		visibleTracked = append(visibleTracked, record.path)
		if record.code == "MISSING" {
			missing[pathIdentity(record.path)] = record.path
		}
	}
	if protected > 0 {
		return nil, nil, &sourcecontrol.Error{Code: "protected_workspace_metadata", Message: "remove Echo or source-control metadata from the Fossil change set before committing", Details: map[string]any{"count": protected}}
	}
	paths := visibleTracked
	if request.Action == "commit_all" {
		if hidden > 0 {
			return nil, nil, &sourcecontrol.Error{Code: "hidden_changes", Message: "this parent checkout has changes outside the workspace; select the visible files to commit", Details: map[string]any{"count": hidden}}
		}
	} else {
		paths, err = state.validatePaths(request.Paths)
		if err != nil {
			return nil, nil, err
		}
		tracked := make(map[string]bool)
		for _, pathValue := range visibleTracked {
			tracked[pathIdentity(pathValue)] = true
		}
		for _, pathValue := range paths {
			if !tracked[pathIdentity(pathValue)] {
				return nil, nil, &sourcecontrol.Error{Code: "untracked_selection", Message: "track untracked files before committing them"}
			}
		}
	}
	if len(paths) == 0 {
		return nil, nil, &sourcecontrol.Error{Code: "no_changes", Message: "there are no tracked changes to commit"}
	}
	// Fossil distinguishes a file that disappeared from disk (MISSING) from a
	// file scheduled for removal (DELETED). Prepare only the missing files in
	// this commit, and force soft removal so a checkout-level mv-rm-files
	// setting can never delete an on-disk file as a side effect.
	missingPaths := make([]string, 0, len(paths))
	for _, pathValue := range paths {
		if missingPath, ok := missing[pathIdentity(pathValue)]; ok {
			missingPaths = append(missingPaths, missingPath)
		}
	}
	if len(missingPaths) > 0 {
		args := append([]string{"rm", "--soft"}, fossilPaths(missingPaths)...)
		if _, err := p.run(ctx, state.workspaceID, state.root, false, args...); err != nil {
			return nil, nil, err
		}
	}
	args := []string{"commit", "--nosync", "--no-prompt", "--no-warnings", "-m", message}
	if request.Action == "commit_selected" {
		args = append(args, fossilPaths(paths)...)
	}
	_, err = p.run(ctx, state.workspaceID, state.root, false, args...)
	return paths, nil, err
}

func (p *Provider) rawStatus(ctx context.Context, state *repositoryState) ([]statusRecord, error) {
	changes, err := p.run(ctx, state.workspaceID, state.root, false, "changes", "--classify", "--differ", "--dotfiles", "--hash", "--rel-paths")
	if err != nil {
		return nil, err
	}
	records := parseClassifiedChanges(string(changes))
	if extras, extrasErr := p.run(ctx, state.workspaceID, state.root, false, "extras", "--dotfiles", "--rel-paths"); extrasErr == nil {
		records = append(records, parseExtras(string(extras))...)
	}
	return records, nil
}

func fossilPaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	for _, pathValue := range paths {
		result = append(result, "./"+strings.TrimPrefix(pathValue, "./"))
	}
	return result
}

func requireFossilPathState(paths []string, records []statusRecord, allowed func(statusRecord) bool, message string) error {
	states := make(map[string]statusRecord, len(records))
	for _, record := range records {
		states[pathIdentity(record.path)] = record
	}
	for _, pathValue := range paths {
		if record, ok := states[pathIdentity(pathValue)]; !ok || !allowed(record) {
			return &sourcecontrol.Error{Code: "invalid_fossil_path_state", Message: message, Details: map[string]any{"path": pathValue}}
		}
	}
	return nil
}

func requireName(value, kind string) error {
	value = strings.TrimSpace(value)
	if !simpleName.MatchString(value) || strings.Contains(value, "..") || strings.HasPrefix(value, "-") {
		return &sourcecontrol.Error{Code: "invalid_" + kind, Message: kind + " name is invalid"}
	}
	return nil
}

func requireRef(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.ContainsRune(value, 0) {
		return &sourcecontrol.Error{Code: "invalid_fossil_ref", Message: "Fossil reference is invalid"}
	}
	return nil
}
