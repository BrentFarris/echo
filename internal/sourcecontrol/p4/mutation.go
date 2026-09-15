package p4

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brent/echo/internal/mutation"
	"github.com/brent/echo/internal/workspacefs"
)

var errUnmapped = errors.New("path is excluded from the P4 client view")

func (p *Provider) Tracks(workspace, path string) bool {
	if p.disabled(workspace) {
		return false
	}
	settings := p.Settings(workspace)
	enabled := false
	for _, settings := range settings.Repositories {
		enabled = enabled || settings.Tracking
	}
	if !enabled {
		return false
	}
	// Discovery is cached and bounded. An enabled identity stays enabled offline.
	states, _ := p.discover(context.Background(), workspace)
	for _, r := range states {
		r.mu.Lock()
		matches := !r.detached && settings.Repositories[r.id].Tracking && r.ref(path) != nil
		r.mu.Unlock()
		if matches {
			return true
		}
	}
	return false
}

func (p *Provider) Handles(op mutation.Operation) bool {
	if op.Kind != "restore" {
		return false
	}
	record, err := p.recovery(op.WorkspaceID, op.RecoveryID)
	return record != nil || err != nil
}

func (p *Provider) Run(ctx context.Context, op mutation.Operation, apply func() error) (mutation.Result, error) {
	if err := ctx.Err(); err != nil {
		return mutation.Result{}, err
	}
	var restored *operationRecord
	if op.Kind == "restore" {
		var err error
		restored, err = p.recovery(op.WorkspaceID, op.RecoveryID)
		if err != nil {
			return mutation.Result{}, err
		}
	}
	if p.disabled(op.WorkspaceID) {
		if restored != nil {
			return mutation.Result{}, errors.New("restore this P4 Trash item with host execution; its original client is unavailable in the sandbox")
		}
		return mutation.Apply(op, apply)
	}
	states, err := p.discover(ctx, op.WorkspaceID)
	if err != nil {
		return mutation.Result{}, err
	}
	settings := p.Settings(op.WorkspaceID)
	var r *repository
	for _, candidate := range states {
		candidate.mu.Lock()
		matches := !candidate.detached && candidate.ref(op.Path) != nil && settings.Repositories[candidate.id].Tracking
		if restored != nil {
			matches = candidate.id == restored.Repository && candidate.ref(op.Path) != nil
		}
		candidate.mu.Unlock()
		if matches {
			if r != nil && r.id != candidate.id {
				return mutation.Result{}, errors.New("path belongs to multiple enabled P4 clients")
			}
			r = candidate
		}
	}
	if restored != nil && r == nil {
		// A completed Trash record can outlive provider discovery and tracking.
		// Recover only its exact identity and currently registered root scopes.
		roots, rootErr := p.fs.Roots(op.WorkspaceID)
		if rootErr != nil {
			return mutation.Result{}, rootErr
		}
		r = p.repositoryFromRecord(*restored, roots)
		if r == nil || r.ref(op.Path) == nil {
			return mutation.Result{}, errors.New("the original P4 Trash workspace mapping is no longer registered")
		}
	}
	if r == nil {
		return mutation.Apply(op, apply)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if op.Validate != nil {
		if err := op.Validate(); err != nil {
			return mutation.Result{}, err
		}
	}
	files, err := p.intents(ctx, r, op)
	if err != nil {
		return mutation.Result{}, err
	}
	if len(files) == 0 {
		return mutation.Result{}, apply()
	}
	record := operationRecord{ID: fmt.Sprintf("%d-%s", time.Now().UnixNano(), digest(op.Path+op.Destination)), Workspace: op.WorkspaceID, Repository: r.id, Connection: r.fileConnection(op.Path), ClientRoot: r.root, Scopes: r.roots, RecoveryID: op.RecoveryID, Kind: op.Kind, Origin: op.Origin, Files: files, Created: time.Now().UTC()}
	if err := p.persist(&record); err != nil {
		return mutation.Result{}, err
	}
	// Preflight every target before any physical mutation. Open only closed files;
	// an existing changelist assignment always wins over the active selector.
	for i := range record.Files {
		file := &record.Files[i]
		if (op.Kind == "edit" || op.Kind == "move") && file.Prior["depotFile"] != "" && file.Prior["action"] == "" {
			if err := p.checkout(ctx, r, file.Path, file.Change); err != nil {
				record.Diagnostic = "Checkout failed before the file was changed: " + err.Error()
				_ = p.persist(&record)
				return mutation.Result{}, errors.New(record.Diagnostic)
			}
		}
	}
	if op.Kind == "move" {
		// Native -k records exact pairs; the workspace callback then moves the
		// directory/file once, retaining untracked children and empty folders.
		for i := range record.Files {
			file := &record.Files[i]
			if file.Prior["depotFile"] == "" {
				continue
			}
			if _, err := p.files(ctx, r.fileConnection(file.Path), []string{"move", "-k", "-c", file.Change}, []string{file.Path, file.Destination}); err != nil {
				record.Diagnostic = "P4 move stopped before local movement; review the recorded paths: " + err.Error()
				_ = p.persist(&record)
				return mutation.Result{}, errors.New(record.Diagnostic)
			}
			file.NativeMoved = true
			if err := p.persist(&record); err != nil {
				return mutation.Result{}, err
			}
		}
	}
	for _, file := range record.Files {
		r.suppressed[file.Path] = time.Now().Add(3 * time.Second)
		if file.Destination != "" {
			r.suppressed[file.Destination] = time.Now().Add(3 * time.Second)
		}
	}
	defer func() {
		if r.ownedStamps == nil {
			r.ownedStamps = map[string]string{}
		}
		for _, file := range record.Files {
			r.ownedStamps[file.Path] = fileStamp(file.Path)
			if file.Destination != "" {
				r.ownedStamps[file.Destination] = fileStamp(file.Destination)
			}
		}
	}()
	if err := ctx.Err(); err != nil {
		record.Diagnostic = err.Error()
		_ = p.persist(&record)
		return mutation.Result{}, err
	}
	if op.Validate != nil {
		if err := op.Validate(); err != nil {
			record.Diagnostic = "Local state changed after P4 preflight: " + err.Error()
			_ = p.persist(&record)
			return mutation.Result{}, err
		}
	}
	if err := apply(); err != nil {
		record.Diagnostic = "Local operation was interrupted; review P4 and the recorded paths: " + err.Error()
		_ = p.persist(&record)
		return mutation.Result{}, err
	}
	record.LocalApplied = true
	if err := p.persist(&record); err != nil {
		result := pendingResult(&record, err)
		return result, nil
	}
	for i := range record.Files {
		file := &record.Files[i]
		if err := p.register(ctx, r, op.Kind, file, false); err != nil {
			result := pendingResult(&record, err)
			_ = p.persist(&record)
			return result, nil
		}
		file.Registered = true
		delete(r.candidates, file.Path)
		delete(r.candidates, file.Destination)
		if err := p.persist(&record); err != nil {
			return pendingResult(&record, err), nil
		}
	}
	record.Complete = true
	if err := p.persist(&record); err != nil {
		return pendingResult(&record, err), nil
	}
	if restored != nil {
		restored.Complete = true
		if err := p.persist(restored); err != nil {
			return pendingResult(&record, err), nil
		}
		_ = os.Remove(p.trashRecordPath(op.WorkspaceID, op.RecoveryID))
	}
	return mutation.Result{OperationID: record.ID}, nil
}

func (p *Provider) checkout(ctx context.Context, r *repository, path, change string) error {
	if _, err := p.files(ctx, r.fileConnection(path), []string{"edit", "-c", change}, []string{path}); err != nil {
		return err
	}
	// Some CLI denials are warnings with a zero exit status. A required checkout
	// is successful only when this client's pending state actually permits edits.
	meta, err := p.stat(ctx, r, path)
	if err != nil {
		return err
	}
	if meta["action"] != "edit" && meta["action"] != "add" && meta["action"] != "move/add" {
		return errors.New("P4 did not open the file for edit; check its lock, permissions, and synced revision")
	}
	return validateActionOwner(r, meta)
}

func validateActionOwner(r *repository, meta record) error {
	if meta["action"] != "" && meta["actionOwner"] != "" && meta["actionOwner"] != r.connection.User {
		return errors.New("this file is opened by another P4 user in the same client; review its ownership before changing it")
	}
	return nil
}

func fileStamp(path string) string {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "absent"
	}
	if err != nil {
		return "unknown"
	}
	return fmt.Sprintf("%d:%d:%d", info.Size(), info.ModTime().UnixNano(), info.Mode())
}

