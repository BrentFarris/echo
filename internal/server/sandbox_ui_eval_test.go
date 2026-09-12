package server

// Opt-in end-to-end evaluation. The ordinary test suite never contacts an LLM.
// Every run owns a disposable Docker sandbox and deterministic fixture oracles.
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/tools"
	"github.com/brent/echo/internal/workspaces"
	"github.com/google/uuid"
)

type uiEvalWorkspace struct{ workspace workspaces.Workspace }

func (r uiEvalWorkspace) Get(id string) (workspaces.Workspace, bool, error) {
	return r.workspace, id == r.workspace.ID, nil
}
func (r uiEvalWorkspace) List() ([]workspaces.Workspace, error) {
	return []workspaces.Workspace{r.workspace}, nil
}

type uiEvalCase struct {
	id, surface, prompt, html, value, selected string
	checked                                    bool
}
type uiEvalOracle struct {
	Count    int    `json:"count"`
	Value    string `json:"value"`
	Checked  bool   `json:"checked"`
	Selected string `json:"selected"`
}
type uiEvalReport struct {
	Case                 string `json:"case"`
	Mode                 string `json:"mode"`
	Model                string `json:"model"`
	Repeat               int    `json:"repeat"`
	Completed            bool   `json:"completed"`
	WrongTarget          bool   `json:"wrongTarget"`
	DuplicateSubmissions int    `json:"duplicateSubmissions"`
	Actions              int    `json:"actions"`
	Retries              int    `json:"retries"`
	FailedToolCalls      int    `json:"failedToolCalls"`
	PromptTokens         int    `json:"promptTokens"`
	CompletionTokens     int    `json:"completionTokens"`
	LatencyMS            int64  `json:"latencyMs"`
	Error                string `json:"error,omitempty"`
}

