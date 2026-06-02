package tools

import (
	"encoding/json"
	"fmt"
	"os"
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
						"description": "Workspace-relative file path to delete.",
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
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

func deleteFile(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args deleteFileArgs
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	if args.Path == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "path is required"}
	}

	path, err := resolveWorkspaceChildPath(ctx.WorkspacePath, args.Path)
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
	before, err := readFileSnapshot(ctx.WorkspacePath, path, info)
	if err != nil {
		return nil, fmt.Errorf("snapshot file before delete: %w", err)
	}
	output := deleteFileOutput{
		Path:  relativeWorkspacePath(ctx.WorkspacePath, path),
		Bytes: info.Size(),
	}

	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("delete file: %w", err)
	}
	ctx.recordFileChanges(fileChangeForPath(ctx.WorkspacePath, path, before, nil))
	return output, nil
}
