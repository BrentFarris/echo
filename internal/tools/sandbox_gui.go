package tools

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	pathpkg "path"
	"strings"

	"github.com/brent/echo/internal/sandbox"
)

var sandboxGUIToolNames = map[string]bool{
	"browser_open": true, "browser_snapshot": true, "browser_click": true,
	"browser_type": true, "browser_select": true, "browser_press": true,
	"browser_scroll": true, "browser_tabs": true, "browser_wait": true,
	"browser_upload": true, "desktop_control": true,
}

func init() {
	registerBrowserTools()
	Register(ToolFunc{Meta: Metadata{
		Name:        "desktop_control",
		Description: "Inspect or control the visible Linux desktop. The user can watch and immediately preempt these actions with Take Control.",
		Parameters: Schema{"type": "object", "additionalProperties": false, "required": []any{"action"}, "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []any{"snapshot", "move", "click", "double_click", "drag", "scroll", "type", "key", "wait"}},
			"x":      map[string]any{"type": "integer"}, "y": map[string]any{"type": "integer"},
			"toX": map[string]any{"type": "integer"}, "toY": map[string]any{"type": "integer"},
			"button": map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
			"clicks": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			"deltaX": map[string]any{"type": "integer"}, "deltaY": map[string]any{"type": "integer"},
			"durationMs": map[string]any{"type": "integer", "minimum": 0, "maximum": 30000},
			"text":       map[string]any{"type": "string", "maxLength": 32768},
			"key":        map[string]any{"type": "string", "maxLength": 100},
		}},
	}, Run: desktopControl})
}

func registerBrowserTools() {
	definitions := []ToolFunc{
		{Meta: Metadata{Name: "browser_open", Description: "Open an absolute HTTP or HTTPS URL in the persistent visible sandbox Chromium browser.", Parameters: objectSchema([]any{"url"}, map[string]any{
			"url": stringProperty("Absolute HTTP or HTTPS URL."), "tabId": stringProperty("Optional target tab ID."),
			"waitUntil": enumProperty("commit", "domcontentloaded", "load", "networkidle"), "timeoutMs": timeoutProperty(),
		})}, Run: browserOpen},
		{Meta: Metadata{Name: "browser_snapshot", Description: "Return the active page accessibility snapshot, stable element references, tabs, URL/title, and optionally a screenshot.", Parameters: objectSchema(nil, map[string]any{
			"tabId": stringProperty("Optional target tab ID."), "screenshot": map[string]any{"type": "boolean"},
		})}, Run: browserSnapshot},
		{Meta: Metadata{Name: "browser_click", Description: "Click an element reference from the latest browser snapshot.", Parameters: objectSchema([]any{"ref"}, map[string]any{
			"ref": stringProperty("Stable element reference from browser_snapshot."), "button": enumProperty("left", "right", "middle"),
			"clickCount": map[string]any{"type": "integer", "minimum": 1, "maximum": 3}, "timeoutMs": timeoutProperty(),
		})}, Run: browserClick},
		{Meta: Metadata{Name: "browser_type", Description: "Fill or append text in an element from the latest browser snapshot.", Parameters: objectSchema([]any{"ref", "text"}, map[string]any{
			"ref": stringProperty("Stable element reference."), "text": map[string]any{"type": "string", "maxLength": 32768},
			"append": map[string]any{"type": "boolean"}, "submit": map[string]any{"type": "boolean"},
			"delayMs": map[string]any{"type": "integer", "minimum": 0, "maximum": 1000}, "timeoutMs": timeoutProperty(),
		})}, Run: browserType},
		{Meta: Metadata{Name: "browser_select", Description: "Select one or more options in a select element reference.", Parameters: objectSchema([]any{"ref"}, map[string]any{
			"ref": stringProperty("Stable select element reference."), "value": stringProperty("One option value."),
			"values": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 100},
		})}, Run: browserSelect},
		{Meta: Metadata{Name: "browser_press", Description: "Press a keyboard key or chord in the page or a referenced element.", Parameters: objectSchema([]any{"key"}, map[string]any{
			"key": stringProperty("Playwright key or chord, such as Enter or Control+L."), "ref": stringProperty("Optional stable element reference."), "tabId": stringProperty("Optional tab ID."),
		})}, Run: browserPress},
		{Meta: Metadata{Name: "browser_scroll", Description: "Scroll the page or a referenced scrollable element.", Parameters: objectSchema(nil, map[string]any{
			"ref": stringProperty("Optional stable element reference."), "tabId": stringProperty("Optional tab ID."),
			"deltaX": map[string]any{"type": "integer"}, "deltaY": map[string]any{"type": "integer"},
		})}, Run: browserScroll},
		{Meta: Metadata{Name: "browser_tabs", Description: "List, select, create, or close tabs in persistent sandbox Chromium.", Parameters: objectSchema(nil, map[string]any{
			"action": enumProperty("list", "select", "new", "close"), "tabId": stringProperty("Tab used by select or close."), "url": stringProperty("Optional URL for a new tab."),
		})}, Run: browserTabs},
		{Meta: Metadata{Name: "browser_wait", Description: "Wait for text, URL, load state, or a bounded duration in the active browser tab.", Parameters: objectSchema(nil, map[string]any{
			"tabId": stringProperty("Optional tab ID."), "text": stringProperty("Text to wait for."), "exact": map[string]any{"type": "boolean"},
			"url": stringProperty("URL or Playwright URL glob to wait for."), "loadState": enumProperty("domcontentloaded", "load", "networkidle"),
			"milliseconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 30000}, "timeoutMs": timeoutProperty(),
		})}, Run: browserWait},
		{Meta: Metadata{Name: "browser_upload", Description: "Upload files from registered workspace folders or /exchange using a file-input element reference.", Parameters: objectSchema([]any{"ref"}, map[string]any{
			"ref": stringProperty("Stable file input reference."), "path": stringProperty("One labeled workspace path or /exchange path."),
			"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 20},
		})}, Run: browserUpload},
	}
	for _, definition := range definitions {
		Register(definition)
	}
}