func TestUILiveEvaluation(t *testing.T) {
	if os.Getenv("ECHO_UI_LIVE_EVAL") != "1" {
		t.Skip("opt in with ECHO_UI_LIVE_EVAL=1; see docs/ui-evaluation.md")
	}
	settings := llm.DefaultSettings()
	settings.Endpoint, settings.Model = os.Getenv("ECHO_UI_EVAL_ENDPOINT"), os.Getenv("ECHO_UI_EVAL_MODEL")
	if settings.Endpoint == "" || settings.Model == "" {
		t.Fatal("ECHO_UI_EVAL_ENDPOINT and ECHO_UI_EVAL_MODEL are required")
	}
	settings.MaxTokens = 2500
	settings.ThinkingTokenBudget = 0
	chat, err := llm.NewClient(settings, llm.WithAPIKey(os.Getenv("ECHO_UI_EVAL_API_KEY")))
	if err != nil {
		t.Fatal(err)
	}
	visionSettings := settings
	if value := os.Getenv("ECHO_UI_EVAL_VISION_ENDPOINT"); value != "" {
		visionSettings.Endpoint = value
	}
	if value := os.Getenv("ECHO_UI_EVAL_VISION_MODEL"); value != "" {
		visionSettings.Model = value
	}
	visionKey := os.Getenv("ECHO_UI_EVAL_VISION_API_KEY")
	if visionKey == "" && visionSettings.Endpoint == settings.Endpoint {
		visionKey = os.Getenv("ECHO_UI_EVAL_API_KEY")
	}
	visionClient, err := llm.NewClient(visionSettings, llm.WithAPIKey(visionKey))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{visionLLM: visionClient, visionSettings: visionSettings}
	engine, err := sandbox.NewDockerEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	oldRuntime, oldGateway := sandbox.RuntimeImage, sandbox.GatewayImage
	sandbox.RuntimeImage = firstNonBlank(os.Getenv("ECHO_SANDBOX_RUNTIME_IMAGE"), "echo-sandbox-runtime:dev")
	sandbox.GatewayImage = firstNonBlank(os.Getenv("ECHO_SANDBOX_GATEWAY_IMAGE"), "echo-sandbox-egress:dev")
	defer func() { sandbox.RuntimeImage, sandbox.GatewayImage = oldRuntime, oldGateway }()
	root := t.TempDir()
	workspace := workspaces.Workspace{ID: "ui-eval-" + uuid.NewString(), Name: "UI evaluation", MainPath: root, Folders: []string{root}, Sandbox: workspaces.SandboxConfig{Enabled: true, CPULimit: 4, MemoryMiB: 6144}}
	stateRoot := t.TempDir()
	manager := sandbox.NewManager(uiEvalWorkspace{workspace}, stateRoot, "echo-ui-eval", engine)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_ = manager.Delete(ctx, workspace.ID)
		_ = manager.Shutdown(ctx)
	}()
	ctx := context.Background()
	if err := manager.Start(ctx, workspace.ID); err != nil {
		t.Fatal(err)
	}
	state, found, err := sandbox.NewStateStore(stateRoot).Load(workspace.ID)
	if err != nil || !found {
		t.Fatalf("evaluation runtime state missing: %v", err)
	}
	execute := func(command string, input []byte) sandbox.ExecResult {
		t.Helper()
		result, err := engine.Exec(ctx, state, sandbox.ExecRequest{Command: []string{"/bin/bash", "-lc", command}, Input: input})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("fixture setup: %s %v", result.Stderr, err)
		}
		return result
	}
	execute("cat >/tmp/echo-ui-eval-server.py\nnohup python3 /tmp/echo-ui-eval-server.py >/tmp/echo-ui-eval-server.log 2>&1 </dev/null &", []byte(uiEvalServer))
	gtk, err := os.ReadFile(filepath.Join("..", "sandbox", "testdata", "gui_fixture.py"))
	if err != nil {
		t.Fatal(err)
	}
	output := firstNonBlank(os.Getenv("ECHO_UI_EVAL_OUTPUT"), filepath.Join(t.TempDir(), "ui-evaluation.jsonl"))
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	t.Logf("evaluation report: %s", output)
	repeats := 3
	if value := os.Getenv("ECHO_UI_EVAL_REPEATS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 10 {
			t.Fatal("repeats must be 1..10")
		}
		repeats = n
	}
	for _, scenario := range uiEvaluationCases() {
		if filter := os.Getenv("ECHO_UI_EVAL_CASE"); filter != "" && !strings.Contains(scenario.id, filter) {
			continue
		}
		for _, mode := range []string{"coordinates", "semantic", "semantic+vision"} {
			for repeat := 1; repeat <= repeats; repeat++ {
				execute("if test -f /tmp/echo-ui-eval-gtk.pid; then kill $(cat /tmp/echo-ui-eval-gtk.pid) 2>/dev/null || true; fi\nrm -f /tmp/echo-ui-result.json /tmp/echo-ui-clicks", nil)
				if scenario.surface == "browser" {
					execute("cat >/tmp/echo-ui-eval.html\nfor i in {1..50}; do curl -fsS http://127.0.0.1:18886/reset >/dev/null && break; sleep .1; done", []byte(scenario.html))
					if _, err := engine.BrowserCall(ctx, state, "open", json.RawMessage(`{"url":"http://127.0.0.1:18886/"}`)); err != nil {
						t.Fatal(err)
					}
				} else {
					execute("cat >/tmp/echo-ui-eval-gtk.py\nnohup python3 /tmp/echo-ui-eval-gtk.py >/tmp/echo-ui-eval-gtk.log 2>&1 </dev/null &\nprintf '%s' $! >/tmp/echo-ui-eval-gtk.pid\nfor i in {1..50}; do xdotool search --onlyvisible --name '^Echo UI Fixture$' windowactivate --sync && break; sleep .1; done", gtk)
				}
				turn := uuid.NewString()
				report := runUIEvaluation(ctx, t, manager, engine, state, workspace.ID, turn, s, chat, settings, scenario, mode, repeat)
				manager.ReleaseAIControl(workspace.ID, turn)
				var oracle uiEvalOracle
				result := execute("if test -f /tmp/echo-ui-result.json; then cat /tmp/echo-ui-result.json; else printf '{}'; fi", nil)
				_ = json.Unmarshal(result.Stdout, &oracle)
				report.Completed = oracle.Count == 1 && oracle.Value == scenario.value && oracle.Checked == scenario.checked && oracle.Selected == scenario.selected
				report.WrongTarget = oracle.Count > 0 && oracle.Selected != scenario.selected
				report.DuplicateSubmissions = max(0, oracle.Count-1)
				if err := encoder.Encode(report); err != nil {
					t.Fatal(err)
				}
				_ = file.Sync()
				t.Logf("%s %s repeat=%d complete=%v actions=%d", scenario.id, mode, repeat, report.Completed, report.Actions)
			}
		}
	}
}

