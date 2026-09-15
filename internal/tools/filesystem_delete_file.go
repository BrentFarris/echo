package tools

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/brent/echo/internal/mutation"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "filesystem_delete_file",
			Description: "Delete a regular file inside the active workspace.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"path"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Labeled workspace file path to delete. " + labeledPathSchemaHint,
					},
				},
			},
		},
		Run: deleteFile,
	})
}

type deleteFileArgs struct {
	Path string `json:"path"`
}

type deleteFileOutput struct {
	Registration *mutation.Result `json:"registration,omitempty"`
	TrashID      string           `json:"trashId,omitempty"`
	Path         string           `json:"path"`
	Bytes        int64            `json:"bytes"`
}

func deleteFile(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args deleteFileArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	if args.Path == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "path is required"}
	}

	path, err := resolveWorkspaceChildPath(ctx, args.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, SafeError{Code: "path_not_found", Message: "file was not found"}
	}
	if !info.Mode().IsRegular() {
		return nil, SafeError{Code: "not_file", Message: "path is not a regular file"}
	}
	before, err := readFileSnapshot(ctx, path, info)
	if err != nil {
		return nil, fmt.Errorf("snapshot file before delete: %w", err)
	}
	output := deleteFileOutput{
		Path:  relativeWorkspacePath(ctx, path),
		Bytes: info.Size(),
	}

	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	if ctx.WorkspaceFiles != nil {
		ref, err := ctx.WorkspaceFiles.ReferenceForHostPath(ctx.WorkspaceID, path)
		if err != nil {
			return nil, err
		}
		item, err := ctx.WorkspaceFiles.TrashContext(ctx.context(), ctx.WorkspaceID, ref)
		if err != nil {
			return nil, err
		}
		output.Registration, output.TrashID = item.Registration, item.ID
	} else if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("delete file: %w", err)
	}
	ctx.recordFileChanges(fileChangeForPath(ctx, path, before, nil))
	return output, nil
}
