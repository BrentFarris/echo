package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/brent/echo/internal/tools"
)

const (
	defaultLSPQueryMaxResults = 100
	maxLSPQueryMaxResults     = 200
	maxLSPPreviewRunes        = 300
)

type workspaceCodeNavigator struct {
	service   *SystemService
	workspace Workspace
}

type lspHoverResponse struct {
	Contents json.RawMessage `json:"contents"`
	Range    *lspRange       `json:"range,omitempty"`
}

type lspDocumentSymbol struct {
	Name           string              `json:"name"`
	Detail         string              `json:"detail"`
	Kind           int                 `json:"kind"`
	Range          lspRange            `json:"range"`
	SelectionRange lspRange            `json:"selectionRange"`
	Children       []lspDocumentSymbol `json:"children"`
}

type lspSymbolInformation struct {
	Name          string                `json:"name"`
	Kind          int                   `json:"kind"`
	Location      lspDefinitionLocation `json:"location"`
	ContainerName string                `json:"containerName"`
}

func (s *SystemService) codeNavigator(workspace Workspace) tools.CodeNavigator {
	return workspaceCodeNavigator{service: s, workspace: workspace}
}

func (n workspaceCodeNavigator) QueryCode(ctx context.Context, request tools.CodeNavigationRequest) (tools.CodeNavigationResponse, error) {
	operation := normalizeCodeNavigationOperation(request.Operation)
	if operation == "" {
		return tools.CodeNavigationResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "operation is not supported"}
	}
	request.Path = strings.TrimSpace(request.Path)
	if request.Path == "" {
		return tools.CodeNavigationResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "path is required"}
	}

	resolved, err := resolveWorkspaceServicePath(n.workspace, request.Path)
	if err != nil {
		return tools.CodeNavigationResponse{}, err
	}
	file, err := readWorkspaceTextFile(n.workspace, resolved)
	if err != nil {
		return tools.CodeNavigationResponse{}, err
	}

	response := tools.CodeNavigationResponse{
		Operation: operation,
		Path:      file.Path,
	}
	languageID, ok := lspLanguageIDForPath(file.Path)
	if !ok {
		response.Message = "Language server navigation is not available for this file type."
		return response, nil
	}
	response.LanguageID = lspDocumentLanguageIDForPath(languageID, file.Path)

	folder, err := workspaceFolderForAbsolutePath(n.workspace, resolved)
	if err != nil {
		return tools.CodeNavigationResponse{}, err
	}
	client, err := n.service.workspaceLSPClient(n.workspace, folder, languageID)
	if err != nil {
		return tools.CodeNavigationResponse{}, err
	}

	opCtx, cancel := context.WithTimeout(ctx, lspDefinitionTimeout)
	defer cancel()

	if operation == "document_symbols" {
		return n.queryDocumentSymbols(opCtx, client, resolved, file, response, codeNavigationMaxResults(request.MaxResults))
	}

	position, offset, err := codeNavigationRequestPosition(file.Content, request)
	if err != nil {
		return tools.CodeNavigationResponse{}, err
	}
	codePosition := codePositionFromLSP(file.Content, position)
	codePosition.Offset = offset
	response.Position = &codePosition

	switch operation {
	case "completion", "members":
		return n.queryCompletion(opCtx, client, resolved, file, request, response, position, offset, codeNavigationMaxResults(request.MaxResults))
	case "hover":
		return n.queryHover(opCtx, client, resolved, file, response, position)
	default:
		return n.queryLocations(opCtx, client, resolved, file, request, response, position, codeNavigationMaxResults(request.MaxResults))
	}
}

