package ctest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type gcovReport struct {
	FormatVersion           string     `json:"format_version"`
	CurrentWorkingDirectory string     `json:"current_working_directory"`
	Files                   []gcovFile `json:"files"`
}

type gcovFile struct {
	File  string     `json:"file"`
	Lines []gcovLine `json:"lines"`
}

type gcovLine struct {
	Count           uint64 `json:"count"`
	LineNumber      int    `json:"line_number"`
	UnexecutedBlock bool   `json:"unexecuted_block"`
}

func (s *Service) loadGcovCoverage(workspaceID, scratch string, target resolvedTarget) ([]CoverageFile, error) {
	counters, err := discoverFiles(target.objectRoots, ".gcda")
	if err != nil {
		return nil, err
	}
	if len(counters) == 0 {
		return nil, fmt.Errorf("the test run produced no .gcda counters; build and link the suite with GCC --coverage")
	}
	units, err := discoverFiles(target.objectRoots, ".gcno")
	if err != nil {
		return nil, err
	}
	if len(units) == 0 {
		return nil, fmt.Errorf("no .gcno coverage units were found under the configured object roots; build with GCC --coverage")
	}

	accumulators := make(map[string]map[int]*lineAccumulator)
	paths := make(map[string]string)
	for index, unit := range units {
		reportDir := filepath.Join(scratch, fmt.Sprintf("gcov-%06d", index))
		if err := os.MkdirAll(reportDir, 0o700); err != nil {
			return nil, err
		}
		runtimeDir, err := s.runtimePath(workspaceID, reportDir)
		if err != nil {
			return nil, err
		}
		runtimeUnit, err := s.runtimePath(workspaceID, unit)
		if err != nil {
			return nil, err
		}
		ctx, cancel := coverageContext()
		result, runErr := s.runTool(ctx, workspaceID, "gcov", []string{"--json-format", "--all-blocks", "--branch-counts", "--branch-probabilities", runtimeUnit}, runtimeDir, nil)
		cancel()
		if runErr != nil {
			return nil, fmt.Errorf("run gcov: %w", runErr)
		}
		if result.code != 0 {
			return nil, toolError("gcov", result)
		}
		reports, err := discoverFiles([]string{reportDir}, ".gcov.json.gz")
		if err != nil {
			return nil, err
		}
		if len(reports) == 0 {
			return nil, fmt.Errorf("gcov produced no JSON report for %s", filepath.Base(unit))
		}
		for _, reportPath := range reports {
			var report gcovReport
			if err := readGzipJSON(reportPath, &report); err != nil {
				return nil, fmt.Errorf("read %s: %w", filepath.Base(reportPath), err)
			}
			if !supportedGcovVersion(report.FormatVersion) {
				return nil, fmt.Errorf("unsupported gcov JSON format version %q", report.FormatVersion)
			}
			for _, file := range report.Files {
				filename := runtimeReportPath(s, workspaceID, file.File, report.CurrentWorkingDirectory)
				if !isCSource(filename) || !withinAny(target.sourceRoots, filename) {
					continue
				}
				key := coveragePathKey(filename)
				paths[key] = filename
				byLine := accumulators[key]
				if byLine == nil {
					byLine = make(map[int]*lineAccumulator)
					accumulators[key] = byLine
				}
				for _, line := range file.Lines {
					if line.LineNumber < 1 {
						continue
					}
					value := byLine[line.LineNumber]
					if value == nil {
						value = &lineAccumulator{}
						byLine[line.LineNumber] = value
					}
					value.count = addExecutionCounts(value.count, line.Count)
					if line.Count > 0 {
						value.covered = true
					} else {
						value.uncovered = true
					}
					value.partial = value.partial || line.Count > 0 && line.UnexecutedBlock
				}
			}
		}
	}
	return finalizeProviderCoverage(s, workspaceID, target, accumulators, paths)
}

func discoverFiles(roots []string, suffix string) ([]string, error) {
	result := make([]string, 0, 32)
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("coverage root is not a directory: %s", root)
		}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), strings.ToLower(suffix)) {
				result = append(result, path)
				if len(result) > maxSourceFiles {
					return fmt.Errorf("coverage input contains too many files")
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result, nil
}

func supportedGcovVersion(value string) bool {
	major := strings.SplitN(strings.TrimSpace(value), ".", 2)[0]
	return major == "1" || major == "2"
}

func isCSource(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	return extension == ".c" || extension == ".h"
}

func coveragePathKey(path string) string {
	path = filepath.Clean(path)
	if runtimeCaseFold() {
		return strings.ToLower(path)
	}
	return path
}

func finalizeProviderCoverage(s *Service, workspaceID string, target resolvedTarget, accumulators map[string]map[int]*lineAccumulator, paths map[string]string) ([]CoverageFile, error) {
	named := make(map[string]map[int]*lineAccumulator, len(accumulators))
	for key, lines := range accumulators {
		named[paths[key]] = lines
	}
	return finalizeCoverage(s, workspaceID, target, named)
}
