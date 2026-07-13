package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	lspCompletionTimeout = 15 * time.Second
	lspDefinitionTimeout = 15 * time.Second
	lspInitializeTimeout = 20 * time.Second
	lspMaxDocumentation  = 4096
)

type WorkspaceCompletionRequest struct {
	FilePath         string `json:"filePath"`
	Content          string `json:"content"`
	Position         int    `json:"position"`
	TriggerKind      int    `json:"triggerKind,omitempty"`
	TriggerCharacter string `json:"triggerCharacter,omitempty"`
}

type WorkspaceCompletionResponse struct {
	WorkspaceID  string                    `json:"workspaceId"`
	FilePath     string                    `json:"filePath"`
	IsIncomplete bool                      `json:"isIncomplete"`
	Items        []WorkspaceCompletionItem `json:"items"`
}

type WorkspaceCompletionItem struct {
	Label               string              `json:"label"`
	Kind                int                 `json:"kind,omitempty"`
	Detail              string              `json:"detail,omitempty"`
	Documentation       string              `json:"documentation,omitempty"`
	InsertText          string              `json:"insertText"`
	SortText            string              `json:"sortText,omitempty"`
	FilterText          string              `json:"filterText,omitempty"`
	From                int                 `json:"from"`
	To                  int                 `json:"to"`
	AdditionalTextEdits []WorkspaceTextEdit `json:"additionalTextEdits,omitempty"`
}

type WorkspaceTextEdit struct {
	From    int    `json:"from"`
	To      int    `json:"to"`
	NewText string `json:"newText"`
}

type WorkspaceDefinitionRequest struct {
	FilePath string `json:"filePath"`
	Content  string `json:"content"`
	Position int    `json:"position"`
}

type WorkspaceDefinitionResponse struct {
	WorkspaceID string `json:"workspaceId"`
	SourcePath  string `json:"sourcePath"`
	TargetPath  string `json:"targetPath,omitempty"`
	Position    int    `json:"position"`
	Line        int    `json:"line"`
	Character   int    `json:"character"`
	Found       bool   `json:"found"`
	Message     string `json:"message,omitempty"`
}

type WorkspaceReferenceRequest struct {
	FilePath           string `json:"filePath"`
	Content            string `json:"content"`
	Position           int    `json:"position"`
	IncludeDeclaration *bool  `json:"includeDeclaration,omitempty"`
	MaxResults         int    `json:"maxResults,omitempty"`
}

type WorkspaceReferenceResponse struct {
	WorkspaceID             string                       `json:"workspaceId"`
	SourcePath              string                       `json:"sourcePath"`
	Position                int                          `json:"position"`
	Found                   bool                         `json:"found"`
	Message                 string                       `json:"message,omitempty"`
	Locations               []WorkspaceReferenceLocation `json:"locations,omitempty"`
	ResultCount             int                          `json:"resultCount,omitempty"`
	ReturnedCount           int                          `json:"returnedCount,omitempty"`
	Truncated               bool                         `json:"truncated,omitempty"`
	SkippedOutsideWorkspace int                          `json:"skippedOutsideWorkspace,omitempty"`
}

type WorkspaceReferenceLocation struct {
	Path         string                          `json:"path"`
	Range        WorkspaceReferenceRange         `json:"range"`
	Preview      string                          `json:"preview,omitempty"`
	PreviewLines []WorkspaceReferencePreviewLine `json:"previewLines,omitempty"`
}

type WorkspaceReferenceRange struct {
	Start WorkspaceReferencePosition `json:"start"`
	End   WorkspaceReferencePosition `json:"end"`
}

type WorkspaceReferencePosition struct {
	Line   int `json:"line"`
	Column int `json:"column"`
	Offset int `json:"offset"`
}

type WorkspaceReferencePreviewLine struct {
	Line           int    `json:"line"`
	Text           string `json:"text"`
	HighlightStart int    `json:"highlightStart"`
	HighlightEnd   int    `json:"highlightEnd"`
}

type workspaceLocationLookup struct {
	Method                  string
	PositionName            string
	UnavailableMessage      string
	NoLocationsMessage      string
	OutsideWorkspaceMessage string
	ExtraParams             func(WorkspaceReferenceRequest) map[string]any
}

type lspServerCommand struct {
	name string
	args []string
}

type lspLanguageDefinition struct {
	ID                 string
	DisplayName        string
	Extensions         []string
	WorkspaceMarkers   []string
	Command            lspServerCommand
	DocumentLanguageID func(path string) string
	CompletionFilter   func([]WorkspaceCompletionItem) []WorkspaceCompletionItem
}

var (
	lspCommandForLanguage = registeredLSPCommandForLanguage
	lspLanguagesByID      = map[string]lspLanguageDefinition{}
	lspLanguageIDsByExt   = map[string]string{}
)

type lspClient struct {
	languageID    string
	workspaceID   string
	workspaceName string
	rootPath      string
	rootURI       string
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	readerDone    chan struct{}

	writeMu     sync.Mutex
	operationMu sync.Mutex
	pendingMu   sync.Mutex
	pending     map[string]chan lspPendingResponse
	nextID      uint64

	docMu     sync.Mutex
	documents map[string]lspDocumentState

	closeOnce      sync.Once
	resolveFilePath func(absolutePath string) string
	onDiagnostics  func(workspaceID string, filePath string, diagnostics []WorkspaceDiagnostic)
}

type lspDocumentState struct {
	content string
	version int
}

type lspPendingResponse struct {
	result json.RawMessage
	err    error
}

type lspWireMessage struct {
	JSONRPC string            `json:"jsonrpc,omitempty"`
	ID      *json.RawMessage  `json:"id,omitempty"`
	Method  string            `json:"method,omitempty"`
	Params  json.RawMessage   `json:"params,omitempty"`
	Result  json.RawMessage   `json:"result,omitempty"`
	Error   *lspResponseError `json:"error,omitempty"`
}

type lspResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspTextEdit struct {
	Range   lspRange `json:"range"`
	NewText string   `json:"newText"`
}

type lspInsertReplaceEdit struct {
	NewText string   `json:"newText"`
	Insert  lspRange `json:"insert"`
	Replace lspRange `json:"replace"`
}

type lspCompletionList struct {
	IsIncomplete bool `json:"isIncomplete"`
	ItemDefaults struct {
		EditRange json.RawMessage `json:"editRange"`
	} `json:"itemDefaults"`
	Items []lspCompletionItem `json:"items"`
}

