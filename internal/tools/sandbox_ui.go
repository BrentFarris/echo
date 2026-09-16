package tools

import (
	"encoding/json"

	"github.com/brent/echo/internal/sandbox"
)

var canonicalUITools = map[string]bool{"ui_observe": true, "ui_act": true, "ui_verify": true, "ui_locate": true}
var legacyUITools = map[string]bool{"browser_snapshot": true, "browser_click": true, "browser_type": true, "browser_select": true, "browser_press": true, "browser_scroll": true, "browser_wait": true, "desktop_control": true}

// GUIPreviewProvider displays screenshots in the transcript without turning
// them into user image messages or selecting Vision for the planner history.
type GUIPreviewProvider interface {
	GUIPreview() (LLMImageContent, bool)
}

type uiOutput struct{ *sandbox.UIResult }

func (o uiOutput) GUIPreview() (LLMImageContent, bool) {
	if o.UIResult == nil || o.Observation == nil || o.Observation.Screenshot == nil {
		return LLMImageContent{}, false
	}
	shot := o.Observation.Screenshot
	if shot.DataBase64 == "" {
		return LLMImageContent{}, false
	}
	return LLMImageContent{Path: "sandbox-" + o.Observation.Surface.Kind, Name: "UI observation", MediaType: shot.MediaType, Bytes: shot.Bytes, DataURL: "data:" + shot.MediaType + ";base64," + shot.DataBase64, Detail: "high"}, true
}

func (o browserSnapshotOutput) GUIPreview() (LLMImageContent, bool) { return o.LLMImageContent() }
func (o desktopSnapshotOutput) GUIPreview() (LLMImageContent, bool) { return o.LLMImageContent() }

func init() {
	bound := map[string]any{"type": "object", "additionalProperties": false, "required": []any{"x", "y", "width", "height"}, "properties": map[string]any{
		"x": map[string]any{"type": "number", "minimum": 0}, "y": map[string]any{"type": "number", "minimum": 0},
		"width": map[string]any{"type": "number", "minimum": 1}, "height": map[string]any{"type": "number", "minimum": 1},
	}}
	expect := objectSchema([]any{"kind"}, map[string]any{
		"kind": enumProperty("visible", "hidden", "text", "value", "checked", "selected", "focused", "url", "window", "dialog"),
		"ref":  stringProperty("Observed target to verify, defaults to the action target."), "value": stringProperty("Exact expected text, input value, URL, title or dialog message."),
		"checked": map[string]any{"type": "boolean"}, "values": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	})
	definitions := []struct {
		name, description string
		required          []any
		properties        map[string]any
	}{
		{"ui_observe", "Inspect the current sandbox UI before acting. Prefer browser semantics for Chromium and desktop accessibility for native apps. List surfaces, search controls, expand a scope, or follow nextCursor to see more targets. Screenshots are previews; ui_locate can inspect them independently.", nil, map[string]any{
			"surface": enumProperty("browser", "desktop"), "surfaceId": stringProperty("Tab/window ID from a prior list; defaults to active browser tab or entire desktop."),
			"list": map[string]any{"type": "boolean"}, "search": stringProperty("Filter by role, name, or ancestor context."),
			"observationId": stringProperty("Required when expanding scopeRef."), "scopeRef": stringProperty("Inspect descendants of an observed control."),
			"cursor": stringProperty("nextCursor from an observation of the same surface."), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}, "screenshot": map[string]any{"type": "boolean"},
		}},
		{"ui_act", "Act once on an observed target. References are scoped to this turn and observation. Supply an expected result when possible. execution=completed means input was delivered; only verification=passed establishes the supplied postcondition. Never blindly repeat an unknown outcome.", []any{"observationId", "action"}, map[string]any{
			"observationId": stringProperty("Observation containing every referenced target."), "ref": stringProperty("Target ref from ui_observe or ui_locate; required except dialog."),
			"action": enumProperty("click", "fill", "type", "check", "select", "focus", "press", "hover", "scroll", "drag", "dialog"),
			"text":   map[string]any{"type": "string", "maxLength": 32768}, "key": stringProperty("Key/chord: browser uses Playwright names; desktop uses X11 names."),
			"checked": map[string]any{"type": "boolean"}, "values": map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string"}},
			"toRef": stringProperty("Observed drag destination, or native selection child."), "button": enumProperty("left", "middle", "right"), "clickCount": map[string]any{"type": "integer", "minimum": 1, "maximum": 3},
			"deltaX": map[string]any{"type": "integer"}, "deltaY": map[string]any{"type": "integer"}, "accept": map[string]any{"type": "boolean"},
			"expect": expect, "timeoutMs": map[string]any{"type": "integer", "minimum": 1, "maximum": 60000}, "verifyTimeoutMs": map[string]any{"type": "integer", "minimum": 1, "maximum": 30000},
		}},
		{"ui_verify", "Check an explicit condition against fresh state. Deterministic predicates are preferred. visual=true sends a fresh screenshot to the selected Vision endpoint and reports model_assessed evidence separately.", []any{"observationId"}, map[string]any{
			"observationId": stringProperty("Observation identifying the surface and any referenced controls."), "ref": stringProperty("Observed target for a deterministic predicate."), "expect": expect,
			"visual": map[string]any{"type": "boolean"}, "description": stringProperty("Required visual condition when visual=true."), "region": bound,
			"timeoutMs": map[string]any{"type": "integer", "minimum": 1, "maximum": 30000},
		}},
		{"ui_locate", "Ask the configured Vision endpoint to locate a described control in an observed screenshot. Returns a visual target ref without acting. Use only when semantic inspection is insufficient. A source-pixel region provides a detailed zoom. Ambiguous/absent targets return no coordinates.", []any{"observationId", "description"}, map[string]any{
			"observationId": stringProperty("Observation captured with screenshot=true."), "description": stringProperty("Unambiguous target description and nearby context."), "region": bound,
		}},
	}
	for _, definition := range definitions {
		method := definition.name
		sandboxGUIToolNames[method] = true
		Register(ToolFunc{Meta: Metadata{Name: method, Description: definition.description, Parameters: objectSchema(definition.required, definition.properties)}, Run: func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
			manager, turn, err := sandboxGUIContext(ctx)
			if err != nil {
				return nil, err
			}
			var request sandbox.UIRequest
			if err := DecodeToolArguments(arguments, &request); err != nil {
				return nil, SafeError{Code: "invalid_arguments", Message: "Invalid UI tool arguments"}
			}
			result, err := manager.UICall(ctx.context(), ctx.WorkspaceID, turn, ctx.ToolCallID, method, request, ctx.UIVision)
			if err != nil {
				if method == "ui_act" {
					return uiOutput{&sandbox.UIResult{Execution: "not_started", Verification: &sandbox.UIVerification{Status: "unverified", Method: "none"}, Error: &sandbox.Error{Code: sandbox.ErrorCode(err), Message: err.Error()}}}, nil
				}
				return nil, sandboxToolError(err)
			}
			return uiOutput{result}, nil
		}})
	}
}
