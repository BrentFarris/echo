package plugins

import (
	"slices"
	"strings"

	"github.com/brent/echo/internal/domain"
)

// Enforced both when advertising a tool and on each guest-to-host call. A
// backend cannot bypass the standalone boundary by calling another capability.
func StandaloneMethodReason(method string) string {
	if strings.HasPrefix(method, "model.") || method == "chat.start" {
		return "Requires a model run"
	}
	if strings.HasPrefix(method, "comfyui.") {
		return "Requires saved chat artifacts"
	}
	if method == "plugin.open" || method == "plugin.install" {
		return "Requires chat app events; use an app command or plugin settings"
	}
	return ""
}

func StandaloneToolReason(manifest domain.PluginManifest, tool domain.Tool) string {
	if slices.Contains(manifest.Providers, tool.Name) {
		return "Model providers require a model run"
	}
	return StandaloneMethodReason(tool.Capability)
}
