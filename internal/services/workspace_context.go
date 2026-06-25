package services

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/brent/echo/internal/tools"
)

const (
	workspaceContextMaxMatchLines    = 4
	workspaceContextMaxTestFiles     = 8
	workspaceContextMaxSymbolFiles   = 4
	workspaceContextSymbolsPerFile   = 20
	workspaceContextLinePreviewRunes = 220
	workspaceContextSymbolTimeout    = 3 * time.Second
)

type workspaceContextBuildFunc func(context.Context, Workspace, tools.WorkspaceContextRequest) (tools.WorkspaceContextResponse, error)

type workspaceContextProvider struct {
	service   *SystemService
	workspace Workspace
}

type workspaceContextStartPath struct {
	absolute string
	relative string
}

type workspaceContextManifestCandidate struct {
	path         string
	absolute     string
	kind         string
	rootPath     string
	rootAbsolute string
	rootKind     string
}

type workspaceContextProjectRootCandidate struct {
	path      string
	absolute  string
	kind      string
	manifests []string
}

type workspaceContextFileCandidate struct {
	path     string
	absolute string
	kind     string
	score    int
	reasons  map[string]bool
	matches  []tools.WorkspaceContextMatch
	isTest   bool
}

type workspaceContextSymbolTarget struct {
	index      int
	languageID string
}

func (s *SystemService) workspaceContextProvider(workspace Workspace) tools.WorkspaceContextProvider {
	return workspaceContextProvider{service: s, workspace: workspace}
}

func (p workspaceContextProvider) QueryWorkspaceContext(ctx context.Context, request tools.WorkspaceContextRequest) (tools.WorkspaceContextResponse, error) {
	return p.service.buildWorkspaceContext(ctx, p.workspace, request)
}

func (s *SystemService) buildWorkspaceContext(ctx context.Context, workspace Workspace, request tools.WorkspaceContextRequest) (tools.WorkspaceContextResponse, error) {
	if s.workspaceContextBuilder != nil {
		return s.workspaceContextBuilder(ctx, workspace, request)
	}
	return s.defaultWorkspaceContext(ctx, workspace, request)
}

func (s *SystemService) defaultWorkspaceContext(ctx context.Context, workspace Workspace, request tools.WorkspaceContextRequest) (tools.WorkspaceContextResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request = tools.NormalizeWorkspaceContextRequest(request)
	terms := workspaceContextTerms(request.Task)
	changed := workspaceContextChangedPathSet(request.ChangedPaths)

	starts, err := workspaceContextStartPaths(workspace, request.Path)
	if err != nil {
		return tools.WorkspaceContextResponse{}, err
	}

	candidates := make([]workspaceContextFileCandidate, 0)
	manifests := make([]workspaceContextManifestCandidate, 0)
	projectRootsByPath := map[string]*workspaceContextProjectRootCandidate{}
	languages := map[string]bool{}
	warnings := []string{}

	for _, start := range starts {
		if err := ctx.Err(); err != nil {
			return tools.WorkspaceContextResponse{}, err
		}
		scanned, scannedManifests, err := scanWorkspaceContextStart(ctx, workspace, start, terms, changed)
		if err != nil {
			return tools.WorkspaceContextResponse{}, err
		}
		candidates = append(candidates, scanned...)
		for _, manifest := range scannedManifests {
			manifests = append(manifests, manifest)
			root := projectRootsByPath[manifest.rootPath]
			if root == nil {
				root = &workspaceContextProjectRootCandidate{
					path:     manifest.rootPath,
					absolute: manifest.rootAbsolute,
					kind:     manifest.rootKind,
				}
				projectRootsByPath[manifest.rootPath] = root
			} else if root.kind != manifest.rootKind {
				root.kind = "mixed"
			}
			root.manifests = append(root.manifests, manifest.path)
		}
	}

	if len(projectRootsByPath) == 0 {
		for _, folder := range workspace.Folders {
			if folder.Missing {
				continue
			}
			rootPath := folder.Label
			if strings.TrimSpace(rootPath) == "" {
				continue
			}
			projectRootsByPath[rootPath] = &workspaceContextProjectRootCandidate{
				path:     rootPath,
				absolute: folder.Path,
				kind:     "workspace",
			}
		}
	}

	for _, candidate := range candidates {
		if candidate.kind != "" && candidate.kind != "text" && candidate.kind != "markdown" && candidate.kind != "json" {
			languages[candidate.kind] = true
		}
	}

	sortWorkspaceContextCandidates(candidates)
	relevant := workspaceContextRelevantFiles(candidates, request.MaxFiles)
	tests := workspaceContextLikelyTests(candidates, relevant)
	relevant = s.enrichWorkspaceContextSymbols(ctx, workspace, relevant, &warnings)

	manifestOutput := workspaceContextManifestOutput(manifests)
	projectRoots := workspaceContextProjectRootOutput(projectRootsByPath)
	likelyCommands := workspaceContextLikelyCommands(workspace, projectRootsByPath)
	verificationInputs := request.ChangedPaths
	if len(verificationInputs) == 0 {
		verificationInputs = workspaceContextFilePaths(relevant)
	}
	verificationCommands := workspaceContextVerificationCommands(workspace, verificationInputs)

	response := tools.WorkspaceContextResponse{
		Task:                 request.Task,
		Path:                 request.Path,
		ProjectRoots:         projectRoots,
		DetectedLanguages:    workspaceContextSortedKeys(languages),
		Manifests:            manifestOutput,
		LikelyCommands:       likelyCommands,
		RelevantFiles:        relevant,
		LikelyTestFiles:      tests,
		VerificationCommands: verificationCommands,
		Warnings:             workspaceContextUniqueWarnings(warnings),
	}
	response.Brief, response.Truncated = renderWorkspaceContextBrief(response)
	return response, nil
}