func (n workspaceCodeNavigator) queryLocations(ctx context.Context, client *lspClient, resolved string, file WorkspaceFile, request tools.CodeNavigationRequest, response tools.CodeNavigationResponse, position lspPosition, maxResults int) (tools.CodeNavigationResponse, error) {
	method, ok := lspLocationMethodForOperation(response.Operation)
	if !ok {
		return tools.CodeNavigationResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "operation is not supported"}
	}
	params := map[string]any{}
	if response.Operation == "references" {
		includeDeclaration := true
		if request.IncludeDeclaration != nil {
			includeDeclaration = *request.IncludeDeclaration
		}
		params["context"] = map[string]any{"includeDeclaration": includeDeclaration}
	}
	locations, err := client.locations(ctx, method, resolved, file.Content, position, params)
	if err != nil {
		return tools.CodeNavigationResponse{}, err
	}
	converted, skippedOutside := n.codeLocations(locations)
	response.ResultCount = len(converted)
	response.SkippedOutsideWorkspace = skippedOutside
	response.Locations, response.Truncated = limitCodeLocations(converted, maxResults)
	response.ReturnedCount = len(response.Locations)
	response.Found = len(response.Locations) > 0
	if !response.Found {
		response.Message = codeNavigationNoLocationMessage(response.Operation, skippedOutside)
	}
	return response, nil
}

func (n workspaceCodeNavigator) queryCompletion(ctx context.Context, client *lspClient, resolved string, file WorkspaceFile, request tools.CodeNavigationRequest, response tools.CodeNavigationResponse, position lspPosition, offset int, maxResults int) (tools.CodeNavigationResponse, error) {
	triggerKind := request.TriggerKind
	triggerCharacter := request.TriggerCharacter
	if response.Operation == "members" && triggerKind == 0 {
		triggerKind = 2
		if triggerCharacter == "" {
			triggerCharacter = "."
		}
	}
	completion, err := client.unfilteredCompletion(ctx, resolved, file.Content, position, offset, triggerKind, triggerCharacter)
	if err != nil {
		return tools.CodeNavigationResponse{}, err
	}
	items := codeCompletionItems(file.Content, completion.Items)
	response.ResultCount = len(items)
	response.Items, response.Truncated = limitCodeCompletionItems(items, maxResults)
	response.ReturnedCount = len(response.Items)
	response.Found = len(response.Items) > 0
	if !response.Found {
		response.Message = "No completion items found."
	}
	return response, nil
}

func (n workspaceCodeNavigator) queryHover(ctx context.Context, client *lspClient, resolved string, file WorkspaceFile, response tools.CodeNavigationResponse, position lspPosition) (tools.CodeNavigationResponse, error) {
	hover, found, err := client.hover(ctx, resolved, file.Content, position)
	if err != nil {
		return tools.CodeNavigationResponse{}, err
	}
	if !found {
		response.Message = "No hover information found."
		return response, nil
	}
	response.Hover = lspHoverString(hover.Contents)
	if hover.Range != nil {
		rangeValue := codeRangeFromLSP(file.Content, *hover.Range)
		response.Range = &rangeValue
	}
	response.Found = strings.TrimSpace(response.Hover) != ""
	if !response.Found {
		response.Message = "No hover information found."
	}
	return response, nil
}

func (n workspaceCodeNavigator) queryDocumentSymbols(ctx context.Context, client *lspClient, resolved string, file WorkspaceFile, response tools.CodeNavigationResponse, maxResults int) (tools.CodeNavigationResponse, error) {
	raw, err := client.documentSymbols(ctx, resolved, file.Content)
	if err != nil {
		return tools.CodeNavigationResponse{}, err
	}
	symbols, err := n.codeDocumentSymbols(file, raw)
	if err != nil {
		return tools.CodeNavigationResponse{}, err
	}
	response.ResultCount = len(symbols)
	response.Symbols, response.Truncated = limitCodeSymbols(symbols, maxResults)
	response.ReturnedCount = len(response.Symbols)
	response.Found = len(response.Symbols) > 0
	if !response.Found {
		response.Message = "No document symbols found."
	}
	return response, nil
}

