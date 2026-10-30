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
		return map[string]any{"manifest": manifest, "html": UITemplate, "instructions": "UI packages need no compiler. Install with plugin_install. The optional app registration exposes one floating or embedded view in Apps under the plugin's name. Set app.viewId to that view's id; app.icon may be an SVG, PNG, JPEG, or WebP image data URL up to 256 KiB. Full manifests without app are not listed in Apps. Use view.kind=embedded to initially fill Echo's main area; omitted kind defaults to floating. Users can switch any app between embedded and floating without resetting it. Clicking the app opens or focuses its live view; startup uses the existing echo-plugin-ready/init handshake. Users can dock enabled apps for quick access. Views use sandboxed opaque-origin frames. Echo owns drag/resize. The optional bridge requires the delivered nonce, pluginId and viewId. Supported methods are window.close, declared storage.get/set/delete, web.get, web.request and websocket.open/send/receive/close. For networking declare network.origins as exact http(s) or ws(s) origins (scheme, hostname and port, no paths or wildcards), plus each broker method in capabilities. Origins require owner approval, including additions on updates. HTTP(S) grants also allow remote scripts, styles, images, fonts and media. API requests and WebSockets run on the plugin host; assets load in the browser. Use the included echoCall and echoSocket helpers; direct fetch/WebSocket remains blocked in frames. web.request takes url, method, headers, body and encoding (text/base64), returning status, url, headers, contentType, content and encoding. WebSocket send/receive use data, encoding and binary; open returns handle/protocol. Messages are limited to 1 MiB. Parent DOM, host cookies and other plugin state remain unavailable."}, nil
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
		return map[string]any{"manifest": manifest, "backendSource": ProviderTemplate, "instructions": "A provider is a WASI tool named in providers; it is hidden from model tools and selected in connection settings. Input contains messages, tools, generation. Return {result:{content,reasoning,toolCalls,finishReason}}. Optional result usage:{inputTokens,outputTokens,totalTokens} reports this request's token counts. A context rejection may return overflow:{message,limitTokens,inputTokens,outputTokens}; omit unknown counts. Do not classify rate limits or generic payload-size errors as context overflow. model.complete delegates to the compatible streaming client and preserves usage and context rejection for host recovery. For a different protocol declare model.request and model.emit: request accepts endpoint-relative path, GET/POST method, JSON body, headers, and authHeader (Authorization, x-api-key, api-key); returns status/contentType/body. Echo injects the selected endpoint credential, enforces its network policy, and never passes the secret to WASI. model.emit accepts content/reasoning deltas. No provider can select another connection or credential. Output is bounded; retain full final text in the result. Owner approves privileged capabilities before activation."}, nil
	case "backend":
		manifest.Tools = []domain.Tool{{Name: "my_plugin_echo", Description: "Return supplied text", Parameters: Schema(map[string]any{"text": S("Text")}, "text")}}
		return map[string]any{"manifest": manifest, "backendSource": BackendTemplate, "instructions": "Rename manifest id and tool prefix together. Go source is compiled to WASI with no dependencies or install scripts. No filesystem, environment, process, network or clocks are granted implicitly. Use call(method, params) for declared file.read/write/list/search/delete/move/mkdir, web.get, web.request, websocket.open/send/receive/close, process.run, or storage.get/set/delete. Network operations require network.origins with exact HTTP(S)/WS(S) origins and owner approval. web.request accepts url/method/headers/body/encoding (text or base64); returns status/url/headers/contentType/content/encoding. WebSocket open accepts url/headers/protocols and returns handle/protocol; send accepts handle/data/encoding/binary, receive accepts handle and returns data/encoding/binary, close accepts handle. Handles last only for this invocation. HTTP and WebSocket payloads are capped at 1 MiB. Broker checks bot scope; new capabilities await direct owner approval. Return exactly one JSON result object on stdout. Memory is 64 MiB, output 2 MiB, deadline 5 minutes; cancellation terminates execution."}, nil
	}
	return nil, fmt.Errorf("template kind must be ui, backend, provider, or renderer")
}