func workspaceContextStartPaths(workspace Workspace, requestedPath string) ([]workspaceContextStartPath, error) {
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" || requestedPath == "." {
		starts := make([]workspaceContextStartPath, 0, len(workspace.Folders))
		for _, folder := range workspace.Folders {
			if folder.Missing {
				continue
			}
			absolute, err := workspaceFolderAbsolutePath(folder)
			if err != nil {
				return nil, err
			}
			starts = append(starts, workspaceContextStartPath{
				absolute: absolute,
				relative: folder.Label,
			})
		}
		if len(starts) == 0 {
			return nil, fmt.Errorf("workspace has no available folders")
		}
		return starts, nil
	}

	absolute, err := resolveWorkspaceServicePath(workspace, requestedPath)
	if err != nil {
		return nil, err
	}
	return []workspaceContextStartPath{{
		absolute: absolute,
		relative: workspaceRelativePath(workspace, absolute),
	}}, nil
}

func scanWorkspaceContextStart(ctx context.Context, workspace Workspace, start workspaceContextStartPath, terms []string, changed map[string]bool) ([]workspaceContextFileCandidate, []workspaceContextManifestCandidate, error) {
	info, err := os.Stat(start.absolute)
	if err != nil {
		return nil, nil, fmt.Errorf("context path %s was not found", start.relative)
	}
	candidates := []workspaceContextFileCandidate{}
	manifests := []workspaceContextManifestCandidate{}
	visit := func(path string, entry os.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if path != start.absolute {
			relative := workspaceRelativePath(workspace, path)
			if tools.IsIgnoredChangePath(relative) {
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if entry != nil && entry.IsDir() {
			return nil
		}
		fileInfo := info
		if entry != nil {
			var infoErr error
			fileInfo, infoErr = entry.Info()
			if infoErr != nil {
				return nil
			}
		}
		if !fileInfo.Mode().IsRegular() || fileInfo.Size() > maxWorkspaceEditorFileBytes {
			return nil
		}
		relative := workspaceRelativePath(workspace, path)
		if workspaceContextNoisyFile(relative) && !changed[strings.ToLower(relative)] {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || !isWorkspaceTextLike(data) || !utf8.Valid(data) {
			return nil
		}
		content := string(data)
		candidate := workspaceContextCandidate(relative, path, content, terms, changed)
		if candidate.score > 0 {
			candidates = append(candidates, candidate)
		}
		if manifest, ok := workspaceContextManifest(workspace, relative, path); ok {
			manifests = append(manifests, manifest)
		}
		return nil
	}

	if info.Mode().IsRegular() {
		if err := visit(start.absolute, nil); err != nil && err != filepath.SkipDir {
			return nil, nil, err
		}
		return candidates, manifests, nil
	}
	if !info.IsDir() {
		return candidates, manifests, nil
	}
	err = filepath.WalkDir(start.absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == start.absolute {
			return nil
		}
		return visit(path, entry)
	})
	if err != nil {
		return nil, nil, err
	}
	return candidates, manifests, nil
}

func workspaceContextCandidate(relative string, absolute string, content string, terms []string, changed map[string]bool) workspaceContextFileCandidate {
	relative = strings.ReplaceAll(relative, "\\", "/")
	lowerPath := strings.ToLower(relative)
	base := strings.ToLower(filepath.Base(relative))
	candidate := workspaceContextFileCandidate{
		path:     relative,
		absolute: absolute,
		kind:     workspaceContextKind(relative),
		reasons:  map[string]bool{},
		isTest:   workspaceContextIsTestPath(relative),
	}
	if changed[lowerPath] {
		candidate.score += 100
		candidate.reasons["changed path"] = true
	}
	for changedPath := range changed {
		if changedPath == lowerPath {
			continue
		}
		if sameWorkspaceContextDir(changedPath, lowerPath) {
			candidate.score += 12
			candidate.reasons["near changed path"] = true
		}
	}
	if workspaceContextManifestKind(filepath.Base(relative)) != "" {
		candidate.score += 16
		candidate.reasons["manifest"] = true
	}
	if candidate.kind != "" && candidate.kind != "text" {
		candidate.score += 2
	}
	if candidate.isTest {
		candidate.score += 2
	}
	for _, term := range terms {
		if strings.Contains(lowerPath, term) {
			candidate.score += 10
			candidate.reasons["path match"] = true
		}
		if strings.Contains(base, term) {
			candidate.score += 4
		}
	}
	matches := workspaceContextContentMatches(content, terms)
	if len(matches) > 0 {
		candidate.matches = matches
		candidate.score += len(matches) * 5
		candidate.reasons["content match"] = true
	}
	if candidate.isTest && workspaceContextTermSet(terms)["test"] {
		candidate.score += 8
		candidate.reasons["test task"] = true
	}
	return candidate
}

func workspaceContextManifest(workspace Workspace, relative string, absolute string) (workspaceContextManifestCandidate, bool) {
	kind := workspaceContextManifestKind(filepath.Base(relative))
	if kind == "" {
		return workspaceContextManifestCandidate{}, false
	}
	rootAbsolute := filepath.Dir(absolute)
	rootPath := workspaceRelativePath(workspace, rootAbsolute)
	return workspaceContextManifestCandidate{
		path:         relative,
		absolute:     absolute,
		kind:         kind,
		rootPath:     rootPath,
		rootAbsolute: rootAbsolute,
		rootKind:     workspaceContextRootKind(kind),
	}, true
}

func workspaceContextManifestKind(base string) string {
	switch strings.ToLower(base) {
	case "go.mod":
		return "go module"
	case "package.json":
		return "node package"
	case "cargo.toml":
		return "rust package"
	case "pyproject.toml":
		return "python package"
	case "pom.xml":
		return "maven project"
	case "build.gradle", "build.gradle.kts":
		return "gradle project"
	case "composer.json":
		return "php package"
	case "tsconfig.json":
		return "typescript config"
	case "cmakelists.txt":
		return "cmake project"
	case "compile_commands.json":
		return "clang compile database"
	case "compile_flags.txt":
		return "clang compile flags"
	case ".clangd":
		return "clangd config"
	case "meson.build":
		return "meson project"
	default:
		return ""
	}
}

func workspaceContextRootKind(manifestKind string) string {
	switch manifestKind {
	case "go module":
		return "go"
	case "node package", "typescript config":
		return "node"
	case "rust package":
		return "rust"
	case "python package":
		return "python"
	case "maven project", "gradle project":
		return "java"
	case "php package":
		return "php"
	case "cmake project", "clang compile database", "clang compile flags", "clangd config", "meson project":
		return "cpp"
	default:
		return "project"
	}
}

func workspaceContextKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".css", ".scss", ".sass", ".less":
		return "css"
	case ".html", ".htm":
		return "html"
	case ".json":
		return "json"
	case ".md", ".markdown":
		return "markdown"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".java", ".kt", ".kts":
		return "java"
	case ".cs":
		return "csharp"
	case ".cpp", ".cc", ".cxx", ".c++", ".c", ".h", ".hh", ".hpp", ".hxx", ".ipp", ".inl", ".ixx", ".cppm":
		return "cpp"
	case ".yml", ".yaml", ".toml", ".xml":
		return "config"
	default:
		return "text"
	}
}

func workspaceContextNoisyFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "go.sum", "cargo.lock":
		return true
	default:
		return false
	}
}

func workspaceContextIsTestPath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(lower)
	return strings.Contains(lower, "/test/") ||
		strings.Contains(lower, "/tests/") ||
		strings.HasSuffix(base, "_test.go") ||
		strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") ||
		strings.HasPrefix(base, "test_") ||
		strings.HasSuffix(base, "test.py")
}

func workspaceContextContentMatches(content string, terms []string) []tools.WorkspaceContextMatch {
	if len(terms) == 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	matches := make([]tools.WorkspaceContextMatch, 0, workspaceContextMaxMatchLines)
	for index, line := range lines {
		lower := strings.ToLower(line)
		for _, term := range terms {
			if !strings.Contains(lower, term) {
				continue
			}
			matches = append(matches, tools.WorkspaceContextMatch{
				Line: index + 1,
				Text: workspaceContextPreview(line),
			})
			break
		}
		if len(matches) >= workspaceContextMaxMatchLines {
			break
		}
	}
	return matches
}

func workspaceContextPreview(value string) string {
	value = strings.TrimSpace(strings.TrimRight(value, "\r"))
	runes := []rune(value)
	if len(runes) <= workspaceContextLinePreviewRunes {
		return value
	}
	return string(runes[:workspaceContextLinePreviewRunes]) + "..."
}

