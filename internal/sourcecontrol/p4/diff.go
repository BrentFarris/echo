package p4

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/hunk"
	"github.com/brent/echo/internal/workspacefs"
)

func (p *Provider) Diff(ctx context.Context, workspace, id string, target sourcecontrol.DiffTarget) (result sourcecontrol.DiffDocument, resultErr error) {
	defer func() {
		if resultErr != nil {
			var coded *sourcecontrol.Error
			if !errors.As(resultErr, &coded) {
				resultErr = &sourcecontrol.Error{Code: "p4_diff_failed", Message: resultErr.Error(), Cause: resultErr}
			}
		}
	}()
	r, err := p.repo(ctx, workspace, id)
	if err != nil {
		return sourcecontrol.DiffDocument{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if target.Kind != "change" && target.Kind != "working" && target.Kind != "working-copy" && target.Kind != "" {
		return sourcecontrol.DiffDocument{}, &sourcecontrol.Error{Code: "unsupported_source_control_capability", Message: "P4 supports working-copy diffs only"}
	}
	path, err := p.resolve(r, target.Path)
	if err != nil {
		return sourcecontrol.DiffDocument{}, err
	}
	meta, err := p.stat(ctx, r, path)
	if err != nil {
		return sourcecontrol.DiffDocument{}, err
	}
	doc := sourcecontrol.DiffDocument{RepositoryID: id, ProviderID: ID, Target: target, Ref: r.ref(path), Revision: r.revision, Kind: "text"}
	depot, rev := meta["depotFile"], meta["haveRev"]
	connection := r.fileConnection(path)
	if meta["action"] != "" && meta["action"] != "add" {
		if meta["workRev"] != "" {
			rev = meta["workRev"]
		}
		opened, openErr := p.files(ctx, connection, []string{"opened", "-C", r.connection.Client, "-u", r.connection.User}, []string{path})
		if openErr != nil {
			return doc, openErr
		}
		for _, row := range opened {
			if row["rev"] != "" {
				rev = row["rev"]
			}
		}
	}
	if meta["action"] == "move/add" {
		depot, rev = meta["movedFile"], meta["movedRev"]
		if rev == "" {
			return doc, errors.New("P4 did not return the move's base revision")
		}
	}
	if meta["action"] == "add" {
		rev = ""
	}
	var original, modified []byte
	if depot != "" && rev != "" && rev != "0" {
		key := depot + "#" + rev + "|" + r.lineEnd + "|" + connection.Charset + "|" + connection.Directory
		original = r.baselines[key]
		if original == nil {
			original, err = p.printBaseline(ctx, connection, depot+"#"+rev)
			if errors.Is(err, errOutputLimit) {
				doc.Kind, doc.UnavailableReason = "too-large", "File exceeds the 10 MiB editor limit"
				return doc, nil
			}
			if err != nil {
				return doc, err
			}
			if len(original) <= int(workspacefs.MaxEditableBytes) {
				cachedBytes := 0
				for _, data := range r.baselines {
					cachedBytes += len(data)
				}
				if len(r.baselines) >= 32 || cachedBytes+len(original) > 32<<20 {
					clear(r.baselines)
				}
				r.baselines[key] = original
			}
		}
		doc.Original.Exists = true
	}
	file, readErr := os.Open(path)
	if readErr == nil {
		modified, err = io.ReadAll(io.LimitReader(file, workspacefs.MaxEditableBytes+1))
		_ = file.Close()
		if err != nil {
			return doc, err
		}
		doc.Modified.Exists = true
	} else if !os.IsNotExist(readErr) {
		return doc, readErr
	}
	doc.Original.Label, doc.Modified.Label = "Base #"+rev, "Working copy"
	if len(original) > int(workspacefs.MaxEditableBytes) || len(modified) > int(workspacefs.MaxEditableBytes) {
		doc.Kind, doc.UnavailableReason = "too-large", "File exceeds the 10 MiB editor limit"
		return doc, nil
	}
	filetype := meta["type"]
	if filetype == "" {
		filetype = meta["headType"]
	}
	if strings.HasPrefix(filetype, "binary") || bytes.IndexByte(original, 0) >= 0 || bytes.IndexByte(modified, 0) >= 0 || !utf8.Valid(original) || !utf8.Valid(modified) {
		doc.Kind, doc.UnavailableReason = "binary", "Binary or non-UTF-8 file"
		return doc, nil
	}
	setSide := func(side *sourcecontrol.DiffSide, data []byte) {
		side.HasBOM = bytes.HasPrefix(data, []byte{239, 187, 191})
		side.Content = string(bytes.TrimPrefix(data, []byte{239, 187, 191}))
		side.EOL = "lf"
		if bytes.Count(data, []byte("\r\n"))*2 >= bytes.Count(data, []byte("\n")) && bytes.Contains(data, []byte("\r\n")) {
			side.EOL = "crlf"
		}
	}
	setSide(&doc.Original, original)
	setSide(&doc.Modified, modified)
	sum := sha256.Sum256(modified)
	if doc.Modified.Exists {
		doc.ModifiedRevision = "sha256:" + hex.EncodeToString(sum[:])
	}
	doc.Editable = doc.Modified.Exists && validateActionOwner(r, meta) == nil
	if doc.Editable {
		doc.HunkActions = []string{"revert_hunk"}
		doc.HunkToken = hunk.Token(doc.Target, doc.Original, doc.Modified)
	}
	return doc, nil
}

// P4 2024.1 stdout printing omits the BOM of utf8 files. File output uses the
// client's native charset, BOM, line ending and keyword rules, as sync does.
// Size metadata is checked first, and raw content never passes through JSON.
func (p *Provider) printBaseline(ctx context.Context, connection connection, revision string) ([]byte, error) {
	rows, err := p.runInput(ctx, connection, []string{"sizes"}, []string{revision})
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, errors.New("P4 did not return the selected base revision's size")
	}
	size, err := strconv.ParseInt(rows[0]["fileSize"], 10, 64)
	if err != nil || size < 0 {
		return nil, errors.New("P4 did not return a valid base revision size")
	}
	if size > workspacefs.MaxEditableBytes {
		return nil, errOutputLimit
	}
	file, err := os.CreateTemp("", "echo-p4-baseline-*")
	if err != nil {
		return nil, err
	}
	name := file.Name()
	file.Close()
	defer func() { _ = os.Chmod(name, 0600); _ = os.Remove(name) }()
	argument, err := outputFileArgument(connection.Directory, name)
	if err != nil {
		return nil, err
	}
	if _, err := p.command(ctx, connection, []byte(revision+"\n"), false, "print", "-q", "-o", argument); err != nil {
		return nil, err
	}
	file, err = os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, workspacefs.MaxEditableBytes+1))
}

func relativeOutputPath(directory, name string) string {
	if relative, err := filepath.Rel(directory, name); err == nil {
		return relative
	}
	return name
}
