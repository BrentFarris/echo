// Package gotestconfig owns portable, workspace-scoped testing settings. The
// historical package name is retained so existing internal imports stay small.
package gotestconfig

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultTimeout      = "30s"
	DefaultBuildTimeout = "5m"
	MaxFlags            = 128
	MaxEnvironment      = 256
	MaxCTargets         = 64
)

type WorkspaceConfig struct {
	Go GoConfig `json:"go,omitempty"`
	C  CConfig  `json:"c,omitempty"`
}

type CConfig struct {
	CodeLens    bool      `json:"codeLens"`
	Coverage    bool      `json:"coverage"`
	Targets     []CTarget `json:"targets,omitempty"`
	codeLensSet bool
	coverageSet bool
}

type CTarget struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Entry       CEntry            `json:"entry"`
	Build       *Command          `json:"build,omitempty"`
	Executable  string            `json:"executable"`
	Args        []string          `json:"args,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Timeout     string            `json:"timeout,omitempty"`
	SourceRoots []string          `json:"sourceRoots"`
	Coverage    CCoverage         `json:"coverage"`
}

type CEntry struct {
	File     string `json:"file"`
	Function string `json:"function"`
}

type Command struct {
	Command     string            `json:"command"`
	Args        []string          `json:"args,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Timeout     string            `json:"timeout,omitempty"`
}

type CCoverage struct {
	Provider    string   `json:"provider"`
	ObjectRoots []string `json:"objectRoots,omitempty"`
	Objects     []string `json:"objects,omitempty"`
}

type GoConfig struct {
	CodeLens    bool              `json:"codeLens"`
	Coverage    bool              `json:"coverage"`
	Timeout     string            `json:"timeout"`
	Flags       []string          `json:"flags,omitempty"`
	Tags        string            `json:"tags,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	codeLensSet bool
	coverageSet bool
}

func DefaultGoConfig() GoConfig {
	return GoConfig{CodeLens: true, Coverage: true, Timeout: DefaultTimeout, codeLensSet: true, coverageSet: true}
}

func DefaultCConfig() CConfig {
	return CConfig{CodeLens: true, Coverage: true, codeLensSet: true, coverageSet: true}
}

func (config *CConfig) UnmarshalJSON(data []byte) error {
	var wire struct {
		CodeLens *bool     `json:"codeLens"`
		Coverage *bool     `json:"coverage"`
		Targets  []CTarget `json:"targets"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*config = DefaultCConfig()
	if wire.CodeLens != nil {
		config.CodeLens, config.codeLensSet = *wire.CodeLens, true
	}
	if wire.Coverage != nil {
		config.Coverage, config.coverageSet = *wire.Coverage, true
	}
	config.Targets = wire.Targets
	return nil
}

