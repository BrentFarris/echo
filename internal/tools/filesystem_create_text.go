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
			Name:        "filesystem_create_text",
			Description: "Create a UTF-8 or plain text file inside the active workspace.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"path", "content"},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Labeled workspace file path to create. " + labeledPathSchemaHint,
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
	Registration *mutation.Result `json:"registration,omitempty"`
	Path         string           `json:"path"`
	BytesWritten int64            `json:"bytesWritten"`
	Overwritten  bool             `json:"overwritten"`
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

	path, err := resolveWorkspaceChildPath(ctx, args.Path)
	if err != nil {
		return nil, err
	}
	before, err := snapshotExistingFile(ctx, path)
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
	written, registration, err := writeToolFile(ctx, path, "filesystem_create_text", []byte(args.Content), before, args.Overwrite)
	if err != nil {
		return nil, err
	}
	after, err := snapshotExistingFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("snapshot file after create: %w", err)
	}
	ctx.recordFileChanges(fileChangeForPath(ctx, path, before, after))

	return createTextFileOutput{
		Registration: registration,
		Path:         relativeWorkspacePath(ctx, path),
		BytesWritten: int64(written),
		Overwritten:  overwritten,
	}, nil
}
