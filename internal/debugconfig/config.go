// Package debugconfig reads VS Code configuration. Launch settings are never
// persisted in Echo; only debugger UI state lives in the records store.
package debugconfig

import (
	"fmt"
	"strings"
)

type Configuration struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	AdapterProfileID string         `json:"adapterProfileId"` // Host-owned provider ID.
	Request          string         `json:"request"`
	Arguments        map[string]any `json:"arguments,omitempty"`
	RootID           string         `json:"rootId"`
	Folder           string         `json:"folder"`
	Settings         map[string]any `json:"-"`
	Error            string         `json:"error,omitempty"`
}
type Compound struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	ConfigurationIDs []string `json:"configurationIds"`
	StopAll          bool     `json:"stopAll"`
	Error            string   `json:"error,omitempty"`
}
type Input struct {
	PluginID    string   `json:"pluginId,omitempty"`
	Filter      string   `json:"filter,omitempty"`
	GoOnly      bool     `json:"goOnly,omitempty"`
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
}
type WorkspaceConfig struct {
	Version        int             `json:"version"`
	Configurations []Configuration `json:"configurations"`
	Compounds      []Compound      `json:"compounds"`
	Inputs         []Input         `json:"inputs"`
	Diagnostics    []string        `json:"diagnostics"`
}

// AdapterProfile is discovery metadata supplied exclusively by a built-in
// provider, not an executable definition accepted from an API or launch file.
type AdapterProfile struct {
	PluginID  string     `json:"pluginId"`
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	AdapterID string     `json:"adapterId"`
	Selectors []Selector `json:"selectors"`
}
type Selector struct {
	LanguageID string   `json:"languageId,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
}

func (c WorkspaceConfig) Configuration(id string) (Configuration, bool) {
	for _, v := range c.Configurations {
		if v.ID == id {
			return v, true
		}
	}
	return Configuration{}, false
}
func (c WorkspaceConfig) Compound(id string) (Compound, bool) {
	for _, v := range c.Compounds {
		if v.ID == id {
			return v, true
		}
	}
	return Compound{}, false
}
func (c WorkspaceConfig) Normalized() WorkspaceConfig {
	c.Version = 1
	if c.Configurations == nil {
		c.Configurations = []Configuration{}
	}
	if c.Compounds == nil {
		c.Compounds = []Compound{}
	}
	if c.Inputs == nil {
		c.Inputs = []Input{}
	}
	if c.Diagnostics == nil {
		c.Diagnostics = []string{}
	}
	return c
}
func ValidateConfiguration(c Configuration) error {
	if c.Error != "" {
		return fmt.Errorf("%s: %s", c.Name, c.Error)
	}
	if c.Name == "" || c.AdapterProfileID == "" {
		return fmt.Errorf("launch configuration requires name and type")
	}
	if c.Request != "launch" && c.Request != "attach" {
		return fmt.Errorf("%s: request must be launch or attach", c.Name)
	}
	for _, key := range []string{"preLaunchTask", "postDebugTask", "debugServer", "serverReadyAction"} {
		if value, ok := c.Arguments[key]; ok && value != nil && value != "" {
			return fmt.Errorf("%s: %s is not supported by Echo", c.Name, key)
		}
	}
	if console, _ := c.Arguments["console"].(string); console == "externalTerminal" {
		return fmt.Errorf("external terminals are unsupported; use integratedTerminal")
	}
	return nil
}
func stringValue(value any) string { s, _ := value.(string); return strings.TrimSpace(s) }