func objectSchema(required []any, properties map[string]any) Schema {
	schema := Schema{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func enumProperty(values ...string) map[string]any {
	enums := make([]any, len(values))
	for index, value := range values {
		enums[index] = value
	}
	return map[string]any{"type": "string", "enum": enums}
}
func timeoutProperty() map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "maximum": 120000}
}

func browserOpen(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	return browserMapCall(ctx, "open", arguments)
}
func browserClick(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	return browserMapCall(ctx, "click", arguments)
}
func browserType(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	return browserMapCall(ctx, "type", arguments)
}
func browserSelect(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	return browserMapCall(ctx, "select", arguments)
}
func browserPress(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	return browserMapCall(ctx, "press", arguments)
}
func browserScroll(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	return browserMapCall(ctx, "scroll", arguments)
}
func browserTabs(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	return browserMapCall(ctx, "tabs", arguments)
}
func browserWait(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	return browserMapCall(ctx, "wait", arguments)
}

func browserMapCall(ctx ExecutionContext, method string, arguments json.RawMessage) (any, error) {
	manager, turnID, err := sandboxGUIContext(ctx)
	if err != nil {
		return nil, err
	}
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	var validate map[string]any
	if err := DecodeToolArguments(arguments, &validate); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	data, err := manager.BrowserCall(ctx.context(), ctx.WorkspaceID, turnID, method, arguments)
	if err != nil {
		return nil, sandboxToolError(err)
	}
	var output any
	if err := json.Unmarshal(data, &output); err != nil {
		return nil, SafeError{Code: "browser_protocol_error", Message: "browser returned invalid data"}
	}
	return output, nil
}

type browserSnapshotScreenshot struct {
	MediaType  string `json:"mediaType"`
	Bytes      int64  `json:"bytes"`
	DataBase64 string `json:"dataBase64"`
}
type browserSnapshotBridge struct {
	TabID         string                     `json:"tabId"`
	URL           string                     `json:"url"`
	Title         string                     `json:"title"`
	Revision      string                     `json:"revision"`
	Accessibility string                     `json:"accessibility"`
	Elements      []map[string]any           `json:"elements"`
	Tabs          []map[string]any           `json:"tabs"`
	Screenshot    *browserSnapshotScreenshot `json:"screenshot,omitempty"`
}
type browserSnapshotOutput struct {
	TabID         string           `json:"tabId"`
	URL           string           `json:"url"`
	Title         string           `json:"title"`
	Revision      string           `json:"revision"`
	Accessibility string           `json:"accessibility"`
	Elements      []map[string]any `json:"elements"`
	Tabs          []map[string]any `json:"tabs"`
	Screenshot    *imageMetadata   `json:"screenshot,omitempty"`
	dataURL       string
}
type imageMetadata struct {
	MediaType string `json:"mediaType"`
	Bytes     int64  `json:"bytes"`
}

