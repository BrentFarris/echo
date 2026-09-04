package ctest

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/terminal"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

const (
	maxCoverageBytes = 32 << 20
	maxSourceFiles   = 20000
	maxSourceBytes   = 256 << 20
)

type CoverageLine struct {
	Line           int    `json:"line"`
	ExecutionCount uint64 `json:"executionCount"`
	State          string `json:"state"`
}

type CoverageFile struct {
	Ref   workspacefs.FileRef `json:"ref"`
	Lines []CoverageLine      `json:"lines"`
}

type CoverageSnapshot struct {
	Revision  uint64         `json:"revision"`
	SessionID string         `json:"sessionId"`
	TargetID  string         `json:"targetId"`
	Provider  string         `json:"provider"`
	Files     []CoverageFile `json:"files"`
}

type CoverageEvent struct {
	Type        string            `json:"type"`
	WorkspaceID string            `json:"workspaceId"`
	Revision    uint64            `json:"revision"`
	State       string            `json:"state"`
	Coverage    *CoverageSnapshot `json:"coverage,omitempty"`
	Message     string            `json:"message,omitempty"`
}

type coverageRecord struct {
	revision       uint64
	generation     string
	targetID       string
	sourceRoots    []string
	sourceRootRefs []workspacefs.FileRef
	fingerprint    string
	snapshot       *CoverageSnapshot
}

type lineAccumulator struct {
	count     uint64
	covered   bool
	uncovered bool
	partial   bool
}

func (s *Service) SetCoverageNotifier(notify func(CoverageEvent)) {
	s.mu.Lock()
	s.coverageNotify = notify
	s.mu.Unlock()
}

func (s *Service) ClearCoverage(workspaceID string) { s.clearCoverage(workspaceID, "") }

func (s *Service) beginCoverage(workspaceID string, target resolvedTarget) string {
	generation := newGeneration()
	s.mu.Lock()
	revision := s.coverage[workspaceID].revision + 1
	s.coverage[workspaceID] = coverageRecord{
		revision: revision, generation: generation, targetID: target.config.ID,
		sourceRoots: append([]string(nil), target.sourceRoots...), sourceRootRefs: append([]workspacefs.FileRef(nil), target.sourceRootRefs...),
	}
	notify := s.coverageNotify
	s.mu.Unlock()
	if notify != nil {
		notify(CoverageEvent{Type: "c_test_coverage", WorkspaceID: workspaceID, Revision: revision, State: "cleared"})
	}
	return generation
}

func (s *Service) abortCoverage(workspaceID, generation string) {
	s.mu.Lock()
	record := s.coverage[workspaceID]
	if record.generation == generation {
		record.generation = ""
		s.coverage[workspaceID] = record
	}
	s.mu.Unlock()
}

func (s *Service) finishCoverage(workspaceID, generation, scratch, fingerprint string, target resolvedTarget, result terminal.TaskResult) {
	defer os.RemoveAll(scratch)
	if result.Status != "passed" || result.ExitCode != 0 {
		s.abortCoverage(workspaceID, generation)
		return
	}
	current, err := sourceFingerprint(target.sourceRoots)
	if err != nil || fingerprint == "" || current != fingerprint {
		s.clearCoverage(workspaceID, generation)
		return
	}
	files, err := s.loadCoverage(workspaceID, scratch, target)
	if err != nil {
		s.failCoverage(workspaceID, generation, "Tests passed, but Echo could not load C coverage: "+err.Error())
		return
	}
	s.mu.Lock()
	record := s.coverage[workspaceID]
	if record.generation != generation {
		s.mu.Unlock()
		return
	}
	revision := record.revision + 1
	snapshot := &CoverageSnapshot{
		Revision: revision, SessionID: result.SessionID, TargetID: target.config.ID,
		Provider: target.config.Coverage.Provider, Files: files,
	}
	record.revision, record.fingerprint, record.snapshot = revision, fingerprint, snapshot
	s.coverage[workspaceID] = record
	notify := s.coverageNotify
	s.mu.Unlock()
	if notify != nil {
		notify(CoverageEvent{Type: "c_test_coverage", WorkspaceID: workspaceID, Revision: revision, State: "ready", Coverage: snapshot})
	}
}

