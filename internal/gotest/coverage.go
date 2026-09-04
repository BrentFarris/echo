package gotest

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/brent/echo/internal/terminal"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/google/uuid"
)

const MaxCoverageProfileBytes = 16 << 20

var coverageLinePattern = regexp.MustCompile(`^(.+):([0-9]+)\.([0-9]+),([0-9]+)\.([0-9]+) ([0-9]+) ([0-9]+)$`)

type CoveragePosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type CoverageRange struct {
	Start      CoveragePosition `json:"start"`
	End        CoveragePosition `json:"end"`
	Statements int              `json:"statements"`
	Count      uint64           `json:"count"`
}

type CoverageFile struct {
	Ref    workspacefs.FileRef `json:"ref"`
	Ranges []CoverageRange     `json:"ranges"`
}

type CoverageSnapshot struct {
	Revision  uint64              `json:"revision"`
	SessionID string              `json:"sessionId"`
	Package   workspacefs.FileRef `json:"package"`
	Mode      string              `json:"mode"`
	Files     []CoverageFile      `json:"files"`
}

type CoverageEvent struct {
	Type        string            `json:"type"`
	WorkspaceID string            `json:"workspaceId"`
	Revision    uint64            `json:"revision"`
	State       string            `json:"state"`
	Coverage    *CoverageSnapshot `json:"coverage,omitempty"`
	Message     string            `json:"message,omitempty"`
}

type parsedCoverageRecord struct {
	prefix   string
	base     string
	coverage CoverageRange
}

type coverageRecord struct {
	revision         uint64
	generation       string
	packageDirectory string
	packageRef       workspacefs.FileRef
	fingerprint      string
	snapshot         *CoverageSnapshot
}

func (s *Service) SetCoverageNotifier(notify func(CoverageEvent)) {
	s.mu.Lock()
	s.coverageNotify = notify
	s.mu.Unlock()
}

func (s *Service) ClearCoverage(workspaceID string) {
	s.clearCoverage(workspaceID, "")
}

func (s *Service) RemoveWorkspace(workspaceID string) {
	s.mu.Lock()
	delete(s.coverage, workspaceID)
	delete(s.latest, workspaceID)
	s.mu.Unlock()
}

