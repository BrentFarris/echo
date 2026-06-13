package tools

import (
	"encoding/json"
	"os"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "filesystem_stat",
			Description: "Inspect metadata for a path inside the active workspace.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Labeled workspace path to inspect. Defaults to . for the virtual workspace root. " + labeledPathSchemaHint,
					},
				},
			},
		},
		Run: statPath,
	})
}

type statPathArgs struct {
	Path string `json:"path"`
}

type statPathOutput struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Bytes      int64  `json:"bytes,omitempty"`
	Mode       string `json:"mode"`
	ModifiedAt string `json:"modifiedAt"`
}

func statPath(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args statPathArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	path, err := resolveWorkspacePath(ctx, args.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, SafeError{Code: "path_not_found", Message: "path was not found"}
	}
	return statPathOutput{
		Path:       relativeWorkspacePath(ctx, path),
		Kind:       fileKind(info),
		Bytes:      info.Size(),
		Mode:       info.Mode().String(),
		ModifiedAt: info.ModTime().UTC().Format("2006-01-02T15:04:05Z07:00"),
	}, nil
}