func (c *lspClient) locations(ctx context.Context, method string, absolutePath string, content string, position lspPosition, extraParams map[string]any) ([]lspDefinitionLocation, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()

	uri := fileURI(absolutePath)
	if err := c.syncDocument(absolutePath, uri, content); err != nil {
		return nil, err
	}
	params := map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     position,
	}
	for key, value := range extraParams {
		params[key] = value
	}
	raw, err := c.requestWithRetry(ctx, method, params)
	if err != nil {
		return nil, err
	}
	return parseLSPDefinitionResponse(raw)
}

func (c *lspClient) unfilteredCompletion(ctx context.Context, absolutePath string, content string, position lspPosition, offset int, triggerKind int, triggerCharacter string) (WorkspaceCompletionResponse, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()

	uri := fileURI(absolutePath)
	if err := c.syncDocument(absolutePath, uri, content); err != nil {
		return WorkspaceCompletionResponse{}, err
	}
	params := map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     position,
	}
	if triggerKind > 0 {
		context := map[string]any{"triggerKind": triggerKind}
		if triggerCharacter != "" {
			context["triggerCharacter"] = triggerCharacter
		}
		params["context"] = context
	}
	raw, err := c.requestWithRetry(ctx, "textDocument/completion", params)
	if err != nil {
		return WorkspaceCompletionResponse{}, err
	}
	return parseLSPCompletionResponse(raw, content, offset)
}

func (c *lspClient) hover(ctx context.Context, absolutePath string, content string, position lspPosition) (lspHoverResponse, bool, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()

	uri := fileURI(absolutePath)
	if err := c.syncDocument(absolutePath, uri, content); err != nil {
		return lspHoverResponse{}, false, err
	}
	raw, err := c.requestWithRetry(ctx, "textDocument/hover", map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     position,
	})
	if err != nil {
		return lspHoverResponse{}, false, err
	}
	return parseLSPHoverResponse(raw)
}

func (c *lspClient) documentSymbols(ctx context.Context, absolutePath string, content string) (json.RawMessage, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()

	uri := fileURI(absolutePath)
	if err := c.syncDocument(absolutePath, uri, content); err != nil {
		return nil, err
	}
	return c.requestWithRetry(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]string{"uri": uri},
	})
}

func (n workspaceCodeNavigator) codeLocations(locations []lspDefinitionLocation) ([]tools.CodeLocation, int) {
	output := make([]tools.CodeLocation, 0, len(locations))
	skippedOutside := 0
	for _, location := range locations {
		targetPath, err := pathFromFileURI(location.URI)
		if err != nil {
			continue
		}
		relative, err := workspaceRelativeCandidate(n.workspace, targetPath)
		if err != nil {
			skippedOutside++
			continue
		}
		resolved, err := resolveWorkspaceServicePath(n.workspace, relative)
		if err != nil {
			continue
		}
		targetFile, err := readWorkspaceTextFile(n.workspace, resolved)
		if err != nil {
			continue
		}
		output = append(output, tools.CodeLocation{
			Path:    targetFile.Path,
			Range:   codeRangeFromLSP(targetFile.Content, location.Range),
			Preview: codeLinePreview(targetFile.Content, location.Range.Start.Line+1),
		})
	}
	return output, skippedOutside
}

func (n workspaceCodeNavigator) codeDocumentSymbols(file WorkspaceFile, raw json.RawMessage) ([]tools.CodeSymbol, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []tools.CodeSymbol{}, nil
	}

	var infos []lspSymbolInformation
	if err := json.Unmarshal(trimmed, &infos); err == nil && len(infos) > 0 && infos[0].Location.URI != "" {
		return n.codeSymbolInformation(infos)
	}

	var symbols []lspDocumentSymbol
	if err := json.Unmarshal(trimmed, &symbols); err != nil {
		return nil, fmt.Errorf("parse document symbols: %w", err)
	}
	output := make([]tools.CodeSymbol, 0, len(symbols))
	for _, symbol := range symbols {
		appendCodeDocumentSymbol(&output, file.Path, file.Content, "", symbol)
	}
	return output, nil
}

