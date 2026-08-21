package server

import (
	"context"
	"fmt"

	"github.com/brent/echo/internal/plugins"
	"github.com/brent/echo/internal/tools"
)

type pluginAuthoringProvider struct {
	manager     *plugins.Manager
	workspaceID string
	resolveNew  func(string) (string, error)
	resolve     func(string) (string, error)
}

func (p pluginAuthoringProvider) ScaffoldPlugin(ctx context.Context, request tools.PluginScaffoldRequest) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := p.resolveNew(request.Path)
	if err != nil {
		return nil, err
	}
	return plugins.Scaffold(path, plugins.ScaffoldOptions{Template: request.Template, ID: request.ID, Name: request.Name, Description: request.Description})
}

func (p pluginAuthoringProvider) ValidatePlugin(ctx context.Context, request tools.PluginPackageRequest) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := p.resolve(request.Path)
	if err != nil {
		return nil, err
	}
	return p.manager.ValidateLocal(path)
}

func (p pluginAuthoringProvider) PluginStatus(ctx context.Context) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.manager.Catalog(p.workspaceID)
}

func (p pluginAuthoringProvider) StagePlugin(ctx context.Context, request tools.PluginPackageRequest) (any, error) {
	path, err := p.resolve(request.Path)
	if err != nil {
		return nil, err
	}
	stage, err := p.manager.StageLocal(ctx, path)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"stage": stage, "approvalRequired": true,
		"message": "Plugin staged for owner review. This operation did not install, enable, approve, or execute it.",
	}, nil
}

func (s *Server) pluginAuthoring(workspaceID string, roots []tools.WorkspaceRoot) tools.PluginAuthoringProvider {
	if s.plugins == nil {
		return nil
	}
	return pluginAuthoringProvider{
		manager: s.plugins, workspaceID: workspaceID,
		resolveNew: s.toolPathResolver(workspaceID, roots, true),
		resolve:    s.toolPathResolver(workspaceID, roots, false),
	}
}

var _ tools.PluginAuthoringProvider = pluginAuthoringProvider{}

func unavailablePluginAuthoring() error { return fmt.Errorf("plugin authoring is unavailable") }
