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
	t.Run("Xfce Applications menu", func(t *testing.T) { testNativePanelClickIntegration(t, ctx, engine, state) })
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

func testNativePanelClickIntegration(t *testing.T, ctx context.Context, engine *DockerEngine, state MachineState) {
	observe := func(search string) UIObservation {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"search": search, "limit": 200})
		data, err := engine.NativeUICall(ctx, state, "ui_observe", params)
		if err != nil {
			t.Fatal(err)
		}
		var observation UIObservation
		if err := json.Unmarshal(data, &observation); err != nil {
			t.Fatal(err)
		}
		return observation
	}
	closeMenu := func() {
		t.Helper()
		result, err := engine.Exec(ctx, state, ExecRequest{Command: []string{"xdotool", "key", "Escape"}})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("close panel menu: %+v %v", result, err)
		}
	}
	closeMenu()
	t.Cleanup(closeMenu)
	var observation UIObservation
	var button UITarget
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		observation = observe("Applications")
		for _, target := range observation.Targets {
			if target.Name == "Applications" && target.Role == "toggle_button" && target.States["visible"] == true {
				button = target
			}
		}
		if button.Ref != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if button.Ref == "" {
		data, _ := json.Marshal(observe(""))
		t.Fatalf("Xfce Applications button did not appear: %s", data)
	}
	menuVisible := func() bool {
		for _, target := range observe("Run Program").Targets {
			if target.Role == "menu_item" && target.States["visible"] == true {
				return true
			}
		}
		return false
	}
	for _, requestID := range []string{"panel-open", "panel-reopen"} {
		if menuVisible() {
			t.Fatal("Applications menu was already visible before clicking")
		}
		params, _ := json.Marshal(map[string]any{"requestId": requestID, "action": "click", "ref": button.Ref, "epoch": observation.Surface.Epoch})
		// A transport retry must not send a second click. Reopening also checks
		// clicking when the pointer is already at the button's center.
		for range 2 {
			data, err := engine.NativeUICall(ctx, state, "ui_act", params)
			if err != nil {
				t.Fatal(err)
			}
			var result UIResult
			if err := json.Unmarshal(data, &result); err != nil || result.Execution != "completed" || result.Error != nil {
				t.Fatalf("panel click failed: %s %v", data, err)
			}
			if result.Verification == nil || result.Verification.Status != "unverified" {
				t.Fatalf("panel click claimed verification without a postcondition: %s", data)
			}
		}
		deadline = time.Now().Add(5 * time.Second)
		for !menuVisible() {
			if time.Now().After(deadline) {
				t.Fatal("Applications click completed but no menu item became visible")
			}
			time.Sleep(100 * time.Millisecond)
		}
		closeMenu()
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
