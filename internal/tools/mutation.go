package tools

import (
	"errors"
	"os"

	"github.com/brent/echo/internal/mutation"
)

func writeToolFile(ctx ExecutionContext, path, origin string, data []byte, before *FileSnapshot, overwrite bool) (int, *mutation.Result, error) {
	kind := "create"
	if before != nil && before.Exists {
		kind = "edit"
	}
	op := mutation.Operation{WorkspaceID: ctx.WorkspaceID, Path: path, Kind: kind, Origin: origin, Validate: func() error {
		current, err := snapshotExistingFile(ctx, path)
		if err != nil {
			return err
		}
		if before != nil && before.Exists {
			if current == nil || current.SHA256 != before.SHA256 {
				return SafeError{Code: "file_changed", Message: "file changed since it was read; read it again before editing"}
			}
		} else if current != nil && current.Exists {
			return SafeError{Code: "file_exists", Message: "file was created concurrently"}
		}
		return nil
	}}
	written := 0
	apply := func() error {
		flag := os.O_WRONLY | os.O_CREATE
		if overwrite {
			flag |= os.O_TRUNC
		} else {
			flag |= os.O_EXCL
		}
		file, err := os.OpenFile(path, flag, 0600)
		if err != nil {
			return err
		}
		written, err = file.Write(data)
		closeErr := file.Close()
		return errors.Join(err, closeErr)
	}
	var result mutation.Result
	var err error
	if ctx.WorkspaceFiles != nil {
		result, err = ctx.WorkspaceFiles.Run(ctx.context(), op, apply)
	} else {
		result, err = mutation.Apply(op, apply)
	}
	if err != nil {
		return written, nil, err
	}
	if result.Pending {
		return written, &result, nil
	}
	return written, nil, nil
}
