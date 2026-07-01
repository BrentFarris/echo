package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const lspRenameTimeout = 30 * time.Second

type WorkspacePrepareRenameResponse struct {
	WorkspaceID string `json:"workspaceId"`
	FilePath    string `json:"filePath"`
	Available   bool   `json:"available"`
	From        int    `json:"from"`
	To          int    `json:"to"`
	Placeholder string `json:"placeholder,omitempty"`
	Message     string `json:"message,omitempty"`
}

type WorkspaceRenameFileContent struct {
	FilePath   string `json:"filePath"`
	Content    string `json:"content"`
	ModifiedAt string `json:"modifiedAt"`
}

type WorkspaceRenameRequest struct {
	FilePath  string                       `json:"filePath"`
	Content   string                       `json:"content"`
	Position  int                          `json:"position"`
	NewName   string                       `json:"newName"`
	OpenFiles []WorkspaceRenameFileContent `json:"openFiles,omitempty"`
}

type WorkspaceRenameResponse struct {
	WorkspaceID string          `json:"workspaceId"`
	SourcePath  string          `json:"sourcePath"`
	Applied     bool            `json:"applied"`
	Files       []WorkspaceFile `json:"files,omitempty"`
	Message     string          `json:"message,omitempty"`
}

type lspPrepareRenameResult struct {
	Range           lspRange `json:"range"`
	Placeholder     string   `json:"placeholder"`
	DefaultBehavior bool     `json:"defaultBehavior"`
}

type lspWorkspaceEdit struct {
	Changes         map[string][]lspTextEdit `json:"changes"`
	DocumentChanges []json.RawMessage        `json:"documentChanges"`
}

type lspTextDocumentEdit struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Edits []lspTextEdit `json:"edits"`
}

type workspaceRenameFile struct {
	resolved string
	path     string
	original string
	updated  string
	mode     os.FileMode
}

func (s *SystemService) PrepareWorkspaceSymbolRename(workspaceID string, request WorkspaceDefinitionRequest) (WorkspacePrepareRenameResponse, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspacePrepareRenameResponse{}, err
	}
	response := WorkspacePrepareRenameResponse{
		WorkspaceID: workspace.ID,
		FilePath:    request.FilePath,
	}
	resolved, client, err := s.workspaceRenameClient(workspace, request.FilePath, request.Content, request.Position)
	if err != nil {
		return WorkspacePrepareRenameResponse{}, err
	}
	if client == nil {
		response.Message = "Rename is not available for this file type."
		return response, nil
	}
	response.FilePath = workspaceRelativePath(workspace, resolved)

	ctx, cancel := context.WithTimeout(context.Background(), lspRenameTimeout)
	defer cancel()
	target, placeholder, available, err := client.prepareRename(ctx, resolved, request.Content, request.Position)
	if err != nil {
		return WorkspacePrepareRenameResponse{}, err
	}
	if !available {
		response.Message = "The selected symbol cannot be renamed."
		return response, nil
	}
	from := utf16OffsetForPosition(request.Content, target.Start)
	to := utf16OffsetForPosition(request.Content, target.End)
	if target == (lspRange{}) {
		from, to = renameFallbackRange(request.Content, request.Position)
	}
	if from < 0 || to <= from || to > utf16Length(request.Content) {
		response.Message = "The selected symbol cannot be renamed."
		return response, nil
	}
	if placeholder == "" {
		placeholder = textForUTF16Range(request.Content, from, to)
	}
	response.Available = true
	response.From = from
	response.To = to
	response.Placeholder = placeholder
	return response, nil
}