type lspCompletionItem struct {
	Label               string            `json:"label"`
	Kind                int               `json:"kind"`
	Detail              string            `json:"detail"`
	Documentation       json.RawMessage   `json:"documentation"`
	InsertText          string            `json:"insertText"`
	SortText            string            `json:"sortText"`
	FilterText          string            `json:"filterText"`
	TextEdit            json.RawMessage   `json:"textEdit"`
	AdditionalTextEdits []json.RawMessage `json:"additionalTextEdits"`
}

type lspDefinitionLocation struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

type lspDefinitionLocationLink struct {
	TargetURI            string   `json:"targetUri"`
	TargetRange          lspRange `json:"targetRange"`
	TargetSelectionRange lspRange `json:"targetSelectionRange"`
}

// LSP diagnostic types for textDocument/publishDiagnostics

type lspDiagnosticRelatedInformation struct {
	Location lspDefinitionLocation `json:"location"`
	Message  string                `json:"message"`
}

type lspDiagnostic struct {
	Range                       lspRange                          `json:"range"`
	Severity                    int                               `json:"severity"`
	Code                      json.RawMessage                     `json:"code,omitempty"`
	CodeDescription           lspCodeDescription                  `json:"codeDescription,omitempty"`
	Source                    string                              `json:"source,omitempty"`
	Message                   string                              `json:"message"`
	RelatedInformation        []lspDiagnosticRelatedInformation   `json:"relatedInformation,omitempty"`
	Tags                      []int                               `json:"tags,omitempty"`
}

type lspCodeDescription struct {
	Href string `json:"href"`
}

// WorkspaceDiagnostic is the diagnostic payload emitted in the echo:lsp:diagnostics event.
type WorkspaceDiagnostic struct {
	Range            WorkspaceDiagnosticRange     `json:"range"`
	Severity         int                          `json:"severity"`
	Code             any                          `json:"code,omitempty"`
	CodeDescription  string                       `json:"codeDescription,omitempty"`
	Source           string                       `json:"source,omitempty"`
	Message          string                       `json:"message"`
	RelatedInformation []WorkspaceDiagnosticRelatedInfo `json:"relatedInformation,omitempty"`
	Tags             []int                        `json:"tags,omitempty"`
}

type WorkspaceDiagnosticRange struct {
	Start WorkspaceDiagnosticPosition `json:"start"`
	End   WorkspaceDiagnosticPosition `json:"end"`
}

type WorkspaceDiagnosticPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type WorkspaceDiagnosticRelatedInfo struct {
	Location WorkspaceDiagnosticLocation `json:"location"`
	Message  string                      `json:"message"`
}

type WorkspaceDiagnosticLocation struct {
	Path  string                       `json:"path"`
	Range WorkspaceDiagnosticRange     `json:"range"`
}

// LSPDiagnosticsPayload is the data emitted in the echo:lsp:diagnostics event.
type LSPDiagnosticsPayload struct {
	WorkspaceID string                `json:"workspaceId"`
	FilePath    string                `json:"filePath"`
	Diagnostics []WorkspaceDiagnostic `json:"diagnostics"`
}

func (s *SystemService) SyncLSPDocument(workspaceID string, request WorkspaceDefinitionRequest) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return
	}
	if strings.TrimSpace(request.FilePath) == "" {
		return
	}
	languageID, ok := lspLanguageIDForPath(request.FilePath)
	if !ok {
		return
	}
	resolved, err := resolveWorkspaceServicePath(workspace, request.FilePath)
	if err != nil {
		return
	}
	folder, err := workspaceFolderForAbsolutePath(workspace, resolved)
	if err != nil {
		return
	}
	client, err := s.workspaceLSPClient(workspace, folder, languageID)
	if err != nil {
		return
	}
	uri := fileURI(resolved)
	_ = client.syncDocumentNoLock(resolved, uri, request.Content)
}

func (c *lspClient) syncDocumentNoLock(absolutePath string, uri string, content string) error {
	c.docMu.Lock()
	state, opened := c.documents[uri]
	if !opened {
		state = lspDocumentState{content: content, version: 1}
		c.documents[uri] = state
		c.docMu.Unlock()
		return c.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri":        uri,
				"languageId": lspDocumentLanguageIDForPath(c.languageID, absolutePath),
				"version":    state.version,
				"text":       content,
			},
		})
	}
	if state.content == content {
		c.docMu.Unlock()
		return nil
	}
	state.content = content
	state.version++
	c.documents[uri] = state
	c.docMu.Unlock()

	return c.notify("textDocument/didChange", map[string]any{
		"textDocument": map[string]any{
			"uri":     uri,
			"version": state.version,
		},
		"contentChanges": []map[string]string{
			{"text": content},
		},
	})
}

func (s *SystemService) CompleteWorkspaceFile(workspaceID string, request WorkspaceCompletionRequest) (WorkspaceCompletionResponse, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceCompletionResponse{}, err
	}
	if strings.TrimSpace(request.FilePath) == "" {
		return WorkspaceCompletionResponse{}, fmt.Errorf("file path is required")
	}
	if len([]byte(request.Content)) > maxWorkspaceEditorFileBytes {
		return WorkspaceCompletionResponse{}, fmt.Errorf("content is larger than the %d byte editor limit", maxWorkspaceEditorFileBytes)
	}
	if request.Position < 0 || request.Position > utf16Length(request.Content) {
		return WorkspaceCompletionResponse{}, fmt.Errorf("completion position is outside the file")
	}

	languageID, ok := lspLanguageIDForPath(request.FilePath)
	if !ok {
		return WorkspaceCompletionResponse{
			WorkspaceID: workspace.ID,
			FilePath:    request.FilePath,
			Items:       []WorkspaceCompletionItem{},
		}, nil
	}

	resolved, err := resolveWorkspaceServicePath(workspace, request.FilePath)
	if err != nil {
		return WorkspaceCompletionResponse{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return WorkspaceCompletionResponse{}, fmt.Errorf("file was not found")
	}
	if !info.Mode().IsRegular() {
		return WorkspaceCompletionResponse{}, fmt.Errorf("path is not a regular file")
	}

	folder, err := workspaceFolderForAbsolutePath(workspace, resolved)
	if err != nil {
		return WorkspaceCompletionResponse{}, err
	}
	client, err := s.workspaceLSPClient(workspace, folder, languageID)
	if err != nil {
		return WorkspaceCompletionResponse{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), lspCompletionTimeout)
	defer cancel()
	response, err := client.complete(ctx, resolved, request)
	if err != nil {
		return WorkspaceCompletionResponse{}, err
	}
	response.WorkspaceID = workspace.ID
	response.FilePath = workspaceRelativePath(workspace, resolved)
	return response, nil
}

