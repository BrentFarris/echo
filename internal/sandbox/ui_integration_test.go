package sandbox

import (
	"context"
	_ "embed"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

//go:embed testdata/gui_fixture.py
var gtkUIFixture []byte

// Called inside the existing opt-in lifecycle acceptance test so it shares the
// authenticated agent, real display, session bus and isolated Docker resources.
func testNativeUIIntegration(t *testing.T, ctx context.Context, engine *DockerEngine, state MachineState) {
	t.Helper()
	launch, err := engine.Exec(ctx, state, ExecRequest{Command: []string{"/bin/bash", "-lc", `cat > /tmp/echo-ui-fixture.py
nohup python3 /tmp/echo-ui-fixture.py >/tmp/echo-ui-fixture.log 2>&1 </dev/null &
nohup thunar /tmp >/tmp/echo-ui-thunar.log 2>&1 </dev/null &
`}, Input: gtkUIFixture})
	if err != nil || launch.ExitCode != 0 {
		t.Fatalf("launch GTK fixture: %+v %v", launch, err)
	}
	var observation UIObservation
	var lastError error
	var lastData json.RawMessage
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		data, err := engine.NativeUICall(ctx, state, "ui_observe", json.RawMessage(`{"search":"Echo UI Fixture","limit":200}`))
		lastData, lastError = data, err
		if err == nil {
			err = json.Unmarshal(data, &observation)
		}
		if err == nil && nativeTarget(observation, "Save document") != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if nativeTarget(observation, "Save document") == "" {
		logs, _ := engine.Exec(ctx, state, ExecRequest{Command: []string{"cat", "/tmp/echo-ui-fixture.log"}})
		t.Fatalf("native fixture did not appear: %+v logs=%s last=%s error=%v", observation, string(logs.Stdout), lastData, lastError)
	}
	call := func(id, action, ref string, extra map[string]any) UIResult {
		t.Helper()
		params := map[string]any{"requestId": id, "action": action, "ref": ref, "epoch": observation.Surface.Epoch}
		for key, value := range extra {
			params[key] = value
		}
		data, _ := json.Marshal(params)
		response, err := engine.NativeUICall(ctx, state, "ui_act", data)
		if err != nil {
			t.Fatal(err)
		}
		var result UIResult
		if err = json.Unmarshal(response, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, item := range []struct {
		name, action string
		extra        map[string]any
	}{
		{"Document title", "fill", map[string]any{"text": "Accurate native targeting"}},
		{"Ready", "check", map[string]any{"checked": true}},
		{"Document title", "focus", nil},
	} {
		result := call(item.action, item.action, nativeTarget(observation, item.name), item.extra)
		if result.Execution != "completed" || result.Verification == nil || result.Verification.Status != "passed" {
			t.Fatalf("native %s: %+v verification=%+v", item.action, result, result.Verification)
		}
	}
	moved, err := engine.Exec(ctx, state, ExecRequest{Command: []string{"/bin/bash", "-lc", `xdotool search --onlyvisible --name '^Echo UI Fixture$' windowmove 250 180`}})
	if err != nil || moved.ExitCode != 0 {
		t.Fatalf("move native fixture: %+v %v", moved, err)
	}
	// Exercise the physical fallback through the same private helper. Activate
	// the test window before choosing a display point, as a visual observation does.
	_, err = engine.Exec(ctx, state, ExecRequest{Command: []string{"/bin/bash", "-lc", `xdotool search --onlyvisible --name '^Echo UI Fixture$' windowactivate --sync`}})
	if err != nil {
		t.Fatal(err)
	}
	freshData, err := engine.NativeUICall(ctx, state, "ui_observe", json.RawMessage(`{"search":"Echo UI Fixture","limit":200}`))
	if err != nil {
		t.Fatal(err)
	}
	var movedObservation UIObservation
	if err = json.Unmarshal(freshData, &movedObservation); err != nil {
		t.Fatal(err)
	}
	for _, target := range movedObservation.Targets {
		if target.Name != "Document title" || target.Bounds == nil {
			continue
		}
		if result := call("clear-for-physical", "fill", target.Ref, map[string]any{"text": ""}); result.Execution != "completed" {
			t.Fatalf("clear physical fixture: %+v", result)
		}
		body, _ := json.Marshal(map[string]any{"requestId": "physical-text", "action": "point", "pointAction": "type", "epoch": observation.Surface.Epoch, "x": target.Bounds.X + target.Bounds.Width/2, "y": target.Bounds.Y + target.Bounds.Height/2, "text": "!"})
		response, err := engine.NativeUICall(ctx, state, "ui_act", body)
		if err != nil || !strings.Contains(string(response), `"execution": "completed"`) {
			t.Fatalf("native physical typing: %s %v", response, err)
		}
		verification, _ := json.Marshal(map[string]any{"ref": target.Ref, "expect": map[string]any{"kind": "value", "value": "!"}, "timeoutMs": 1000})
		response, err = engine.NativeUICall(ctx, state, "ui_verify", verification)
		if err != nil || !strings.Contains(string(response), `"status": "passed"`) {
			t.Fatalf("native physical type result: %s %v", response, err)
		}
	}
	for range 2 {
		if result := call("submit-once", "click", nativeTarget(observation, "Save document"), nil); result.Execution != "completed" {
			t.Fatalf("moved window target: %+v", result)
		}
	}
	count, err := engine.Exec(ctx, state, ExecRequest{Command: []string{"cat", "/tmp/echo-ui-clicks"}})
	if err != nil || strings.TrimSpace(string(count.Stdout)) != "1" {
		t.Fatalf("duplicate native submission: %s %v", count.Stdout, err)
	}
	if result := call("disabled", "click", nativeTarget(observation, "Unavailable action"), nil); result.Execution != "not_started" {
		t.Fatalf("disabled native control activated: %+v", result)
	}
	listed, err := engine.NativeUICall(ctx, state, "ui_observe", json.RawMessage(`{"list":true}`))
	if err != nil || !strings.Contains(strings.ToLower(string(listed)), "mousepad") || !strings.Contains(strings.ToLower(string(listed)), "thunar") {
		t.Fatalf("native app discovery incomplete: %s %v", listed, err)
	}
	closed, err := engine.Exec(ctx, state, ExecRequest{Command: []string{"/bin/bash", "-lc", `xdotool search --onlyvisible --name '^Echo UI Fixture$' windowclose`}})
	if err != nil || closed.ExitCode != 0 {
		t.Fatalf("close fixture: %+v %v", closed, err)
	}
	time.Sleep(200 * time.Millisecond)
	if result := call("after-close", "click", nativeTarget(observation, "Save document"), nil); result.Execution != "not_started" {
		t.Fatalf("defunct native control accepted: %+v", result)
	}
}

func nativeTarget(observation UIObservation, name string) string {
	for _, target := range observation.Targets {
		if target.Name == name {
			return target.Ref
		}
	}
	return ""
}