func (s *SystemService) RenameWorkspaceSymbol(workspaceID string, request WorkspaceRenameRequest) (WorkspaceRenameResponse, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceRenameResponse{}, err
	}
	if strings.TrimSpace(request.NewName) == "" {
		return WorkspaceRenameResponse{}, fmt.Errorf("new symbol name is required")
	}
	resolved, client, err := s.workspaceRenameClient(workspace, request.FilePath, request.Content, request.Position)
	if err != nil {
		return WorkspaceRenameResponse{}, err
	}
	response := WorkspaceRenameResponse{
		WorkspaceID: workspace.ID,
		SourcePath:  workspaceRelativePath(workspace, resolved),
	}
	if client == nil {
		response.Message = "Rename is not available for this file type."
		return response, nil
	}

	openFiles, err := workspaceRenameOpenFiles(workspace, request.OpenFiles)
	if err != nil {
		return WorkspaceRenameResponse{}, err
	}
	sourceFile, _ := workspaceRenameOpenFileForPath(openFiles, resolved)
	openFiles[resolved] = WorkspaceRenameFileContent{
		FilePath:   response.SourcePath,
		Content:    request.Content,
		ModifiedAt: sourceFile.ModifiedAt,
	}
	for absolutePath, file := range openFiles {
		if err := validateWorkspaceRenameOpenFile(absolutePath, file); err != nil {
			return WorkspaceRenameResponse{}, err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), lspRenameTimeout)
	defer cancel()
	edits, err := client.rename(ctx, resolved, request.Content, request.Position, request.NewName, openFiles)
	if err != nil {
		return WorkspaceRenameResponse{}, err
	}
	if len(edits) == 0 {
		response.Message = "The selected symbol cannot be renamed."
		return response, nil
	}
	files, err := prepareWorkspaceRenameFiles(workspace, edits, openFiles)
	if err != nil {
		return WorkspaceRenameResponse{}, err
	}
	if len(files) == 0 {
		response.Message = "The selected symbol cannot be renamed."
		return response, nil
	}
	if err := writeWorkspaceRenameFiles(files); err != nil {
		return WorkspaceRenameResponse{}, err
	}
	s.removeWorkspaceFileDatabases(workspaceID)

	response.Files = make([]WorkspaceFile, 0, len(files))
	for _, file := range files {
		renamed, readErr := readWorkspaceTextFile(workspace, file.resolved)
		if readErr != nil {
			return WorkspaceRenameResponse{}, readErr
		}
		response.Files = append(response.Files, renamed)
		_ = client.syncDocument(file.resolved, fileURI(file.resolved), file.updated)
	}
	response.Applied = true
	return response, nil
}

func (s *SystemService) workspaceRenameClient(workspace Workspace, path string, content string, position int) (string, *lspClient, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil, fmt.Errorf("file path is required")
	}
	if len([]byte(content)) > maxWorkspaceEditorFileBytes {
		return "", nil, fmt.Errorf("content is larger than the %d byte editor limit", maxWorkspaceEditorFileBytes)
	}
	if position < 0 || position > utf16Length(content) {
		return "", nil, fmt.Errorf("rename position is outside the file")
	}
	resolved, err := resolveWorkspaceServicePath(workspace, path)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("file was not found")
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("path is not a regular file")
	}
	languageID, ok := lspLanguageIDForPath(path)
	if !ok {
		return resolved, nil, nil
	}
	folder, err := workspaceFolderForAbsolutePath(workspace, resolved)
	if err != nil {
		return "", nil, err
	}
	client, err := s.workspaceLSPClient(workspace, folder, languageID)
	return resolved, client, err
}

func (c *lspClient) prepareRename(ctx context.Context, absolutePath string, content string, position int) (lspRange, string, bool, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	uri := fileURI(absolutePath)
	if err := c.syncDocument(absolutePath, uri, content); err != nil {
		return lspRange{}, "", false, err
	}
	raw, err := c.requestWithRetry(ctx, "textDocument/prepareRename", map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     lspPositionFromUTF16Offset(content, position),
	})
	if err != nil {
		return lspRange{}, "", false, err
	}
	return parseLSPPrepareRenameResponse(raw)
}

func (c *lspClient) rename(
	ctx context.Context,
	absolutePath string,
	content string,
	position int,
	newName string,
	openFiles map[string]WorkspaceRenameFileContent,
) (map[string][]lspTextEdit, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	for path, file := range openFiles {
		languageID, ok := lspLanguageIDForPath(path)
		if !ok || languageID != c.languageID || !pathWithinRoot(c.rootPath, path) {
			continue
		}
		if err := c.syncDocument(path, fileURI(path), file.Content); err != nil {
			return nil, err
		}
	}
	uri := fileURI(absolutePath)
	if err := c.syncDocument(absolutePath, uri, content); err != nil {
		return nil, err
	}
	raw, err := c.requestWithRetry(ctx, "textDocument/rename", map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     lspPositionFromUTF16Offset(content, position),
		"newName":      newName,
	})
	if err != nil {
		return nil, err
	}
	return parseLSPWorkspaceEdit(raw)
}

