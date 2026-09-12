package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type uiTestEngine struct {
	fakeEngine
	shot    *UIImage
	actions atomic.Int32
	action  func(context.Context, json.RawMessage) (json.RawMessage, error)
}

func (e *uiTestEngine) BrowserCall(ctx context.Context, _ MachineState, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "invalidate_references":
		return json.RawMessage(`{}`), nil
	case "ui_observe":
		var request UIRequest
		_ = json.Unmarshal(params, &request)
		observation := UIObservation{Surface: UISurface{Kind: "browser", ID: "tab-1", Epoch: "runtime-1:page-1"}, Targets: []UITarget{{Ref: "e1", Role: "button", Name: "Save"}}}
		if request.Screenshot {
			observation.Screenshot = e.shot
		}
		return json.Marshal(observation)
	case "ui_act":
		e.actions.Add(1)
		if e.action != nil {
			return e.action(ctx, params)
		}
		return json.RawMessage(`{"execution":"completed","verification":{"status":"unverified","method":"none"},"backend":"browser"}`), nil
	}
	return nil, errors.New("unexpected method")
}

func testShot(t *testing.T, width, height int, pixelX, pixelY int) *UIImage {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, width, height))
	if pixelX >= 0 {
		im.Set(pixelX, pixelY, color.RGBA{R: 255, A: 255})
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, im); err != nil {
		t.Fatal(err)
	}
	shot := &UIImage{DataBase64: base64.StdEncoding.EncodeToString(buffer.Bytes()), MediaType: "image/png", Bytes: int64(buffer.Len()), Space: "browser-viewport", ScaleX: 1, ScaleY: 1}
	if err := prepareUIImage(shot); err != nil {
		t.Fatal(err)
	}
	return shot
}

func TestUIObservationScopeAndPreviewIsolation(t *testing.T) {
	engine := &uiTestEngine{shot: testShot(t, 100, 100, -1, -1)}
	m, workspace, _ := newSandboxManagerForTest(t, engine)
	result, err := m.UICall(context.Background(), workspace.ID, "turn", "observe", "ui_observe", UIRequest{Screenshot: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "dataBase64") || strings.Contains(string(data), engine.shot.DataBase64) {
		t.Fatal("screenshot bytes leaked into planner result")
	}
	if result.Observation.Screenshot.DataBase64 == "" {
		t.Fatal("preview bytes were removed from memory")
	}
	request := UIRequest{ObservationID: result.Observation.ID, Ref: "e1", Action: "click"}
	for _, wrong := range []UIRequest{{ObservationID: request.ObservationID, Ref: "unknown", Action: "click"}, {ObservationID: request.ObservationID, Ref: "e1", Action: "click", SurfaceID: "tab-2"}, {ObservationID: request.ObservationID, Ref: "e1", Action: "click", ClickCount: 1000}} {
		if _, err := m.UICall(context.Background(), workspace.ID, "turn", "bad", "ui_act", wrong, nil); err == nil {
			t.Fatal("invalid reference or surface admitted")
		}
	}
	if _, err := m.uiRecord(workspace.ID, "other-turn", 0, request); ErrorCode(err) != "ui_stale_observation" {
		t.Fatalf("cross-turn observation: %v", err)
	}
	if _, err := m.uiRecord("other-workspace", "turn", 0, request); ErrorCode(err) != "ui_stale_observation" {
		t.Fatalf("cross-workspace observation: %v", err)
	}
	m.ReleaseAIControl(workspace.ID, "turn")
	if _, err := m.uiRecord(workspace.ID, "turn", 0, request); ErrorCode(err) != "ui_stale_observation" {
		t.Fatalf("turn-end observation: %v", err)
	}
	if engine.actions.Load() != 0 {
		t.Fatal("invalid request reached input")
	}
}

func TestUIConcurrentDedupAndUnknownOutcome(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "unknown"}[unknown], func(t *testing.T) {
			engine := &uiTestEngine{}
			if unknown {
				engine.action = func(context.Context, json.RawMessage) (json.RawMessage, error) {
					return nil, errors.New("connection lost after click")
				}
			}
			m, w, _ := newSandboxManagerForTest(t, engine)
			observed, err := m.UICall(context.Background(), w.ID, "turn", "o", "ui_observe", UIRequest{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			request := UIRequest{ObservationID: observed.Observation.ID, Ref: "e1", Action: "click"}
			var group sync.WaitGroup
			for range 8 {
				group.Go(func() {
					result, err := m.UICall(context.Background(), w.ID, "turn", "same-call", "ui_act", request, nil)
					if err != nil {
						t.Error(err)
						return
					}
					if result.Execution != map[bool]string{false: "completed", true: "unknown"}[unknown] {
						t.Errorf("incorrect outcome %+v", result)
					}
				})
			}
			group.Wait()
			if engine.actions.Load() != 1 {
				t.Fatalf("duplicate input: %d", engine.actions.Load())
			}
			request.Action = "focus"
			if _, err := m.UICall(context.Background(), w.ID, "turn", "same-call", "ui_act", request, nil); ErrorCode(err) != "ui_request_conflict" {
				t.Fatalf("ID reuse accepted: %v", err)
			}
		})
	}
}

