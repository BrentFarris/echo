// Package debugconfig owns the persisted, runtime-independent debugger
// configuration. DAP processes and sessions live in internal/debugger.
package debugconfig

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

const (
	CurrentVersion        = 1
	DefaultHookTimeoutMS  = 300000
	MinHookTimeoutMS      = 1000
	MaxHookTimeoutMS      = 30 * 60 * 1000
	DefaultStartupTimeout = 15000
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type Selector struct {
	LanguageID string   `json:"languageId,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
	Filenames  []string `json:"filenames,omitempty"`
}

type Transport struct {
	Kind             string `json:"kind"` // stdio, server, or connect
	Host             string `json:"host,omitempty"`
	Port             int    `json:"port,omitempty"`
	ReadyPattern     string `json:"readyPattern,omitempty"`
	StartupTimeoutMS int    `json:"startupTimeoutMs,omitempty"`
}

type AdapterProfile struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	AdapterID   string            `json:"adapterId"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Selectors   []Selector        `json:"selectors,omitempty"`
	Transport   Transport         `json:"transport"`
}

// AdapterOverride follows the LSP override model. Pointer fields distinguish
// an omitted value from an intentional empty replacement.
type AdapterOverride struct {
	Name        *string            `json:"name,omitempty"`
	Command     *string            `json:"command,omitempty"`
	Args        *[]string          `json:"args,omitempty"`
	Environment *map[string]string `json:"environment,omitempty"`
	Transport   *Transport         `json:"transport,omitempty"`
}

type LifecycleHook struct {
	Command     string            `json:"command"`
	Args        []string          `json:"args,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	TimeoutMS   int               `json:"timeoutMs,omitempty"`
}

type Configuration struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	AdapterProfileID string         `json:"adapterProfileId"`
	Request          string         `json:"request"`
	Arguments        map[string]any `json:"arguments,omitempty"`
	PreLaunch        *LifecycleHook `json:"preLaunch,omitempty"`
	PostDebug        *LifecycleHook `json:"postDebug,omitempty"`
}

type Compound struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	ConfigurationIDs []string `json:"configurationIds"`
	StopAll          bool     `json:"stopAll"`
}

func (c *Compound) UnmarshalJSON(data []byte) error {
	type alias Compound
	var wire struct {
		alias
		StopAll *bool `json:"stopAll"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*c = Compound(wire.alias)
	c.StopAll = wire.StopAll == nil || *wire.StopAll
	return nil
}

type Input struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"` // promptString, secret, pickString, pickProcess
	Description string   `json:"description,omitempty"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
}

type WorkspaceConfig struct {
	Version                  int                        `json:"version,omitempty"`
	EnabledAdapterProfileIDs []string                   `json:"enabledAdapterProfileIds,omitempty"`
	Overrides                map[string]AdapterOverride `json:"overrides,omitempty"`
	Configurations           []Configuration            `json:"configurations,omitempty"`
	Compounds                []Compound                 `json:"compounds,omitempty"`
	Inputs                   []Input                    `json:"inputs,omitempty"`
}

type Template struct {
	ID           string         `json:"id"`
	Description  string         `json:"description"`
	InstallGuide string         `json:"installGuide"`
	Profile      AdapterProfile `json:"profile"`
}

func Templates() []Template {
	return []Template{
		{ID: "delve", Description: "Go debugging with Delve", InstallGuide: "Install Delve and ensure dlv is available on PATH.", Profile: AdapterProfile{
			ID: "delve", Name: "Delve", AdapterID: "go", Command: "dlv",
			Args:      []string{"dap", "--listen=127.0.0.1:${debugAdapterPort}"},
			Selectors: []Selector{{LanguageID: "go", Extensions: []string{".go"}}},
			Transport: Transport{Kind: "server", Host: "127.0.0.1", StartupTimeoutMS: DefaultStartupTimeout},
		}},
		{ID: "debugpy", Description: "Python debugging with debugpy", InstallGuide: "Install debugpy for the selected Python interpreter.", Profile: AdapterProfile{
			ID: "debugpy", Name: "debugpy", AdapterID: "python", Command: "python",
			Args: []string{"-m", "debugpy.adapter"}, Selectors: []Selector{{LanguageID: "python", Extensions: []string{".py", ".pyw"}}},
			Transport: Transport{Kind: "stdio", StartupTimeoutMS: DefaultStartupTimeout},
		}},
		{ID: "js-debug", Description: "Node and browser debugging with Microsoft's standalone js-debug server", InstallGuide: "Download a standalone js-debug release and set JS_DEBUG_ADAPTER_PATH to dapDebugServer.js.", Profile: AdapterProfile{
			ID: "js-debug", Name: "js-debug", AdapterID: "pwa-node", Command: "node",
			Args:      []string{"${env:JS_DEBUG_ADAPTER_PATH}", "${debugAdapterPort}"},
			Selectors: []Selector{{LanguageID: "javascript", Extensions: []string{".js", ".mjs", ".cjs"}}, {LanguageID: "typescript", Extensions: []string{".ts", ".mts", ".cts"}}},
			Transport: Transport{Kind: "server", Host: "127.0.0.1", StartupTimeoutMS: DefaultStartupTimeout},
		}},
		{ID: "codelldb", Description: "C, C++, and Rust debugging with CodeLLDB", InstallGuide: "Install CodeLLDB and ensure codelldb is available on PATH.", Profile: AdapterProfile{
			ID: "codelldb", Name: "CodeLLDB", AdapterID: "lldb", Command: "codelldb",
			Args:      []string{"--port", "${debugAdapterPort}"},
			Selectors: []Selector{{LanguageID: "cpp", Extensions: []string{".c", ".cc", ".cpp", ".cxx", ".h", ".hpp"}}, {LanguageID: "rust", Extensions: []string{".rs"}}},
			Transport: Transport{Kind: "server", Host: "127.0.0.1", StartupTimeoutMS: DefaultStartupTimeout},
		}},
	}
}

func TemplateByID(id string) (AdapterProfile, bool) {
	for _, template := range Templates() {
		if template.ID == strings.TrimSpace(id) {
			return template.Profile.Clone(), true
		}
	}
	return AdapterProfile{}, false
}

func (p AdapterProfile) Clone() AdapterProfile {
	p.Args = append([]string(nil), p.Args...)
	p.Selectors = cloneSelectors(p.Selectors)
	p.Environment = cloneStringMap(p.Environment)
	return p
}

func (p AdapterProfile) Normalized() AdapterProfile {
	p = p.Clone()
	p.ID = strings.ToLower(strings.TrimSpace(p.ID))
	p.Name = strings.TrimSpace(p.Name)
	p.AdapterID = strings.TrimSpace(p.AdapterID)
	p.Command = strings.TrimSpace(p.Command)
	p.Transport.Kind = strings.ToLower(strings.TrimSpace(p.Transport.Kind))
	p.Transport.Host = strings.TrimSpace(p.Transport.Host)
	if (p.Transport.Kind == "server" || p.Transport.Kind == "connect") && p.Transport.Host == "" {
		p.Transport.Host = "127.0.0.1"
	}
	if p.Transport.StartupTimeoutMS == 0 {
		p.Transport.StartupTimeoutMS = DefaultStartupTimeout
	}
	for index := range p.Selectors {
		p.Selectors[index].LanguageID = strings.TrimSpace(p.Selectors[index].LanguageID)
		p.Selectors[index].Extensions = normalizedStrings(p.Selectors[index].Extensions, true)
		p.Selectors[index].Filenames = normalizedStrings(p.Selectors[index].Filenames, false)
	}
	if len(p.Environment) == 0 {
		p.Environment = nil
	}
	return p
}

func (p AdapterProfile) Validate() error {
	p = p.Normalized()
	if !idPattern.MatchString(p.ID) {
		return fmt.Errorf("debug adapter id must match %s", idPattern)
	}
	if p.Name == "" {
		return fmt.Errorf("debug adapter %q requires a name", p.ID)
	}
	if p.AdapterID == "" {
		return fmt.Errorf("debug adapter %q requires an adapterId", p.ID)
	}
	switch p.Transport.Kind {
	case "stdio", "server":
		if p.Command == "" {
			return fmt.Errorf("debug adapter %q requires a command", p.ID)
		}
	case "connect":
		if p.Transport.Host == "" || p.Transport.Port < 1 || p.Transport.Port > 65535 {
			return fmt.Errorf("debug adapter %q connect transport requires a valid host and port", p.ID)
		}
	default:
		return fmt.Errorf("debug adapter %q transport must be stdio, server, or connect", p.ID)
	}
	if p.Transport.Kind == "server" {
		if !loopbackHost(p.Transport.Host) {
			return fmt.Errorf("debug adapter %q spawned server host must be loopback", p.ID)
		}
		portTemplate := strings.Contains(p.Command, "${debugAdapterPort}")
		for _, value := range append(p.Args, mapValues(p.Environment)...) {
			portTemplate = portTemplate || strings.Contains(value, "${debugAdapterPort}")
		}
		if !portTemplate {
			return fmt.Errorf("debug adapter %q server transport must use ${debugAdapterPort}", p.ID)
		}
	}
	if pattern := p.Transport.ReadyPattern; pattern != "" {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("debug adapter %q has an invalid readyPattern: %w", p.ID, err)
		}
	}
	if p.Transport.StartupTimeoutMS < 1000 || p.Transport.StartupTimeoutMS > 120000 {
		return fmt.Errorf("debug adapter %q startup timeout must be between 1000 and 120000 milliseconds", p.ID)
	}
	for _, value := range append(append([]string{p.Command}, p.Args...), p.Transport.Host, p.Transport.ReadyPattern) {
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("debug adapter %q contains an invalid character", p.ID)
		}
	}
	for key := range p.Environment {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("debug adapter %q has an invalid environment variable name", p.ID)
		}
	}
	return nil
}

func NormalizeProfiles(profiles []AdapterProfile) ([]AdapterProfile, error) {
	result := make([]AdapterProfile, len(profiles))
	seen := map[string]bool{}
	for index, profile := range profiles {
		profile = profile.Normalized()
		if err := profile.Validate(); err != nil {
			return nil, err
		}
		if seen[profile.ID] {
			return nil, fmt.Errorf("debug adapter id %q is duplicated", profile.ID)
		}
		seen[profile.ID] = true
		result[index] = profile
	}
	sort.SliceStable(result, func(i, j int) bool { return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name) })
	return result, nil
}

func (c WorkspaceConfig) Normalized() WorkspaceConfig {
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
	c.EnabledAdapterProfileIDs = normalizedStrings(c.EnabledAdapterProfileIDs, true)
	if len(c.Overrides) == 0 {
		c.Overrides = nil
	} else {
		overrides := make(map[string]AdapterOverride, len(c.Overrides))
		for id, override := range c.Overrides {
			overrides[strings.ToLower(strings.TrimSpace(id))] = override
		}
		c.Overrides = overrides
	}
	for index := range c.Configurations {
		entry := &c.Configurations[index]
		entry.ID = strings.ToLower(strings.TrimSpace(entry.ID))
		entry.Name = strings.TrimSpace(entry.Name)
		entry.AdapterProfileID = strings.ToLower(strings.TrimSpace(entry.AdapterProfileID))
		entry.Request = strings.ToLower(strings.TrimSpace(entry.Request))
		normalizeHook(entry.PreLaunch)
		normalizeHook(entry.PostDebug)
	}
	for index := range c.Compounds {
		entry := &c.Compounds[index]
		entry.ID = strings.ToLower(strings.TrimSpace(entry.ID))
		entry.Name = strings.TrimSpace(entry.Name)
		entry.ConfigurationIDs = normalizedStrings(entry.ConfigurationIDs, true)
	}
	for index := range c.Inputs {
		entry := &c.Inputs[index]
		entry.ID = strings.ToLower(strings.TrimSpace(entry.ID))
		entry.Type = strings.TrimSpace(entry.Type)
		entry.Description = strings.TrimSpace(entry.Description)
		entry.Options = normalizedStrings(entry.Options, false)
	}
	return c
}

func (c WorkspaceConfig) ValidateStructure() error {
	return c.validateStructure(true)
}

func (c WorkspaceConfig) validateStructure(rejectCommandVariables bool) error {
	c = c.Normalized()
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported debug configuration version %d", c.Version)
	}
	configurationIDs := map[string]bool{}
	for _, entry := range c.Configurations {
		if !idPattern.MatchString(entry.ID) {
			return fmt.Errorf("debug configuration id %q is invalid", entry.ID)
		}
		if configurationIDs[entry.ID] {
			return fmt.Errorf("debug configuration id %q is duplicated", entry.ID)
		}
		configurationIDs[entry.ID] = true
		if entry.Name == "" || entry.AdapterProfileID == "" {
			return fmt.Errorf("debug configuration %q requires name and adapterProfileId", entry.ID)
		}
		if entry.Request != "launch" && entry.Request != "attach" {
			return fmt.Errorf("debug configuration %q request must be launch or attach", entry.ID)
		}
		if rejectCommandVariables && containsCommandVariable(entry.Arguments) {
			return fmt.Errorf("debug configuration %q uses unsupported ${command:...} variables", entry.ID)
		}
		if err := validateHook(entry.ID, entry.PreLaunch); err != nil {
			return err
		}
		if err := validateHook(entry.ID, entry.PostDebug); err != nil {
			return err
		}
	}
	compoundIDs := map[string]bool{}
	for _, compound := range c.Compounds {
		if !idPattern.MatchString(compound.ID) || compound.Name == "" {
			return fmt.Errorf("debug compound %q requires a valid id and name", compound.ID)
		}
		if compoundIDs[compound.ID] || configurationIDs[compound.ID] {
			return fmt.Errorf("debug launch id %q is duplicated", compound.ID)
		}
		compoundIDs[compound.ID] = true
		if len(compound.ConfigurationIDs) == 0 {
			return fmt.Errorf("debug compound %q requires at least one configuration", compound.ID)
		}
		for _, id := range compound.ConfigurationIDs {
			if !configurationIDs[id] {
				return fmt.Errorf("debug compound %q references missing configuration %q", compound.ID, id)
			}
		}
	}
	inputIDs := map[string]bool{}
	for _, input := range c.Inputs {
		if !idPattern.MatchString(input.ID) || inputIDs[input.ID] {
			return fmt.Errorf("debug input id %q is invalid or duplicated", input.ID)
		}
		inputIDs[input.ID] = true
		switch input.Type {
		case "promptString", "secret", "pickProcess":
		case "pickString":
			if len(input.Options) == 0 {
				return fmt.Errorf("debug input %q requires options", input.ID)
			}
		default:
			return fmt.Errorf("debug input %q has unsupported type %q", input.ID, input.Type)
		}
	}
	return nil
}

func (c WorkspaceConfig) Validate(profiles []AdapterProfile) error {
	if err := c.ValidateStructure(); err != nil {
		return err
	}
	profiles, err := NormalizeProfiles(profiles)
	if err != nil {
		return err
	}
	byID := map[string]AdapterProfile{}
	for _, profile := range profiles {
		byID[profile.ID] = profile
	}
	for _, id := range c.EnabledAdapterProfileIDs {
		if _, ok := byID[id]; !ok {
			return fmt.Errorf("debug adapter profile %q was not found", id)
		}
	}
	for _, entry := range c.Configurations {
		profile, ok := byID[entry.AdapterProfileID]
		if !ok {
			return fmt.Errorf("debug configuration %q references missing adapter profile %q", entry.ID, entry.AdapterProfileID)
		}
		if !contains(c.EnabledAdapterProfileIDs, entry.AdapterProfileID) {
			return fmt.Errorf("debug configuration %q references disabled adapter profile %q", entry.ID, entry.AdapterProfileID)
		}
		if override, ok := c.Overrides[entry.AdapterProfileID]; ok {
			profile = ApplyOverride(profile, override)
		}
		if err := profile.Validate(); err != nil {
			return err
		}
	}
	for id := range c.Overrides {
		if _, ok := byID[id]; !ok {
			return fmt.Errorf("debug adapter override %q references a missing profile", id)
		}
	}
	return nil
}

func ApplyOverride(profile AdapterProfile, override AdapterOverride) AdapterProfile {
	profile = profile.Clone()
	if override.Name != nil {
		profile.Name = *override.Name
	}
	if override.Command != nil {
		profile.Command = *override.Command
	}
	if override.Args != nil {
		profile.Args = append([]string(nil), (*override.Args)...)
	}
	if override.Environment != nil {
		profile.Environment = cloneStringMap(*override.Environment)
	}
	if override.Transport != nil {
		profile.Transport = *override.Transport
	}
	return profile.Normalized()
}

func (c WorkspaceConfig) Configuration(id string) (Configuration, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, entry := range c.Configurations {
		if entry.ID == id {
			return entry, true
		}
	}
	return Configuration{}, false
}

func (c WorkspaceConfig) Compound(id string) (Compound, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, entry := range c.Compounds {
		if entry.ID == id {
			return entry, true
		}
	}
	return Compound{}, false
}

func normalizeHook(hook *LifecycleHook) {
	if hook == nil {
		return
	}
	hook.Command = strings.TrimSpace(hook.Command)
	hook.Cwd = strings.TrimSpace(hook.Cwd)
	if hook.TimeoutMS == 0 {
		hook.TimeoutMS = DefaultHookTimeoutMS
	}
	if len(hook.Environment) == 0 {
		hook.Environment = nil
	}
}

func validateHook(configurationID string, hook *LifecycleHook) error {
	if hook == nil {
		return nil
	}
	if hook.Command == "" {
		return fmt.Errorf("debug configuration %q lifecycle hook requires a command", configurationID)
	}
	if hook.TimeoutMS < MinHookTimeoutMS || hook.TimeoutMS > MaxHookTimeoutMS {
		return fmt.Errorf("debug configuration %q lifecycle hook timeout must be between %d and %d milliseconds", configurationID, MinHookTimeoutMS, MaxHookTimeoutMS)
	}
	for _, value := range append(append([]string{hook.Command, hook.Cwd}, hook.Args...), mapValues(hook.Environment)...) {
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("debug configuration %q lifecycle hook contains an invalid character", configurationID)
		}
	}
	for key := range hook.Environment {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("debug configuration %q lifecycle hook has an invalid environment variable name", configurationID)
		}
	}
	return nil
}

func loopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func cloneSelectors(input []Selector) []Selector {
	result := make([]Selector, len(input))
	for index, selector := range input {
		result[index] = Selector{LanguageID: selector.LanguageID, Extensions: append([]string(nil), selector.Extensions...), Filenames: append([]string(nil), selector.Filenames...)}
	}
	return result
}
func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
func mapValues(input map[string]string) []string {
	result := make([]string, 0, len(input)*2)
	for key, value := range input {
		result = append(result, key, value)
	}
	return result
}
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func normalizedStrings(values []string, lower bool) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if lower {
			value = strings.ToLower(value)
		}
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