func workspaceContextTerms(task string) []string {
	task = strings.ToLower(task)
	seen := map[string]bool{}
	terms := make([]string, 0)
	var builder strings.Builder
	flush := func() {
		value := builder.String()
		builder.Reset()
		if len(value) < 3 || workspaceContextStopWords[value] || seen[value] {
			return
		}
		seen[value] = true
		terms = append(terms, value)
	}
	for _, char := range task {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' {
			builder.WriteRune(char)
			continue
		}
		flush()
	}
	flush()
	if len(terms) > 24 {
		terms = terms[:24]
	}
	sort.Strings(terms)
	return terms
}

var workspaceContextStopWords = map[string]bool{
	"about": true, "acceptance": true, "add": true, "after": true, "all": true, "and": true,
	"any": true, "are": true, "before": true, "bug": true, "can": true, "card": true,
	"change": true, "code": true, "criteria": true, "does": true, "done": true, "for": true,
	"from": true, "have": true, "into": true, "make": true, "must": true, "need": true,
	"needs": true, "not": true, "only": true, "plan": true, "should": true, "task": true,
	"that": true, "the": true, "this": true, "update": true, "use": true, "when": true,
	"with": true, "work": true,
}

func workspaceContextTermSet(terms []string) map[string]bool {
	set := make(map[string]bool, len(terms))
	for _, term := range terms {
		set[term] = true
	}
	return set
}

func workspaceContextChangedPathSet(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, path := range paths {
		path = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(path, "\\", "/")))
		if path != "" {
			set[path] = true
		}
	}
	return set
}

func sortWorkspaceContextCandidates(candidates []workspaceContextFileCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return strings.ToLower(candidates[i].path) < strings.ToLower(candidates[j].path)
	})
}

func workspaceContextRelevantFiles(candidates []workspaceContextFileCandidate, maxFiles int) []tools.WorkspaceContextFile {
	maxFiles = tools.NormalizeWorkspaceContextMaxFiles(maxFiles)
	count := maxFiles
	if len(candidates) < count {
		count = len(candidates)
	}
	files := make([]tools.WorkspaceContextFile, 0, count)
	for _, candidate := range candidates[:count] {
		files = append(files, workspaceContextFileOutput(candidate))
	}
	return files
}

func workspaceContextLikelyTests(candidates []workspaceContextFileCandidate, relevant []tools.WorkspaceContextFile) []tools.WorkspaceContextFile {
	relevantPaths := map[string]bool{}
	for _, file := range relevant {
		relevantPaths[strings.ToLower(file.Path)] = true
	}
	tests := make([]tools.WorkspaceContextFile, 0, workspaceContextMaxTestFiles)
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if !candidate.isTest {
			continue
		}
		key := strings.ToLower(candidate.path)
		if seen[key] {
			continue
		}
		if relevantPaths[key] || workspaceContextNearRelevantTest(candidate.path, relevant) || candidate.score > 0 {
			tests = append(tests, workspaceContextFileOutput(candidate))
			seen[key] = true
		}
		if len(tests) >= workspaceContextMaxTestFiles {
			break
		}
	}
	return tests
}