func parseLSPPrepareRenameResponse(raw json.RawMessage) (lspRange, string, bool, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return lspRange{}, "", false, nil
	}
	var direct lspRange
	if err := json.Unmarshal(raw, &direct); err == nil && direct.End != (lspPosition{}) {
		return direct, "", true, nil
	}
	var result lspPrepareRenameResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return lspRange{}, "", false, fmt.Errorf("parse prepare rename response: %w", err)
	}
	if result.Range != (lspRange{}) {
		return result.Range, result.Placeholder, true, nil
	}
	return lspRange{}, result.Placeholder, result.DefaultBehavior, nil
}

func parseLSPWorkspaceEdit(raw json.RawMessage) (map[string][]lspTextEdit, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return map[string][]lspTextEdit{}, nil
	}
	var edit lspWorkspaceEdit
	if err := json.Unmarshal(raw, &edit); err != nil {
		return nil, fmt.Errorf("parse rename response: %w", err)
	}
	output := make(map[string][]lspTextEdit, len(edit.Changes)+len(edit.DocumentChanges))
	for uri, edits := range edit.Changes {
		output[uri] = append(output[uri], edits...)
	}
	for _, rawChange := range edit.DocumentChanges {
		var change lspTextDocumentEdit
		if err := json.Unmarshal(rawChange, &change); err != nil {
			return nil, fmt.Errorf("parse rename document edit: %w", err)
		}
		if strings.TrimSpace(change.TextDocument.URI) == "" {
			return nil, fmt.Errorf("rename returned an unsupported workspace resource operation")
		}
		output[change.TextDocument.URI] = append(output[change.TextDocument.URI], change.Edits...)
	}
	return output, nil
}

func workspaceRenameOpenFiles(workspace Workspace, files []WorkspaceRenameFileContent) (map[string]WorkspaceRenameFileContent, error) {
	output := make(map[string]WorkspaceRenameFileContent, len(files))
	for _, file := range files {
		if strings.TrimSpace(file.FilePath) == "" {
			continue
		}
		if len([]byte(file.Content)) > maxWorkspaceEditorFileBytes || !utf8.ValidString(file.Content) {
			return nil, fmt.Errorf("open file %q has invalid editor content", file.FilePath)
		}
		resolved, err := resolveWorkspaceServicePath(workspace, file.FilePath)
		if err != nil {
			return nil, err
		}
		output[resolved] = file
	}
	return output, nil
}

func validateWorkspaceRenameOpenFile(path string, file WorkspaceRenameFileContent) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("open file %q was not found", file.FilePath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("open path %q is not a regular file", file.FilePath)
	}
	if file.ModifiedAt != "" && formatWorkspaceModifiedAt(info.ModTime()) != file.ModifiedAt {
		return fmt.Errorf("file %q changed on disk; reload it before renaming", file.FilePath)
	}
	return nil
}

