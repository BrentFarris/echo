package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "filesystem_list",
			Description: "List direct children of a directory inside the active workspace.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Labeled workspace directory path. Defaults to . for the virtual workspace root. " + labeledPathSchemaHint,
					},
					"includeHidden": map[string]any{
						"type":        "boolean",
						"description": "Whether to include dotfiles and dot directories.",
					},
				},
			},
		},
		Run: listDirectory,
	})
}

type listDirectoryArgs struct {
	Path          string `json:"path"`
	IncludeHidden bool   `json:"includeHidden"`
}

type directoryEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Bytes int64  `json:"bytes,omitempty"`
}

type listDirectoryOutput struct {
	Path    string           `json:"path"`
	Entries []directoryEntry `json:"entries"`
}

func listDirectory(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args listDirectoryArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}

	requestedPath := strings.TrimSpace(args.Path)
	if len(ctx.WorkspaceRoots) > 0 && (requestedPath == "" || requestedPath == ".") {
		return listWorkspaceVirtualRoots(ctx, args.IncludeHidden), nil
	}
	path, err := resolveWorkspacePath(ctx, args.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, SafeError{Code: "path_not_found", Message: "directory was not found"}
	}
	if !info.IsDir() {
		return nil, SafeError{Code: "not_directory", Message: "path is not a directory"}
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read directory: %w", err)
	}
	output := listDirectoryOutput{
		Path:    relativeWorkspacePath(ctx, path),
		Entries: make([]directoryEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		if err := ctx.context().Err(); err != nil {
			return nil, err
		}
		if !args.IncludeHidden && len(entry.Name()) > 0 && entry.Name()[0] == '.' {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("read directory entry: %w", err)
		}
		output.Entries = append(output.Entries, directoryEntry{
			Name:  entry.Name(),
			Path:  relativeWorkspacePath(ctx, filepath.Join(path, entry.Name())),
			Kind:  fileKind(info),
			Bytes: info.Size(),
		})
	}
	sort.Slice(output.Entries, func(i, j int) bool {
		left := output.Entries[i]
		right := output.Entries[j]
		if left.Kind != right.Kind {
			return left.Kind == "directory"
		}
		return left.Name < right.Name
	})
	return output, nil
}

func listWorkspaceVirtualRoots(ctx ExecutionContext, includeHidden bool) listDirectoryOutput {
	roots := ctx.workspaceRoots()
	output := listDirectoryOutput{
		Path:    ".",
		Entries: make([]directoryEntry, 0, len(roots)),
	}
	for _, root := range roots {
		if !includeHidden && strings.HasPrefix(root.Label, ".") {
			continue
		}
		entry := directoryEntry{
			Name: root.Label,
			Path: root.Label,
			Kind: "directory",
		}
		if info, err := os.Stat(root.Path); err == nil {
			entry.Bytes = info.Size()
		}
		output.Entries = append(output.Entries, entry)
	}
	sort.Slice(output.Entries, func(i, j int) bool {
		return output.Entries[i].Name < output.Entries[j].Name
	})
	return output
}