func (s *SystemService) FindWorkspaceFileDefinition(workspaceID string, request WorkspaceDefinitionRequest) (WorkspaceDefinitionResponse, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceDefinitionResponse{}, err
	}
	if strings.TrimSpace(request.FilePath) == "" {
		return WorkspaceDefinitionResponse{}, fmt.Errorf("file path is required")
	}
	if len([]byte(request.Content)) > maxWorkspaceEditorFileBytes {
		return WorkspaceDefinitionResponse{}, fmt.Errorf("content is larger than the %d byte editor limit", maxWorkspaceEditorFileBytes)
	}
	if request.Position < 0 || request.Position > utf16Length(request.Content) {
		return WorkspaceDefinitionResponse{}, fmt.Errorf("definition position is outside the file")
	}

	response := WorkspaceDefinitionResponse{
		WorkspaceID: workspace.ID,
		SourcePath:  request.FilePath,
	}
	languageID, ok := lspLanguageIDForPath(request.FilePath)
	if !ok {
		response.Message = "Definition lookup is not available for this file type."
		return response, nil
	}

	resolved, err := resolveWorkspaceServicePath(workspace, request.FilePath)
	if err != nil {
		return WorkspaceDefinitionResponse{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return WorkspaceDefinitionResponse{}, fmt.Errorf("file was not found")
	}
	if !info.Mode().IsRegular() {
		return WorkspaceDefinitionResponse{}, fmt.Errorf("path is not a regular file")
	}
	response.SourcePath = workspaceRelativePath(workspace, resolved)

	folder, err := workspaceFolderForAbsolutePath(workspace, resolved)
	if err != nil {
		return WorkspaceDefinitionResponse{}, err
	}
	client, err := s.workspaceLSPClient(workspace, folder, languageID)
	if err != nil {
		return WorkspaceDefinitionResponse{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), lspDefinitionTimeout)
	defer cancel()
	locations, err := client.definition(ctx, resolved, request)
	if err != nil {
		return WorkspaceDefinitionResponse{}, err
	}
	// If no definition found, fall back to type definition (e.g., for Go types like options.App).
	if len(locations) == 0 {
		locations, err = client.typeDefinition(ctx, resolved, request)
		if err != nil {
			return WorkspaceDefinitionResponse{}, err
		}
	}
	if len(locations) == 0 {
		response.Message = "No definition found."
		return response, nil
	}

	for _, location := range locations {
		targetPath, err := pathFromFileURI(location.URI)
		if err != nil {
			continue
		}
		file, err := readDefinitionTargetFile(workspace, targetPath)
		if err != nil {
			continue
		}
		response.TargetPath = file.Path
		response.Position = utf16OffsetForPosition(file.Content, location.Range.Start)
		response.Line = location.Range.Start.Line
		response.Character = location.Range.Start.Character
		response.Found = true
		response.Message = ""
		return response, nil
	}

	response.Message = "No text file definition found."
	return response, nil
}

// readDefinitionTargetFile preserves workspace-relative paths for project files,
// while allowing language servers to point at readable source in module caches,
// SDKs, and standard libraries.
func readDefinitionTargetFile(workspace Workspace, targetPath string) (WorkspaceFile, error) {
	if _, err := workspaceRelativeCandidate(workspace, targetPath); err == nil {
		return readWorkspaceTextFile(workspace, targetPath)
	}
	resolved, err := resolveExternalTextFilePath(targetPath)
	if err != nil {
		return WorkspaceFile{}, err
	}
	return readExternalTextFile(resolved)
}

func (s *SystemService) FindWorkspaceFileReferences(workspaceID string, request WorkspaceReferenceRequest) (WorkspaceReferenceResponse, error) {
	return s.findWorkspaceFileLocations(workspaceID, request, workspaceLocationLookup{
		Method:                  "textDocument/references",
		PositionName:            "reference",
		UnavailableMessage:      "Reference lookup is not available for this file type.",
		NoLocationsMessage:      "No references found.",
		OutsideWorkspaceMessage: "References are outside the active workspace.",
		ExtraParams: func(request WorkspaceReferenceRequest) map[string]any {
			includeDeclaration := true
			if request.IncludeDeclaration != nil {
				includeDeclaration = *request.IncludeDeclaration
			}
			return map[string]any{"context": map[string]any{"includeDeclaration": includeDeclaration}}
		},
	})
}

func (s *SystemService) FindWorkspaceFileImplementations(workspaceID string, request WorkspaceReferenceRequest) (WorkspaceReferenceResponse, error) {
	return s.findWorkspaceFileLocations(workspaceID, request, workspaceLocationLookup{
		Method:                  "textDocument/implementation",
		PositionName:            "implementation",
		UnavailableMessage:      "Implementation lookup is not available for this file type.",
		NoLocationsMessage:      "No implementations found.",
		OutsideWorkspaceMessage: "Implementations are outside the active workspace.",
	})
}

func (s *SystemService) findWorkspaceFileLocations(workspaceID string, request WorkspaceReferenceRequest, lookup workspaceLocationLookup) (WorkspaceReferenceResponse, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceReferenceResponse{}, err
	}
	if strings.TrimSpace(request.FilePath) == "" {
		return WorkspaceReferenceResponse{}, fmt.Errorf("file path is required")
	}
	if len([]byte(request.Content)) > maxWorkspaceEditorFileBytes {
		return WorkspaceReferenceResponse{}, fmt.Errorf("content is larger than the %d byte editor limit", maxWorkspaceEditorFileBytes)
	}
	if request.Position < 0 || request.Position > utf16Length(request.Content) {
		return WorkspaceReferenceResponse{}, fmt.Errorf("%s position is outside the file", lookup.PositionName)
	}

	response := WorkspaceReferenceResponse{
		WorkspaceID: workspace.ID,
		SourcePath:  request.FilePath,
		Position:    request.Position,
	}
	languageID, ok := lspLanguageIDForPath(request.FilePath)
	if !ok {
		response.Message = lookup.UnavailableMessage
		return response, nil
	}

	resolved, err := resolveWorkspaceServicePath(workspace, request.FilePath)
	if err != nil {
		return WorkspaceReferenceResponse{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return WorkspaceReferenceResponse{}, fmt.Errorf("file was not found")
	}
	if !info.Mode().IsRegular() {
		return WorkspaceReferenceResponse{}, fmt.Errorf("path is not a regular file")
	}
	response.SourcePath = workspaceRelativePath(workspace, resolved)

	folder, err := workspaceFolderForAbsolutePath(workspace, resolved)
	if err != nil {
		return WorkspaceReferenceResponse{}, err
	}
	client, err := s.workspaceLSPClient(workspace, folder, languageID)
	if err != nil {
		return WorkspaceReferenceResponse{}, err
	}

	extraParams := map[string]any{}
	if lookup.ExtraParams != nil {
		extraParams = lookup.ExtraParams(request)
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspDefinitionTimeout)
	defer cancel()
	locations, err := client.locations(
		ctx,
		lookup.Method,
		resolved,
		request.Content,
		lspPositionFromUTF16Offset(request.Content, request.Position),
		extraParams,
	)
	if err != nil {
		return WorkspaceReferenceResponse{}, err
	}

	converted, skippedOutside := workspaceReferenceLocations(workspace, resolved, request.Content, locations)
	response.ResultCount = len(converted)
	response.SkippedOutsideWorkspace = skippedOutside
	response.Locations, response.Truncated = limitWorkspaceReferenceLocations(converted, codeNavigationMaxResults(request.MaxResults))
	response.ReturnedCount = len(response.Locations)
	response.Found = len(response.Locations) > 0
	if !response.Found {
		if skippedOutside > 0 {
			response.Message = lookup.OutsideWorkspaceMessage
		} else {
			response.Message = lookup.NoLocationsMessage
		}
	}
	return response, nil
}