func (p *Provider) intents(ctx context.Context, r *repository, op mutation.Operation) ([]intent, error) {
	paths := []string{op.Path}
	if info, err := os.Lstat(op.Path); err == nil && info.IsDir() {
		paths = nil
		err = filepath.WalkDir(op.Path, func(path string, entry fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("P4 directory operations containing symbolic links require manual review")
			}
			if entry.IsDir() {
				if ref := r.ref(path); ref != nil && workspacefs.IsProtectedWorkspaceMetadataPath(ref.Path) {
					return errors.New("directory operation includes protected metadata")
				}
				return nil
			}
			paths = append(paths, path)
			if len(paths) > 10000 {
				return errors.New("directory operation exceeds 10000 files; use a smaller subtree")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	// Restore obtains the subtree from the durable deletion record, since the
	// payload is in Echo Trash and the original path is currently absent.
	priorRestore := map[string]intent{}
	if op.Kind == "restore" {
		record, err := p.recovery(op.WorkspaceID, op.RecoveryID)
		if err != nil {
			return nil, err
		}
		if record != nil && record.Repository == r.id {
			if record.Kind == "delete" && record.LocalApplied {
				for _, file := range record.Files {
					if inside(op.Path, file.Path) {
						priorRestore[file.Path] = file
					}
				}
			}
		}
		if len(priorRestore) > 0 {
			paths = nil
			for path := range priorRestore {
				paths = append(paths, path)
			}
		}
	}
	var files []intent
	for _, path := range paths {
		if r.ref(path) == nil {
			return nil, errors.New("P4 operation is outside this client's workspace scopes")
		}
		if _, err := p.resolve(r, r.relative(path)); err != nil {
			return nil, err
		}
		meta, err := p.stat(ctx, r, path)
		if err != nil && op.Kind != "create" {
			return nil, fmt.Errorf("P4 preflight: %w", err)
		}
		if meta == nil {
			meta = record{}
		}
		if err := validateActionOwner(r, meta); err != nil {
			return nil, err
		}
		file := intent{Path: path, Prior: meta, Change: groupID(p.Settings(r.workspace).Repositories[r.id].Active)}
		if err == nil {
			mapped, mapErr := p.mapping(ctx, r, path)
			if mapErr == nil || errors.Is(mapErr, errUnmapped) {
				file.MappingKnown, file.DepotPath = true, mapped["depotFile"]
			} else if op.Kind != "create" {
				return nil, mapErr
			}
		}
		if meta["action"] != "" {
			file.Change = groupID(meta["change"])
		}
		if op.Kind == "restore" {
			if prior, ok := priorRestore[path]; ok {
				if prior.Prior["depotFile"] != "" {
					mapped, mapErr := p.mapping(ctx, r, path)
					if mapErr != nil || mapped["depotFile"] != prior.Prior["depotFile"] {
						return nil, errors.New("the original P4 Trash mapping changed; restore its original client view before retrying")
					}
				}
				if meta["action"] != "" && meta["action"] != "delete" && meta["action"] != prior.Prior["action"] {
					return nil, errors.New("P4 state changed after this Trash operation; review the current pending action before restoring")
				}
				if meta["action"] != "" && groupID(meta["change"]) != prior.Change {
					return nil, errors.New("P4V moved this file to another changelist; review it before restoring")
				}
				file.Prior = prior.Prior
				file.Change = prior.Change
			}
		}
		if meta["unresolved"] != "" && meta["unresolved"] != "0" && (op.Kind == "move" || op.Kind == "delete") {
			return nil, errors.New("resolve the file in P4 before moving or deleting it")
		}
		if op.Kind == "move" {
			rel, _ := filepath.Rel(op.Path, path)
			file.Destination = op.Destination
			if rel != "." {
				file.Destination = filepath.Join(op.Destination, rel)
			}
			if r.ref(file.Destination) == nil || r.ref(file.Destination).RootID != r.ref(path).RootID {
				return nil, errors.New("P4 moves must stay within one client and workspace root")
			}
			if _, err := p.resolve(r, r.relative(file.Destination)); err != nil {
				return nil, err
			}
			if meta["action"] == "delete" || meta["action"] == "move/delete" {
				return nil, errors.New("cannot move a file opened for deletion")
			}
			mapped, mapErr := p.mapping(ctx, r, file.Destination)
			if mapErr != nil && (meta["depotFile"] != "" || !errors.Is(mapErr, errUnmapped)) {
				return nil, fmt.Errorf("move destination is not mapped: %w", mapErr)
			}
			file.DestinationDepotPath = mapped["depotFile"]
			if meta["depotFile"] == "" {
				destination, err := p.stat(ctx, r, file.Destination)
				if err != nil {
					return nil, err
				}
				if destination["depotFile"] != "" {
					return nil, errors.New("an untracked move cannot replace a path with existing P4 state; review the destination first")
				}
			}
		}
		if op.Kind == "delete" && strings.HasPrefix(meta["action"], "move/") {
			return nil, errors.New("revert or complete the P4 move before deleting its destination")
		}
		if op.Kind == "edit" && (meta["action"] == "delete" || meta["action"] == "move/delete") {
			return nil, errors.New("file is opened for deletion; reconcile or revert that action before saving")
		}
		files = append(files, file)
	}
	return files, nil
}

func (p *Provider) mapping(ctx context.Context, r *repository, path string) (record, error) {
	rows, err := p.files(ctx, r.fileConnection(path), []string{"where"}, []string{path})
	if err != nil {
		return nil, err
	}
	var mapped record
	for _, row := range rows {
		if row["depotFile"] != "" {
			_, excluded := row["unmap"]
			if excluded || strings.HasPrefix(row["depotFile"], "-") {
				mapped = nil
			} else {
				mapped = row
			}
		}
	}
	if mapped == nil {
		return nil, errUnmapped
	}
	return mapped, nil
}
func (p *Provider) ignored(ctx context.Context, r *repository, path string) (bool, error) {
	c := r.fileConnection(path)
	argument := relativeOutputPath(c.Directory, path)
	// The local ignores command does not accept -x and older Windows CLIs lose
	// Unicode argv. An exact add preview runs native ignore rules without writes.
	for _, character := range argument {
		if character > 127 {
			rows, err := p.files(ctx, c, []string{"add", "-n"}, []string{path})
			if ignoredAdd(rows) {
				return true, nil
			}
			return false, err
		}
	}
	output, err := p.command(ctx, c, nil, false, "ignores", "-i", argument)
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

func ignoredAdd(rows []record) bool {
	if len(rows) == 0 {
		return false
	}
	for _, row := range rows {
		if row["level"] != "34" || !strings.HasSuffix(strings.TrimSpace(row["data"]), " - ignored file can't be added.") {
			return false
		}
	}
	return true
}

func (p *Provider) register(ctx context.Context, r *repository, kind string, file *intent, retry bool) error {
	path := file.Path
	if kind == "move" {
		path = file.Destination
	}
	current, err := p.stat(ctx, r, path)
	if err != nil {
		return err
	}
	if err := validateActionOwner(r, current); err != nil {
		return err
	}
	if retry {
		// Respect concurrent P4V actions. A matching registration is complete;
		// a different action is a conflict requiring the user's reconciliation.
		want := "add"
		if kind == "edit" {
			want = "edit"
		}
		if kind == "delete" {
			want = "delete"
		}
		if kind == "move" {
			if file.Prior["depotFile"] != "" {
				want = "move/add"
			}
		}
		if kind == "restore" {
			want = file.Prior["action"]
		}
		if kind == "delete" && file.Prior["action"] == "add" {
			want = ""
		}
		if current["action"] == want {
			return nil
		}
		if current["action"] != "" || (kind == "edit" && file.Prior["depotFile"] != "") || file.NativeMoved {
			return errors.New("P4 state changed since Echo's operation; review reconciliation before retrying")
		}
	}
	run := func(args []string, paths ...string) error {
		_, err := p.files(ctx, r.fileConnection(path), args, paths)
		return err
	}
	switch kind {
	case "edit":
		if file.Prior["depotFile"] != "" {
			return nil
		}
		fallthrough
	case "create":
		if current["action"] != "" {
			return nil
		}
		if _, err := p.mapping(ctx, r, path); err != nil {
			if errors.Is(err, errUnmapped) {
				return nil
			}
			return err
		}
		// add -f accepts reserved names but still honors native P4IGNORE. Its
		// ignored-file result is informational even though the CLI exits with 1.
		rows, err := p.files(ctx, r.fileConnection(path), []string{"add", "-c", file.Change}, []string{path})
		if ignoredAdd(rows) {
			return nil
		}
		return err
	case "move":
		if file.NativeMoved {
			return nil
		}
		if file.Prior["depotFile"] != "" {
			return errors.New("native move was interrupted; review both paths before reconciliation")
		}
		return p.register(ctx, r, "create", &intent{Path: path, Change: file.Change}, retry)
	case "delete":
		if file.Prior["depotFile"] == "" {
			return nil
		}
		if current["action"] == "delete" {
			return nil
		}
		if current["action"] != "" {
			if err := run([]string{"revert", "-k"}, path); err != nil {
				return err
			}
		}
		if file.Prior["action"] == "add" {
			return nil
		}
		return run([]string{"delete", "-k", "-c", file.Change}, path)
	case "restore":
		if current["action"] == "delete" {
			if err := run([]string{"revert", "-k"}, path); err != nil {
				return err
			}
		}
		switch file.Prior["action"] {
		case "edit":
			return run([]string{"edit", "-c", file.Change}, path)
		case "add":
			return run([]string{"add", "-c", file.Change}, path)
		}
		return nil
	}
	return nil
}
