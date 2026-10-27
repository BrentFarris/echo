package plugins

import (
	_ "embed"
	"fmt"
	"github.com/brent/echo/internal/domain"
	"strings"
)

//go:embed templates/backend.go.txt
var BackendTemplate string

//go:embed templates/ui.html
var UITemplate string

//go:embed templates/renderer.html
var RendererTemplate string

var ProviderTemplate = strings.Replace(BackendTemplate, `json.NewEncoder(os.Stdout).Encode(map[string]any{"result":input.Params})`, `os.Stdout.Write(call("model.complete", input.Params))`, 1)

func Template(kind string) (any, error) {
	manifest := domain.PluginManifest{API: 1, ID: "my-plugin", Name: "My plugin", Version: "1.0.0", Scope: "bot", Settings: Schema(map[string]any{}), Config: map[string]any{}}
	switch kind {
	case "ui":
		manifest.Views = []domain.View{{ID: "main", Title: "My plugin", Kind: "floating", HTML: UITemplate}}
		manifest.App = &domain.AppRegistration{ViewID: "main"}
		return map[string]any{"manifest": manifest, "html": UITemplate, "instructions": "UI packages need no compiler. Install with plugin_install. The optional app registration exposes one floating view in Apps under the plugin's name. Set app.viewId to that view's id; app.icon may be an SVG, PNG, JPEG, or WebP image data URL up to 256 KiB. Full manifests without app are not listed in Apps. Clicking the app opens or focuses its window; startup uses the existing echo-plugin-ready/init handshake. Users can dock enabled apps for quick access. Views use sandboxed opaque-origin frames. Echo owns drag/resize. The optional bridge requires the delivered nonce, pluginId and viewId. Supported methods are window.close and declared storage.get/set/delete. Network, parent DOM, cookies and other plugin state are unavailable."}, nil
	case "renderer":
		manifest.Renderers = []string{"main"}
		manifest.Views = []domain.View{{ID: "main", Title: "My renderer", Kind: "renderer", HTML: RendererTemplate}}
		return map[string]any{"manifest": manifest, "instructions": "Install this manifest without the html argument. An enabled custom renderer replaces Markdown for its enabled bots; first plugin id alphabetically wins. Send renderer-ready, accept init with nonce/theme, then render with nonce/id/text. Return rendered with the same nonce/id and html. Echo sanitizes every response. No host bridge or ambient privileges are provided."}, nil
	case "provider":
		manifest.ID = "my-provider"
		manifest.Name = "My model provider"
		manifest.Capabilities = []string{"model.complete"}
		manifest.Providers = []string{"my_provider_complete"}
		manifest.Tools = []domain.Tool{{Name: "my_provider_complete", Description: "Complete a model request", Parameters: Schema(map[string]any{"messages": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}, "tools": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}, "generation": map[string]any{"type": "object"}}, "messages", "generation")}}
		return map[string]any{"manifest": manifest, "backendSource": ProviderTemplate, "instructions": "A provider is a WASI tool named in providers; it is hidden from model tools and selected in connection settings. Input contains messages, tools, generation. Return {result:{content,reasoning,toolCalls,finishReason}}. model.complete delegates to the compatible streaming client. For a different protocol declare model.request and model.emit: request accepts endpoint-relative path, GET/POST method, JSON body, headers, and authHeader (Authorization, x-api-key, api-key); returns status/contentType/body. Echo injects the selected endpoint credential, enforces its network policy, and never passes the secret to WASI. model.emit accepts content/reasoning deltas. No provider can select another connection or credential. Output is bounded; retain full final text in the result. Owner approves privileged capabilities before activation."}, nil
	case "backend":
		manifest.Tools = []domain.Tool{{Name: "my_plugin_echo", Description: "Return supplied text", Parameters: Schema(map[string]any{"text": S("Text")}, "text")}}
		return map[string]any{"manifest": manifest, "backendSource": BackendTemplate, "instructions": "Rename manifest id and tool prefix together. Go source is compiled to WASI with no dependencies or install scripts. No filesystem, environment, process, network or clocks are granted implicitly. Use call(method, params) for declared file.read/write/list/search/delete/move/mkdir, web.get, process.run, or storage.get/set/delete. Broker checks bot scope; new capabilities await direct owner approval. Return exactly one JSON result object on stdout. Memory is 64 MiB, output 2 MiB, deadline 5 minutes; cancellation terminates execution."}, nil
	}
	return nil, fmt.Errorf("template kind must be ui, backend, provider, or renderer")
}
