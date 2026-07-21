package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLSPUTF16PositionMapping(t *testing.T) {
	content := "a🙂b\néx"
	cases := []struct {
		offset   int
		position lspPosition
	}{
		{offset: 0, position: lspPosition{Line: 0, Character: 0}},
		{offset: 1, position: lspPosition{Line: 0, Character: 1}},
		{offset: 3, position: lspPosition{Line: 0, Character: 3}},
		{offset: 4, position: lspPosition{Line: 0, Character: 4}},
		{offset: 5, position: lspPosition{Line: 1, Character: 0}},
		{offset: 6, position: lspPosition{Line: 1, Character: 1}},
	}
	for _, tc := range cases {
		if got := lspPositionFromUTF16Offset(content, tc.offset); got != tc.position {
			t.Fatalf("offset %d: expected position %#v, got %#v", tc.offset, tc.position, got)
		}
		if got := utf16OffsetForPosition(content, tc.position); got != tc.offset {
			t.Fatalf("position %#v: expected offset %d, got %d", tc.position, tc.offset, got)
		}
	}
}

func TestParseLSPCompletionResponseUsesTextEdits(t *testing.T) {
	content := "package main\n\nfunc main() {\n\tfmt.Pr\n}\n"
	position := utf16Length(content[:strings.Index(content, "Pr")+len("Pr")])
	raw := json.RawMessage(`{
		"isIncomplete": false,
		"items": [
			{
				"label": "Println",
				"kind": 3,
				"detail": "func Println(a ...any) (n int, err error)",
				"documentation": {"kind": "markdown", "value": "Println formats its operands."},
				"textEdit": {
					"range": {
						"start": {"line": 3, "character": 5},
						"end": {"line": 3, "character": 7}
					},
					"newText": "Println"
				},
				"additionalTextEdits": [
					{
						"range": {
							"start": {"line": 1, "character": 0},
							"end": {"line": 1, "character": 0}
						},
						"newText": "import \"fmt\"\n"
					}
				]
			}
		]
	}`)

	response, err := parseLSPCompletionResponse(raw, content, position)
	if err != nil {
		t.Fatalf("parse completion response: %v", err)
	}
	if len(response.Items) != 1 {
		t.Fatalf("expected one completion item, got %#v", response.Items)
	}
	item := response.Items[0]
	expectedFrom := utf16Length(content[:strings.Index(content, "Pr")])
	if item.From != expectedFrom || item.To != position {
		t.Fatalf("expected completion edit range %d-%d, got %d-%d", expectedFrom, position, item.From, item.To)
	}
	if item.InsertText != "Println" || item.Label != "Println" || item.Kind != 3 {
		t.Fatalf("unexpected item metadata: %#v", item)
	}
	if item.Documentation != "Println formats its operands." {
		t.Fatalf("expected markdown documentation value, got %q", item.Documentation)
	}
	if len(item.AdditionalTextEdits) != 1 {
		t.Fatalf("expected import edit, got %#v", item.AdditionalTextEdits)
	}
	importOffset := utf16Length("package main\n")
	edit := item.AdditionalTextEdits[0]
	if edit.From != importOffset || edit.To != importOffset || edit.NewText != "import \"fmt\"\n" {
		t.Fatalf("unexpected import edit: %#v", edit)
	}
}

func TestParseLSPCompletionResponseUsesFallbackRange(t *testing.T) {
	content := "package main\n\nfunc main() {\n\tPrin\n}\n"
	position := utf16Length(content[:strings.Index(content, "Prin")+len("Prin")])
	response, err := parseLSPCompletionResponse(
		json.RawMessage(`[{"label":"Println","kind":3}]`),
		content,
		position,
	)
	if err != nil {
		t.Fatalf("parse completion response: %v", err)
	}
	if len(response.Items) != 1 {
		t.Fatalf("expected one completion item, got %#v", response.Items)
	}
	expectedFrom := utf16Length(content[:strings.Index(content, "Prin")])
	if response.Items[0].From != expectedFrom || response.Items[0].To != position {
		t.Fatalf("expected fallback range %d-%d, got %d-%d", expectedFrom, position, response.Items[0].From, response.Items[0].To)
	}
}

