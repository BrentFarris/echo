package plugins

import (
	"encoding/json"
	"fmt"

	echotools "github.com/brent/echo/internal/tools"
)

// BindTools connects the plugin manager to Echo's server-owned registry. Core
// tools are already present and immutable; only registrations owned by a
// plugin ID are created here.
func (m *Manager) BindTools(registry *echotools.Registry) error {
	if registry == nil {
		return fmt.Errorf("tool registry is required")
	}
	m.toolMu.Lock()
	for _, disposers := range m.toolDisposers {
		for _, dispose := range disposers {
			dispose()
		}
	}
	m.toolDisposers = map[string][]func(){}
	m.toolRegistry = registry
	m.toolMu.Unlock()
	registry.SetPluginPolicy(m.IsToolAllowed)
	return m.reconcileTools()
}

func (m *Manager) reconcileTools() error {
	m.toolMu.Lock()
	defer m.toolMu.Unlock()
	if m.toolRegistry == nil {
		return nil
	}
	state, err := m.store.load()
	if err != nil {
		return err
	}
	for _, disposers := range m.toolDisposers {
		for _, dispose := range disposers {
			dispose()
		}
	}
	m.toolDisposers = map[string][]func(){}
	for pluginID, installed := range state.Plugins {
		if !m.enabledAnywhere(installed) {
			continue
		}
		for _, contribution := range installed.Manifest.Contributes.Tools {
			pluginID := pluginID
			contribution := contribution
			dispose, registerErr := m.toolRegistry.RegisterOwned(pluginID, echotools.ToolFunc{
				Meta: echotools.Metadata{
					Name: contribution.Name, Description: contribution.Description,
					Parameters: echotools.Schema(contribution.InputSchema),
				},
				Run: func(ctx echotools.ExecutionContext, arguments json.RawMessage) (any, error) {
					if ctx.UsesSandbox() {
						return nil, echotools.SafeError{Code: "host_tool_blocked", Message: "native plugin tools are unavailable in sandbox-enabled workspaces"}
					}
					roots := make([]map[string]string, 0, len(ctx.WorkspaceRoots))
					for _, root := range ctx.WorkspaceRoots {
						roots = append(roots, map[string]string{"id": root.ID, "label": root.Label, "path": root.Path})
					}
					workspace := map[string]any{"id": ctx.WorkspaceID, "roots": roots}
					return m.InvokeTool(ctx.Context, pluginID, contribution.Name, ctx.WorkspaceID, arguments, workspace)
				},
			})
			if registerErr != nil {
				for _, registered := range m.toolDisposers {
					for _, rollback := range registered {
						rollback()
					}
				}
				m.toolDisposers = map[string][]func(){}
				return fmt.Errorf("register plugin tool %s: %w", contribution.Name, registerErr)
			}
			m.toolDisposers[pluginID] = append(m.toolDisposers[pluginID], dispose)
		}
	}
	return nil
}