func workspaceContextNearRelevantTest(testPath string, relevant []tools.WorkspaceContextFile) bool {
	testDir := workspaceContextDir(testPath)
	testStem := workspaceContextTestStem(testPath)
	for _, file := range relevant {
		if file.Path == testPath || workspaceContextDir(file.Path) != testDir {
			continue
		}
		stem := workspaceContextFileStem(file.Path)
		if stem != "" && testStem != "" && (strings.Contains(testStem, stem) || strings.Contains(stem, testStem)) {
			return true
		}
	}
	return false
}

func workspaceContextFileOutput(candidate workspaceContextFileCandidate) tools.WorkspaceContextFile {
	return tools.WorkspaceContextFile{
		Path:    candidate.path,
		Kind:    candidate.kind,
		Reason:  workspaceContextReason(candidate.reasons),
		Score:   candidate.score,
		Matches: append([]tools.WorkspaceContextMatch(nil), candidate.matches...),
	}
}

func workspaceContextReason(reasons map[string]bool) string {
	if len(reasons) == 0 {
		return ""
	}
	ordered := make([]string, 0, len(reasons))
	for reason := range reasons {
		ordered = append(ordered, reason)
	}
	sort.Strings(ordered)
	return strings.Join(ordered, ", ")
}

func (s *SystemService) enrichWorkspaceContextSymbols(ctx context.Context, workspace Workspace, files []tools.WorkspaceContextFile, warnings *[]string) []tools.WorkspaceContextFile {
	targets := make([]workspaceContextSymbolTarget, 0, workspaceContextMaxSymbolFiles)
	for i, file := range files {
		if workspaceContextIsTestPath(file.Path) {
			continue
		}
		languageID, ok := lspLanguageIDForPath(file.Path)
		if !ok {
			continue
		}
		targets = append(targets, workspaceContextSymbolTarget{
			index:      i,
			languageID: languageID,
		})
		if len(targets) >= workspaceContextMaxSymbolFiles {
			break
		}
	}
	if len(targets) == 0 {
		return files
	}

	checkedLanguages := map[string]bool{}
	unavailableLanguages := map[string]bool{}
	navigator := s.codeNavigator(workspace)
	for _, target := range targets {
		displayName := lspLanguageDisplayName(target.languageID)
		if !checkedLanguages[target.languageID] {
			checkedLanguages[target.languageID] = true
			command, ok := lspCommandForLanguage(target.languageID)
			if !ok {
				*warnings = append(*warnings, fmt.Sprintf("%s language server symbols unavailable: %s LSP is not configured.", displayName, target.languageID))
				unavailableLanguages[target.languageID] = true
			} else if _, err := exec.LookPath(command.name); err != nil {
				*warnings = append(*warnings, fmt.Sprintf("%s language server symbols unavailable: %s was not found on PATH.", displayName, command.name))
				unavailableLanguages[target.languageID] = true
			}
		}
		if unavailableLanguages[target.languageID] {
			continue
		}
		symbolCtx, cancel := context.WithTimeout(ctx, workspaceContextSymbolTimeout)
		response, err := navigator.QueryCode(symbolCtx, tools.CodeNavigationRequest{
			Operation:  "document_symbols",
			Path:       files[target.index].Path,
			MaxResults: workspaceContextSymbolsPerFile,
		})
		cancel()
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("%s symbols unavailable for %s: %s", displayName, files[target.index].Path, err.Error()))
			continue
		}
		files[target.index].Symbols = response.Symbols
	}
	return files
}

func workspaceContextManifestOutput(manifests []workspaceContextManifestCandidate) []tools.WorkspaceContextManifest {
	output := make([]tools.WorkspaceContextManifest, 0, len(manifests))
	seen := map[string]bool{}
	for _, manifest := range manifests {
		key := strings.ToLower(manifest.path)
		if seen[key] {
			continue
		}
		seen[key] = true
		output = append(output, tools.WorkspaceContextManifest{
			Path: manifest.path,
			Kind: manifest.kind,
		})
	}
	sort.Slice(output, func(i, j int) bool {
		return strings.ToLower(output[i].Path) < strings.ToLower(output[j].Path)
	})
	return output
}