func (s *Service) failCoverage(workspaceID, generation, message string) {
	s.mu.Lock()
	record := s.coverage[workspaceID]
	if record.generation != generation {
		s.mu.Unlock()
		return
	}
	revision := record.revision + 1
	s.coverage[workspaceID] = coverageRecord{revision: revision}
	notify := s.coverageNotify
	s.mu.Unlock()
	if notify != nil {
		notify(CoverageEvent{Type: "c_test_coverage", WorkspaceID: workspaceID, Revision: revision, State: "error", Message: message})
	}
}

func (s *Service) clearCoverage(workspaceID, generation string) {
	s.mu.Lock()
	record := s.coverage[workspaceID]
	if generation != "" && record.generation != generation {
		s.mu.Unlock()
		return
	}
	revision := record.revision + 1
	s.coverage[workspaceID] = coverageRecord{revision: revision}
	notify := s.coverageNotify
	s.mu.Unlock()
	if notify != nil {
		notify(CoverageEvent{Type: "c_test_coverage", WorkspaceID: workspaceID, Revision: revision, State: "cleared"})
	}
}

func (s *Service) Coverage(workspaceID string) (*CoverageSnapshot, uint64, error) {
	if _, err := s.Config(workspaceID); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	record := s.coverage[workspaceID]
	s.mu.Unlock()
	if record.snapshot == nil {
		return nil, record.revision, nil
	}
	fingerprint, err := sourceFingerprint(record.sourceRoots)
	if err != nil || fingerprint != record.fingerprint {
		s.clearCoverage(workspaceID, record.generation)
		s.mu.Lock()
		current := s.coverage[workspaceID]
		s.mu.Unlock()
		return current.snapshot, current.revision, nil
	}
	return record.snapshot, record.revision, nil
}

func (s *Service) HandleWorkspaceChanges(workspaceID string, changes []workspacefs.Change) {
	s.mu.Lock()
	record := s.coverage[workspaceID]
	s.mu.Unlock()
	if record.snapshot == nil {
		return
	}
	for _, change := range changes {
		extension := strings.ToLower(filepath.Ext(change.Ref.Path))
		if extension != ".c" && extension != ".h" {
			continue
		}
		for _, root := range record.sourceRootRefs {
			if refWithin(root, change.Ref) {
				fingerprint, err := sourceFingerprint(record.sourceRoots)
				if err != nil || fingerprint != record.fingerprint {
					s.clearCoverage(workspaceID, record.generation)
				}
				return
			}
		}
	}
}

