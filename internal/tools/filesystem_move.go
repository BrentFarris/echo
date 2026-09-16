package tools

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	Register(ToolFunc{Meta: Metadata{Name: "filesystem_move", Description: "Move or rename a file or folder within one workspace root. Retains exact move pairs for source control.", Parameters: Schema{"type": "object", "additionalProperties": false, "required": []any{"path", "destination"}, "properties": map[string]any{"path": map[string]any{"type": "string", "description": "Existing labeled workspace path. " + labeledPathSchemaHint}, "destination": map[string]any{"type": "string", "description": "New labeled workspace path including the final file or folder name. The parent must exist."}}}}, Run: moveFile})
}

func moveFile(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	var args struct {
		Path        string `json:"path"`
		Destination string `json:"destination"`
	}
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "path and destination must be strings"}
	}
	if args.Path == "" || args.Destination == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "path and destination are required"}
	}
	source, err := resolveWorkspacePath(ctx, args.Path)
	if err != nil {
		return nil, err
	}
	destination, err := resolveWorkspaceChildPath(ctx, args.Destination)
	if err != nil {
		return nil, err
	}
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	// Validate every affected descendant against tool scopes and the sandbox's
	// post-takeover freshness boundary, before moving any of them.
	count := 0
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.context().Err(); err != nil {
			return err
		}
		count++
		if count > 10000 {
			return SafeError{Code: "move_too_large", Message: "move a smaller folder (at most 10000 entries)"}
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := destination
		if rel != "." {
			target = filepath.Join(destination, rel)
		}
		for _, candidate := range []string{path, target} {
			if ctx.ToolScopes != nil {
				relative := relativeWorkspacePath(ctx, candidate)
				for _, root := range ctx.workspaceRoots() {
					if strings.HasPrefix(relative, root.Label+"/") {
						relative = strings.TrimPrefix(relative, root.Label+"/")
						break
					}
				}
				if !ctx.ToolScopes.Allowed("filesystem_move", relative) {
					return SafeError{Code: "path_not_allowed", Message: "move includes a path outside this tool's permissions"}
				}
			}
		}
		if !entry.IsDir() && ctx.AIGeneration != nil && ctx.Sandbox != nil && *ctx.AIGeneration != 0 && ctx.Sandbox.IsEnabled(ctx.WorkspaceID) && !ctx.Sandbox.FileReadIsFresh(ctx.WorkspaceID, ctx.TurnID, path, *ctx.AIGeneration) {
			return SafeError{Code: "file_context_stale", Message: "The user had desktop control. Read every file in this move again before moving it."}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if ctx.WorkspaceFiles != nil {
		from, err := ctx.WorkspaceFiles.ReferenceForHostPath(ctx.WorkspaceID, source)
		if err != nil {
			return nil, err
		}
		to, err := ctx.WorkspaceFiles.ReferenceForHostPath(ctx.WorkspaceID, destination)
		if err != nil {
			return nil, err
		}
		return ctx.WorkspaceFiles.MoveToContext(ctx.context(), ctx.WorkspaceID, from, to)
	}
	for _, root := range ctx.workspaceRoots() {
		rootPath, err := workspaceRootAbsolutePath(root)
		if err != nil {
			return nil, err
		}
		a, aErr := filepath.Rel(rootPath, source)
		b, bErr := filepath.Rel(rootPath, destination)
		inside := func(path string) bool {
			return path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator)) && !filepath.IsAbs(path)
		}
		if aErr == nil && bErr == nil && inside(a) && inside(b) {
			if _, err := os.Lstat(destination); err == nil {
				return nil, SafeError{Code: "file_exists", Message: "destination already exists"}
			}
			if err := os.Rename(source, destination); err != nil {
				return nil, err
			}
			return map[string]string{"path": args.Path, "destination": args.Destination}, nil
		}
	}
	return nil, SafeError{Code: "cross_root_move_unsupported", Message: "move must stay within one workspace root"}
}