func (n workspaceCodeNavigator) codeSymbolInformation(infos []lspSymbolInformation) ([]tools.CodeSymbol, error) {
	output := make([]tools.CodeSymbol, 0, len(infos))
	for _, info := range infos {
		targetPath, err := pathFromFileURI(info.Location.URI)
		if err != nil {
			continue
		}
		relative, err := workspaceRelativeCandidate(n.workspace, targetPath)
		if err != nil {
			continue
		}
		resolved, err := resolveWorkspaceServicePath(n.workspace, relative)
		if err != nil {
			continue
		}
		file, err := readWorkspaceTextFile(n.workspace, resolved)
		if err != nil {
			continue
		}
		output = append(output, tools.CodeSymbol{
			Name:          info.Name,
			Kind:          info.Kind,
			KindName:      lspSymbolKindName(info.Kind),
			ContainerName: info.ContainerName,
			Path:          file.Path,
			Range:         codeRangeFromLSP(file.Content, info.Location.Range),
		})
	}
	return output, nil
}

func appendCodeDocumentSymbol(output *[]tools.CodeSymbol, path string, content string, container string, symbol lspDocumentSymbol) {
	rangeValue := codeRangeFromLSP(content, symbol.Range)
	selectionRange := codeRangeFromLSP(content, symbol.SelectionRange)
	*output = append(*output, tools.CodeSymbol{
		Name:           symbol.Name,
		Kind:           symbol.Kind,
		KindName:       lspSymbolKindName(symbol.Kind),
		Detail:         symbol.Detail,
		ContainerName:  container,
		Path:           path,
		Range:          rangeValue,
		SelectionRange: &selectionRange,
	})

	childContainer := symbol.Name
	if container != "" {
		childContainer = container + "." + symbol.Name
	}
	for _, child := range symbol.Children {
		appendCodeDocumentSymbol(output, path, content, childContainer, child)
	}
}

func parseLSPHoverResponse(raw json.RawMessage) (lspHoverResponse, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return lspHoverResponse{}, false, nil
	}
	var response lspHoverResponse
	if err := json.Unmarshal(trimmed, &response); err != nil {
		return lspHoverResponse{}, false, fmt.Errorf("parse hover response: %w", err)
	}
	return response, true, nil
}

func lspHoverString(raw json.RawMessage) string {
	text := lspMarkedString(raw)
	return truncateLSPDocumentation(text)
}