func (s *Service) beginCoverage(workspaceID, packageDirectory, fingerprint string, packageRef workspacefs.FileRef) string {
	generation := uuid.NewString()
	s.mu.Lock()
	revision := s.coverage[workspaceID].revision + 1
	s.coverage[workspaceID] = coverageRecord{
		revision: revision, generation: generation, packageDirectory: packageDirectory,
		packageRef: packageRef, fingerprint: fingerprint,
	}
	notify := s.coverageNotify
	s.mu.Unlock()
	if notify != nil {
		notify(CoverageEvent{Type: "go_test_coverage", WorkspaceID: workspaceID, Revision: revision, State: "cleared"})
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

func (s *Service) finishCoverage(workspaceID, generation, profilePath, packageDirectory, fingerprint string, packageRef workspacefs.FileRef, result terminal.TaskResult) {
	defer os.Remove(profilePath)
	if result.Status != "passed" || result.ExitCode != 0 {
		s.abortCoverage(workspaceID, generation)
		return
	}
	currentFingerprint, err := packageFingerprint(packageDirectory)
	if err != nil || currentFingerprint != fingerprint {
		s.clearCoverage(workspaceID, generation)
		return
	}
	mode, files, err := parseCoverageProfile(profilePath, packageDirectory, packageRef)
	if err != nil {
		s.failCoverage(workspaceID, generation, "Tests passed, but Echo could not load the Go coverage profile.")
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
		Revision: revision, SessionID: result.SessionID, Package: packageRef, Mode: mode, Files: files,
	}
	record.revision = revision
	record.snapshot = snapshot
	s.coverage[workspaceID] = record
	notify := s.coverageNotify
	s.mu.Unlock()
	if notify != nil {
		notify(CoverageEvent{
			Type: "go_test_coverage", WorkspaceID: workspaceID, Revision: revision,
			State: "ready", Coverage: snapshot,
		})
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
		notify(CoverageEvent{
			Type: "go_test_coverage", WorkspaceID: workspaceID, Revision: revision,
			State: "error", Message: message,
		})
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
		notify(CoverageEvent{Type: "go_test_coverage", WorkspaceID: workspaceID, Revision: revision, State: "cleared"})
	}
}

// Coverage returns the current in-memory coverage result. It verifies the
// package fingerprint again so a disconnected client can never restore stale
// decorations after missing a filesystem notification.
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
	fingerprint, err := packageFingerprint(record.packageDirectory)
	if err != nil || fingerprint != record.fingerprint {
		s.clearCoverage(workspaceID, record.generation)
		s.mu.Lock()
		current := s.coverage[workspaceID]
		s.mu.Unlock()
		return current.snapshot, current.revision, nil
	}
	return record.snapshot, record.revision, nil
}

// HandleWorkspaceChanges invalidates coverage only for direct Go source
// changes in the package represented by the active profile. Fingerprinting
// makes delayed notifications from the pre-run save-all operation harmless.
func (s *Service) HandleWorkspaceChanges(workspaceID string, changes []workspacefs.Change) {
	s.mu.Lock()
	record := s.coverage[workspaceID]
	s.mu.Unlock()
	if record.packageDirectory == "" || record.generation == "" {
		return
	}
	touched := false
	for _, change := range changes {
		if change.Ref.RootID != record.packageRef.RootID || !strings.HasSuffix(strings.ToLower(change.Ref.Path), ".go") {
			continue
		}
		if referencePathEqual(referenceDirectory(change.Ref.Path), record.packageRef.Path) {
			touched = true
			break
		}
	}
	if !touched {
		return
	}
	fingerprint, err := packageFingerprint(record.packageDirectory)
	if err != nil || fingerprint != record.fingerprint {
		s.clearCoverage(workspaceID, record.generation)
	}
}

func referenceDirectory(value string) string {
	value = strings.Trim(strings.ReplaceAll(value, `\`, "/"), "/")
	if slash := strings.LastIndexByte(value, '/'); slash >= 0 {
		return value[:slash]
	}
	return ""
}

func referencePathEqual(left, right string) bool {
	left = strings.Trim(strings.ReplaceAll(left, `\`, "/"), "/")
	right = strings.Trim(strings.ReplaceAll(right, `\`, "/"), "/")
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func parseCoverageProfile(profilePath, packageDirectory string, packageRef workspacefs.FileRef) (string, []CoverageFile, error) {
	info, err := os.Stat(profilePath)
	if err != nil {
		return "", nil, fmt.Errorf("coverage profile is unavailable")
	}
	if info.Size() <= 0 || info.Size() > MaxCoverageProfileBytes {
		return "", nil, fmt.Errorf("coverage profile has an invalid size")
	}
	file, err := os.Open(profilePath)
	if err != nil {
		return "", nil, fmt.Errorf("coverage profile is unavailable")
	}
	defer file.Close()

	sources, err := packageCoverageSources(packageDirectory)
	if err != nil {
		return "", nil, err
	}
	scanner := bufio.NewScanner(io.LimitReader(file, MaxCoverageProfileBytes+1))
	scanner.Buffer(make([]byte, 64*1024), MaxCoverageProfileBytes)
	if !scanner.Scan() {
		return "", nil, fmt.Errorf("coverage profile is empty")
	}
	header := strings.TrimSpace(scanner.Text())
	if !strings.HasPrefix(header, "mode: ") {
		return "", nil, fmt.Errorf("coverage profile has no mode header")
	}
	mode := strings.TrimSpace(strings.TrimPrefix(header, "mode: "))
	if mode != "set" && mode != "count" && mode != "atomic" {
		return "", nil, fmt.Errorf("coverage profile mode %q is unsupported", mode)
	}

	records := make([]parsedCoverageRecord, 0, 256)
	prefixFiles := map[string]map[string]bool{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		match := coverageLinePattern.FindStringSubmatch(line)
		if match == nil {
			return "", nil, fmt.Errorf("coverage profile contains a malformed record")
		}
		filename := strings.ReplaceAll(match[1], `\`, "/")
		base := coverageSourceKey(filepath.Base(filename))
		if _, ok := sources[base]; !ok {
			continue
		}
		startLine, startColumn, endLine, endColumn, statements, count, parseErr := parseCoverageNumbers(match[2:])
		if parseErr != nil {
			return "", nil, parseErr
		}
		if statements == 0 {
			continue
		}
		prefix := strings.TrimSuffix(filename, "/"+filepath.Base(filename))
		records = append(records, parsedCoverageRecord{
			prefix: prefix, base: base,
			coverage: CoverageRange{
				Start:      CoveragePosition{Line: startLine - 1, Character: startColumn - 1},
				End:        CoveragePosition{Line: endLine - 1, Character: endColumn - 1},
				Statements: statements, Count: count,
			},
		})
		if prefixFiles[prefix] == nil {
			prefixFiles[prefix] = map[string]bool{}
		}
		prefixFiles[prefix][base] = true
	}
	if err := scanner.Err(); err != nil {
		return "", nil, fmt.Errorf("coverage profile could not be read")
	}
	if len(records) == 0 {
		return mode, nil, nil
	}
	prefix := bestCoveragePrefix(prefixFiles, packageRef.Path)
	byFile := map[string][]CoverageRange{}
	for _, record := range records {
		if record.prefix == prefix {
			byFile[record.base] = append(byFile[record.base], record.coverage)
		}
	}
	files := make([]CoverageFile, 0, len(byFile))
	for base, ranges := range byFile {
		source := sources[base]
		sort.Slice(ranges, func(i, j int) bool {
			if ranges[i].Start.Line != ranges[j].Start.Line {
				return ranges[i].Start.Line < ranges[j].Start.Line
			}
			return ranges[i].Start.Character < ranges[j].Start.Character
		})
		files = append(files, CoverageFile{
			Ref:    workspacefs.FileRef{RootID: packageRef.RootID, Path: joinReferencePath(packageRef.Path, source)},
			Ranges: ranges,
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Ref.Path < files[j].Ref.Path })
	return mode, files, nil
}

func parseCoverageNumbers(values []string) (int, int, int, int, int, uint64, error) {
	if len(values) != 6 {
		return 0, 0, 0, 0, 0, 0, fmt.Errorf("coverage profile contains a malformed record")
	}
	parsed := make([]uint64, len(values))
	for index, value := range values {
		number, err := strconv.ParseUint(value, 10, 64)
		// Go emits zero-statement records for empty functions. Source positions
		// must remain one-based, while statement and execution counts may be 0.
		if err != nil || number == 0 && index < 4 {
			return 0, 0, 0, 0, 0, 0, fmt.Errorf("coverage profile contains an invalid range")
		}
		parsed[index] = number
	}
	if parsed[0] > uint64(^uint(0)>>1) || parsed[1] > uint64(^uint(0)>>1) || parsed[2] > uint64(^uint(0)>>1) || parsed[3] > uint64(^uint(0)>>1) || parsed[4] > uint64(^uint(0)>>1) {
		return 0, 0, 0, 0, 0, 0, fmt.Errorf("coverage profile contains an invalid range")
	}
	if parsed[2] < parsed[0] || parsed[2] == parsed[0] && parsed[3] < parsed[1] {
		return 0, 0, 0, 0, 0, 0, fmt.Errorf("coverage profile contains a reversed range")
	}
	return int(parsed[0]), int(parsed[1]), int(parsed[2]), int(parsed[3]), int(parsed[4]), parsed[5], nil
}

func packageCoverageSources(directory string) (map[string]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read Go package for coverage: %w", err)
	}
	sources := make(map[string]string)
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(name), ".go") || strings.HasSuffix(strings.ToLower(name), "_test.go") {
			continue
		}
		sources[coverageSourceKey(name)] = name
	}
	return sources, nil
}

func coverageSourceKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(name)
	}
	return name
}

func bestCoveragePrefix(prefixFiles map[string]map[string]bool, packagePath string) string {
	best := ""
	bestCount := -1
	bestSuffix := false
	normalizedPackage := strings.Trim(strings.ReplaceAll(packagePath, `\`, "/"), "/")
	for prefix, files := range prefixFiles {
		suffix := normalizedPackage != "" && (prefix == normalizedPackage || strings.HasSuffix(prefix, "/"+normalizedPackage))
		if len(files) > bestCount || len(files) == bestCount && suffix && !bestSuffix || len(files) == bestCount && suffix == bestSuffix && prefix < best {
			best, bestCount, bestSuffix = prefix, len(files), suffix
		}
	}
	return best
}

func packageFingerprint(directory string) (string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(strings.ToLower(entry.Name()), ".go") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		_, _ = io.WriteString(hash, name)
		_, _ = hash.Write([]byte{0})
		file, openErr := os.Open(filepath.Join(directory, name))
		if openErr != nil {
			return "", openErr
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func joinReferencePath(directory, name string) string {
	directory = strings.Trim(strings.ReplaceAll(directory, `\`, "/"), "/")
	if directory == "" || directory == "." {
		return name
	}
	return directory + "/" + name
}