func workspaceContextProjectRootOutput(projectRoots map[string]*workspaceContextProjectRootCandidate) []tools.WorkspaceContextProjectRoot {
	output := make([]tools.WorkspaceContextProjectRoot, 0, len(projectRoots))
	for _, root := range projectRoots {
		manifests := append([]string(nil), root.manifests...)
		sort.Strings(manifests)
		output = append(output, tools.WorkspaceContextProjectRoot{
			Path:      root.path,
			Kind:      root.kind,
			Manifests: manifests,
		})
	}
	sort.Slice(output, func(i, j int) bool {
		if output[i].Path != output[j].Path {
			return strings.ToLower(output[i].Path) < strings.ToLower(output[j].Path)
		}
		return output[i].Kind < output[j].Kind
	})
	return output
}

func workspaceContextLikelyCommands(workspace Workspace, projectRoots map[string]*workspaceContextProjectRootCandidate) []tools.WorkspaceContextCommand {
	commands := make([]tools.WorkspaceContextCommand, 0)
	seen := map[string]bool{}
	roots := make([]*workspaceContextProjectRootCandidate, 0, len(projectRoots))
	for _, root := range projectRoots {
		roots = append(roots, root)
	}
	sort.Slice(roots, func(i, j int) bool {
		return strings.ToLower(roots[i].path) < strings.ToLower(roots[j].path)
	})
	for _, root := range roots {
		for _, manifest := range root.manifests {
			switch strings.ToLower(filepath.Base(manifest)) {
			case "go.mod":
				workspaceContextAppendCommand(&commands, seen, tools.WorkspaceContextCommand{
					Kind:             "go",
					Command:          "go test ./...",
					WorkingDirectory: root.path,
				})
			case "package.json":
				if command, ok := nodeVerificationCommand(workspace, root.absolute); ok {
					workspaceContextAppendCommand(&commands, seen, workspaceContextCommandFromVerification(command))
				}
			}
		}
	}
	return commands
}

func workspaceContextVerificationCommands(workspace Workspace, paths []string) []tools.WorkspaceContextCommand {
	commands := detectKanbanVerificationCommands(workspace, paths)
	output := make([]tools.WorkspaceContextCommand, 0, len(commands))
	seen := map[string]bool{}
	for _, command := range commands {
		workspaceContextAppendCommand(&output, seen, workspaceContextCommandFromVerification(command))
	}
	return output
}

func workspaceContextCommandFromVerification(command kanbanVerificationCommand) tools.WorkspaceContextCommand {
	return tools.WorkspaceContextCommand{
		Kind:             command.Kind,
		Command:          command.Command,
		WorkingDirectory: command.WorkingDirectory,
	}
}

func workspaceContextAppendCommand(commands *[]tools.WorkspaceContextCommand, seen map[string]bool, command tools.WorkspaceContextCommand) {
	key := strings.ToLower(command.Kind + "\x00" + command.Command + "\x00" + command.WorkingDirectory)
	if seen[key] || strings.TrimSpace(command.Command) == "" {
		return
	}
	seen[key] = true
	*commands = append(*commands, command)
}

func workspaceContextFilePaths(files []tools.WorkspaceContextFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if file.Path != "" {
			paths = append(paths, file.Path)
		}
	}
	return paths
}

func workspaceContextSortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func workspaceContextUniqueWarnings(warnings []string) []string {
	if len(warnings) == 0 {
		return nil
	}
	output := make([]string, 0, len(warnings))
	seen := map[string]bool{}
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" || seen[warning] {
			continue
		}
		seen[warning] = true
		output = append(output, warning)
	}
	return output
}

func renderWorkspaceContextBrief(response tools.WorkspaceContextResponse) (string, bool) {
	var builder strings.Builder
	builder.WriteString("Workspace Context Brief\n")
	if response.Task != "" {
		builder.WriteString("Task: ")
		builder.WriteString(response.Task)
		builder.WriteString("\n")
	}
	if response.Path != "" && response.Path != "." {
		builder.WriteString("Scope: ")
		builder.WriteString(response.Path)
		builder.WriteString("\n")
	}
	appendWorkspaceContextProjectRoots(&builder, response.ProjectRoots)
	appendWorkspaceContextStrings(&builder, "Detected languages", response.DetectedLanguages)
	appendWorkspaceContextManifests(&builder, response.Manifests)
	appendWorkspaceContextCommands(&builder, "Likely commands", response.LikelyCommands)
	appendWorkspaceContextFiles(&builder, "Relevant files", response.RelevantFiles)
	appendWorkspaceContextFiles(&builder, "Likely test files", response.LikelyTestFiles)
	appendWorkspaceContextCommands(&builder, "Verification commands", response.VerificationCommands)
	appendWorkspaceContextStrings(&builder, "Warnings", response.Warnings)
	return truncateWorkspaceContextBrief(builder.String())
}

