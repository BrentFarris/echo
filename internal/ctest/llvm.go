package ctest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type llvmExport struct {
	Type    string     `json:"type"`
	Version string     `json:"version"`
	Data    []llvmData `json:"data"`
}

type llvmData struct {
	Functions []llvmFunction `json:"functions"`
	Files     []llvmFile     `json:"files"`
}

type llvmFunction struct {
	Filenames []string          `json:"filenames"`
	Regions   []json.RawMessage `json:"regions"`
}

type llvmFile struct {
	Filename string            `json:"filename"`
	Segments []json.RawMessage `json:"segments"`
	Branches []json.RawMessage `json:"branches"`
}

func (s *Service) loadLLVMCoverage(workspaceID, scratch string, target resolvedTarget) ([]CoverageFile, error) {
	rawProfiles, err := discoverFiles([]string{scratch}, ".profraw")
	if err != nil {
		return nil, err
	}
	if len(rawProfiles) == 0 {
		return nil, fmt.Errorf("no raw LLVM profiles were produced; build with -fprofile-instr-generate -fcoverage-mapping")
	}
	merged := filepath.Join(scratch, "coverage.profdata")
	runtimeMerged, err := s.runtimePath(workspaceID, merged)
	if err != nil {
		return nil, err
	}
	runtimeCwd, err := s.runtimePath(workspaceID, target.cwd)
	if err != nil {
		return nil, err
	}
	mergeArgs := []string{"merge", "-sparse", "-o", runtimeMerged}
	for _, raw := range rawProfiles {
		runtimeRaw, mapErr := s.runtimePath(workspaceID, raw)
		if mapErr != nil {
			return nil, mapErr
		}
		mergeArgs = append(mergeArgs, runtimeRaw)
	}
	ctx, cancel := coverageContext()
	mergeResult, runErr := s.runTool(ctx, workspaceID, "llvm-profdata", mergeArgs, runtimeCwd, nil)
	cancel()
	if runErr != nil {
		return nil, fmt.Errorf("run llvm-profdata: %w", runErr)
	}
	if mergeResult.code != 0 {
		return nil, toolError("llvm-profdata", mergeResult)
	}
	if info, err := os.Stat(merged); err != nil || info.Size() == 0 {
		return nil, fmt.Errorf("llvm-profdata did not produce a merged profile")
	}

	exportArgs := []string{"export", "-format=text", "-instr-profile=" + runtimeMerged, target.runtimeExecutable}
	for _, object := range target.runtimeObjects {
		exportArgs = append(exportArgs, "-object", object)
	}
	ctx, cancel = coverageContext()
	exportResult, runErr := s.runTool(ctx, workspaceID, "llvm-cov", exportArgs, runtimeCwd, nil)
	cancel()
	if runErr != nil {
		return nil, fmt.Errorf("run llvm-cov: %w", runErr)
	}
	if exportResult.code != 0 {
		return nil, toolError("llvm-cov", exportResult)
	}
	var report llvmExport
	if err := decodeJSON(exportResult.stdout, &report); err != nil {
		return nil, fmt.Errorf("parse llvm-cov export: %w", err)
	}
	if report.Type != "llvm.coverage.json.export" || strings.SplitN(strings.TrimSpace(report.Version), ".", 2)[0] != "2" {
		return nil, fmt.Errorf("unsupported llvm-cov export format")
	}

	accumulators := make(map[string]map[int]*lineAccumulator)
	paths := make(map[string]string)
	for _, data := range report.Data {
		for _, file := range data.Files {
			filename := runtimeReportPath(s, workspaceID, file.Filename, "")
			if !isCSource(filename) || !withinAny(target.sourceRoots, filename) {
				continue
			}
			for index, encoded := range file.Segments {
				var segment []json.RawMessage
				if err := json.Unmarshal(encoded, &segment); err != nil || len(segment) < 6 {
					return nil, fmt.Errorf("llvm-cov contains a malformed source segment")
				}
				startLine, err1 := uintFromRaw(segment[0])
				count, err2 := uintFromRaw(segment[2])
				hasCount, err3 := boolFromRaw(segment[3])
				gap, err4 := boolFromRaw(segment[5])
				if err1 != nil || err2 != nil || err3 != nil || err4 != nil || startLine < 1 {
					return nil, fmt.Errorf("llvm-cov contains a malformed source segment")
				}
				if !hasCount || gap {
					continue
				}
				endLine := startLine
				if index+1 < len(file.Segments) {
					var next []json.RawMessage
					if err := json.Unmarshal(file.Segments[index+1], &next); err != nil || len(next) < 2 {
						return nil, fmt.Errorf("llvm-cov contains a malformed source segment")
					}
					nextLine, lineErr := uintFromRaw(next[0])
					nextColumn, columnErr := uintFromRaw(next[1])
					if lineErr != nil || columnErr != nil || nextLine < startLine {
						return nil, fmt.Errorf("llvm-cov contains a malformed source segment")
					}
					endLine = nextLine
					if endLine > startLine && nextColumn <= 1 {
						endLine--
					}
				}
				for line := startLine; line <= endLine; line++ {
					accumulateCoverage(accumulators, paths, filename, int(line), count, false)
				}
			}
			for _, encoded := range file.Branches {
				var branch []json.RawMessage
				if err := json.Unmarshal(encoded, &branch); err != nil || len(branch) < 6 {
					return nil, fmt.Errorf("llvm-cov contains a malformed branch region")
				}
				line, err1 := uintFromRaw(branch[0])
				trueCount, err2 := uintFromRaw(branch[4])
				falseCount, err3 := uintFromRaw(branch[5])
				if err1 != nil || err2 != nil || err3 != nil || line < 1 {
					return nil, fmt.Errorf("llvm-cov contains a malformed branch region")
				}
				if (trueCount == 0) != (falseCount == 0) {
					accumulateCoverage(accumulators, paths, filename, int(line), addExecutionCounts(trueCount, falseCount), true)
				}
			}
		}
	}
	return finalizeProviderCoverage(s, workspaceID, target, accumulators, paths)
}

func accumulateCoverage(accumulators map[string]map[int]*lineAccumulator, paths map[string]string, filename string, line int, count uint64, partial bool) {
	key := coveragePathKey(filename)
	paths[key] = filename
	byLine := accumulators[key]
	if byLine == nil {
		byLine = make(map[int]*lineAccumulator)
		accumulators[key] = byLine
	}
	value := byLine[line]
	if value == nil {
		value = &lineAccumulator{}
		byLine[line] = value
	}
	if count > value.count {
		value.count = count
	}
	if count > 0 {
		value.covered = true
	} else {
		value.uncovered = true
	}
	value.partial = value.partial || partial
}