func TestParseLSPDefinitionResponse(t *testing.T) {
	raw := json.RawMessage(`[
		{
			"targetUri": "file:///C:/work/main.go",
			"targetRange": {
				"start": {"line": 2, "character": 0},
				"end": {"line": 4, "character": 1}
			},
			"targetSelectionRange": {
				"start": {"line": 3, "character": 5},
				"end": {"line": 3, "character": 11}
			}
		},
		{
			"uri": "file:///C:/work/other.go",
			"range": {
				"start": {"line": 6, "character": 2},
				"end": {"line": 6, "character": 8}
			}
		}
	]`)

	locations, err := parseLSPDefinitionResponse(raw)
	if err != nil {
		t.Fatalf("parse definition response: %v", err)
	}
	if len(locations) != 2 {
		t.Fatalf("expected two locations, got %#v", locations)
	}
	if locations[0].URI != "file:///C:/work/main.go" || locations[0].Range.Start != (lspPosition{Line: 3, Character: 5}) {
		t.Fatalf("expected location link target selection range, got %#v", locations[0])
	}
	if locations[1].URI != "file:///C:/work/other.go" || locations[1].Range.Start != (lspPosition{Line: 6, Character: 2}) {
		t.Fatalf("expected location range, got %#v", locations[1])
	}
}

func TestReadDefinitionTargetFileSupportsWorkspaceAndExternalSource(t *testing.T) {
	root := t.TempDir()
	workspace := workspaceFromPath(root)
	workspacePath := filepath.Join(root, "main.go")
	externalPath := filepath.Join(t.TempDir(), "dependency.go")
	if err := os.WriteFile(workspacePath, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(externalPath, []byte("package dependency\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	workspaceFile, err := readDefinitionTargetFile(workspace, workspacePath)
	if err != nil {
		t.Fatalf("read workspace definition target: %v", err)
	}
	if workspaceFile.WorkspaceID != workspace.ID || workspaceFile.Path != workspaceRelativePath(workspace, workspacePath) {
		t.Fatalf("expected workspace-relative definition target, got %#v", workspaceFile)
	}

	externalFile, err := readDefinitionTargetFile(workspace, externalPath)
	if err != nil {
		t.Fatalf("read external definition target: %v", err)
	}
	if externalFile.WorkspaceID != "" || externalFile.Path != filepath.Clean(externalPath) || externalFile.Content != "package dependency\n" {
		t.Fatalf("expected absolute external definition target, got %#v", externalFile)
	}
}

func TestDetectWorkspaceFolderLSPLanguagesFindsGoMarker(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/warmup\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !stringSliceContains(detectWorkspaceFolderLSPLanguages(root), "go") {
		t.Fatalf("expected Go LSP warm-up from go.mod marker")
	}
}

func TestDetectWorkspaceFolderLSPLanguagesFindsClangdMarkers(t *testing.T) {
	for _, marker := range []string{"CMakeLists.txt", "compile_commands.json", "compile_flags.txt", ".clangd", "meson.build"} {
		t.Run(marker, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, marker), []byte("\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if !stringSliceContains(detectWorkspaceFolderLSPLanguages(root), "cpp") {
				t.Fatalf("expected C/C++ LSP warm-up from %s marker", marker)
			}
		})
	}
}

func TestDetectWorkspaceFolderLSPLanguagesFindsClangdSourceFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.cpp"), []byte("int main() { return 0; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !stringSliceContains(detectWorkspaceFolderLSPLanguages(root), "cpp") {
		t.Fatalf("expected C/C++ LSP warm-up from workspace C++ file")
	}
}

func TestDetectWorkspaceFolderLSPLanguagesSkipsIgnoredDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "pkg", "ignored.go"), []byte("package ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if stringSliceContains(detectWorkspaceFolderLSPLanguages(root), "go") {
		t.Fatalf("expected Go files under ignored directories not to trigger warm-up")
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !stringSliceContains(detectWorkspaceFolderLSPLanguages(root), "go") {
		t.Fatalf("expected Go LSP warm-up from workspace Go file")
	}
}