func (o browserSnapshotOutput) LLMImageContent() (LLMImageContent, bool) {
	if o.dataURL == "" || o.Screenshot == nil {
		return LLMImageContent{}, false
	}
	return LLMImageContent{Path: "sandbox-browser", Name: "browser-snapshot", MediaType: o.Screenshot.MediaType, Bytes: o.Screenshot.Bytes, DataURL: o.dataURL, Detail: "auto"}, true
}

func browserSnapshot(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	manager, turnID, err := sandboxGUIContext(ctx)
	if err != nil {
		return nil, err
	}
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	data, err := manager.BrowserCall(ctx.context(), ctx.WorkspaceID, turnID, "snapshot", arguments)
	if err != nil {
		return nil, sandboxToolError(err)
	}
	var bridge browserSnapshotBridge
	if json.Unmarshal(data, &bridge) != nil {
		return nil, SafeError{Code: "browser_protocol_error", Message: "browser snapshot was invalid"}
	}
	output := browserSnapshotOutput{TabID: bridge.TabID, URL: bridge.URL, Title: bridge.Title, Revision: bridge.Revision, Accessibility: bridge.Accessibility, Elements: bridge.Elements, Tabs: bridge.Tabs}
	if bridge.Screenshot != nil {
		image, decodeErr := base64.StdEncoding.DecodeString(bridge.Screenshot.DataBase64)
		if decodeErr != nil || len(image) > 5<<20 || int64(len(image)) != bridge.Screenshot.Bytes {
			return nil, SafeError{Code: "browser_protocol_error", Message: "browser screenshot was invalid"}
		}
		if bridge.Screenshot.MediaType != "image/png" && bridge.Screenshot.MediaType != "image/jpeg" {
			return nil, SafeError{Code: "browser_protocol_error", Message: "browser screenshot format was invalid"}
		}
		output.Screenshot = &imageMetadata{MediaType: bridge.Screenshot.MediaType, Bytes: int64(len(image))}
		output.dataURL = imageDataURL(bridge.Screenshot.MediaType, image)
	}
	return output, nil
}

func browserUpload(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	manager, turnID, err := sandboxGUIContext(ctx)
	if err != nil {
		return nil, err
	}
	var args struct {
		Ref, Path string
		Paths     []string
	}
	if DecodeToolArguments(arguments, &args) != nil || strings.TrimSpace(args.Ref) == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "ref and at least one path are required"}
	}
	paths := append([]string(nil), args.Paths...)
	if strings.TrimSpace(args.Path) != "" {
		paths = append(paths, args.Path)
	}
	if len(paths) == 0 || len(paths) > 20 {
		return nil, SafeError{Code: "invalid_arguments", Message: "between 1 and 20 upload paths are required"}
	}
	guestPaths := make([]string, 0, len(paths))
	for _, candidate := range paths {
		candidate = strings.TrimSpace(candidate)
		if strings.HasPrefix(strings.ReplaceAll(candidate, "\\", "/"), "/exchange/") {
			guestPaths = append(guestPaths, pathpkg.Clean(strings.ReplaceAll(candidate, "\\", "/")))
			continue
		}
		if ctx.ToolScopes != nil && !ctx.ToolScopes.Allowed("browser_upload", candidate) {
			return nil, SafeError{Code: "path_not_allowed", Message: fmt.Sprintf("path %q is not allowed by the current agent mode", candidate)}
		}
		hostPath, resolveErr := resolveWorkspacePath(ctx, candidate)
		if resolveErr != nil {
			return nil, resolveErr
		}
		guestPath, mapErr := manager.HostToGuest(ctx.WorkspaceID, hostPath)
		if mapErr != nil {
			return nil, SafeError{Code: "sandbox_path_mapping_failed", Message: mapErr.Error()}
		}
		guestPaths = append(guestPaths, guestPath)
	}
	payload, _ := json.Marshal(map[string]any{"ref": args.Ref, "paths": guestPaths})
	data, err := manager.BrowserCall(ctx.context(), ctx.WorkspaceID, turnID, "upload", payload)
	if err != nil {
		return nil, sandboxToolError(err)
	}
	var output any
	if json.Unmarshal(data, &output) != nil {
		return nil, SafeError{Code: "browser_protocol_error", Message: "browser returned invalid data"}
	}
	return output, nil
}