func appendWorkspaceContextProjectRoots(builder *strings.Builder, roots []tools.WorkspaceContextProjectRoot) {
	if len(roots) == 0 {
		return
	}
	builder.WriteString("\nProject roots:\n")
	for _, root := range roots {
		fmt.Fprintf(builder, "- %s (%s)", root.Path, root.Kind)
		if len(root.Manifests) > 0 {
			builder.WriteString(": ")
			builder.WriteString(strings.Join(root.Manifests, ", "))
		}
		builder.WriteString("\n")
	}
}

func appendWorkspaceContextStrings(builder *strings.Builder, title string, values []string) {
	if len(values) == 0 {
		return
	}
	builder.WriteString("\n")
	builder.WriteString(title)
	builder.WriteString(":\n")
	for _, value := range values {
		builder.WriteString("- ")
		builder.WriteString(value)
		builder.WriteString("\n")
	}
}

func appendWorkspaceContextManifests(builder *strings.Builder, manifests []tools.WorkspaceContextManifest) {
	if len(manifests) == 0 {
		return
	}
	builder.WriteString("\nManifests:\n")
	for _, manifest := range manifests {
		fmt.Fprintf(builder, "- %s (%s)\n", manifest.Path, manifest.Kind)
	}
}

func appendWorkspaceContextCommands(builder *strings.Builder, title string, commands []tools.WorkspaceContextCommand) {
	if len(commands) == 0 {
		return
	}
	builder.WriteString("\n")
	builder.WriteString(title)
	builder.WriteString(":\n")
	for _, command := range commands {
		fmt.Fprintf(builder, "- %s (%s)\n", command.Command, command.WorkingDirectory)
	}
}

func appendWorkspaceContextFiles(builder *strings.Builder, title string, files []tools.WorkspaceContextFile) {
	if len(files) == 0 {
		return
	}
	builder.WriteString("\n")
	builder.WriteString(title)
	builder.WriteString(":\n")
	for _, file := range files {
		fmt.Fprintf(builder, "- %s", file.Path)
		if file.Kind != "" {
			fmt.Fprintf(builder, " [%s]", file.Kind)
		}
		if file.Reason != "" {
			fmt.Fprintf(builder, " - %s", file.Reason)
		}
		builder.WriteString("\n")
		for _, match := range file.Matches {
			fmt.Fprintf(builder, "  - line %d: %s\n", match.Line, match.Text)
		}
		if len(file.Symbols) > 0 {
			builder.WriteString("  Symbols:")
			limit := len(file.Symbols)
			if limit > 8 {
				limit = 8
			}
			for i := 0; i < limit; i++ {
				symbol := file.Symbols[i]
				fmt.Fprintf(builder, " %s", symbol.Name)
				if symbol.KindName != "" {
					fmt.Fprintf(builder, ":%s", symbol.KindName)
				}
			}
			if len(file.Symbols) > limit {
				builder.WriteString(" ...")
			}
			builder.WriteString("\n")
		}
	}
}

func truncateWorkspaceContextBrief(value string) (string, bool) {
	if len(value) <= tools.WorkspaceContextBriefMaxBytes {
		return value, false
	}
	var builder strings.Builder
	for _, char := range value {
		next := string(char)
		if builder.Len()+len(next) > tools.WorkspaceContextBriefMaxBytes-len("\n... context brief truncated by Echo ...") {
			break
		}
		builder.WriteString(next)
	}
	builder.WriteString("\n... context brief truncated by Echo ...")
	return builder.String(), true
}

func sameWorkspaceContextDir(left string, right string) bool {
	return workspaceContextDir(left) == workspaceContextDir(right)
}

func workspaceContextDir(path string) string {
	path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	if slash := strings.LastIndex(path, "/"); slash >= 0 {
		return path[:slash]
	}
	return ""
}

func workspaceContextFileStem(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

func workspaceContextTestStem(path string) string {
	stem := workspaceContextFileStem(path)
	stem = strings.TrimSuffix(stem, "_test")
	stem = strings.TrimSuffix(stem, ".test")
	stem = strings.TrimSuffix(stem, ".spec")
	stem = strings.TrimPrefix(stem, "test_")
	return stem
}
