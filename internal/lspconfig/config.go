// Package lspconfig owns the persisted, editor-agnostic configuration for
// language servers. Runtime process and protocol concerns live in internal/lsp.
package lspconfig

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	DefaultFormatOnSaveTimeoutMS = 3000
	MinFormatOnSaveTimeoutMS     = 250
	MaxFormatOnSaveTimeoutMS     = 30000
)

var profileIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type DocumentSelector struct {
	LanguageID string   `json:"languageId"`
	Extensions []string `json:"extensions,omitempty"`
	Filenames  []string `json:"filenames,omitempty"`
}

type Profile struct {
	ID                    string             `json:"id"`
	Name                  string             `json:"name"`
	Command               string             `json:"command"`
	Args                  []string           `json:"args,omitempty"`
	Selectors             []DocumentSelector `json:"selectors"`
	Environment           map[string]string  `json:"environment,omitempty"`
	InitializationOptions map[string]any     `json:"initializationOptions,omitempty"`
	Settings              map[string]any     `json:"settings,omitempty"`
}

// ProfileOverride uses pointers so an explicitly empty array or object can
// replace a global value instead of being mistaken for an omitted field.
type ProfileOverride struct {
	Name                  *string             `json:"name,omitempty"`
	Command               *string             `json:"command,omitempty"`
	Args                  *[]string           `json:"args,omitempty"`
	Selectors             *[]DocumentSelector `json:"selectors,omitempty"`
	Environment           *map[string]string  `json:"environment,omitempty"`
	InitializationOptions *map[string]any     `json:"initializationOptions,omitempty"`
	Settings              *map[string]any     `json:"settings,omitempty"`
}

type WorkspaceConfig struct {
	EnabledProfileIDs   []string                   `json:"enabledProfileIds,omitempty"`
	Overrides           map[string]ProfileOverride `json:"overrides,omitempty"`
	FormatOnSave        bool                       `json:"formatOnSave,omitempty"`
	FormatOnSaveTimeout int                        `json:"formatOnSaveTimeoutMs,omitempty"`
}

type Template struct {
	ID          string  `json:"id"`
	Description string  `json:"description"`
	Profile     Profile `json:"profile"`
}

