// Package gotestconfig owns portable, workspace-scoped Go testing settings.
package gotestconfig

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultTimeout = "30s"
	MaxFlags       = 128
	MaxEnvironment = 256
)

type WorkspaceConfig struct {
	Go GoConfig `json:"go,omitempty"`
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

func (config WorkspaceConfig) Validate() error { return config.Go.Validate() }