func lspMarkedString(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err == nil {
		return strings.TrimSpace(value)
	}
	var markup struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(trimmed, &markup); err == nil && markup.Value != "" {
		return strings.TrimSpace(markup.Value)
	}
	var marked struct {
		Language string `json:"language"`
		Value    string `json:"value"`
	}
	if err := json.Unmarshal(trimmed, &marked); err == nil && marked.Value != "" {
		value = strings.TrimSpace(marked.Value)
		if marked.Language != "" {
			return "```" + marked.Language + "\n" + value + "\n```"
		}
		return value
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err == nil {
		parts := make([]string, 0, len(items))
		for _, item := range items {
			if text := lspMarkedString(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

func normalizeCodeNavigationOperation(operation string) string {
	operation = strings.ToLower(strings.TrimSpace(operation))
	operation = strings.ReplaceAll(operation, "-", "_")
	switch operation {
	case "definition", "references", "implementation", "hover", "completion", "members":
		return operation
	case "type_definition", "typedef", "type":
		return "type_definition"
	case "document_symbols", "document_symbol", "symbols", "outline":
		return "document_symbols"
	default:
		return ""
	}
}

func lspLocationMethodForOperation(operation string) (string, bool) {
	switch operation {
	case "definition":
		return "textDocument/definition", true
	case "references":
		return "textDocument/references", true
	case "implementation":
		return "textDocument/implementation", true
	case "type_definition":
		return "textDocument/typeDefinition", true
	default:
		return "", false
	}
}

func codeNavigationRequestPosition(content string, request tools.CodeNavigationRequest) (lspPosition, int, error) {
	if request.Position != nil {
		position := *request.Position
		if position < 0 || position > utf16Length(content) {
			return lspPosition{}, 0, tools.SafeError{Code: "invalid_arguments", Message: "position is outside the file"}
		}
		return lspPositionFromUTF16Offset(content, position), position, nil
	}
	if request.Line <= 0 || request.Column <= 0 {
		return lspPosition{}, 0, tools.SafeError{Code: "invalid_arguments", Message: "line and column are required for this operation"}
	}
	return lspPositionForLineColumn(content, request.Line, request.Column)
}

func lspPositionForLineColumn(content string, targetLine int, targetColumn int) (lspPosition, int, error) {
	if targetLine <= 0 || targetColumn <= 0 {
		return lspPosition{}, 0, tools.SafeError{Code: "invalid_arguments", Message: "line and column must be 1-based"}
	}
	line := 1
	column := 1
	lineCharacter := 0
	offset := 0
	if targetLine == line && targetColumn == column {
		return lspPosition{Line: 0, Character: 0}, 0, nil
	}
	for _, char := range content {
		if line == targetLine {
			if char == '\n' {
				break
			}
			units := utf16RuneLen(char)
			offset += units
			lineCharacter += units
			column++
			if column == targetColumn {
				return lspPosition{Line: targetLine - 1, Character: lineCharacter}, offset, nil
			}
			continue
		}

		offset += utf16RuneLen(char)
		if char == '\n' {
			line++
			column = 1
			lineCharacter = 0
			if line == targetLine && targetColumn == 1 {
				return lspPosition{Line: targetLine - 1, Character: 0}, offset, nil
			}
		}
	}
	if line < targetLine {
		return lspPosition{}, 0, tools.SafeError{Code: "invalid_arguments", Message: "line is outside the file"}
	}
	return lspPosition{}, 0, tools.SafeError{Code: "invalid_arguments", Message: "column is outside the line"}
}

func codePositionFromLSP(content string, position lspPosition) tools.CodePosition {
	return tools.CodePosition{
		Line:   position.Line + 1,
		Column: codeColumnFromLSP(content, position),
		Offset: utf16OffsetForPosition(content, position),
	}
}

func codeColumnFromLSP(content string, target lspPosition) int {
	if target.Line < 0 || target.Character < 0 {
		return 1
	}
	line := 0
	character := 0
	column := 1
	for _, char := range content {
		if line == target.Line {
			if character >= target.Character || char == '\n' {
				return column
			}
			character += utf16RuneLen(char)
			if character > target.Character {
				return column
			}
			column++
			continue
		}
		if char == '\n' {
			line++
			character = 0
			column = 1
		}
	}
	if line == target.Line {
		return column
	}
	return maxCodeInt(1, target.Character+1)
}

func codeRangeFromLSP(content string, lspRange lspRange) tools.CodeRange {
	return tools.CodeRange{
		Start: codePositionFromLSP(content, lspRange.Start),
		End:   codePositionFromLSP(content, lspRange.End),
	}
}

func codeRangeFromUTF16Offsets(content string, from int, to int) tools.CodeRange {
	if from < 0 {
		from = 0
	}
	if to < from {
		to = from
	}
	length := utf16Length(content)
	if from > length {
		from = length
	}
	if to > length {
		to = length
	}
	start := codePositionFromLSP(content, lspPositionFromUTF16Offset(content, from))
	end := codePositionFromLSP(content, lspPositionFromUTF16Offset(content, to))
	start.Offset = from
	end.Offset = to
	return tools.CodeRange{Start: start, End: end}
}

func codeCompletionItems(content string, items []WorkspaceCompletionItem) []tools.CodeCompletionItem {
	output := make([]tools.CodeCompletionItem, 0, len(items))
	for _, item := range items {
		replaceRange := codeRangeFromUTF16Offsets(content, item.From, item.To)
		output = append(output, tools.CodeCompletionItem{
			Label:         item.Label,
			Kind:          item.Kind,
			KindName:      lspCompletionKindName(item.Kind),
			Detail:        item.Detail,
			Documentation: item.Documentation,
			InsertText:    item.InsertText,
			ReplaceRange:  &replaceRange,
		})
	}
	return output
}

func codeLinePreview(content string, lineNumber int) string {
	if lineNumber <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if lineNumber > len(lines) {
		return ""
	}
	line := strings.TrimRight(lines[lineNumber-1], "\r")
	runes := []rune(line)
	if len(runes) > maxLSPPreviewRunes {
		return string(runes[:maxLSPPreviewRunes]) + "..."
	}
	return line
}

func codeNavigationMaxResults(value int) int {
	if value <= 0 {
		return defaultLSPQueryMaxResults
	}
	if value > maxLSPQueryMaxResults {
		return maxLSPQueryMaxResults
	}
	return value
}

func limitCodeLocations(locations []tools.CodeLocation, maxResults int) ([]tools.CodeLocation, bool) {
	if len(locations) <= maxResults {
		return locations, false
	}
	return append([]tools.CodeLocation(nil), locations[:maxResults]...), true
}

func limitCodeCompletionItems(items []tools.CodeCompletionItem, maxResults int) ([]tools.CodeCompletionItem, bool) {
	if len(items) <= maxResults {
		return items, false
	}
	return append([]tools.CodeCompletionItem(nil), items[:maxResults]...), true
}

func limitCodeSymbols(symbols []tools.CodeSymbol, maxResults int) ([]tools.CodeSymbol, bool) {
	if len(symbols) <= maxResults {
		return symbols, false
	}
	return append([]tools.CodeSymbol(nil), symbols[:maxResults]...), true
}

func codeNavigationNoLocationMessage(operation string, skippedOutside int) string {
	if skippedOutside > 0 {
		return "Language server returned locations outside the active workspace."
	}
	switch operation {
	case "definition":
		return "No definition found."
	case "references":
		return "No references found."
	case "implementation":
		return "No implementation found."
	case "type_definition":
		return "No type definition found."
	default:
		return "No locations found."
	}
}

func lspCompletionKindName(kind int) string {
	switch kind {
	case 1:
		return "text"
	case 2:
		return "method"
	case 3:
		return "function"
	case 4:
		return "constructor"
	case 5:
		return "field"
	case 6:
		return "variable"
	case 7:
		return "class"
	case 8:
		return "interface"
	case 9:
		return "module"
	case 10:
		return "property"
	case 11:
		return "unit"
	case 12:
		return "value"
	case 13:
		return "enum"
	case 14:
		return "keyword"
	case 15:
		return "snippet"
	case 16:
		return "color"
	case 17:
		return "file"
	case 18:
		return "reference"
	case 19:
		return "folder"
	case 20:
		return "enum_member"
	case 21:
		return "constant"
	case 22:
		return "struct"
	case 23:
		return "event"
	case 24:
		return "operator"
	case 25:
		return "type_parameter"
	default:
		return ""
	}
}

func lspSymbolKindName(kind int) string {
	switch kind {
	case 1:
		return "file"
	case 2:
		return "module"
	case 3:
		return "namespace"
	case 4:
		return "package"
	case 5:
		return "class"
	case 6:
		return "method"
	case 7:
		return "property"
	case 8:
		return "field"
	case 9:
		return "constructor"
	case 10:
		return "enum"
	case 11:
		return "interface"
	case 12:
		return "function"
	case 13:
		return "variable"
	case 14:
		return "constant"
	case 15:
		return "string"
	case 16:
		return "number"
	case 17:
		return "boolean"
	case 18:
		return "array"
	case 19:
		return "object"
	case 20:
		return "key"
	case 21:
		return "null"
	case 22:
		return "enum_member"
	case 23:
		return "struct"
	case 24:
		return "event"
	case 25:
		return "operator"
	case 26:
		return "type_parameter"
	default:
		return ""
	}
}

func maxCodeInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}
