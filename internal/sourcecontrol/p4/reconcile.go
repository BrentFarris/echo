package p4

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/hunk"
	"github.com/brent/echo/internal/workspacefs"
)

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

type preview struct {
	sourcecontrol.ReconcilePreview
	Paths       []string
	Fingerprint string
	Target      string
	Created     time.Time
}

func (p *Provider) fingerprint(ctx context.Context, r *repository, paths []string) (string, error) {
	values := []any{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		meta, err := p.stat(ctx, r, path)
		if err != nil {
			return "", err
		}
		mapped, err := p.mapping(ctx, r, path)
		if err != nil && !errors.Is(err, errUnmapped) {
			return "", err
		}
		sum := "absent"
		file, err := os.Open(path)
		if err == nil {
			if info, statErr := file.Stat(); statErr != nil || !info.Mode().IsRegular() {
				file.Close()
				return "", errors.New("reconciliation selection must contain regular files or missing file paths")
			}
			hash := sha256.New()
			_, err = io.Copy(hash, contextReader{ctx, file})
			file.Close()
			if err != nil {
				return "", err
			}
			sum = hex.EncodeToString(hash.Sum(nil))
		} else if !os.IsNotExist(err) {
			return "", err
		}
		values = append(values, path, meta, mapped, sum)
	}
	return hunk.Token(values...), nil
}
func (p *Provider) makePreview(ctx context.Context, r *repository, paths []string, target string, scan bool) (sourcecontrol.ReconcilePreview, error) {
	result := sourcecontrol.ReconcilePreview{Changes: []sourcecontrol.Change{}}
	if len(paths) == 0 {
		return result, nil
	}
	if !scan {
		selected := map[string]bool{}
		for _, path := range paths {
			selected[path] = true
		}
		for _, record := range p.pending(r) {
			for _, file := range record.Files {
				if file.Destination != "" && (selected[file.Path] || selected[file.Destination]) {
					for _, path := range []string{file.Path, file.Destination} {
						if _, err := p.resolve(r, r.relative(path)); err != nil {
							return result, err
						}
						selected[path] = true
					}
				}
			}
		}
		paths = nil
		for path := range selected {
			paths = append(paths, path)
		}
		sort.Strings(paths)
	}
	var rows []record
	var err error
	if scan {
		for _, path := range paths {
			info, statErr := os.Stat(path)
			if statErr != nil || !info.IsDir() {
				return result, errors.New("choose an existing folder for Scan Folder")
			}
			records, scanErr := p.runInput(ctx, r.fileConnection(path), []string{"reconcile", "-n", "-f"}, []string{path + string(filepath.Separator) + "..."})
			rows = append(rows, records...)
			if scanErr != nil {
				return result, scanErr
			}
		}
	} else {
		rows, err = p.reconcilePaths(ctx, r, paths, target, true, nil)
		if err != nil {
			return result, err
		}
	}
	reviewed := map[string]bool{}
	if !scan {
		for _, path := range paths {
			if _, err := p.resolve(r, r.relative(path)); err != nil {
				return result, err
			}
			reviewed[path] = true
		}
	}
	for _, row := range rows {
		local := row["clientFile"]
		if local == "" {
			continue
		}
		if !filepath.IsAbs(local) {
			return result, errors.New("P4 reconciliation did not return an absolute local path")
		}
		if ref := r.ref(local); ref != nil && workspacefs.IsProtectedWorkspaceMetadataPath(ref.Path) {
			continue
		}
		resolved, err := p.resolve(r, r.relative(local))
		if err != nil {
			return result, errors.New("reconciliation includes a hidden or excluded path")
		}
		if _, err := p.mapping(ctx, r, resolved); err != nil {
			return result, err
		}
		reviewed[resolved] = true
		meta, err := p.stat(ctx, r, resolved)
		if err != nil {
			return result, err
		}
		change := target
		if meta["action"] != "" {
			change = groupID(meta["change"])
		}
		result.Changes = append(result.Changes, nativeChange(r.relative(resolved), change, row["action"]))
		if len(result.Changes) > sourcecontrol.StatusLimit {
			result.Truncated = true
			result.Diagnostic = "Choose a smaller folder; this preview is incomplete"
			return result, nil
		}
	}
	if len(reviewed) == 0 {
		return result, nil
	}
	exact := []string{}
	for path := range reviewed {
		exact = append(exact, path)
	}
	sort.Strings(exact)
	if scan {
		// Folder scans only discover candidates. The review and eventual apply
		// use the same exact-path batches and preserve existing assignments.
		return p.makePreview(ctx, r, exact, target, false)
	}
	for _, path := range exact {
		result.Paths = append(result.Paths, r.relative(path))
	}
	fingerprint, err := p.fingerprint(ctx, r, exact)
	if err != nil {
		return result, err
	}
	result.Token = hunk.Token(fingerprint, target, time.Now().UnixNano())
	if len(result.Changes) == 0 {
		result.Diagnostic = "P4 registration already matches these selected paths. Confirm this review to clear their pending recovery records."
	}
	if len(r.previews) > 16 {
		clear(r.previews)
	}
	r.previews[result.Token] = preview{ReconcilePreview: result, Paths: exact, Fingerprint: fingerprint, Target: target, Created: time.Now()}
	return result, nil
}
func (p *Provider) applyPreview(ctx context.Context, r *repository, token string, begin func() error) error {
	preview, ok := r.previews[token]
	if !ok || token == "" || time.Since(preview.Created) > 10*time.Minute {
		return errors.New("reconciliation preview expired; preview again")
	}
	if len(preview.Paths) == 0 {
		return nil
	}
	fingerprint, err := p.fingerprint(ctx, r, preview.Paths)
	if err != nil {
		return err
	}
	if fingerprint != preview.Fingerprint {
		return errors.New("files or P4 assignments changed since the preview; review a new preview")
	}
	current, err := p.makePreview(ctx, r, preview.Paths, preview.Target, false)
	if err != nil {
		return err
	}
	currentState := r.previews[current.Token]
	delete(r.previews, current.Token)
	if current.Truncated || currentState.Fingerprint != preview.Fingerprint || reconcileSignature(current) != reconcileSignature(preview.ReconcilePreview) {
		return errors.New("the reconciliation plan changed; review a new preview")
	}
	// Reconcile uses only reviewed exact paths. Never replay the original folder
	// wildcard or an empty argument list at this mutation boundary.
	if _, err := p.reconcilePaths(ctx, r, preview.Paths, preview.Target, false, begin); err != nil {
		return err
	}
	if err := p.completeReviewed(r, preview.Paths); err != nil {
		return err
	}
	for _, path := range preview.Paths {
		delete(r.candidates, path)
	}
	delete(r.previews, token)
	return nil
}

