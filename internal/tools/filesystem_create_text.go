package tools

import (
	"encoding/json"
	"fmt"
	"os"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "filesystem_create_text",
			Description: "Create a UTF-8 or plain text file inside the active workspace.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"path", "content"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Workspace-relative file path to create.",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Text content to write to the file.",
					},
					"overwrite": map[string]any{
						"type":        "boolean",
						"description": "Whether to replace an existing regular file. Defaults to false.",
					},
				},
			},
		},
		Run: createTextFile,
	})
}

type createTextFileArgs struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Overwrite bool   `json:"overwrite"`
}

type createTextFileOutput struct {
	Path         string `json:"path"`
	BytesWritten int64  `json:"bytesWritten"`
	Overwritten  bool   `json:"overwritten"`
}

func createTextFile(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args createTextFileArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	if args.Path == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "path is required"}
	}
	args.Content = normalizeToolTextLineBreaks(args.Content)
	if len(args.Content) > maxTextFileBytes {
		return nil, SafeError{Code: "file_too_large", Message: fmt.Sprintf("content is larger than the %d byte creation limit", maxTextFileBytes)}
	}

	path, err := resolveWorkspaceChildPath(ctx.WorkspacePath, args.Path)
	if err != nil {
		return nil, err
	}
	before, err := snapshotExistingFile(ctx.WorkspacePath, path)
	if err != nil {
		return nil, fmt.Errorf("snapshot file before create: %w", err)
	}
	overwritten := false
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, SafeError{Code: "not_file", Message: "path exists and is not a regular file"}
		}
		if !args.Overwrite {
			return nil, SafeError{Code: "file_exists", Message: "file already exists"}
		}
		overwritten = true
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat file: %w", err)
	}

	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	flag := os.O_WRONLY | os.O_CREATE
	if args.Overwrite {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_EXCL
	}
	file, err := os.OpenFile(path, flag, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, SafeError{Code: "file_exists", Message: "file already exists"}
		}
		return nil, fmt.Errorf("create file: %w", err)
	}
	written, err := file.WriteString(args.Content)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close file: %w", err)
	}
	after, err := snapshotExistingFile(ctx.WorkspacePath, path)
	if err != nil {
		return nil, fmt.Errorf("snapshot file after create: %w", err)
	}
	ctx.recordFileChanges(fileChangeForPath(ctx.WorkspacePath, path, before, after))

	return createTextFileOutput{
		Path:         relativeWorkspacePath(ctx.WorkspacePath, path),
		BytesWritten: int64(written),
		Overwritten:  overwritten,
	}, nil
}
