package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const lspCodeActionTimeout = 15 * time.Second

type lspCodeAction struct {
	Title   string            `json:"title"`
	Kind    string            `json:"kind"`
	Edit    *lspWorkspaceEdit `json:"edit,omitempty"`
	Command *lspCommand       `json:"command,omitempty"`
}

type lspCommand struct {
	Title     string            `json:"title"`
	Command   string            `json:"command"`
	Arguments []json.RawMessage `json:"arguments,omitempty"`
}

var organizeWorkspaceGoImportsBeforeSave = func(s *SystemService, workspace Workspace, resolvedPath string, content string) (string, error) {
	return s.organizeWorkspaceGoImportsBeforeSave(workspace, resolvedPath, content)
}

func (s *SystemService) prepareWorkspaceFileContentBeforeSave(workspace Workspace, resolvedPath string, content string) (string, error) {
	if !strings.EqualFold(filepath.Ext(resolvedPath), ".go") {
		return content, nil
	}
	content, err := organizeWorkspaceGoImportsBeforeSave(s, workspace, resolvedPath, content)
	if err != nil {
		return "", err
	}
	return formatWorkspaceFileContentBeforeSave(resolvedPath, content)
}

func (s *SystemService) organizeWorkspaceGoImportsBeforeSave(workspace Workspace, resolvedPath string, content string) (string, error) {
	folder, err := workspaceFolderForAbsolutePath(workspace, resolvedPath)
	if err != nil {
		return "", err
	}
	client, err := s.workspaceLSPClient(workspace, folder, "go")
	if err != nil {
		return "", fmt.Errorf("organize Go imports: %w", err)
	}
	updated, _, err := client.organizeImports(context.Background(), resolvedPath, content)
	if err != nil {
		return "", fmt.Errorf("organize Go imports: %w", err)
	}
	return updated, nil
}

func (c *lspClient) organizeImports(ctx context.Context, absolutePath string, content string) (string, bool, error) {
	uri := fileURI(absolutePath)
	release, err := c.acquireOperation(ctx, "textDocument/codeAction")
	if err != nil {
		return "", false, err
	}
	defer release()
	requestCtx, cancel := c.documentOperationContext(ctx, uri, lspCodeActionTimeout)
	defer cancel()
	if err := c.syncDocument(absolutePath, uri, content); err != nil {
		return "", false, err
	}
	raw, err := c.requestWithRetry(requestCtx, "textDocument/codeAction", map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"range": lspRange{
			Start: lspPosition{Line: 0, Character: 0},
			End:   lspPositionFromUTF16Offset(content, utf16Length(content)),
		},
		"context": map[string]any{
			"diagnostics": []any{},
			"only":        []string{"source.organizeImports"},
		},
	})
	if err != nil {
		return "", false, err
	}
	c.markDocumentReady(uri)
	actions, err := parseLSPCodeActionResponse(raw)
	if err != nil {
		return "", false, err
	}
	for _, action := range actions {
		updated, changed, ok, err := c.applyCodeActionWorkspaceEdit(requestCtx, absolutePath, content, action)
		if err != nil {
			return "", false, err
		}
		if ok {
			if changed {
				_ = c.syncDocument(absolutePath, uri, updated)
			}
			return updated, changed, nil
		}
	}
	return content, false, nil
}

func (c *lspClient) applyCodeActionWorkspaceEdit(ctx context.Context, absolutePath string, content string, action lspCodeAction) (string, bool, bool, error) {
	if action.Edit != nil {
		updated, changed, err := applyWorkspaceEditToSingleFile(content, absolutePath, *action.Edit)
		return updated, changed, true, err
	}
	if action.Command == nil || strings.TrimSpace(action.Command.Command) == "" {
		return content, false, false, nil
	}
	edit, ok, err := c.executeCommandWorkspaceEdit(ctx, *action.Command)
	if err != nil || !ok {
		return content, false, ok, err
	}
	updated, changed, err := applyWorkspaceEditToSingleFile(content, absolutePath, edit)
	return updated, changed, true, err
}

func (c *lspClient) executeCommandWorkspaceEdit(ctx context.Context, command lspCommand) (lspWorkspaceEdit, bool, error) {
	args := make([]any, 0, len(command.Arguments))
	for _, raw := range command.Arguments {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return lspWorkspaceEdit{}, false, fmt.Errorf("parse command argument: %w", err)
		}
		args = append(args, value)
	}
	raw, err := c.requestWithRetry(ctx, "workspace/executeCommand", map[string]any{
		"command":   command.Command,
		"arguments": args,
	})
	if err != nil {
		return lspWorkspaceEdit{}, false, err
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return lspWorkspaceEdit{}, false, nil
	}
	edits, err := parseLSPWorkspaceEdit(raw)
	if err != nil {
		return lspWorkspaceEdit{}, false, err
	}
	return workspaceEditFromEdits(edits), true, nil
}

func applyWorkspaceEditToSingleFile(content string, absolutePath string, edit lspWorkspaceEdit) (string, bool, error) {
	editsByURI := map[string][]lspTextEdit{}
	for uri, edits := range edit.Changes {
		editsByURI[uri] = append(editsByURI[uri], edits...)
	}
	for _, rawChange := range edit.DocumentChanges {
		var change lspTextDocumentEdit
		if err := json.Unmarshal(rawChange, &change); err != nil {
			return "", false, fmt.Errorf("parse code action document edit: %w", err)
		}
		if strings.TrimSpace(change.TextDocument.URI) == "" {
			return "", false, fmt.Errorf("code action returned an unsupported workspace resource operation")
		}
		editsByURI[change.TextDocument.URI] = append(editsByURI[change.TextDocument.URI], change.Edits...)
	}

	var targetEdits []lspTextEdit
	for uri, edits := range editsByURI {
		if len(edits) == 0 {
			continue
		}
		path, err := pathFromFileURI(uri)
		if err != nil {
			return "", false, fmt.Errorf("code action returned an invalid file URI")
		}
		if !samePath(path, absolutePath) {
			return "", false, fmt.Errorf("code action attempted to edit another file")
		}
		targetEdits = append(targetEdits, edits...)
	}
	if len(targetEdits) == 0 {
		return content, false, nil
	}
	updated, err := applyLSPTextEdits(content, targetEdits)
	if err != nil {
		return "", false, err
	}
	return updated, updated != content, nil
}

func parseLSPCodeActionResponse(raw json.RawMessage) ([]lspCodeAction, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []lspCodeAction{}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, fmt.Errorf("parse code action response: %w", err)
	}
	actions := make([]lspCodeAction, 0, len(items))
	for _, item := range items {
		var action lspCodeAction
		if err := json.Unmarshal(item, &action); err == nil && (action.Title != "" || action.Kind != "" || action.Edit != nil || action.Command != nil) {
			actions = append(actions, action)
			continue
		}
		var command lspCommand
		if err := json.Unmarshal(item, &command); err == nil && command.Command != "" {
			actions = append(actions, lspCodeAction{
				Title:   command.Title,
				Command: &command,
			})
		}
	}
	return actions, nil
}

func workspaceEditFromEdits(edits map[string][]lspTextEdit) lspWorkspaceEdit {
	return lspWorkspaceEdit{Changes: edits}
}
