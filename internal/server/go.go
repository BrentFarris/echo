package server

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/gotool"
)

func (s *Server) goConfig(m domain.PluginManifest, updates map[string]any, owner bool) (map[string]any, error) {
	if value, ok := updates["goplsExecutable"]; ok && !owner && value != m.Config["goplsExecutable"] {
		return nil, errors.New("change gopls executable grants directly in Go settings")
	}
	// Go uses the same owner-only runtime/network grants as native SCM clients.
	config, err := s.sourceControlConfig(m, updates, owner)
	if err != nil {
		return nil, err
	}
	if err = gotool.ValidateProxy(text(config, "proxy")); err != nil {
		return nil, err
	}
	if path := text(config, "goplsExecutable"); path != "" {
		if !filepath.IsAbs(path) || runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(path), ".exe") || !s.Files.Allowed(domain.Bot{}, path) {
			return nil, errors.New("gopls must be an absolute executable outside Echo private state")
		}
	}
	return config, nil
}

func (s *Server) runGo(ctx context.Context, scope domain.ExecutionScope, m domain.PluginManifest, args map[string]any) (any, error) {
	if !m.Builtin || m.ID != "go" || scope.PluginID != "go" {
		return nil, errors.New("Go capability belongs to its built-in plugin")
	}
	request, err := gotool.Prepare(ctx, s.Files, scope.Bot, gotool.Settings{
		Executable: text(m.Config, "executable"), Toolchains: stringsValue(m.Config["toolchains"]),
		Internet: boolean(m.Config, "internet"), PrivateNetwork: boolean(m.Config, "privateNetwork"),
		Proxy: text(m.Config, "proxy"), Private: text(m.Config, "private"),
	}, gotool.Command{Command: text(args, "command"), Args: stringsValue(args["args"]), Directory: text(args, "directory"), Input: text(args, "input"), TimeoutSeconds: integer(args, "timeoutSeconds")})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(request.TimeoutSeconds)*time.Second)
	defer cancel()
	return s.runTrackedProcess(ctx, scope.Bot, request, func(output string) { s.nativeOutput(ctx, scope, output) })
}