func prepareWorkspaceRenameFiles(
	workspace Workspace,
	editsByURI map[string][]lspTextEdit,
	openFiles map[string]WorkspaceRenameFileContent,
) ([]workspaceRenameFile, error) {
	editsByPath := make(map[string][]lspTextEdit, len(editsByURI))
	for uri, edits := range editsByURI {
		resolved, err := pathFromFileURI(uri)
		if err != nil {
			return nil, fmt.Errorf("rename returned an invalid file URI")
		}
		if _, err := workspaceRelativeCandidate(workspace, resolved); err != nil {
			return nil, fmt.Errorf("rename attempted to edit a file outside the active workspace")
		}
		matchedPath := resolved
		for existingPath := range editsByPath {
			if samePath(existingPath, resolved) {
				matchedPath = existingPath
				break
			}
		}
		editsByPath[matchedPath] = append(editsByPath[matchedPath], edits...)
	}

	files := make([]workspaceRenameFile, 0, len(editsByPath))
	for resolved, edits := range editsByPath {
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("rename target was not found")
		}
		if info.Size() > maxWorkspaceEditorFileBytes {
			return nil, fmt.Errorf("rename target is larger than the %d byte editor limit", maxWorkspaceEditorFileBytes)
		}
		originalData, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read rename target: %w", err)
		}
		if !isWorkspaceTextLike(originalData) || !utf8.Valid(originalData) {
			return nil, fmt.Errorf("rename target appears to be binary")
		}
		original := string(originalData)
		content := original
		if open, ok := workspaceRenameOpenFileForPath(openFiles, resolved); ok {
			content = open.Content
		}
		updated, err := applyLSPTextEdits(content, edits)
		if err != nil {
			return nil, fmt.Errorf("apply rename edits to %q: %w", workspaceRelativePath(workspace, resolved), err)
		}
		if updated == content {
			continue
		}
		files = append(files, workspaceRenameFile{
			resolved: resolved,
			path:     workspaceRelativePath(workspace, resolved),
			original: original,
			updated:  updated,
			mode:     info.Mode().Perm(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

func applyLSPTextEdits(content string, edits []lspTextEdit) (string, error) {
	type offsetEdit struct {
		from, to int
		newText  string
	}
	converted := make([]offsetEdit, 0, len(edits))
	for _, edit := range edits {
		fromUTF16 := utf16OffsetForPosition(content, edit.Range.Start)
		toUTF16 := utf16OffsetForPosition(content, edit.Range.End)
		if lspPositionFromUTF16Offset(content, fromUTF16) != edit.Range.Start ||
			lspPositionFromUTF16Offset(content, toUTF16) != edit.Range.End ||
			toUTF16 < fromUTF16 {
			return "", fmt.Errorf("language server returned an invalid text range")
		}
		from, ok := byteOffsetForUTF16Offset(content, fromUTF16)
		if !ok {
			return "", fmt.Errorf("language server returned a split Unicode range")
		}
		to, ok := byteOffsetForUTF16Offset(content, toUTF16)
		if !ok {
			return "", fmt.Errorf("language server returned a split Unicode range")
		}
		converted = append(converted, offsetEdit{from: from, to: to, newText: edit.NewText})
	}
	sort.SliceStable(converted, func(i, j int) bool {
		if converted[i].from == converted[j].from {
			return converted[i].to > converted[j].to
		}
		return converted[i].from > converted[j].from
	})
	lastFrom := len(content)
	for _, edit := range converted {
		if edit.to > lastFrom {
			return "", fmt.Errorf("language server returned overlapping text edits")
		}
		lastFrom = edit.from
	}
	var builder strings.Builder
	builder.Grow(len(content))
	cursor := 0
	for index := len(converted) - 1; index >= 0; index-- {
		edit := converted[index]
		builder.WriteString(content[cursor:edit.from])
		builder.WriteString(edit.newText)
		cursor = edit.to
	}
	builder.WriteString(content[cursor:])
	return builder.String(), nil
}

func writeWorkspaceRenameFiles(files []workspaceRenameFile) error {
	written := make([]workspaceRenameFile, 0, len(files))
	for _, file := range files {
		if err := os.WriteFile(file.resolved, []byte(file.updated), file.mode); err != nil {
			for index := len(written) - 1; index >= 0; index-- {
				_ = os.WriteFile(written[index].resolved, []byte(written[index].original), written[index].mode)
			}
			return fmt.Errorf("write renamed file %q: %w", file.path, err)
		}
		written = append(written, file)
	}
	return nil
}

func workspaceRenameOpenFileForPath(files map[string]WorkspaceRenameFileContent, target string) (WorkspaceRenameFileContent, bool) {
	for path, file := range files {
		if samePath(path, target) {
			return file, true
		}
	}
	return WorkspaceRenameFileContent{}, false
}

func pathWithinRoot(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func renameFallbackRange(content string, position int) (int, int) {
	entries := utf16RuneEntries(content)
	from, to := position, position
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if entry.end > position {
			continue
		}
		if !isRenameIdentifierRune(entry.char) {
			break
		}
		from = entry.start
	}
	for _, entry := range entries {
		if entry.start < position {
			continue
		}
		if !isRenameIdentifierRune(entry.char) {
			break
		}
		to = entry.end
	}
	return from, to
}

func isRenameIdentifierRune(char rune) bool {
	return char == '_' || unicode.IsLetter(char) || unicode.IsDigit(char)
}

func textForUTF16Range(content string, from int, to int) string {
	fromByte, fromOK := byteOffsetForUTF16Offset(content, from)
	toByte, toOK := byteOffsetForUTF16Offset(content, to)
	if !fromOK || !toOK || toByte < fromByte {
		return ""
	}
	return content[fromByte:toByte]
}

func byteOffsetForUTF16Offset(content string, target int) (int, bool) {
	if target < 0 {
		return 0, false
	}
	utf16Offset := 0
	for byteOffset, char := range content {
		if utf16Offset == target {
			return byteOffset, true
		}
		units := utf16RuneLen(char)
		if utf16Offset+units > target {
			return 0, false
		}
		utf16Offset += units
	}
	return len(content), utf16Offset == target
}