func TestUITakeoverCancelsActionAndQueuedLegacyWork(t *testing.T) {
	entered := make(chan struct{})
	engine := &uiTestEngine{action: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	m, w, _ := newSandboxManagerForTest(t, engine)
	ctx := WithAIControlGeneration(context.Background(), 0)
	observed, err := m.UICall(ctx, w.ID, "turn", "o", "ui_observe", UIRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan *UIResult, 1)
	go func() {
		result, _ := m.UICall(ctx, w.ID, "turn", "click", "ui_act", UIRequest{ObservationID: observed.Observation.ID, Ref: "e1", Action: "click"}, nil)
		finished <- result
	}()
	<-entered
	legacy := make(chan error, 1)
	go func() { legacy <- m.DesktopAction(ctx, w.ID, "turn", DesktopActionRequest{Action: "click"}) }()
	if _, err := m.TakeUserControl(w.ID, "owner", true); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-finished:
		if result == nil || result.Execution != "unknown" {
			t.Fatalf("lost execution evidence: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("action did not cancel")
	}
	select {
	case err := <-legacy:
		if err == nil {
			t.Fatal("queued legacy action crossed takeover")
		}
	case <-time.After(time.Second):
		t.Fatal("queued action did not cancel")
	}
	_, _ = m.ReleaseUserControl(w.ID, "owner")
	if _, err := m.UICall(context.Background(), w.ID, "turn", "retry", "ui_act", UIRequest{ObservationID: observed.Observation.ID, Ref: "e1", Action: "click"}, nil); ErrorCode(err) != "ui_stale_observation" {
		t.Fatalf("old observation admitted: %v", err)
	}
}

func TestUIVisualTransformsValidationAndRegionFreshness(t *testing.T) {
	shot := testShot(t, 2000, 1000, -1, -1)
	if shot.RenderedWidth != 1280 || shot.RenderedHeight != 640 {
		t.Fatalf("wrong transport dimensions: %+v", shot)
	}
	_, transform, err := specialistImage(shot, &UIBounds{X: 1500, Y: 400, Width: 200, Height: 100})
	if err != nil || transform.width != 200 || transform.region.X != 1500 {
		t.Fatalf("zoom lost source detail: %+v %v", transform, err)
	}
	for _, text := range []string{`{"status":"found","box":{"x":200,"y":0,"width":1,"height":1}}`, `{"status":"found","box":{"x":0,"y":0,"width":0,"height":2}}`, `{"status":"ambiguous","box":{"x":0,"y":0,"width":1,"height":1}}`, `{"status":"found","confidence":1}`, `{"status":"absent"} {"status":"found"}`} {
		if _, err := parseVisionAnswer(text, transform, false); err == nil {
			t.Fatalf("accepted malformed answer: %s", text)
		}
	}
	if _, err := parseVisionAnswer(`{"status":"absent"}`, transform, false); err != nil {
		t.Fatal(err)
	}
	box := UIBounds{X: 20, Y: 20, Width: 20, Height: 20}
	before := testShot(t, 100, 100, -1, -1)
	if !targetRegionUnchanged(before, testShot(t, 100, 100, 90, 90), box) {
		t.Fatal("unrelated pixel invalidated target")
	}
	if targetRegionUnchanged(before, testShot(t, 100, 100, 25, 25), box) {
		t.Fatal("changed target was accepted")
	}
	before.OriginX, before.OriginY, before.ScaleX, before.ScaleY = 10, 20, 2, 2
	x, y := sourcePoint(before, box)
	if x != 70 || y != 80 {
		t.Fatalf("wrong input transform: %v %v", x, y)
	}
}

func TestUIVisualLocateIsBoundedAndCannotAct(t *testing.T) {
	engine := &uiTestEngine{shot: testShot(t, 100, 100, -1, -1)}
	m, w, _ := newSandboxManagerForTest(t, engine)
	observed, err := m.UICall(context.Background(), w.ID, "turn", "o", "ui_observe", UIRequest{Screenshot: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := UIRequest{ObservationID: observed.Observation.ID, Target: "Save icon"}
	if _, err := m.UICall(context.Background(), w.ID, "turn", "locate", "ui_locate", request, nil); ErrorCode(err) != "ui_vision_unavailable" {
		t.Fatal(err)
	}
	calls := 0
	vision := func(ctx context.Context, input UIVisionRequest) (string, UISpecialistUsage, error) {
		calls++
		if input.Width != 100 || input.Height != 100 || input.Target != "Save icon" {
			t.Fatalf("incorrect specialist request: %+v", input)
		}
		if calls == 1 {
			return "not json", UISpecialistUsage{PromptTokens: 10}, nil
		}
		if input.Correction == "" {
			t.Fatal("malformed correction missing")
		}
		return `{"status":"found","box":{"x":20,"y":20,"width":20,"height":20}}`, UISpecialistUsage{PromptTokens: 10}, nil
	}
	located, err := m.UICall(context.Background(), w.ID, "turn", "locate", "ui_locate", request, vision)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || located.Specialist.PromptTokens != 20 || located.Target == nil || engine.actions.Load() != 0 {
		t.Fatalf("invalid locate: %+v", located)
	}
	engine.shot = testShot(t, 100, 100, 25, 25)
	if _, err := m.UICall(context.Background(), w.ID, "turn", "click", "ui_act", UIRequest{ObservationID: located.Observation.ID, Ref: located.Target.Ref, Action: "click"}, nil); ErrorCode(err) != "ui_visual_target_stale" {
		t.Fatalf("stale visual action accepted: %v", err)
	}
	if engine.actions.Load() != 0 {
		t.Fatal("visual stale target reached input")
	}
}
