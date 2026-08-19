package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/brent/echo/internal/agentmodes"
	"github.com/brent/echo/internal/tools"
)

type agentModeToolProvider struct {
	manager       *agentmodes.Manager
	workspacePath string
}

func (p agentModeToolProvider) CreateAgentMode(ctx context.Context, request tools.AgentModeCreationRequest) (tools.AgentModeCreationResult, error) {
	if err := ctx.Err(); err != nil {
		return tools.AgentModeCreationResult{}, err
	}
	permissions := make(map[string]tools.ToolPermission)
	if request.Permissions != nil {
		for name, paths := range request.Permissions {
			name = strings.TrimSpace(name)
			if name != "" {
				permissions[name] = tools.ToolPermission{Name: name, Paths: append([]string(nil), paths...)}
			}
		}
	} else {
		for _, name := range request.ToolPermissions {
			name = strings.TrimSpace(name)
			if name != "" {
				permissions[name] = tools.ToolPermission{Name: name, Paths: append([]string(nil), request.PathPermissions...)}
			}
		}
	}
	if len(permissions) == 0 {
		permissions = nil
	}

	modes, err := p.manager.Create(p.workspacePath, agentmodes.Mode{
		Name:        request.Name,
		Prompt:      request.Prompt,
		Permissions: permissions,
	})
	if err != nil {
		return tools.AgentModeCreationResult{}, err
	}
	for i := len(modes) - 1; i >= 0; i-- {
		mode := modes[i]
		if !mode.BuiltIn && strings.EqualFold(mode.Name, request.Name) {
			return tools.AgentModeCreationResult{
				ID: mode.ID, Name: mode.Name, Prompt: mode.Prompt,
				Permissions: cloneAgentModeToolPermissions(mode.Permissions),
			}, nil
		}
	}
	return tools.AgentModeCreationResult{}, fmt.Errorf("agent mode creation did not return the created mode")
}

func cloneAgentModeToolPermissions(source map[string]tools.ToolPermission) map[string]tools.ToolPermission {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]tools.ToolPermission, len(source))
	for name, permission := range source {
		permission.Paths = append([]string(nil), permission.Paths...)
		result[name] = permission
	}
	return result
}
