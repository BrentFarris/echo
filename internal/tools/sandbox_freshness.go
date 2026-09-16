package tools

import (
	"encoding/json"
	"os"
)

// A takeover invalidates observations in every chat. Existing files must be
// reread by this turn before a host file tool can edit, replace, or delete them.
func requireFreshSandboxFile(ctx ExecutionContext, arguments json.RawMessage) error {
	if ctx.AIGeneration == nil || ctx.Sandbox == nil || *ctx.AIGeneration == 0 || !ctx.Sandbox.IsEnabled(ctx.WorkspaceID) {
		return nil
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return err
	}
	path, err := resolveWorkspacePath(ctx, args.Path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if !ctx.Sandbox.FileReadIsFresh(ctx.WorkspaceID, ctx.TurnID, path, *ctx.AIGeneration) {
		return SafeError{Code: "file_context_stale", Message: "The user had desktop control. Read this file again with filesystem_read_text before changing it."}
	}
	return nil
}

func recordSandboxFileRead(ctx ExecutionContext, arguments json.RawMessage) {
	if ctx.AIGeneration == nil || ctx.Sandbox == nil {
		return
	}
	var args struct {
		Path string `json:"path"`
	}
	if DecodeToolArguments(arguments, &args) != nil {
		return
	}
	path, err := resolveWorkspacePath(ctx, args.Path)
	if err == nil {
		ctx.Sandbox.RecordFileRead(ctx.WorkspaceID, ctx.TurnID, path, *ctx.AIGeneration)
	}
}