func workspaceReferenceLocations(workspace Workspace, sourceResolved string, sourceContent string, locations []lspDefinitionLocation) ([]WorkspaceReferenceLocation, int) {
	output := make([]WorkspaceReferenceLocation, 0, len(locations))
	skippedOutside := 0
	contentByPath := map[string]WorkspaceFile{}
	sourceFile := WorkspaceFile{
		WorkspaceID: workspace.ID,
		Path:        workspaceRelativePath(workspace, sourceResolved),
		Content:     sourceContent,
	}

	for _, location := range locations {
		targetPath, err := pathFromFileURI(location.URI)
		if err != nil {
			continue
		}
		if _, err := workspaceRelativeCandidate(workspace, targetPath); err != nil {
			skippedOutside++
			continue
		}

		file := sourceFile
		if !samePath(targetPath, sourceResolved) {
			cached, ok := contentByPath[targetPath]
			if !ok {
				cached, err = readWorkspaceTextFile(workspace, targetPath)
				if err != nil {
					continue
				}
				contentByPath[targetPath] = cached
			}
			file = cached
		}
		output = append(output, WorkspaceReferenceLocation{
			Path:         file.Path,
			Range:        workspaceReferenceRangeFromLSP(file.Content, location.Range),
			Preview:      codeLinePreview(file.Content, location.Range.Start.Line+1),
			PreviewLines: workspaceReferencePreviewLines(file.Content, location.Range, 4),
		})
	}
	return output, skippedOutside
}

func workspaceReferenceRangeFromLSP(content string, target lspRange) WorkspaceReferenceRange {
	start := codePositionFromLSP(content, target.Start)
	end := codePositionFromLSP(content, target.End)
	return WorkspaceReferenceRange{
		Start: WorkspaceReferencePosition{
			Line:   start.Line,
			Column: start.Column,
			Offset: start.Offset,
		},
		End: WorkspaceReferencePosition{
			Line:   end.Line,
			Column: end.Column,
			Offset: end.Offset,
		},
	}
}

func workspaceReferencePreviewLines(content string, target lspRange, contextLines int) []WorkspaceReferencePreviewLine {
	if target.Start.Line < 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	targetLine := target.Start.Line + 1
	if targetLine <= 0 || targetLine > len(lines) {
		return nil
	}
	firstLine := targetLine - contextLines
	if firstLine < 1 {
		firstLine = 1
	}
	lastLine := targetLine + contextLines
	if lastLine > len(lines) {
		lastLine = len(lines)
	}

	output := make([]WorkspaceReferencePreviewLine, 0, lastLine-firstLine+1)
	for lineNumber := firstLine; lineNumber <= lastLine; lineNumber++ {
		text := strings.TrimRight(lines[lineNumber-1], "\r")
		runes := []rune(text)
		if len(runes) > maxLSPPreviewRunes {
			text = string(runes[:maxLSPPreviewRunes]) + "..."
		}
		previewLine := WorkspaceReferencePreviewLine{
			Line:           lineNumber,
			Text:           text,
			HighlightStart: -1,
			HighlightEnd:   -1,
		}
		if lineNumber == targetLine {
			previewLine.HighlightStart = target.Start.Character
			previewLine.HighlightEnd = target.Start.Character
			if target.End.Line == target.Start.Line {
				previewLine.HighlightEnd = target.End.Character
			}
			if previewLine.HighlightEnd < previewLine.HighlightStart {
				previewLine.HighlightEnd = previewLine.HighlightStart
			}
		}
		output = append(output, previewLine)
	}
	return output
}

func limitWorkspaceReferenceLocations(locations []WorkspaceReferenceLocation, maxResults int) ([]WorkspaceReferenceLocation, bool) {
	if len(locations) <= maxResults {
		return locations, false
	}
	return append([]WorkspaceReferenceLocation(nil), locations[:maxResults]...), true
}

func (s *SystemService) workspaceLSPClient(workspace Workspace, folder WorkspaceFolder, languageID string) (*lspClient, error) {
	key := workspaceLSPClientKey(workspace.ID, folder.ID, languageID)
	s.lspMu.Lock()
	existing := s.lspClients[key]
	s.lspMu.Unlock()
	if existing != nil {
		return existing, nil
	}

	onDiagnostics := func(workspaceID string, filePath string, diagnostics []WorkspaceDiagnostic) {
		s.emitLSPDiagnosticsEvent(workspaceID, filePath, diagnostics)
	}
	resolvePath := func(absolutePath string) string {
		return workspaceRelativePath(workspace, absolutePath)
	}
	client, err := startWorkspaceLSPClient(workspace, folder, languageID, onDiagnostics, resolvePath)
	if err != nil {
		return nil, err
	}

	s.lspMu.Lock()
	defer s.lspMu.Unlock()
	if existing = s.lspClients[key]; existing != nil {
		client.close()
		return existing, nil
	}
	s.lspClients[key] = client
	return client, nil
}