func TestWorkspaceLSPWarmupTargetsFindNestedGoModule(t *testing.T) {
	root := t.TempDir()
	moduleRoot := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(moduleRoot, "rendering"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleRoot, "go.mod"), []byte("module example.com/nested\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(moduleRoot, "rendering", "draw.go")
	if err := os.WriteFile(sourcePath, []byte("package rendering\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	targets := workspaceLSPWarmupTargets(workspaceFromPath(root))
	for _, target := range targets {
		if target.languageID == "go" {
			if len(target.sourcePaths) != 1 || !samePath(target.sourcePaths[0], sourcePath) {
				t.Fatalf("expected nested Go module seed %q, got %#v", sourcePath, target.sourcePaths)
			}
			return
		}
	}
	t.Fatalf("expected Go warmup target, got %#v", targets)
}

func TestGoWarmupSourcePrefersCurrentNonTestBuild(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/preferred\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inactiveOS := "windows"
	if runtime.GOOS == "windows" {
		inactiveOS = "linux"
	}
	inactive := filepath.Join(root, "a_"+inactiveOS+".go")
	testFile := filepath.Join(root, "b_test.go")
	preferred := filepath.Join(root, "z.go")
	for path, content := range map[string]string{
		inactive:  "package preferred\n",
		testFile:  "package preferred\n",
		preferred: "package preferred\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	scan := scanWorkspaceFolderLSPWarmup(root)
	selected := goWarmupSourcePaths(scan.sourcePaths["go"], scan.goBuildRoots)
	if len(selected) != 1 || !samePath(selected[0], preferred) {
		t.Fatalf("expected current non-test build seed %q, got %#v", preferred, selected)
	}
}

func TestWorkspaceLSPWarmupCapsNestedGoBuilds(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < lspWarmupMaxGoBuilds+2; i++ {
		moduleRoot := filepath.Join(root, fmt.Sprintf("module-%02d", i))
		if err := os.MkdirAll(moduleRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(moduleRoot, "go.mod"), []byte(fmt.Sprintf("module example.com/module%d\n\ngo 1.23\n", i)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(moduleRoot, "main.go"), []byte("package main\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	targets := workspaceLSPWarmupTargets(workspaceFromPath(root))
	for _, target := range targets {
		if target.languageID == "go" {
			if len(target.sourcePaths) != lspWarmupMaxGoBuilds {
				t.Fatalf("expected %d Go warmup seeds, got %#v", lspWarmupMaxGoBuilds, target.sourcePaths)
			}
			return
		}
	}
	t.Fatalf("expected Go warmup target, got %#v", targets)
}

func TestWorkspaceLSPClientStartIsSingleFlight(t *testing.T) {
	root := t.TempDir()
	workspace := workspaceFromPath(root)
	folder := workspace.Folders[0]
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))

	oldStart := startLSPClient
	defer func() { startLSPClient = oldStart }()
	var starts atomic.Int32
	releaseStart := make(chan struct{})
	fakeClient := &lspClient{languageID: "go"}
	startLSPClient = func(ctx context.Context, gotWorkspace Workspace, gotFolder WorkspaceFolder, languageID string) (*lspClient, error) {
		starts.Add(1)
		select {
		case <-releaseStart:
			return fakeClient, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	const callers = 8
	clients := make(chan *lspClient, callers)
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for i := 0; i < callers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			client, err := service.workspaceLSPClient(workspace, folder, "go")
			clients <- client
			errs <- err
		}()
	}
	for starts.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(releaseStart)
	wait.Wait()
	close(clients)
	close(errs)

	if starts.Load() != 1 {
		t.Fatalf("expected one language server start, got %d", starts.Load())
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("unexpected client error: %v", err)
		}
	}
	for client := range clients {
		if client != fakeClient {
			t.Fatalf("expected callers to share the same client")
		}
	}
	service.lspMu.Lock()
	delete(service.lspClients, workspaceLSPClientKey(workspace.ID, folder.ID, "go"))
	service.lspMu.Unlock()
}

func TestCloseWorkspaceCancelsLSPClientStart(t *testing.T) {
	root := t.TempDir()
	workspace := workspaceFromPath(root)
	folder := workspace.Folders[0]
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))

	oldStart := startLSPClient
	defer func() { startLSPClient = oldStart }()
	started := make(chan struct{})
	startLSPClient = func(ctx context.Context, gotWorkspace Workspace, gotFolder WorkspaceFolder, languageID string) (*lspClient, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	result := make(chan error, 1)
	go func() {
		_, err := service.workspaceLSPClient(workspace, folder, "go")
		result <- err
	}()
	<-started
	service.closeWorkspaceLSPClients(workspace.ID)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled client start, got %v", err)
	}
}

func TestLSPOperationTimeoutStartsAfterQueue(t *testing.T) {
	client := &lspClient{
		languageID:    "go",
		operationGate: make(chan struct{}, 1),
		documents:     map[string]lspDocumentState{},
	}
	client.operationGate <- struct{}{}
	uri := "file:///workspace/main.go"
	client.documents[uri] = lspDocumentState{ready: true}

	warmupRelease, err := client.acquireOperation(context.Background(), "textDocument/documentSymbol")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan time.Duration, 1)
	go func() {
		foregroundRelease, acquireErr := client.acquireOperation(context.Background(), "textDocument/definition")
		if acquireErr != nil {
			result <- 0
			return
		}
		defer foregroundRelease()
		ctx, cancel := client.documentOperationContext(context.Background(), uri, 50*time.Millisecond)
		defer cancel()
		deadline, _ := ctx.Deadline()
		result <- time.Until(deadline)
	}()
	time.Sleep(75 * time.Millisecond)
	warmupRelease()
	if remaining := <-result; remaining < 35*time.Millisecond {
		t.Fatalf("expected a fresh foreground timeout after queueing, got %s", remaining)
	}
}

func TestLSPColdDocumentGetsExtendedTimeout(t *testing.T) {
	client := &lspClient{documents: map[string]lspDocumentState{}}
	ctx, cancel := client.documentOperationContext(context.Background(), "file:///workspace/cold.go", time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if remaining := time.Until(deadline); remaining < 59*time.Second {
		t.Fatalf("expected cold-start timeout, got %s", remaining)
	}
}

func TestLSPTimeoutErrorIsLanguageSpecific(t *testing.T) {
	for _, tc := range []struct {
		languageID string
		method     string
		expected   string
	}{
		{languageID: "go", method: "textDocument/definition", expected: "The Go language server timed out while loading the workspace for definition lookup."},
		{languageID: "cpp", method: "textDocument/references", expected: "The C/C++ language server timed out while loading the workspace for reference lookup."},
	} {
		t.Run(tc.languageID, func(t *testing.T) {
			err := (&lspClient{languageID: tc.languageID}).contextError(tc.method, context.DeadlineExceeded)
			if err.Error() != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, err)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected timeout error to unwrap to context deadline")
			}
			if strings.Contains(err.Error(), "context deadline exceeded") || strings.Contains(err.Error(), "LLM") {
				t.Fatalf("unexpected transport or LLM wording: %q", err)
			}
		})
	}
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestWorkspaceReferenceLocationsUseActiveContentAndFilterWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := workspaceFromPath(root)
	mainPath := filepath.Join(root, "main.go")
	otherPath := filepath.Join(root, "other.go")
	outsidePath := filepath.Join(t.TempDir(), "outside.go")

	if err := os.WriteFile(mainPath, []byte("package main\n\nvar diskOnly = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, []byte("package main\n\nfunc use() {\n\tName()\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	activeContent := "package main\r\n\r\nfunc Name() {}\r\n\r\nfunc main() {\r\n\tName()\r\n}\r\n"
	locations := []lspDefinitionLocation{
		{
			URI: fileURI(mainPath),
			Range: lspRange{
				Start: lspPosition{Line: 2, Character: 5},
				End:   lspPosition{Line: 2, Character: 9},
			},
		},
		{
			URI: fileURI(mainPath),
			Range: lspRange{
				Start: lspPosition{Line: 5, Character: 1},
				End:   lspPosition{Line: 5, Character: 5},
			},
		},
		{
			URI: fileURI(otherPath),
			Range: lspRange{
				Start: lspPosition{Line: 3, Character: 1},
				End:   lspPosition{Line: 3, Character: 5},
			},
		},
		{
			URI: fileURI(outsidePath),
			Range: lspRange{
				Start: lspPosition{Line: 0, Character: 0},
				End:   lspPosition{Line: 0, Character: 4},
			},
		},
	}

	references, skippedOutside := workspaceReferenceLocations(workspace, mainPath, activeContent, locations)
	if skippedOutside != 1 {
		t.Fatalf("expected one outside-workspace location to be skipped, got %d", skippedOutside)
	}
	if len(references) != 3 {
		t.Fatalf("expected three workspace references, got %#v", references)
	}
	if references[0].Path != workspaceRelativePath(workspace, mainPath) {
		t.Fatalf("expected source path %q, got %q", workspaceRelativePath(workspace, mainPath), references[0].Path)
	}
	if references[0].Preview != "func Name() {}" {
		t.Fatalf("expected active editor content preview, got %q", references[0].Preview)
	}
	expectedCallOffset := utf16Length(activeContent[:strings.Index(activeContent, "\tName")+1])
	if references[1].Range.Start.Offset != expectedCallOffset {
		t.Fatalf("expected CRLF-aware call offset %d, got %d", expectedCallOffset, references[1].Range.Start.Offset)
	}
	if references[1].PreviewLines == nil || references[1].PreviewLines[4].HighlightStart != 1 || references[1].PreviewLines[4].HighlightEnd != 5 {
		t.Fatalf("expected target line highlight 1-5, got %#v", references[1].PreviewLines)
	}
	if references[2].Path != workspaceRelativePath(workspace, otherPath) || references[2].Preview != "\tName()" {
		t.Fatalf("expected disk-backed other-file preview, got %#v", references[2])
	}
}

func TestSystemServiceFindWorkspaceFileImplementationsUnsupportedFile(t *testing.T) {
	root := t.TempDir()
	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}

	response, err := service.FindWorkspaceFileImplementations(state.ActiveWorkspaceID, WorkspaceReferenceRequest{
		FilePath: "notes.txt",
		Content:  "plain text",
		Position: 0,
	})
	if err != nil {
		t.Fatalf("find implementations: %v", err)
	}
	if response.Found || response.Message != "Implementation lookup is not available for this file type." {
		t.Fatalf("expected unsupported implementation lookup response, got %#v", response)
	}
}

func TestSystemServiceCompleteWorkspaceFileWithGopls(t *testing.T) {
	if os.Getenv("ECHO_RUN_GOPLS_INTEGRATION") != "1" {
		t.Skip("set ECHO_RUN_GOPLS_INTEGRATION=1 to run the real gopls integration test")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skipf("gopls was not found on PATH: %v", err)
	}
	base := os.Getenv("ECHO_GOPLS_INTEGRATION_DIR")
	if base == "" {
		if runtime.GOOS == "windows" {
			base = filepath.Join(`C:\tmp`, "echo-gopls-integration")
		} else {
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			base = filepath.Join(cwd, "..", "..", ".gotmp", "gopls-integration")
		}
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "workspace-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(root)
	})
	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID
	defer service.Shutdown()
	logPath := filepath.Join(root, "gopls.log")
	oldCommandForLanguage := lspCommandForLanguage
	lspCommandForLanguage = func(languageID string) (lspServerCommand, bool) {
		if languageID != "go" {
			return lspServerCommand{}, false
		}
		return lspServerCommand{
			name: "gopls",
			args: []string{"-rpc.trace", "-logfile=" + logPath, "serve"},
		}, true
	}
	defer func() {
		lspCommandForLanguage = oldCommandForLanguage
	}()

	moduleRoot := filepath.Join(root, "src")
	if err := os.MkdirAll(moduleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleRoot, "go.mod"), []byte("module example.com/echo_lsp_test\n\ngo 1.23\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content := "package main\n\nimport \"fmt\"\n\nfunc helper() {}\n\nfunc main() {\n\thelper()\n\tfmt.Pr\n}\n"
	if err := os.WriteFile(filepath.Join(moduleRoot, "main.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, _, err := service.workspaceAndSettings(workspaceID)
	if err != nil {
		t.Fatalf("workspace settings: %v", err)
	}
	foundGoTarget := false
	for _, target := range workspaceLSPWarmupTargets(workspace) {
		if target.languageID == "go" {
			service.warmWorkspaceLSPTarget(context.Background(), workspace, target)
			foundGoTarget = true
			break
		}
	}
	if !foundGoTarget {
		t.Fatal("expected nested Go warmup target")
	}

	filePath := labeledTestPath(t, service, workspaceID, "src/main.go")
	position := utf16Length(content[:strings.Index(content, "Pr")+len("Pr")])

	response, err := service.CompleteWorkspaceFile(workspaceID, WorkspaceCompletionRequest{
		FilePath:    filePath,
		Content:     content,
		Position:    position,
		TriggerKind: 1,
	})
	if err != nil {
		if data, readErr := os.ReadFile(logPath); readErr == nil {
			t.Logf("gopls log:\n%s", data)
		}
		t.Fatalf("complete workspace file: %v", err)
	}
	for _, item := range response.Items {
		if item.Label == "Println" {
			helperPosition := utf16Length(content[:strings.LastIndex(content, "helper()")+len("helper")])
			definition, err := service.FindWorkspaceFileDefinition(workspaceID, WorkspaceDefinitionRequest{
				FilePath: filePath,
				Content:  content,
				Position: helperPosition,
			})
			if err != nil {
				t.Fatalf("find definition: %v", err)
			}
			if !definition.Found || definition.TargetPath != filePath {
				t.Fatalf("expected main definition in main.go, got %#v", definition)
			}
			includeDeclaration := true
			references, err := service.FindWorkspaceFileReferences(workspaceID, WorkspaceReferenceRequest{
				FilePath:           filePath,
				Content:            content,
				Position:           helperPosition,
				IncludeDeclaration: &includeDeclaration,
			})
			if err != nil {
				t.Fatalf("find references: %v", err)
			}
			if !references.Found || references.ResultCount < 2 {
				t.Fatalf("expected declaration and call references, got %#v", references)
			}
			return
		}
	}
	t.Fatalf("expected Println completion, got %#v", response.Items)
}