func reconcileSignature(preview sourcecontrol.ReconcilePreview) string {
	values := append([]string{}, preview.Paths...)
	for _, change := range preview.Changes {
		values = append(values, hunk.Token(change.Path, change.OldPath, change.GroupID, change.StatusCode))
	}
	sort.Strings(values)
	return hunk.Token(values)
}

func (p *Provider) reconcilePaths(ctx context.Context, r *repository, paths []string, target string, preview bool, begin func() error) ([]record, error) {
	byChange := map[string][]string{}
	for _, path := range paths {
		meta, err := p.stat(ctx, r, path)
		if err != nil {
			return nil, err
		}
		if err := validateActionOwner(r, meta); err != nil {
			return nil, err
		}
		change := target
		if meta["action"] != "" {
			change = groupID(meta["change"])
		}
		byChange[change] = append(byChange[change], path)
	}
	changes := make([]string, 0, len(byChange))
	for change := range byChange {
		changes = append(changes, change)
	}
	sort.Strings(changes)
	var rows []record
	for _, change := range changes {
		if begin != nil {
			if err := begin(); err != nil {
				return rows, err
			}
		}
		args := []string{"reconcile", "-f", "-c", change}
		if preview {
			args = append(args, "-n")
		}
		part, err := p.filesInScopes(ctx, r, args, byChange[change])
		rows = append(rows, part...)
		if err != nil {
			return rows, err
		}
	}
	return rows, nil
}