func startWorkspaceLSPClient(workspace Workspace, folder WorkspaceFolder, languageID string, onDiagnostics func(WorkspaceID string, filePath string, diagnostics []WorkspaceDiagnostic), resolveFilePath func(string) string) (*lspClient, error) {
	command, ok := lspCommandForLanguage(languageID)
	if !ok {
		return nil, fmt.Errorf("language server is not configured for %s files", languageID)
	}
	if _, err := exec.LookPath(command.name); err != nil {
		return nil, fmt.Errorf("%s language server unavailable: %s was not found on PATH", languageID, command.name)
	}
	rootPath, err := filepath.Abs(folder.Path)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace path: %w", err)
	}
	rootURI := fileURI(rootPath)
	cmd := newLSPServerProcess(command, rootPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open language server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open language server stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open language server stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s language server: %w", languageID, err)
	}

	client := &lspClient{
		languageID:    languageID,
		workspaceID:   workspace.ID,
		workspaceName: workspace.DisplayName + "/" + folder.Label,
		rootPath:      rootPath,
		rootURI:       rootURI,
		cmd:           cmd,
		stdin:         stdin,
		readerDone:    make(chan struct{}),
		pending:       make(map[string]chan lspPendingResponse),
		documents:     make(map[string]lspDocumentState),
		resolveFilePath: resolveFilePath,
		onDiagnostics: onDiagnostics,
	}
	go io.Copy(io.Discard, stderr)
	go client.readLoop(stdout)
	go func() {
		err := cmd.Wait()
		client.failPending(fmt.Errorf("%s language server stopped: %w", languageID, err))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), lspInitializeTimeout)
	defer cancel()
	if err := client.initialize(ctx, workspace.DisplayName+"/"+folder.Label); err != nil {
		client.close()
		return nil, err
	}
	return client, nil
}

func newLSPServerProcess(command lspServerCommand, rootPath string) *exec.Cmd {
	cmd := exec.Command(command.name, command.args...)
	cmd.Dir = rootPath
	cmd.Env = os.Environ()
	configureWorkspaceCommandProcess(cmd)
	return cmd
}

func (c *lspClient) initialize(ctx context.Context, workspaceName string) error {
	params := map[string]any{
		"processId": nil,
		"rootPath":  c.rootPath,
		"rootUri":   c.rootURI,
		"workspaceFolders": []map[string]string{
			{
				"uri":  c.rootURI,
				"name": workspaceName,
			},
		},
		"capabilities": map[string]any{
			"workspace": map[string]any{
				"configuration":    true,
				"workspaceFolders": true,
				"workspaceEdit": map[string]any{
					"documentChanges": true,
				},
			},
			"textDocument": map[string]any{
				"synchronization": map[string]any{
					"didSave": true,
				},
				"completion": map[string]any{
					"contextSupport": true,
					"completionItem": map[string]any{
						"documentationFormat":  []string{"markdown", "plaintext"},
						"insertReplaceSupport": true,
						"labelDetailsSupport":  true,
						"preselectSupport":     true,
						"snippetSupport":       false,
					},
					"completionList": map[string]any{
						"itemDefaults": []string{"editRange", "insertTextFormat", "data"},
					},
				},
				"codeAction": map[string]any{
					"codeActionLiteralSupport": map[string]any{
						"codeActionKind": map[string]any{
							"valueSet": []string{"source.organizeImports"},
						},
					},
					"isPreferredSupport": true,
				},
				"definition": map[string]any{
					"linkSupport": true,
				},
				"documentSymbol": map[string]any{
					"hierarchicalDocumentSymbolSupport": true,
				},
				"hover": map[string]any{
					"contentFormat": []string{"markdown", "plaintext"},
				},
				"implementation": map[string]any{
					"linkSupport": true,
				},
				"references": map[string]any{},
				"rename": map[string]any{
					"prepareSupport": true,
				},
				"typeDefinition": map[string]any{
					"linkSupport": true,
				},
			},
		},
	}
	if _, err := c.request(ctx, "initialize", params); err != nil {
		return err
	}
	return c.notify("initialized", map[string]any{})
}

func (c *lspClient) complete(ctx context.Context, absolutePath string, request WorkspaceCompletionRequest) (WorkspaceCompletionResponse, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()

	uri := fileURI(absolutePath)
	if err := c.syncDocument(absolutePath, uri, request.Content); err != nil {
		return WorkspaceCompletionResponse{}, err
	}
	params := map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     lspPositionFromUTF16Offset(request.Content, request.Position),
	}
	if request.TriggerKind > 0 {
		context := map[string]any{"triggerKind": request.TriggerKind}
		if request.TriggerCharacter != "" {
			context["triggerCharacter"] = request.TriggerCharacter
		}
		params["context"] = context
	}
	raw, err := c.requestWithRetry(ctx, "textDocument/completion", params)
	if err != nil {
		return WorkspaceCompletionResponse{}, err
	}
	response, err := parseLSPCompletionResponse(raw, request.Content, request.Position)
	if err != nil {
		return WorkspaceCompletionResponse{}, err
	}
	response.Items = filterLSPCompletionItems(c.languageID, response.Items)
	return response, nil
}

func (c *lspClient) definition(ctx context.Context, absolutePath string, request WorkspaceDefinitionRequest) ([]lspDefinitionLocation, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()

	uri := fileURI(absolutePath)
	if err := c.syncDocument(absolutePath, uri, request.Content); err != nil {
		return nil, err
	}
	params := map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     lspPositionFromUTF16Offset(request.Content, request.Position),
	}
	raw, err := c.requestWithRetry(ctx, "textDocument/definition", params)
	if err != nil {
		return nil, err
	}
	return parseLSPDefinitionResponse(raw)
}

func (c *lspClient) typeDefinition(ctx context.Context, absolutePath string, request WorkspaceDefinitionRequest) ([]lspDefinitionLocation, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()

	uri := fileURI(absolutePath)
	if err := c.syncDocument(absolutePath, uri, request.Content); err != nil {
		return nil, err
	}
	params := map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     lspPositionFromUTF16Offset(request.Content, request.Position),
	}
	raw, err := c.requestWithRetry(ctx, "textDocument/typeDefinition", params)
	if err != nil {
		return nil, err
	}
	return parseLSPDefinitionResponse(raw)
}

func (c *lspClient) requestWithRetry(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	var lastErr error
	delays := []time.Duration{0, 150 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}
	for _, delay := range delays {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		raw, err := c.request(ctx, method, params)
		if err == nil {
			return raw, nil
		}
		lastErr = err
		if !isRetryableLSPRequestError(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func isRetryableLSPRequestError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no views") || strings.Contains(message, "view not found")
}

func (c *lspClient) syncDocument(absolutePath string, uri string, content string) error {
	c.docMu.Lock()
	state, opened := c.documents[uri]
	if !opened {
		state = lspDocumentState{content: content, version: 1}
		c.documents[uri] = state
		c.docMu.Unlock()
		return c.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri":        uri,
				"languageId": lspDocumentLanguageIDForPath(c.languageID, absolutePath),
				"version":    state.version,
				"text":       content,
			},
		})
	}
	if state.content == content {
		c.docMu.Unlock()
		return nil
	}
	state.content = content
	state.version++
	c.documents[uri] = state
	c.docMu.Unlock()
	return c.notify("textDocument/didChange", map[string]any{
		"textDocument": map[string]any{
			"uri":     uri,
			"version": state.version,
		},
		"contentChanges": []map[string]string{
			{"text": content},
		},
	})
}