func (config *GoConfig) UnmarshalJSON(data []byte) error {
	var wire struct {
		CodeLens    *bool             `json:"codeLens"`
		Coverage    *bool             `json:"coverage"`
		Timeout     string            `json:"timeout"`
		Flags       []string          `json:"flags"`
		Tags        string            `json:"tags"`
		Environment map[string]string `json:"environment"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*config = DefaultGoConfig()
	if wire.CodeLens != nil {
		config.CodeLens = *wire.CodeLens
		config.codeLensSet = true
	}
	if wire.Coverage != nil {
		config.Coverage = *wire.Coverage
		config.coverageSet = true
	}
	config.Timeout = wire.Timeout
	config.Flags = wire.Flags
	config.Tags = wire.Tags
	config.Environment = wire.Environment
	return nil
}

func (config GoConfig) Normalized() GoConfig {
	if !config.codeLensSet && config.Timeout == "" && len(config.Flags) == 0 && config.Tags == "" && len(config.Environment) == 0 {
		config.CodeLens = true
	}
	if !config.coverageSet {
		config.Coverage = true
	}
	config.codeLensSet = true
	config.coverageSet = true
	config.Timeout = strings.TrimSpace(config.Timeout)
	if config.Timeout == "" {
		config.Timeout = DefaultTimeout
	}
	config.Tags = strings.TrimSpace(config.Tags)
	config.Flags = append([]string(nil), config.Flags...)
	if len(config.Flags) == 0 {
		config.Flags = nil
	}
	if len(config.Environment) == 0 {
		config.Environment = nil
	} else {
		environment := make(map[string]string, len(config.Environment))
		for key, value := range config.Environment {
			environment[key] = value
		}
		config.Environment = environment
	}
	return config
}

func (config WorkspaceConfig) Normalized() WorkspaceConfig {
	config.Go = config.Go.Normalized()
	config.C = config.C.Normalized()
	return config
}

func (config CConfig) Normalized() CConfig {
	if !config.codeLensSet {
		config.CodeLens = true
	}
	if !config.coverageSet {
		config.Coverage = true
	}
	config.codeLensSet, config.coverageSet = true, true
	config.Targets = append([]CTarget(nil), config.Targets...)
	for index := range config.Targets {
		target := &config.Targets[index]
		target.ID = strings.ToLower(strings.TrimSpace(target.ID))
		target.Name = strings.TrimSpace(target.Name)
		target.Entry.File = strings.TrimSpace(target.Entry.File)
		target.Entry.Function = strings.TrimSpace(target.Entry.Function)
		target.Executable = strings.TrimSpace(target.Executable)
		target.Cwd = strings.TrimSpace(target.Cwd)
		if target.Cwd == "" {
			target.Cwd = "${workspaceFolder}"
		}
		target.Timeout = strings.TrimSpace(target.Timeout)
		if target.Timeout == "" {
			target.Timeout = DefaultTimeout
		}
		target.Args = append([]string(nil), target.Args...)
		target.SourceRoots = trimStrings(target.SourceRoots)
		target.Environment = cloneMap(target.Environment)
		target.Coverage.Provider = strings.ToLower(strings.TrimSpace(target.Coverage.Provider))
		target.Coverage.ObjectRoots = trimStrings(target.Coverage.ObjectRoots)
		target.Coverage.Objects = trimStrings(target.Coverage.Objects)
		if target.Build != nil {
			build := *target.Build
			build.Command = strings.TrimSpace(build.Command)
			build.Cwd = strings.TrimSpace(build.Cwd)
			if build.Cwd == "" {
				build.Cwd = "${workspaceFolder}"
			}
			build.Timeout = strings.TrimSpace(build.Timeout)
			if build.Timeout == "" {
				build.Timeout = DefaultBuildTimeout
			}
			build.Args = append([]string(nil), build.Args...)
			build.Environment = cloneMap(build.Environment)
			target.Build = &build
		}
	}
	return config
}

func (config GoConfig) Validate() error {
	config = config.Normalized()
	duration, err := time.ParseDuration(config.Timeout)
	if err != nil || duration < 0 {
		return fmt.Errorf("timeout must be a non-negative Go duration")
	}
	if len(config.Flags) > MaxFlags {
		return fmt.Errorf("flags may contain at most %d values", MaxFlags)
	}
	for _, flag := range config.Flags {
		if strings.ContainsRune(flag, 0) {
			return fmt.Errorf("flags contain an invalid character")
		}
	}
	if strings.ContainsRune(config.Tags, 0) {
		return fmt.Errorf("tags contain an invalid character")
	}
	if len(config.Environment) > MaxEnvironment {
		return fmt.Errorf("environment may contain at most %d values", MaxEnvironment)
	}
	for key, value := range config.Environment {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("environment variable name %q is invalid", key)
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("environment variable %q contains an invalid character", key)
		}
	}
	return nil
}

func (config WorkspaceConfig) Validate() error {
	if err := config.Go.Validate(); err != nil {
		return err
	}
	return config.C.Validate()
}

func (config CConfig) Validate() error {
	config = config.Normalized()
	if len(config.Targets) > MaxCTargets {
		return fmt.Errorf("C testing may contain at most %d targets", MaxCTargets)
	}
	seen := map[string]bool{}
	for _, target := range config.Targets {
		if !validID(target.ID) {
			return fmt.Errorf("C test target id %q is invalid", target.ID)
		}
		if seen[target.ID] {
			return fmt.Errorf("C test target id %q is duplicated", target.ID)
		}
		seen[target.ID] = true
		if target.Name == "" || target.Entry.File == "" || !validIdentifier(target.Entry.Function) || target.Executable == "" {
			return fmt.Errorf("C test target %q requires a name, entry file, entry function, and executable", target.ID)
		}
		if len(target.SourceRoots) == 0 {
			return fmt.Errorf("C test target %q requires at least one source root", target.ID)
		}
		if target.Coverage.Provider != "gcov" && target.Coverage.Provider != "llvm" {
			return fmt.Errorf("C test target %q coverage provider must be gcov or llvm", target.ID)
		}
		if target.Coverage.Provider == "gcov" && len(target.Coverage.ObjectRoots) == 0 {
			return fmt.Errorf("C test target %q requires at least one gcov object root", target.ID)
		}
		if err := validateDuration(target.Timeout, "C test timeout"); err != nil {
			return err
		}
		if err := validateValues(target.Args, target.Environment); err != nil {
			return fmt.Errorf("C test target %q: %w", target.ID, err)
		}
		if target.Build != nil {
			if target.Build.Command == "" {
				return fmt.Errorf("C test target %q build command is required", target.ID)
			}
			if err := validateDuration(target.Build.Timeout, "C build timeout"); err != nil {
				return err
			}
			if err := validateValues(target.Build.Args, target.Build.Environment); err != nil {
				return fmt.Errorf("C test target %q build: %w", target.ID, err)
			}
		}
		values := append([]string{target.Entry.File, target.Executable, target.Cwd}, target.SourceRoots...)
		values = append(values, target.Coverage.ObjectRoots...)
		values = append(values, target.Coverage.Objects...)
		values = append(values, target.Args...)
		for _, value := range target.Environment {
			values = append(values, value)
		}
		if target.Build != nil {
			values = append(values, target.Build.Command, target.Build.Cwd)
			values = append(values, target.Build.Args...)
			for _, value := range target.Build.Environment {
				values = append(values, value)
			}
		}
		for _, value := range values {
			if usesDynamicVariable(value) {
				return fmt.Errorf("C test target %q uses a dynamic variable that is not supported", target.ID)
			}
		}
	}
	return nil
}

func usesDynamicVariable(value string) bool {
	for _, token := range []string{
		"${file}", "${fileDirname}", "${fileBasename}", "${fileBasenameNoExtension}", "${fileExtname}", "${relativeFile}",
		"${selectedText}", "${debugAdapterPort}", "${input:", "${command:",
	} {
		if strings.Contains(value, token) {
			return true
		}
	}
	return false
}

func validateDuration(value, label string) error {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || duration < 0 {
		return fmt.Errorf("%s must be a non-negative Go duration", label)
	}
	return nil
}

func validateValues(args []string, environment map[string]string) error {
	if len(args) > MaxFlags {
		return fmt.Errorf("arguments may contain at most %d values", MaxFlags)
	}
	for _, value := range args {
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("arguments contain an invalid character")
		}
	}
	if len(environment) > MaxEnvironment {
		return fmt.Errorf("environment may contain at most %d values", MaxEnvironment)
	}
	for key, value := range environment {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return fmt.Errorf("environment contains an invalid name or value")
		}
	}
	return nil
}

func validID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9' && index > 0) || (index > 0 && strings.ContainsRune("._-", character)) {
			continue
		}
		return false
	}
	return true
}

func validIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if character == '_' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func trimStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func cloneMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
