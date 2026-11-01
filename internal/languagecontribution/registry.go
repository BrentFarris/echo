// Package languagecontribution describes trusted native language plugins.
// Only the host registers contributions; plugin manifests cannot register
// arbitrary executables with the owner's permissions.
package languagecontribution

import (
	"fmt"
	"github.com/brent/echo/internal/debugger"
)

type Contribution struct {
	PluginID        string                                        `json:"pluginId"`
	Languages       []string                                      `json:"languages"`
	DebugTypes      []string                                      `json:"debugTypes"`
	LanguageService string                                        `json:"languageService,omitempty"`
	Capability      string                                        `json:"capability,omitempty"`
	Debugger        func(botID string) (debugger.Provider, error) `json:"-"`
}

type Registry struct{ contributions []Contribution }

func New(contributions ...Contribution) *Registry {
	r := &Registry{}
	seen := map[string]bool{}
	types := map[string]bool{}
	for _, c := range contributions {
		if c.PluginID == "" || seen[c.PluginID] {
			panic("duplicate or empty language contribution")
		}
		seen[c.PluginID] = true
		for _, kind := range c.DebugTypes {
			if kind == "" || types[kind] {
				panic("duplicate or empty debugger contribution type")
			}
			types[kind] = true
		}
		r.contributions = append(r.contributions, c)
	}
	return r
}
func (r *Registry) Contributions() []Contribution {
	return append([]Contribution(nil), r.contributions...)
}
func (r *Registry) Debugger(botID, kind string) (debugger.Provider, error) {
	for _, c := range r.contributions {
		for _, t := range c.DebugTypes {
			if t == kind && c.Debugger != nil {
				return c.Debugger(botID)
			}
		}
	}
	return nil, fmt.Errorf("no enabled language plugin provides debugger type %q", kind)
}