func (c *lspClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextRequestID()
	key := strconv.FormatUint(id, 10)
	response := make(chan lspPendingResponse, 1)
	c.pendingMu.Lock()
	c.pending[key] = response
	c.pendingMu.Unlock()

	message := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := c.writeMessage(message); err != nil {
		c.removePending(key)
		return nil, err
	}

	select {
	case result := <-response:
		return result.result, result.err
	case <-ctx.Done():
		c.removePending(key)
		_ = c.notify("$/cancelRequest", map[string]any{"id": id})
		return nil, ctx.Err()
	}
}

func (c *lspClient) notify(method string, params any) error {
	return c.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (c *lspClient) writeMessage(message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	var framed bytes.Buffer
	fmt.Fprintf(&framed, "Content-Length: %d\r\n\r\n", len(payload))
	framed.Write(payload)

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(framed.Bytes()); err != nil {
		return fmt.Errorf("write language server message: %w", err)
	}
	return nil
}

func (c *lspClient) nextRequestID() uint64 {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	c.nextID++
	return c.nextID
}

func (c *lspClient) readLoop(stdout io.Reader) {
	defer close(c.readerDone)
	reader := bufio.NewReader(stdout)
	for {
		payload, err := readLSPPayload(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.failPending(fmt.Errorf("read language server message: %w", err))
			}
			return
		}
		var message lspWireMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			continue
		}
		if message.Method != "" {
			c.handleServerMessage(message)
			continue
		}
		if message.ID == nil {
			continue
		}
		key := lspIDKey(*message.ID)
		response := lspPendingResponse{result: message.Result}
		if message.Error != nil {
			response.err = fmt.Errorf("language server %d: %s", message.Error.Code, message.Error.Message)
		}
		c.pendingMu.Lock()
		waiter := c.pending[key]
		delete(c.pending, key)
		c.pendingMu.Unlock()
		if waiter != nil {
			waiter <- response
		}
	}
}

func (c *lspClient) handleServerMessage(message lspWireMessage) {
	if message.Method == "" {
		return
	}

	// Push notifications (server-initiated, no ID)
	if message.ID == nil {
		switch message.Method {
		case "textDocument/publishDiagnostics":
			c.handlePublishDiagnostics(message.Params)
		}
		return
	}

	result := any(nil)
	switch message.Method {
	case "workspace/configuration":
		result = workspaceConfigurationResponse(message.Params)
	case "workspace/workspaceFolders":
		result = []map[string]string{
			{
				"uri":  c.rootURI,
				"name": c.workspaceName,
			},
		}
	}
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      *message.ID,
		"result":  result,
	}
	_ = c.writeMessage(response)
}

func (c *lspClient) handlePublishDiagnostics(raw json.RawMessage) {
	var params lspDiagnosticParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return
	}
	if params.URI == "" {
		return
	}
	targetPath, err := pathFromFileURI(params.URI)
	if err != nil {
		return
	}

	diagnostics := make([]WorkspaceDiagnostic, 0, len(params.Diagnostics))
	for _, d := range params.Diagnostics {
		diagnostics = append(diagnostics, workspaceDiagnosticFromLSP(d))
	}

	filePath := targetPath
	if c.resolveFilePath != nil {
		filePath = c.resolveFilePath(targetPath)
	}

	if c.onDiagnostics != nil {
		c.onDiagnostics(c.workspaceID, filePath, diagnostics)
	}
}

type lspDiagnosticParams struct {
	URI         string              `json:"uri"`
	Diagnostics []lspDiagnostic     `json:"diagnostics"`
}

func workspaceDiagnosticFromLSP(d lspDiagnostic) WorkspaceDiagnostic {
	w := WorkspaceDiagnostic{
		Range:    workspaceDiagnosticRangeFromLSP(d.Range),
		Severity: d.Severity,
		Code:     lspCodeValue(d.Code),
		Source:   d.Source,
		Message:  d.Message,
		Tags:     d.Tags,
	}
	if d.CodeDescription.Href != "" {
		w.CodeDescription = d.CodeDescription.Href
	}
	if len(d.RelatedInformation) > 0 {
		w.RelatedInformation = make([]WorkspaceDiagnosticRelatedInfo, 0, len(d.RelatedInformation))
		for _, ri := range d.RelatedInformation {
			locPath, _ := pathFromFileURI(ri.Location.URI)
			w.RelatedInformation = append(w.RelatedInformation, WorkspaceDiagnosticRelatedInfo{
				Location: WorkspaceDiagnosticLocation{
					Path:  locPath,
					Range: workspaceDiagnosticRangeFromLSP(ri.Location.Range),
				},
				Message: ri.Message,
			})
		}
	}
	return w
}

func lspCodeValue(raw json.RawMessage) any {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var num int
	if err := json.Unmarshal(raw, &num); err == nil {
		return num
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return nil
}

func workspaceDiagnosticRangeFromLSP(r lspRange) WorkspaceDiagnosticRange {
	return WorkspaceDiagnosticRange{
		Start: WorkspaceDiagnosticPosition{Line: r.Start.Line, Character: r.Start.Character},
		End:   WorkspaceDiagnosticPosition{Line: r.End.Line, Character: r.End.Character},
	}
}

func workspaceConfigurationResponse(raw json.RawMessage) any {
	var params struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || len(params.Items) == 0 {
		return []any{}
	}
	values := make([]any, len(params.Items))
	for i := range values {
		values[i] = map[string]any{}
	}
	return values
}

func readLSPPayload(reader *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue
		}
		length, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("invalid content length")
		}
		contentLength = length
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("missing content length")
	}
	payload := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (c *lspClient) removePending(key string) {
	c.pendingMu.Lock()
	delete(c.pending, key)
	c.pendingMu.Unlock()
}

func (c *lspClient) failPending(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for key, waiter := range c.pending {
		delete(c.pending, key)
		waiter <- lspPendingResponse{err: err}
	}
}

func (c *lspClient) close() {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		select {
		case <-c.readerDone:
		case <-time.After(2 * time.Second):
		}
	})
}

func (s *SystemService) closeWorkspaceLSPClients(workspaceID string) {
	prefix := workspaceID + ":"
	s.lspMu.Lock()
	clients := make([]*lspClient, 0)
	for key, client := range s.lspClients {
		if strings.HasPrefix(key, prefix) {
			clients = append(clients, client)
			delete(s.lspClients, key)
		}
	}
	s.lspMu.Unlock()
	for _, client := range clients {
		client.close()
	}
}