func Templates() []Template {
	return []Template{
		{
			ID: "gopls", Description: "Official Go language server",
			Profile: Profile{ID: "gopls", Name: "gopls", Command: "gopls", Selectors: []DocumentSelector{
				{LanguageID: "go", Extensions: []string{".go"}},
			}},
		},
		{
			ID: "clangd", Description: "Clang language server for C and C++",
			Profile: Profile{ID: "clangd", Name: "clangd", Command: "clangd", Selectors: []DocumentSelector{
				{LanguageID: "cpp", Extensions: []string{".c", ".h", ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx"}},
			}},
		},
		{
			ID: "lua-language-server", Description: "LuaLS language server",
			Profile: Profile{ID: "lua-language-server", Name: "LuaLS", Command: "lua-language-server", Selectors: []DocumentSelector{
				{LanguageID: "lua", Extensions: []string{".lua"}},
			}},
		},
	}
}

func TemplateByID(id string) (Profile, bool) {
	for _, template := range Templates() {
		if template.ID == strings.TrimSpace(id) {
			return template.Profile.Clone(), true
		}
	}
	return Profile{}, false
}

func (p Profile) Clone() Profile {
	p.Args = append([]string(nil), p.Args...)
	p.Selectors = cloneSelectors(p.Selectors)
	p.Environment = cloneStringMap(p.Environment)
	p.InitializationOptions = cloneAnyMap(p.InitializationOptions)
	p.Settings = cloneAnyMap(p.Settings)
	return p
}

func (p Profile) Normalized() Profile {
	p = p.Clone()
	p.ID = strings.ToLower(strings.TrimSpace(p.ID))
	p.Name = strings.TrimSpace(p.Name)
	p.Command = strings.TrimSpace(p.Command)
	for index := range p.Selectors {
		selector := &p.Selectors[index]
		selector.LanguageID = strings.TrimSpace(selector.LanguageID)
		selector.Extensions = normalizeExtensions(selector.Extensions)
		selector.Filenames = normalizeStrings(selector.Filenames, false)
	}
	if len(p.Environment) == 0 {
		p.Environment = nil
	}
	if len(p.InitializationOptions) == 0 {
		p.InitializationOptions = nil
	}
	if len(p.Settings) == 0 {
		p.Settings = nil
	}
	return p
}

func (p Profile) Validate() error {
	p = p.Normalized()
	if !profileIDPattern.MatchString(p.ID) {
		return fmt.Errorf("language server id must match %s", profileIDPattern.String())
	}
	if p.Name == "" {
		return fmt.Errorf("language server %q requires a name", p.ID)
	}
	if p.Command == "" {
		return fmt.Errorf("language server %q requires a command", p.ID)
	}
	if strings.ContainsRune(p.Command, 0) {
		return fmt.Errorf("language server %q command contains an invalid character", p.ID)
	}
	for _, argument := range p.Args {
		if strings.ContainsRune(argument, 0) {
			return fmt.Errorf("language server %q argument contains an invalid character", p.ID)
		}
	}
	if len(p.Selectors) == 0 {
		return fmt.Errorf("language server %q requires at least one selector", p.ID)
	}
	languages := map[string]bool{}
	for _, selector := range p.Selectors {
		if selector.LanguageID == "" {
			return fmt.Errorf("language server %q has a selector without a language id", p.ID)
		}
		if languages[selector.LanguageID] {
			return fmt.Errorf("language server %q repeats language %q", p.ID, selector.LanguageID)
		}
		languages[selector.LanguageID] = true
		if len(selector.Extensions) == 0 && len(selector.Filenames) == 0 {
			return fmt.Errorf("language server %q selector %q requires an extension or filename", p.ID, selector.LanguageID)
		}
		for _, value := range append(append([]string(nil), selector.Extensions...), selector.Filenames...) {
			if strings.ContainsAny(value, "/\\\x00") {
				return fmt.Errorf("language server %q selector contains an invalid file match", p.ID)
			}
		}
	}
	for key := range p.Environment {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("language server %q has an invalid environment variable name", p.ID)
		}
	}
	return nil
}

func NormalizeProfiles(profiles []Profile) ([]Profile, error) {
	result := make([]Profile, len(profiles))
	ids := map[string]bool{}
	for index, profile := range profiles {
		profile = profile.Normalized()
		if err := profile.Validate(); err != nil {
			return nil, err
		}
		if ids[profile.ID] {
			return nil, fmt.Errorf("language server id %q is duplicated", profile.ID)
		}
		ids[profile.ID] = true
		result[index] = profile
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (c WorkspaceConfig) Normalized() WorkspaceConfig {
	c.EnabledProfileIDs = normalizeStrings(c.EnabledProfileIDs, true)
	if len(c.Overrides) == 0 {
		c.Overrides = nil
	}
	if c.FormatOnSaveTimeout == 0 {
		c.FormatOnSaveTimeout = DefaultFormatOnSaveTimeoutMS
	}
	return c
}

func (c WorkspaceConfig) Validate(profiles []Profile) error {
	normalizedProfiles, err := NormalizeProfiles(profiles)
	if err != nil {
		return err
	}
	c = c.Normalized()
	if c.FormatOnSaveTimeout < MinFormatOnSaveTimeoutMS || c.FormatOnSaveTimeout > MaxFormatOnSaveTimeoutMS {
		return fmt.Errorf("format-on-save timeout must be between %d and %d milliseconds", MinFormatOnSaveTimeoutMS, MaxFormatOnSaveTimeoutMS)
	}
	profileByID := make(map[string]Profile, len(normalizedProfiles))
	for _, profile := range normalizedProfiles {
		profileByID[profile.ID] = profile
	}
	seenIDs := map[string]bool{}
	languageOwner := map[string]string{}
	for _, id := range c.EnabledProfileIDs {
		if seenIDs[id] {
			return fmt.Errorf("language server %q is enabled more than once", id)
		}
		seenIDs[id] = true
		profile, ok := profileByID[id]
		if !ok {
			return fmt.Errorf("language server profile %q was not found", id)
		}
		profile = ApplyOverride(profile, c.Overrides[id])
		if err := profile.Validate(); err != nil {
			return err
		}
		for _, selector := range profile.Selectors {
			if previous := languageOwner[selector.LanguageID]; previous != "" {
				return fmt.Errorf("language %q is handled by both %q and %q", selector.LanguageID, previous, id)
			}
			languageOwner[selector.LanguageID] = id
		}
	}
	for id := range c.Overrides {
		if _, ok := profileByID[id]; !ok {
			return fmt.Errorf("language server override %q references a missing profile", id)
		}
	}
	return nil
}

func EffectiveProfiles(config WorkspaceConfig, profiles []Profile) ([]Profile, error) {
	config = config.Normalized()
	if err := config.Validate(profiles); err != nil {
		return nil, err
	}
	byID := make(map[string]Profile, len(profiles))
	for _, profile := range profiles {
		profile = profile.Normalized()
		byID[profile.ID] = profile
	}
	result := make([]Profile, 0, len(config.EnabledProfileIDs))
	for _, id := range config.EnabledProfileIDs {
		result = append(result, ApplyOverride(byID[id], config.Overrides[id]))
	}
	return result, nil
}

func ApplyOverride(profile Profile, override ProfileOverride) Profile {
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
	if override.Selectors != nil {
		profile.Selectors = cloneSelectors(*override.Selectors)
	}
	if override.Environment != nil {
		profile.Environment = cloneStringMap(*override.Environment)
	}
	if override.InitializationOptions != nil {
		profile.InitializationOptions = cloneAnyMap(*override.InitializationOptions)
	}
	if override.Settings != nil {
		profile.Settings = cloneAnyMap(*override.Settings)
	}
	return profile.Normalized()
}

func cloneSelectors(input []DocumentSelector) []DocumentSelector {
	result := make([]DocumentSelector, len(input))
	for index, selector := range input {
		result[index] = DocumentSelector{
			LanguageID: selector.LanguageID,
			Extensions: append([]string(nil), selector.Extensions...),
			Filenames:  append([]string(nil), selector.Filenames...),
		}
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

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	data, _ := json.Marshal(input)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

func normalizeExtensions(values []string) []string {
	for index := range values {
		values[index] = strings.ToLower(strings.TrimSpace(values[index]))
		if values[index] != "" && !strings.HasPrefix(values[index], ".") {
			values[index] = "." + values[index]
		}
	}
	return normalizeStrings(values, true)
}

func normalizeStrings(values []string, lower bool) []string {
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