func refWithin(root, candidate workspacefs.FileRef) bool {
	if root.RootID != candidate.RootID {
		return false
	}
	prefix := strings.Trim(strings.ReplaceAll(root.Path, `\`, "/"), "/")
	path := strings.Trim(strings.ReplaceAll(candidate.Path, `\`, "/"), "/")
	if runtimeCaseFold() {
		prefix, path = strings.ToLower(prefix), strings.ToLower(path)
	}
	return prefix == "" || path == prefix || strings.HasPrefix(path, prefix+"/")
}

func runtimeCaseFold() bool { return filepath.Separator == '\\' }

func sourceFingerprint(roots []string) (string, error) {
	names := make([]string, 0, 256)
	seen := make(map[string]bool)
	total := int64(0)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			extension := strings.ToLower(filepath.Ext(entry.Name()))
			if extension != ".c" && extension != ".h" {
				return nil
			}
			key := coveragePathKey(path)
			if seen[key] {
				return nil
			}
			seen[key] = true
			names = append(names, path)
			if len(names) > maxSourceFiles {
				return fmt.Errorf("C coverage source scope contains too many files")
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	hash := sha256.New()
	for _, name := range names {
		info, err := os.Stat(name)
		if err != nil {
			return "", err
		}
		total += info.Size()
		if total > maxSourceBytes {
			return "", fmt.Errorf("C coverage source scope is too large")
		}
		_, _ = io.WriteString(hash, name)
		_, _ = hash.Write([]byte{0})
		file, err := os.Open(name)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func clearGcovCounters(roots []string) error {
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return fmt.Errorf("gcov object root is unavailable: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("gcov object root is not a directory: %s", root)
		}
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 && entry.IsDir() {
				return filepath.SkipDir
			}
			if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.EqualFold(filepath.Ext(entry.Name()), ".gcda") {
				return os.Remove(path)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("clear gcov counters: %w", err)
		}
	}
	return nil
}

func (s *Service) loadCoverage(workspaceID, scratch string, target resolvedTarget) ([]CoverageFile, error) {
	if target.config.Coverage.Provider == "gcov" {
		return s.loadGcovCoverage(workspaceID, scratch, target)
	}
	return s.loadLLVMCoverage(workspaceID, scratch, target)
}

type toolResult struct {
	stdout []byte
	stderr []byte
	code   int
}

func (s *Service) runTool(ctx context.Context, workspaceID, command string, args []string, cwd string, environment map[string]string) (toolResult, error) {
	workspace, ok, err := s.workspaces.Get(workspaceID)
	if err != nil {
		return toolResult{}, err
	}
	if !ok {
		return toolResult{}, fmt.Errorf("%w: %q", workspaces.ErrWorkspaceNotFound, workspaceID)
	}
	if workspace.Sandbox.Enabled {
		if s.sandbox == nil {
			return toolResult{}, fmt.Errorf("sandbox runtime is unavailable")
		}
		result, err := s.sandbox.Execute(ctx, workspaceID, sandbox.ExecRequest{
			Role: "workbench", Command: append([]string{command}, args...), WorkingDirectory: cwd,
			Environment: environmentList(environment), OutputLimit: maxCoverageBytes,
		})
		if err != nil {
			return toolResult{}, err
		}
		if result.StdoutTruncated || result.StderrTruncated {
			return toolResult{}, fmt.Errorf("coverage tool output exceeded %d bytes", maxCoverageBytes)
		}
		return toolResult{stdout: result.Stdout, stderr: result.Stderr, code: result.ExitCode}, nil
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return toolResult{}, fmt.Errorf("resolve coverage tool %q: %w", command, err)
	}
	process := exec.CommandContext(ctx, resolved, args...)
	process.Dir = cwd
	process.Env = mergeEnvironment(os.Environ(), environment)
	stdout, err := process.StdoutPipe()
	if err != nil {
		return toolResult{}, err
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		return toolResult{}, err
	}
	if err := process.Start(); err != nil {
		return toolResult{}, err
	}
	var wait sync.WaitGroup
	var stdoutData, stderrData []byte
	wait.Add(2)
	go func() { defer wait.Done(); stdoutData, _ = io.ReadAll(io.LimitReader(stdout, maxCoverageBytes+1)) }()
	go func() { defer wait.Done(); stderrData, _ = io.ReadAll(io.LimitReader(stderr, maxCoverageBytes+1)) }()
	waitErr := process.Wait()
	wait.Wait()
	if len(stdoutData) > maxCoverageBytes || len(stderrData) > maxCoverageBytes {
		return toolResult{}, fmt.Errorf("coverage tool output exceeded %d bytes", maxCoverageBytes)
	}
	code := 0
	if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			code = exitError.ExitCode()
		} else {
			return toolResult{}, waitErr
		}
	}
	return toolResult{stdout: stdoutData, stderr: stderrData, code: code}, nil
}

func environmentList(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func mergeEnvironment(base []string, values map[string]string) []string {
	result := append([]string(nil), base...)
	for key, value := range values {
		replaced := false
		for index, existing := range result {
			existingKey, _, found := strings.Cut(existing, "=")
			if found && (existingKey == key || runtimeCaseFold() && strings.EqualFold(existingKey, key)) {
				result[index], replaced = key+"="+value, true
				break
			}
		}
		if !replaced {
			result = append(result, key+"="+value)
		}
	}
	return result
}

func stateFor(line *lineAccumulator) string {
	if line.partial || line.covered && line.uncovered {
		return "partial"
	}
	if line.covered {
		return "covered"
	}
	return "uncovered"
}

func addExecutionCounts(left, right uint64) uint64 {
	if ^uint64(0)-left < right {
		return ^uint64(0)
	}
	return left + right
}

func withinAny(roots []string, path string) bool {
	for _, root := range roots {
		if pathWithin(root, path) {
			return true
		}
	}
	return false
}

func readGzipJSON(path string, destination any) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 || info.Size() > maxCoverageBytes {
		return fmt.Errorf("gcov JSON has an invalid size")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxCoverageBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxCoverageBytes {
		return fmt.Errorf("gcov JSON exceeds %d bytes", maxCoverageBytes)
	}
	return decodeJSON(data, destination)
}

func uintFromRaw(value json.RawMessage) (uint64, error) {
	var number json.Number
	if err := json.Unmarshal(value, &number); err != nil {
		return 0, err
	}
	return strconv.ParseUint(number.String(), 10, 64)
}

func boolFromRaw(value json.RawMessage) (bool, error) {
	var result bool
	return result, json.Unmarshal(value, &result)
}

func runtimeReportPath(s *Service, workspaceID, value, compilationDirectory string) string {
	workspace, ok, _ := s.workspaces.Get(workspaceID)
	if ok && workspace.Sandbox.Enabled && s.sandbox != nil {
		guestPath := strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")
		guestDirectory := strings.ReplaceAll(strings.TrimSpace(compilationDirectory), `\`, "/")
		if !pathpkg.IsAbs(guestPath) && guestDirectory != "" {
			guestPath = pathpkg.Join(guestDirectory, guestPath)
		}
		if host, err := s.sandbox.GuestToHost(workspaceID, pathpkg.Clean(guestPath)); err == nil {
			return filepath.Clean(host)
		}
	}
	value = filepath.FromSlash(strings.TrimSpace(value))
	if !filepath.IsAbs(value) && compilationDirectory != "" {
		value = filepath.Join(filepath.FromSlash(compilationDirectory), value)
	}
	return filepath.Clean(value)
}

func finalizeCoverage(s *Service, workspaceID string, target resolvedTarget, accumulators map[string]map[int]*lineAccumulator) ([]CoverageFile, error) {
	files := make([]CoverageFile, 0, len(accumulators))
	for filename, byLine := range accumulators {
		if !withinAny(target.sourceRoots, filename) {
			continue
		}
		ref, err := refForHostPath(s.fs, workspaceID, filename)
		if err != nil {
			continue
		}
		lines := make([]CoverageLine, 0, len(byLine))
		for number, value := range byLine {
			if number < 1 {
				continue
			}
			lines = append(lines, CoverageLine{Line: number - 1, ExecutionCount: value.count, State: stateFor(value)})
		}
		sort.Slice(lines, func(i, j int) bool { return lines[i].Line < lines[j].Line })
		if len(lines) > 0 {
			files = append(files, CoverageFile{Ref: ref, Lines: lines})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Ref.RootID != files[j].Ref.RootID {
			return files[i].Ref.RootID < files[j].Ref.RootID
		}
		return files[i].Ref.Path < files[j].Ref.Path
	})
	return files, nil
}

func toolError(name string, result toolResult) error {
	message := strings.TrimSpace(string(result.stderr))
	if message == "" {
		message = strings.TrimSpace(string(result.stdout))
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	return fmt.Errorf("%s exited with code %d: %s", name, result.code, message)
}

func coverageContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Minute)
}

func decodeJSON(data []byte, destination any) error {
	if len(data) == 0 || len(data) > maxCoverageBytes {
		return fmt.Errorf("coverage JSON has an invalid size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("coverage JSON contains more than one value")
		}
		return err
	}
	return nil
}