func (s *SystemService) closeAllLSPClients() {
	s.lspMu.Lock()
	clients := make([]*lspClient, 0, len(s.lspClients))
	for key, client := range s.lspClients {
		clients = append(clients, client)
		delete(s.lspClients, key)
	}
	s.lspMu.Unlock()
	for _, client := range clients {
		client.close()
	}
}

func workspaceLSPClientKey(workspaceID string, folderID string, languageID string) string {
	return workspaceID + ":" + folderID + ":" + languageID
}

func registerLSPLanguage(definition lspLanguageDefinition) {
	definition.ID = strings.TrimSpace(definition.ID)
	if definition.ID == "" {
		panic("LSP language ID is required")
	}
	if strings.TrimSpace(definition.Command.name) == "" {
		panic(fmt.Sprintf("LSP command is required for %s", definition.ID))
	}
	if len(definition.Extensions) == 0 {
		panic(fmt.Sprintf("at least one LSP file extension is required for %s", definition.ID))
	}
	if _, exists := lspLanguagesByID[definition.ID]; exists {
		panic(fmt.Sprintf("LSP language %s is already registered", definition.ID))
	}
	definition.DisplayName = strings.TrimSpace(definition.DisplayName)
	if definition.DisplayName == "" {
		definition.DisplayName = definition.ID
	}

	normalizedExtensions := make([]string, 0, len(definition.Extensions))
	for _, extension := range definition.Extensions {
		extension = strings.ToLower(strings.TrimSpace(extension))
		if extension == "" {
			panic(fmt.Sprintf("empty LSP file extension for %s", definition.ID))
		}
		if !strings.HasPrefix(extension, ".") {
			extension = "." + extension
		}
		if existingID, exists := lspLanguageIDsByExt[extension]; exists {
			panic(fmt.Sprintf("LSP file extension %s is already registered for %s", extension, existingID))
		}
		normalizedExtensions = append(normalizedExtensions, extension)
	}
	normalizedMarkers := make([]string, 0, len(definition.WorkspaceMarkers))
	for _, marker := range definition.WorkspaceMarkers {
		marker = strings.Trim(strings.TrimSpace(strings.ReplaceAll(marker, "\\", "/")), "/")
		if marker == "" {
			continue
		}
		normalizedMarkers = append(normalizedMarkers, marker)
	}
	sort.Strings(normalizedMarkers)

	definition.Extensions = normalizedExtensions
	definition.WorkspaceMarkers = normalizedMarkers
	lspLanguagesByID[definition.ID] = definition
	for _, extension := range normalizedExtensions {
		lspLanguageIDsByExt[extension] = definition.ID
	}
}

func registeredLSPCommandForLanguage(languageID string) (lspServerCommand, bool) {
	definition, ok := lspLanguagesByID[languageID]
	if !ok {
		return lspServerCommand{}, false
	}
	return definition.Command, true
}

func lspLanguageIDForPath(path string) (string, bool) {
	languageID, ok := lspLanguageIDsByExt[strings.ToLower(filepath.Ext(path))]
	return languageID, ok
}

func lspDocumentLanguageIDForPath(languageID string, path string) string {
	definition, ok := lspLanguagesByID[languageID]
	if !ok || definition.DocumentLanguageID == nil {
		return languageID
	}
	documentLanguageID := strings.TrimSpace(definition.DocumentLanguageID(path))
	if documentLanguageID == "" {
		return languageID
	}
	return documentLanguageID
}

func lspLanguageDisplayName(languageID string) string {
	definition, ok := lspLanguagesByID[languageID]
	if !ok || strings.TrimSpace(definition.DisplayName) == "" {
		return languageID
	}
	return definition.DisplayName
}

func filterLSPCompletionItems(languageID string, items []WorkspaceCompletionItem) []WorkspaceCompletionItem {
	definition, ok := lspLanguagesByID[languageID]
	if !ok || definition.CompletionFilter == nil {
		return items
	}
	return definition.CompletionFilter(items)
}

func parseLSPDefinitionResponse(raw json.RawMessage) ([]lspDefinitionLocation, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []lspDefinitionLocation{}, nil
	}

	if bytes.HasPrefix(trimmed, []byte("[")) {
		var rawItems []json.RawMessage
		if err := json.Unmarshal(trimmed, &rawItems); err != nil {
			return nil, fmt.Errorf("parse definition response: %w", err)
		}
		locations := make([]lspDefinitionLocation, 0, len(rawItems))
		for _, item := range rawItems {
			if location, ok := parseLSPDefinitionLocation(item); ok {
				locations = append(locations, location)
			}
		}
		return locations, nil
	}

	if location, ok := parseLSPDefinitionLocation(trimmed); ok {
		return []lspDefinitionLocation{location}, nil
	}
	return []lspDefinitionLocation{}, nil
}

func parseLSPDefinitionLocation(raw json.RawMessage) (lspDefinitionLocation, bool) {
	var location lspDefinitionLocation
	if err := json.Unmarshal(raw, &location); err == nil && location.URI != "" {
		return location, true
	}

	var link lspDefinitionLocationLink
	if err := json.Unmarshal(raw, &link); err == nil && link.TargetURI != "" {
		targetRange := link.TargetSelectionRange
		if targetRange == (lspRange{}) {
			targetRange = link.TargetRange
		}
		return lspDefinitionLocation{
			URI:   link.TargetURI,
			Range: targetRange,
		}, true
	}
	return lspDefinitionLocation{}, false
}

func parseLSPCompletionResponse(raw json.RawMessage, content string, position int) (WorkspaceCompletionResponse, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return WorkspaceCompletionResponse{Items: []WorkspaceCompletionItem{}}, nil
	}
	var list lspCompletionList
	if err := json.Unmarshal(raw, &list); err == nil && list.Items != nil {
		return completionListToResponse(list, content, position), nil
	}
	var items []lspCompletionItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return WorkspaceCompletionResponse{}, fmt.Errorf("parse completion response: %w", err)
	}
	return completionListToResponse(lspCompletionList{Items: items}, content, position), nil
}