type desktopControlArgs struct {
	Action     string `json:"action"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	ToX        int    `json:"toX"`
	ToY        int    `json:"toY"`
	Button     int    `json:"button"`
	Clicks     int    `json:"clicks"`
	DeltaX     int    `json:"deltaX"`
	DeltaY     int    `json:"deltaY"`
	DurationMS int    `json:"durationMs"`
	Text       string `json:"text"`
	Key        string `json:"key"`
}
type desktopSnapshotOutput struct {
	MediaType string `json:"mediaType"`
	Bytes     int64  `json:"bytes"`
	dataURL   string
}

func (o desktopSnapshotOutput) LLMImageContent() (LLMImageContent, bool) {
	if o.dataURL == "" {
		return LLMImageContent{}, false
	}
	return LLMImageContent{Path: "sandbox-desktop", Name: "desktop-snapshot", MediaType: o.MediaType, Bytes: o.Bytes, DataURL: o.dataURL, Detail: "auto"}, true
}

func desktopControl(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	manager, turnID, err := sandboxGUIContext(ctx)
	if err != nil {
		return nil, err
	}
	var args desktopControlArgs
	if DecodeToolArguments(arguments, &args) != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	args.Action = strings.TrimSpace(args.Action)
	if args.Action == "snapshot" {
		image, mediaType, captureErr := manager.DesktopScreenshot(ctx.context(), ctx.WorkspaceID, turnID)
		if captureErr != nil {
			return nil, sandboxToolError(captureErr)
		}
		return desktopSnapshotOutput{MediaType: mediaType, Bytes: int64(len(image)), dataURL: imageDataURL(mediaType, image)}, nil
	}
	allowed := map[string]bool{"move": true, "click": true, "double_click": true, "drag": true, "scroll": true, "type": true, "key": true, "wait": true}
	if !allowed[args.Action] {
		return nil, SafeError{Code: "invalid_arguments", Message: "unsupported desktop action"}
	}
	if len(args.Text) > 32768 || len(args.Key) > 100 {
		return nil, SafeError{Code: "invalid_arguments", Message: "desktop input is too large"}
	}
	action := sandbox.DesktopActionRequest{Action: args.Action, X: args.X, Y: args.Y, X2: args.ToX, Y2: args.ToY, Button: args.Button, Clicks: args.Clicks, DeltaX: args.DeltaX, DeltaY: args.DeltaY, DurationMS: args.DurationMS, Text: args.Text, Key: args.Key}
	if err := manager.DesktopAction(ctx.context(), ctx.WorkspaceID, turnID, action); err != nil {
		return nil, sandboxToolError(err)
	}
	return map[string]any{"ok": true, "action": args.Action}, nil
}

func sandboxGUIContext(ctx ExecutionContext) (*sandbox.Manager, string, error) {
	if !ctx.UsesSandbox() {
		return nil, "", SafeError{Code: "sandbox_gui_unavailable", Message: "GUI tools require an enabled workspace sandbox"}
	}
	turnID := strings.TrimSpace(ctx.TurnID)
	if turnID == "" {
		return nil, "", SafeError{Code: "sandbox_turn_missing", Message: "GUI tools require an active chat turn"}
	}
	return ctx.Sandbox, turnID, nil
}

func sandboxToolError(err error) error {
	var sandboxError *sandbox.Error
	if errors.As(err, &sandboxError) {
		return SafeError{Code: sandboxError.Code, Message: sandboxError.Message}
	}
	return err
}