func runUIEvaluation(ctx context.Context, t *testing.T, manager *sandbox.Manager, engine *sandbox.DockerEngine, state sandbox.MachineState, workspace, turn string, s *Server, client chatCompleter, settings llm.Settings, scenario uiEvalCase, mode string, repeat int) uiEvalReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	started := time.Now()
	report := uiEvalReport{Case: scenario.id, Mode: mode, Model: settings.Model, Repeat: repeat}
	permissions := []tools.ToolPermission{{Name: "ui_observe"}, {Name: "ui_act"}, {Name: "ui_verify"}}
	if mode == "semantic+vision" {
		permissions = append(permissions, tools.ToolPermission{Name: "ui_locate"})
	}
	scopes := tools.NewToolScopeChecker(permissions)
	schema := tools.ChatLLMSchemaForScopes(scopes, tools.ChatSchemaOptions{SandboxGUI: true})
	if mode == "coordinates" {
		schema = uiCoordinateSchema()
	}
	messages := []llm.Message{{Role: llm.RoleSystem, Content: "Complete the stated task using only the provided UI tools. Observe before acting. Inspect results and stop once the task is complete. Do not submit twice. " + sandboxUIGuidance}, {Role: llm.RoleUser, Content: "Surface: " + scenario.surface + ". " + scenario.prompt}}
	var last *sandbox.UIObservation
	attempts := map[string]int{}
	for round := 0; round < 20 && ctx.Err() == nil; round++ {
		request, err := llm.NewChatRequest(settings, messages)
		if err != nil {
			report.Error = err.Error()
			break
		}
		request.Tools = schema
		response, err := client.Complete(ctx, request)
		if err != nil {
			report.Error = err.Error()
			break
		}
		if response.Usage != nil {
			report.PromptTokens += response.Usage.PromptTokens
			report.CompletionTokens += response.Usage.CompletionTokens
		}
		if len(response.Choices) == 0 {
			report.Error = "empty model response"
			break
		}
		message := response.Choices[0].Message
		messages = append(messages, message)
		if len(message.ToolCalls) == 0 {
			break
		}
		for _, call := range message.ToolCalls {
			if call.Function.Name == "ui_act" || call.Function.Name == "screen_act" {
				var arguments map[string]any
				_ = json.Unmarshal([]byte(call.Function.Arguments), &arguments)
				delete(arguments, "observationId")
				canonical, _ := json.Marshal(arguments)
				key := call.Function.Name + string(canonical)
				if attempts[key] > 0 {
					report.Retries++
				}
				attempts[key]++
			}
			var result any
			var imageMessage *llm.Message
			if mode == "coordinates" {
				var params struct {
					X, Y              int
					Action, Text, Key string
					DeltaY            int
				}
				if err := json.Unmarshal([]byte(call.Function.Arguments), &params); err != nil {
					result = map[string]any{"error": "invalid arguments"}
				} else if call.Function.Name == "screen_observe" {
					observed, err := manager.UICall(ctx, workspace, turn, call.ID, "ui_observe", sandbox.UIRequest{Surface: scenario.surface, Screenshot: true, Limit: 1}, nil)
					if err != nil {
						result = map[string]any{"error": err.Error()}
					} else {
						last = observed.Observation
						shot := last.Screenshot
						result = map[string]any{"width": shot.Width, "height": shot.Height, "space": shot.Space}
						imageMessage = &llm.Message{Role: llm.RoleUser, ContentParts: []llm.MessageContentPart{llm.TextContentPart("Current screen. Coordinates use these source image pixels."), llm.ImageURLContentPart("data:" + shot.MediaType + ";base64," + shot.DataBase64)}}
					}
				} else if call.Function.Name == "screen_act" && last != nil {
					report.Actions++
					if params.X < 0 || params.Y < 0 || params.X >= last.Screenshot.Width || params.Y >= last.Screenshot.Height {
						result = map[string]any{"error": "coordinates outside image"}
					} else {
						body, _ := json.Marshal(map[string]any{"requestId": turn + call.ID, "action": "point", "pointAction": params.Action, "x": params.X, "y": params.Y, "text": params.Text, "key": params.Key, "deltaY": params.DeltaY, "epoch": last.Surface.Epoch, "surfaceId": last.Surface.ID})
						var data json.RawMessage
						var err error
						if scenario.surface == "browser" {
							data, err = engine.BrowserCall(ctx, state, "ui_act", body)
						} else {
							data, err = engine.NativeUICall(ctx, state, "ui_act", body)
						}
						if err != nil {
							result = map[string]any{"error": err.Error()}
						} else {
							result = data
						}
					}
				} else {
					result = map[string]any{"error": "observe before acting with an available tool"}
				}
			} else {
				if call.Function.Name == "ui_act" {
					report.Actions++
				}
				toolContext := tools.ExecutionContext{Context: ctx, WorkspaceID: workspace, TurnID: turn, ToolCallID: call.ID, Sandbox: manager, SandboxEnabled: true, ToolScopes: scopes}
				if mode == "semantic+vision" {
					toolContext.UIVision = s.uiVision
				}
				toolResult := tools.Execute(toolContext, call.Function.Name, json.RawMessage(call.Function.Arguments))
				result = toolResult
				if !toolResult.Success {
					report.FailedToolCalls++
				}
				if output, ok := toolResult.Output.(interface{ MarshalJSON() ([]byte, error) }); ok {
					data, _ := output.MarshalJSON()
					var uiResult sandbox.UIResult
					if json.Unmarshal(data, &uiResult) == nil {
						if uiResult.Specialist != nil {
							report.PromptTokens += uiResult.Specialist.PromptTokens
							report.CompletionTokens += uiResult.Specialist.CompletionTokens
						}
						if uiResult.Error != nil {
							report.FailedToolCalls++
						}
						if uiResult.Recovery != "" && uiResult.Recovery != "none" {
							report.Retries++
						}
					}
				}
			}
			data, _ := json.Marshal(result)
			messages = append(messages, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Content: string(data)})
			if imageMessage != nil {
				messages = append(messages, *imageMessage)
			}
		}
	}
	report.LatencyMS = time.Since(started).Milliseconds()
	return report
}