func completionListToResponse(list lspCompletionList, content string, position int) WorkspaceCompletionResponse {
	fallbackFrom, fallbackTo := completionFallbackRange(content, position)
	defaultRange, hasDefaultRange := parseLSPCompletionEditRange(list.ItemDefaults.EditRange)
	items := make([]WorkspaceCompletionItem, 0, len(list.Items))
	for _, item := range list.Items {
		if strings.TrimSpace(item.Label) == "" {
			continue
		}
		editText, editRange, hasEditRange := parseLSPCompletionTextEdit(item.TextEdit)
		if !hasEditRange && hasDefaultRange {
			editRange = defaultRange
			hasEditRange = true
		}
		if editText == "" {
			editText = item.InsertText
		}
		if editText == "" {
			editText = item.Label
		}

		from, to := fallbackFrom, fallbackTo
		if hasEditRange {
			from = utf16OffsetForPosition(content, editRange.Start)
			to = utf16OffsetForPosition(content, editRange.End)
			if from > to {
				from, to = to, from
			}
		}

		output := WorkspaceCompletionItem{
			Label:         item.Label,
			Kind:          item.Kind,
			Detail:        item.Detail,
			Documentation: lspDocumentationString(item.Documentation),
			InsertText:    editText,
			SortText:      item.SortText,
			FilterText:    item.FilterText,
			From:          from,
			To:            to,
		}
		output.AdditionalTextEdits = lspAdditionalTextEdits(content, item.AdditionalTextEdits)
		items = append(items, output)
	}
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i].SortText
		if left == "" {
			left = items[i].Label
		}
		right := items[j].SortText
		if right == "" {
			right = items[j].Label
		}
		return strings.ToLower(left) < strings.ToLower(right)
	})
	return WorkspaceCompletionResponse{
		IsIncomplete: list.IsIncomplete,
		Items:        items,
	}
}

func parseLSPCompletionTextEdit(raw json.RawMessage) (string, lspRange, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", lspRange{}, false
	}
	var textEdit lspTextEdit
	if err := json.Unmarshal(raw, &textEdit); err == nil && textEdit.Range != (lspRange{}) {
		return textEdit.NewText, textEdit.Range, true
	}
	var insertReplace lspInsertReplaceEdit
	if err := json.Unmarshal(raw, &insertReplace); err == nil && insertReplace.Replace != (lspRange{}) {
		return insertReplace.NewText, insertReplace.Replace, true
	}
	return "", lspRange{}, false
}

func parseLSPCompletionEditRange(raw json.RawMessage) (lspRange, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return lspRange{}, false
	}
	var editRange lspRange
	if err := json.Unmarshal(raw, &editRange); err == nil && editRange != (lspRange{}) {
		return editRange, true
	}
	var insertReplace struct {
		Insert  lspRange `json:"insert"`
		Replace lspRange `json:"replace"`
	}
	if err := json.Unmarshal(raw, &insertReplace); err == nil && insertReplace.Replace != (lspRange{}) {
		return insertReplace.Replace, true
	}
	return lspRange{}, false
}

func lspAdditionalTextEdits(content string, rawEdits []json.RawMessage) []WorkspaceTextEdit {
	if len(rawEdits) == 0 {
		return nil
	}
	edits := make([]WorkspaceTextEdit, 0, len(rawEdits))
	for _, raw := range rawEdits {
		var edit lspTextEdit
		if err := json.Unmarshal(raw, &edit); err != nil {
			continue
		}
		from := utf16OffsetForPosition(content, edit.Range.Start)
		to := utf16OffsetForPosition(content, edit.Range.End)
		if from > to {
			from, to = to, from
		}
		edits = append(edits, WorkspaceTextEdit{
			From:    from,
			To:      to,
			NewText: edit.NewText,
		})
	}
	return edits
}

func lspDocumentationString(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return truncateLSPDocumentation(text)
	}
	var markup struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &markup); err == nil {
		return truncateLSPDocumentation(markup.Value)
	}
	return ""
}

func truncateLSPDocumentation(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= lspMaxDocumentation {
		return value
	}
	return value[:lspMaxDocumentation] + "..."
}

func lspPositionFromUTF16Offset(content string, target int) lspPosition {
	if target < 0 {
		target = 0
	}
	line, character, offset := 0, 0, 0
	for _, char := range content {
		if offset >= target {
			break
		}
		units := utf16RuneLen(char)
		if offset+units > target {
			break
		}
		offset += units
		if char == '\n' {
			line++
			character = 0
		} else {
			character += units
		}
	}
	return lspPosition{Line: line, Character: character}
}

func utf16OffsetForPosition(content string, target lspPosition) int {
	if target.Line < 0 || target.Character < 0 {
		return 0
	}
	line, character, offset := 0, 0, 0
	for _, char := range content {
		if line == target.Line && character >= target.Character {
			return offset
		}
		if char == '\n' && line == target.Line {
			return offset
		}
		units := utf16RuneLen(char)
		offset += units
		if char == '\n' {
			line++
			character = 0
		} else {
			character += units
		}
	}
	if line <= target.Line {
		return offset
	}
	return offset
}

func completionFallbackRange(content string, position int) (int, int) {
	start := position
	entries := utf16RuneEntries(content)
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.end > position {
			continue
		}
		if !isCompletionWordRune(entry.char) {
			break
		}
		start = entry.start
	}
	return start, position
}

type utf16RuneEntry struct {
	char       rune
	start, end int
}

func utf16RuneEntries(content string) []utf16RuneEntry {
	entries := make([]utf16RuneEntry, 0, len(content))
	offset := 0
	for _, char := range content {
		units := utf16RuneLen(char)
		entries = append(entries, utf16RuneEntry{
			char:  char,
			start: offset,
			end:   offset + units,
		})
		offset += units
	}
	return entries
}

func isCompletionWordRune(char rune) bool {
	return char == '_' || unicode.IsLetter(char) || unicode.IsDigit(char)
}

func utf16Length(content string) int {
	length := 0
	for _, char := range content {
		length += utf16RuneLen(char)
	}
	return length
}

func utf16RuneLen(char rune) int {
	if char > 0xFFFF {
		return 2
	}
	return 1
}

func fileURI(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	slashed := filepath.ToSlash(absolute)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

func pathFromFileURI(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "file" {
		return "", fmt.Errorf("definition URI is not a file")
	}
	path := parsed.Path
	if parsed.Host != "" {
		path = "//" + parsed.Host + path
	}
	path = filepath.FromSlash(path)
	if strings.HasPrefix(path, string(filepath.Separator)) {
		withoutLeadingSeparator := strings.TrimPrefix(path, string(filepath.Separator))
		if filepath.VolumeName(withoutLeadingSeparator) != "" {
			path = withoutLeadingSeparator
		}
	}
	return filepath.Clean(path), nil
}

func lspIDKey(raw json.RawMessage) string {
	return strings.Trim(string(raw), `"`)
}

func (s *SystemService) emitLSPDiagnosticsEvent(workspaceID string, filePath string, diagnostics []WorkspaceDiagnostic) {
	payload := LSPDiagnosticsPayload{
		WorkspaceID: workspaceID,
		FilePath:    filePath,
		Diagnostics: diagnostics,
	}
	s.emitRuntimeEvent(lspDiagnosticsEventName, payload)
	if s.ctx != nil {
		runtime.EventsEmit(s.ctx, lspDiagnosticsEventName, payload)
	}
}
