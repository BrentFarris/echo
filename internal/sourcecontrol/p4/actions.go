package p4

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/brent/echo/internal/sourcecontrol"
)

func (p *Provider) Action(ctx context.Context, workspace, id string, request sourcecontrol.ActionRequest) (result sourcecontrol.ActionResult, resultErr error) {
	defer func() {
		if resultErr != nil {
			var coded *sourcecontrol.Error
			if !errors.As(resultErr, &coded) {
				resultErr = &sourcecontrol.Error{Code: "p4_action_failed", Message: resultErr.Error(), Cause: resultErr}
			}
		}
	}()
	r, err := p.repo(ctx, workspace, id)
	if err != nil {
		return result, err
	}
	result = sourcecontrol.ActionResult{RequestID: request.RequestID, RepositoryID: id}
	paths := []string{}
	if request.Action == "reconcile_apply" {
		r.mu.Lock()
		for _, path := range r.previews[request.PreviewToken].Paths {
			paths = append(paths, path)
		}
		r.mu.Unlock()
	}
	for _, path := range request.Paths {
		r.mu.Lock()
		resolved, err := p.resolve(r, path)
		r.mu.Unlock()
		if err != nil {
			return result, err
		}
		paths = append(paths, resolved)
	}
	// Expand groups using a fresh snapshot, then release the client lock before
	// taking workspace path locks. All mutation entry points use this order.
	if len(paths) == 0 && request.GroupID != "" && (request.Action == "revert" || request.Action == "revert_unchanged") {
		r.mu.Lock()
		snapshot := p.status(ctx, r)
		r.mu.Unlock()
		if snapshot.Stale || snapshot.Truncated {
			return result, errors.New("refresh complete P4 status before changing a whole changelist")
		}
		for _, group := range snapshot.Groups {
			if group.ID == request.GroupID {
				if group.HiddenChangeCount > 0 {
					return result, errors.New("changelist includes hidden paths; select visible files instead")
				}
				for _, file := range group.Changes {
					resolved, err := p.resolve(r, file.Path)
					if err != nil {
						return result, err
					}
					paths = append(paths, resolved)
				}
			}
		}
	}
	// Reverting a move also touches its source. Lock and back up both endpoints,
	// then verify their pairing again after acquiring all mutation locks.
	lockPaths := append([]string{}, paths...)
	moveSources := map[string]string{}
	if request.Action == "revert" || request.Action == "revert_unchanged" {
		r.mu.Lock()
		for _, path := range paths {
			source, err := p.moveSource(ctx, r, path)
			if err != nil {
				r.mu.Unlock()
				return result, err
			}
			if source != "" {
				moveSources[path] = source
				lockPaths = append(lockPaths, source)
			}
		}
		r.mu.Unlock()
	}
	unlock := p.fs.LockMutationPaths(lockPaths...)
	defer unlock()
	r.mu.Lock()
	defer func() {
		r.mu.Unlock()
		if request.Action == "set_tracking" {
			p.updateWatch(workspace)
		}
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// A whole-list request is only valid while the complete visible membership
	// remains the same, including changes made in P4V between our two locks.
	if len(request.Paths) == 0 && request.GroupID != "" && (request.Action == "revert" || request.Action == "revert_unchanged") {
		snapshot := p.status(ctx, r)
		if snapshot.Stale || snapshot.Truncated {
			return result, errors.New("P4 results became incomplete; refresh before reverting")
		}
		visible := map[string]bool{}
		for _, path := range paths {
			visible[r.relative(path)] = true
		}
		for _, group := range snapshot.Groups {
			if group.ID == request.GroupID {
				if group.HiddenChangeCount > 0 || len(group.Changes) != len(paths) {
					return result, errors.New("changelist membership changed; refresh before reverting")
				}
				for _, change := range group.Changes {
					if !visible[change.Path] {
						return result, errors.New("changelist membership changed; refresh before reverting")
					}
				}
			}
		}
	}
	target := groupID(request.TargetGroupID)
	if request.TargetGroupID == "" {
		target = groupID(p.Settings(workspace).Repositories[id].Active)
	}
	validateChange := func(change string) error {
		if change == "default" {
			return nil
		}
		if _, err := strconv.ParseUint(change, 10, 32); err != nil {
			return errors.New("invalid pending changelist")
		}
		rows, err := p.run(ctx, r.connection, "change", "-o", change)
		if err != nil {
			return err
		}
		if len(rows) == 0 || rows[0]["Status"] != "pending" || rows[0]["Client"] != r.connection.Client || rows[0]["User"] != r.connection.User {
			return errors.New("changelist must be pending and owned by this user/client")
		}
		return nil
	}
	var journal *operationRecord
	begin := func() error {
		if journal != nil || len(paths) == 0 {
			return nil
		}
		if (request.Action == "revert" || request.Action == "revert_unchanged" || request.Action == "reconcile_apply") && !request.Confirmed {
			return errors.New("review and confirm the action first")
		}
		record := operationRecord{ID: fmt.Sprint(time.Now().UnixNano()), Workspace: workspace, Repository: id, Connection: r.connection, ClientRoot: r.root, Scopes: r.roots, Kind: request.Action, Origin: "source-control", Created: time.Now()}
		for _, path := range lockPaths {
			meta, err := p.stat(ctx, r, path)
			if err != nil {
				return err
			}
			record.Files = append(record.Files, intent{Path: path, Prior: meta, Change: target})
		}
		if err := p.persist(&record); err != nil {
			return err
		}
		journal = &record
		return nil
	}
	defer func() {
		if journal != nil {
			if resultErr != nil {
				journal.Diagnostic = "P4 action may have partially completed; review these paths before retrying: " + resultErr.Error()
			} else {
				journal.Complete = true
			}
			if err := p.persist(journal); err != nil && resultErr == nil {
				result.Diagnostic = "P4 action completed, but its recovery record could not be finalized: " + err.Error()
			}
		}
	}()
	switch request.Action {
	case "set_tracking":
		if r.detached && request.Confirmed {
			return result, errors.New("restore this P4 connection before enabling automatic tracking")
		}
		settings := p.Settings(workspace)
		value := settings.Repositories[id]
		value.Tracking = request.Confirmed
		value.Active = groupID(value.Active)
		settings.Repositories[id] = value
		if err := p.saveRepositorySettings(workspace, settings, r); err != nil {
			return result, err
		}
	case "set_active":
		if err := validateChange(target); err != nil {
			return result, err
		}
		settings := p.Settings(workspace)
		value := settings.Repositories[id]
		value.Active = target
		settings.Repositories[id] = value
		if err := p.saveRepositorySettings(workspace, settings, r); err != nil {
			return result, err
		}
		result.GroupID = target
	case "create_change", "edit_change":
		if strings.TrimSpace(request.Message) == "" {
			return result, errors.New("a changelist description is required")
		}
		args := []string{"change", "-o"}
		if request.Action == "edit_change" {
			if request.GroupID == "default" {
				return result, errors.New("Default has no editable description")
			}
			if err := validateChange(request.GroupID); err != nil {
				return result, err
			}
			args = append(args, request.GroupID)
		}
		formConnection := r.connection
		if formConnection.Unicode {
			formConnection.CommandCharset = "utf8"
		}
		form, err := p.command(ctx, formConnection, nil, false, args...)
		if err != nil {
			return result, err
		}
		text := replaceField(string(form), "Description", request.Message)
		if request.Action == "create_change" {
			text = replaceField(text, "Files", "")
			text = replaceField(text, "Jobs", "")
			text = replaceField(text, "Stream", "")
		}
		output, err := p.command(ctx, formConnection, []byte(text), false, "change", "-i")
		if err != nil {
			return result, err
		}
		result.GroupID = request.GroupID
		if request.Action == "create_change" {
			match := regexp.MustCompile(`Change (\d+) created`).FindStringSubmatch(string(output))
			if len(match) < 2 {
				return result, errors.New("P4 created a changelist but did not return its identity; refresh before retrying")
			}
			result.GroupID = match[1]
			settings := p.Settings(workspace)
			value := settings.Repositories[id]
			value.Active = match[1]
			settings.Repositories[id] = value
			if err := p.saveRepositorySettings(workspace, settings, r); err != nil {
				return result, err
			}
		}
	case "checkout", "reopen":
		if len(paths) == 0 {
			return result, nil
		}
		if err := validateChange(target); err != nil {
			return result, err
		}
		for _, path := range paths {
			if _, err := p.mapping(ctx, r, path); err != nil {
				return result, err
			}
			meta, err := p.stat(ctx, r, path)
			if err != nil {
				return result, err
			}
			if request.GroupID != "" && request.GroupID != "local" && groupID(meta["change"]) != request.GroupID {
				return result, errors.New("file changelist changed; refresh before retrying")
			}
			if err := validateActionOwner(r, meta); err != nil {
				return result, err
			}
			if request.Action == "checkout" && meta["action"] != "" {
				continue
			}
			if err := begin(); err != nil {
				return result, err
			}
			if request.Action == "checkout" {
				if err := p.checkout(ctx, r, path, target); err != nil {
					return result, err
				}
			} else if _, err := p.files(ctx, r.fileConnection(path), []string{"reopen", "-c", target}, []string{path}); err != nil {
				return result, err
			}
		}
	case "reconcile_preview", "scan_folder":
		if err := validateChange(target); err != nil {
			return result, err
		}
		preview, err := p.makePreview(ctx, r, paths, target, request.Action == "scan_folder")
		if err != nil {
			return result, err
		}
		result.Preview = &preview
	case "reconcile_apply":
		if !request.Confirmed {
			return result, errors.New("review and confirm the reconciliation preview first")
		}
		if err := p.applyPreview(ctx, r, request.PreviewToken, begin); err != nil {
			return result, err
		}
	case "retry_registration":
		if err := p.retry(ctx, r, paths); err != nil {
			return result, err
		}
	case "revert", "revert_unchanged":
		if len(paths) == 0 {
			return result, nil
		}
		if !request.Confirmed {
			return result, errors.New("confirm reverting the selected files before overwriting local work")
		}
		// Revalidate group and move membership after taking both locks.
		for _, path := range paths {
			meta, err := p.stat(ctx, r, path)
			if err != nil {
				return result, err
			}
			if meta["action"] == "" {
				return result, errors.New("selected file is no longer opened")
			}
			if err := validateActionOwner(r, meta); err != nil {
				return result, err
			}
			if request.GroupID != "" && groupID(meta["change"]) != request.GroupID {
				return result, errors.New("file changelist changed; refresh before reverting")
			}
			if meta["action"] == "move/delete" {
				return result, errors.New("select the move destination to revert a native move pair")
			}
			if meta["action"] == "move/add" {
				source, err := p.moveSource(ctx, r, path)
				if err != nil {
					return result, err
				}
				if source == "" || source != moveSources[path] {
					return result, errors.New("move pairing changed; refresh before reverting")
				}
			}
		}
		backup, err := p.backup(ctx, r, lockPaths)
		if err != nil {
			return result, err
		}
		result.Diagnostic = "Saved pre-revert content in " + backup
		args := []string{"revert"}
		if request.Action == "revert_unchanged" {
			args = append(args, "-a")
		}
		if err := begin(); err != nil {
			return result, err
		}
		if _, err := p.filesInScopes(ctx, r, args, paths); err != nil {
			return result, err
		}
	default:
		return result, &sourcecontrol.Error{Code: "unsupported_source_control_capability", Message: "P4 does not support this action in Echo"}
	}
	r.revision++
	result.Revision = r.revision
	result.AffectedPaths = request.Paths
	return result, nil
}

func (p *Provider) moveSource(ctx context.Context, r *repository, path string) (string, error) {
	meta, err := p.stat(ctx, r, path)
	if err != nil || meta["action"] != "move/add" {
		return "", err
	}
	rows, err := p.runInput(ctx, r.connection, []string{"where"}, []string{meta["movedFile"]})
	if err != nil {
		return "", err
	}
	if len(rows) == 0 || rows[len(rows)-1]["path"] == "" {
		return "", errors.New("move source could not be resolved")
	}
	return p.resolve(r, r.relative(rows[len(rows)-1]["path"]))
}

func (p *Provider) saveRepositorySettings(workspace string, settings Settings, r *repository) error {
	value := settings.Repositories[r.id]
	c := r.connection
	value.Connection = &c
	value.ClientRoot = r.root
	value.RootIDs = nil
	value.Overrides = map[string]Override{}
	for _, root := range r.roots {
		value.RootIDs = append(value.RootIDs, root.ID)
		value.Overrides[root.ID] = settings.Roots[root.ID]
	}
	settings.Repositories[r.id] = value
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.config.Workspaces[workspace]
	p.config.Workspaces[workspace] = settings
	if err := writeJSON(filepath.Join(p.dataDir, "settings.json"), p.config); err != nil {
		p.config.Workspaces[workspace] = old
		return err
	}
	return nil
}

// Preserve all unrelated form fields and multiline values when editing a spec.
func replaceField(form, key, value string) string {
	lines := strings.Split(strings.ReplaceAll(form, "\r\n", "\n"), "\n")
	out := []string{}
	found := false
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], key+":") {
			found = true
			out = append(out, key+":")
			if value != "" {
				for _, line := range strings.Split(value, "\n") {
					out = append(out, "\t"+line)
				}
			}
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], "\t") || strings.HasPrefix(lines[i+1], " ")) {
				i++
			}
		} else {
			out = append(out, lines[i])
		}
	}
	if !found && value != "" {
		out = append(out, key+":")
		for _, line := range strings.Split(value, "\n") {
			out = append(out, "\t"+line)
		}
	}
	return strings.Join(out, "\n") + "\n"
}
func (p *Provider) backup(ctx context.Context, r *repository, paths []string) (string, error) {
	directory := filepath.Join(p.dataDir, "backups", r.id, fmt.Sprint(time.Now().UnixNano()))
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	manifest := map[string]string{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		input, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		name := digest(path)
		output, err := os.OpenFile(filepath.Join(directory, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			input.Close()
			return "", err
		}
		_, err = io.Copy(output, contextReader{ctx, input})
		input.Close()
		if err == nil {
			err = output.Sync()
		}
		closeErr := output.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		manifest[name] = path
	}
	if err := writeJSON(filepath.Join(directory, "paths.json"), manifest); err != nil {
		return "", err
	}
	return directory, nil
}