func uiCoordinateSchema() []llm.Tool {
	return []llm.Tool{
		{Type: "function", Function: llm.ToolFunction{Name: "screen_observe", Description: "See a fresh screenshot. Coordinates use source pixels.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		{Type: "function", Function: llm.ToolFunction{Name: "screen_act", Description: "Act at screenshot pixel coordinates; observe again to check the result.", Parameters: map[string]any{"type": "object", "required": []string{"action", "x", "y"}, "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"click", "type", "press", "scroll"}}, "x": map[string]any{"type": "integer"}, "y": map[string]any{"type": "integer"}, "text": map[string]any{"type": "string"}, "key": map[string]any{"type": "string"}, "deltaY": map[string]any{"type": "integer"},
		}}}},
	}
}

func uiEvaluationCases() []uiEvalCase {
	cases := []uiEvalCase{}
	for variant := 0; variant < 4; variant++ {
		post := `<script>let count=0;function done(selected='',value='',checked=false){fetch('/result',{method:'POST',body:JSON.stringify({count:++count,selected,value,checked})}).then(()=>document.querySelector('#status').textContent='Saved')}</script><p id="status" role="status">Ready</p>`
		style := fmt.Sprintf(`<style>body{font:18px sans-serif;padding:%dpx}button,input,select{font:inherit;margin:10px;padding:10px}td{padding:8px}</style>`, 20+variant*17)
		selected := fmt.Sprintf("Invoice %d", variant+1)
		rows := "<table>"
		for row := 0; row < 5; row++ {
			id := fmt.Sprintf("Invoice %d", row+1)
			rows += fmt.Sprintf(`<tr><td>%s</td><td><button onclick="done('%s')">Pay</button></td></tr>`, id, id)
		}
		rows += "</table><p id='clock'></p><script>setInterval(()=>document.querySelector('#clock').textContent=Date.now(),150)</script>"
		cases = append(cases, uiEvalCase{id: fmt.Sprintf("browser-rows-%d", variant), surface: "browser", prompt: "Pay " + selected + " exactly once.", selected: selected, html: style + rows + post})
		value := fmt.Sprintf("Document %d", variant+1)
		form := `<label>Title<input id="title"></label><label><input id="ready" type="checkbox">Ready</label><button onclick="done('',document.querySelector('#title').value,document.querySelector('#ready').checked)">Save</button>`
		cases = append(cases, uiEvalCase{id: fmt.Sprintf("browser-form-%d", variant), surface: "browser", prompt: "Set Title to " + value + ", check Ready, and Save once.", value: value, checked: true, html: style + form + post})
		frame := `<iframe style="width:600px;height:250px" srcdoc="<button style='margin:70px;padding:20px' onclick='parent.done(&quot;frame&quot;)'>Frame Save</button>"></iframe>`
		cases = append(cases, uiEvalCase{id: fmt.Sprintf("browser-frame-%d", variant), surface: "browser", prompt: "Click Frame Save once.", selected: "frame", html: style + frame + post})
		shadow := `<div id="host"></div><script>document.querySelector('#host').attachShadow({mode:'open'}).innerHTML='<button onclick="window.done(\'shadow\')">Shadow Save</button>'</script>`
		cases = append(cases, uiEvalCase{id: fmt.Sprintf("browser-shadow-%d", variant), surface: "browser", prompt: "Click Shadow Save once.", selected: "shadow", html: style + post + shadow})
		canvas := fmt.Sprintf(`<canvas width="900" height="500"></canvas><script>const c=document.querySelector('canvas'),g=c.getContext('2d');g.fillStyle='blue';g.fillRect(%d,100,130,60);g.fillStyle='red';g.fillRect(600,200,130,60);c.onclick=e=>{const r=c.getBoundingClientRect(),x=e.clientX-r.left,y=e.clientY-r.top;done(x>=%d&&x<%d&&y>=100&&y<160?'blue':'wrong')};</script>`, 100+variant*50, 100+variant*50, 230+variant*50)
		cases = append(cases, uiEvalCase{id: fmt.Sprintf("browser-canvas-%d", variant), surface: "browser", prompt: "Click the blue rectangle once.", selected: "blue", html: style + post + canvas})
	}
	for variant := 0; variant < 10; variant++ {
		value := fmt.Sprintf("Native document %d", variant+1)
		checked := variant%2 == 0
		condition := "leave Ready unchecked"
		if checked {
			condition = "check Ready"
		}
		cases = append(cases, uiEvalCase{id: fmt.Sprintf("native-form-%d", variant), surface: "desktop", prompt: "In Echo UI Fixture, set Document title to " + value + ", " + condition + ", then click Save document once.", value: value, checked: checked})
	}
	return cases
}

const uiEvalServer = `from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
class Handler(BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200);self.end_headers()
  if self.path == '/reset':
   Path('/tmp/echo-ui-result.json').unlink(missing_ok=True)
  else:
   self.wfile.write(Path('/tmp/echo-ui-eval.html').read_bytes())
 def do_POST(self):
  data=self.rfile.read(min(int(self.headers.get('Content-Length','0')),4096))
  Path('/tmp/echo-ui-result.json').write_bytes(data)
  self.send_response(200);self.end_headers()
 def log_message(self,*args):pass
HTTPServer(('127.0.0.1',18886),Handler).serve_forever()
`

func TestUIEvaluationSuiteHasThirtyResettableWorkflows(t *testing.T) {
	cases := uiEvaluationCases()
	if len(cases) != 30 {
		t.Fatalf("expected 30 workflows, got %d", len(cases))
	}
	seen := map[string]bool{}
	for _, scenario := range cases {
		if seen[scenario.id] || scenario.prompt == "" {
			t.Fatalf("invalid case %q", scenario.id)
		}
		seen[scenario.id] = true
	}
}
