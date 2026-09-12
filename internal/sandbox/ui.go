package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type UIBounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type UISurface struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Epoch string `json:"epoch"`
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
}

type UITarget struct {
	Ref       string         `json:"ref"`
	Role      string         `json:"role"`
	Name      string         `json:"name"`
	Context   []string       `json:"context,omitempty"`
	States    map[string]any `json:"states,omitempty"`
	Actions   []string       `json:"actions,omitempty"`
	Bounds    *UIBounds      `json:"bounds,omitempty"`
	Grounding string         `json:"grounding,omitempty"`
}

// Width/height are source pixels. Rendered dimensions and scale describe the
// specialist transport; origin and coordinate scale map source pixels to input.
type UIImage struct {
	MediaType      string  `json:"mediaType"`
	Bytes          int64   `json:"bytes"`
	DataBase64     string  `json:"dataBase64,omitempty"`
	Space          string  `json:"space"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	RenderedWidth  int     `json:"renderedWidth"`
	RenderedHeight int     `json:"renderedHeight"`
	OriginX        float64 `json:"originX"`
	OriginY        float64 `json:"originY"`
	ScaleX         float64 `json:"scaleX"`
	ScaleY         float64 `json:"scaleY"`
}

type UIObservation struct {
	ID            string           `json:"observationId"`
	Surface       UISurface        `json:"surface"`
	Timestamp     any              `json:"timestamp"`
	Targets       []UITarget       `json:"targets"`
	Surfaces      []map[string]any `json:"surfaces,omitempty"`
	Screenshot    *UIImage         `json:"screenshot,omitempty"`
	Accessibility string           `json:"accessibility,omitempty"`
	Total         int              `json:"total,omitempty"`
	NextCursor    string           `json:"nextCursor,omitempty"`
	Truncated     bool             `json:"truncated,omitempty"`
	TextTruncated bool             `json:"textTruncated,omitempty"`
	Dialog        map[string]any   `json:"dialog,omitempty"`
	Capabilities  []string         `json:"capabilities,omitempty"`
}

type UIPredicate struct {
	Kind    string   `json:"kind"`
	Ref     string   `json:"ref,omitempty"`
	Value   string   `json:"value,omitempty"`
	Checked *bool    `json:"checked,omitempty"`
	Values  []string `json:"values,omitempty"`
}

type UIRequest struct {
	Surface         string       `json:"surface,omitempty"`
	SurfaceID       string       `json:"surfaceId,omitempty"`
	ObservationID   string       `json:"observationId,omitempty"`
	Ref             string       `json:"ref,omitempty"`
	ScopeRef        string       `json:"scopeRef,omitempty"`
	ToRef           string       `json:"toRef,omitempty"`
	Action          string       `json:"action,omitempty"`
	List            bool         `json:"list,omitempty"`
	Screenshot      bool         `json:"screenshot,omitempty"`
	Search          string       `json:"search,omitempty"`
	Cursor          string       `json:"cursor,omitempty"`
	Limit           int          `json:"limit,omitempty"`
	Text            string       `json:"text,omitempty"`
	Key             string       `json:"key,omitempty"`
	Button          string       `json:"button,omitempty"`
	ClickCount      int          `json:"clickCount,omitempty"`
	Checked         *bool        `json:"checked,omitempty"`
	Values          []string     `json:"values,omitempty"`
	DeltaX          int          `json:"deltaX,omitempty"`
	DeltaY          int          `json:"deltaY,omitempty"`
	Accept          bool         `json:"accept,omitempty"`
	Expect          *UIPredicate `json:"expect,omitempty"`
	TimeoutMS       int          `json:"timeoutMs,omitempty"`
	VerifyTimeoutMS int          `json:"verifyTimeoutMs,omitempty"`
	Target          string       `json:"description,omitempty"`
	Region          *UIBounds    `json:"region,omitempty"`
	Visual          bool         `json:"visual,omitempty"`
}

type UIVerification struct {
	Status   string `json:"status"`
	Method   string `json:"method"`
	Evidence any    `json:"evidence,omitempty"`
	Code     string `json:"code,omitempty"`
}

type UISpecialistUsage struct {
	Calls            int    `json:"calls"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	Model            string `json:"model,omitempty"`
}

type UIResult struct {
	Observation  *UIObservation     `json:"observation,omitempty"`
	Target       *UITarget          `json:"target,omitempty"`
	Execution    string             `json:"execution,omitempty"`
	Verification *UIVerification    `json:"verification,omitempty"`
	Backend      string             `json:"backend,omitempty"`
	Recovery     string             `json:"recovery,omitempty"`
	Error        *Error             `json:"error,omitempty"`
	Specialist   *UISpecialistUsage `json:"specialist,omitempty"`
}

// Screenshots are UI previews and private specialist input, never planner input.
// Keep the bytes in memory but omit them from the ordinary tool-result JSON.
func (r UIResult) MarshalJSON() ([]byte, error) {
	type plain UIResult
	copy := plain(r)
	if r.Observation != nil {
		observation := *r.Observation
		if observation.Screenshot != nil {
			shot := *observation.Screenshot
			shot.DataBase64 = ""
			observation.Screenshot = &shot
		}
		copy.Observation = &observation
	}
	return json.Marshal(copy)
}

type UIVisionRequest struct {
	ImageDataURL  string
	Width, Height int
	Target        string
	Verify        bool
	Correction    string
}

// Implemented by the host using the selected Vision endpoint. No conversation
// history or tools are sent, even when Vision and Chat select the same model.
type UIVision func(context.Context, UIVisionRequest) (string, UISpecialistUsage, error)

type uiRecord struct {
	observation *UIObservation
	turn        string
	generation  uint64
	created     time.Time
}

type uiMemo struct {
	signature string
	result    *UIResult
	err       error
}

type uiState struct {
	gate         chan struct{}
	observations map[string]uiRecord
	requests     map[string]uiMemo
}

func (m *Manager) uiStateLocked(workspace string) *uiState {
	runtime := m.runtimeFor(workspace)
	if runtime.ui == nil {
		runtime.ui = &uiState{gate: make(chan struct{}, 1), observations: make(map[string]uiRecord), requests: make(map[string]uiMemo)}
	}
	return runtime.ui
}

type uiGateContextKey struct{}

// One queue for all UI backends, including legacy adapters. Context-aware
// acquisition lets Stop/Take Control cancel queued work before it reaches input.
func (m *Manager) lockGUI(ctx context.Context, workspace string) (context.Context, func(), error) {
	if ctx.Value(uiGateContextKey{}) == workspace {
		return ctx, func() {}, nil
	}
	m.mu.Lock()
	gate := m.uiStateLocked(workspace).gate
	m.mu.Unlock()
	select {
	case gate <- struct{}{}:
		return context.WithValue(ctx, uiGateContextKey{}, workspace), func() { <-gate }, nil
	case <-ctx.Done():
		return ctx, nil, ctx.Err()
	}
}

func uiFailure(code, message string) error { return &Error{Code: code, Message: message} }

func (m *Manager) uiBackend(ctx context.Context, state MachineState, surface, method string, params any) (json.RawMessage, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	if surface == "browser" {
		result, err = m.engine.BrowserCall(ctx, state, method, data)
	} else {
		engine, ok := m.engine.(NativeUIEngine)
		if !ok {
			return nil, uiFailure("ui_capability_unavailable", "Native accessibility requires a refreshed sandbox runtime image")
		}
		result, err = engine.NativeUICall(ctx, state, method, data)
	}
	if err != nil {
		code := ErrorCode(err)
		if code == "unknown_browser_action" || code == "not_found" || code == "sandbox_service_error" {
			return nil, uiFailure("ui_capability_unavailable", "UI tools require a refreshed sandbox runtime image; legacy tools remain available")
		}
	}
	return result, err
}

func (m *Manager) saveObservation(workspace, turn string, generation uint64, observation *UIObservation) {
	observation.ID = uuid.NewString()
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.uiStateLocked(workspace)
	// Bound screenshots as well as object counts. References expire at turn end,
	// after 10 minutes, or immediately when the ownership generation changes.
	for id, record := range state.observations {
		if record.turn != turn || record.generation != generation || time.Since(record.created) > 10*time.Minute {
			delete(state.observations, id)
		}
	}
	state.observations[observation.ID] = uiRecord{observation, turn, generation, time.Now()}
	for {
		bytes, oldestID := int64(0), ""
		oldest := time.Now()
		for id, record := range state.observations {
			if record.observation.Screenshot != nil {
				bytes += int64(len(record.observation.Screenshot.DataBase64))
			}
			if record.created.Before(oldest) {
				oldest, oldestID = record.created, id
			}
		}
		if (len(state.observations) <= 64 && bytes <= 32<<20) || oldestID == "" {
			break
		}
		delete(state.observations, oldestID)
	}
}

func (m *Manager) uiRecord(workspace, turn string, generation uint64, request UIRequest) (uiRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.uiStateLocked(workspace).observations[request.ObservationID]
	if !ok || record.turn != turn || record.generation != generation || time.Since(record.created) > 10*time.Minute {
		return uiRecord{}, uiFailure("ui_stale_observation", "Observation expired, belongs to another turn, or predates user control. Call ui_observe.")
	}
	if request.SurfaceID != "" && request.SurfaceID != record.observation.Surface.ID || request.Surface != "" && request.Surface != record.observation.Surface.Kind {
		return uiRecord{}, uiFailure("ui_surface_mismatch", "Observation belongs to a different surface")
	}
	for _, ref := range []string{request.Ref, request.ToRef, request.ScopeRef, predicateRef(request.Expect)} {
		if ref == "" {
			continue
		}
		found := false
		for _, target := range record.observation.Targets {
			if target.Ref == ref {
				found = true
				break
			}
		}
		if !found {
			return uiRecord{}, uiFailure("ui_unobserved_target", "Target was not present in this observation; observe its surface or scope again")
		}
	}
	return record, nil
}

func predicateRef(predicate *UIPredicate) string {
	if predicate != nil {
		return predicate.Ref
	}
	return ""
}

func (m *Manager) observeUI(ctx context.Context, state MachineState, workspace, turn string, generation uint64, request UIRequest) (*UIObservation, error) {
	if request.Surface == "browser" {
		m.mu.Lock()
		runtime := m.runtimeFor(workspace)
		invalidate := runtime.browserInvalidatedGeneration != generation || runtime.browserTurn != turn
		m.mu.Unlock()
		if invalidate {
			if _, err := m.engine.BrowserCall(ctx, state, "invalidate_references", nil); err != nil {
				return nil, err
			}
			m.mu.Lock()
			runtime.browserInvalidatedGeneration, runtime.browserTurn = generation, turn
			m.mu.Unlock()
		}
	}
	data, err := m.uiBackend(ctx, state, request.Surface, "ui_observe", request)
	if err != nil {
		return nil, err
	}
	var observation UIObservation
	if err := json.Unmarshal(data, &observation); err != nil {
		return nil, Wrap("ui_protocol_error", "UI observation was invalid", err)
	}
	if observation.Surface.Kind == "" {
		observation.Surface.Kind = request.Surface
	}
	if !request.List && (observation.Surface.ID == "" || observation.Surface.Epoch == "") {
		return nil, uiFailure("ui_protocol_error", "UI observation is missing surface identity")
	}
	if request.Screenshot && request.Surface == "desktop" {
		bytes, mediaType, captureErr := m.engine.DesktopScreenshot(ctx, state)
		if captureErr != nil {
			return nil, captureErr
		}
		observation.Screenshot = &UIImage{MediaType: mediaType, Bytes: int64(len(bytes)), DataBase64: base64.StdEncoding.EncodeToString(bytes), Space: "desktop-display", ScaleX: 1, ScaleY: 1}
	}
	if observation.Screenshot != nil {
		if err := prepareUIImage(observation.Screenshot); err != nil {
			return nil, err
		}
	}
	if err := m.checkAIControlContext(ctx, workspace); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	m.mu.Lock()
	if m.runtimeFor(workspace).controlGeneration == generation {
		if request.Surface == "browser" {
			m.runtimeFor(workspace).browserGeneration = generation
		}
		if observation.Screenshot != nil && request.Surface == "desktop" {
			m.runtimeFor(workspace).needsFreshDesktop = false
		}
	}
	m.mu.Unlock()
	m.saveObservation(workspace, turn, generation, &observation)
	return &observation, nil
}

func (m *Manager) UICall(ctx context.Context, workspace, turn, callID, method string, request UIRequest, vision UIVision) (*UIResult, error) {
	if turn == "" {
		return nil, uiFailure("ui_turn_required", "UI tools require an active chat turn")
	}
	ctx, unlock, err := m.lockGUI(ctx, workspace)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err = m.Start(ctx, workspace); err != nil {
		return nil, err
	}
	if err = m.checkAIControlContext(ctx, workspace); err != nil {
		return nil, err
	}
	lease, _, err := m.AcquireAIControl(workspace, turn, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	ctx, cancel := joinContexts(ctx, lease)
	defer cancel()
	generation, _ := m.AIControl(workspace)
	ctx = WithAIControlGeneration(ctx, generation)
	state, err := m.machineState(workspace)
	if err != nil {
		return nil, err
	}
	m.touch(workspace, 1)
	defer m.touch(workspace, -1)
	if request.TimeoutMS < 0 || request.TimeoutMS > 60000 || request.VerifyTimeoutMS < 0 || request.VerifyTimeoutMS > 30000 || len(request.Text) > 32768 || len(request.Target) > 4000 || len(request.Key) > 100 || request.Limit < 0 || request.Limit > 200 {
		return nil, uiFailure("invalid_arguments", "UI request exceeds its bounds")
	}
	if request.ClickCount < 0 || request.ClickCount > 3 || len(request.Values) > 100 || len(request.Search) > 2000 || request.DeltaX < -100000 || request.DeltaX > 100000 || request.DeltaY < -100000 || request.DeltaY > 100000 {
		return nil, uiFailure("invalid_arguments", "UI action exceeds its input limits")
	}
	if request.Button != "" && request.Button != "left" && request.Button != "middle" && request.Button != "right" {
		return nil, uiFailure("invalid_arguments", "Unsupported mouse button")
	}
	var record uiRecord
	if method != "ui_observe" || request.ScopeRef != "" {
		record, err = m.uiRecord(workspace, turn, generation, request)
		if err != nil {
			return nil, err
		}
		request.Surface = record.observation.Surface.Kind
		request.SurfaceID = record.observation.Surface.ID
	}
	if request.Surface == "" {
		request.Surface = "browser"
	}
	if request.Surface != "browser" && request.Surface != "desktop" {
		return nil, uiFailure("invalid_arguments", "surface must be browser or desktop")
	}
	if method == "ui_observe" {
		observation, err := m.observeUI(ctx, state, workspace, turn, generation, request)
		return &UIResult{Observation: observation, Backend: request.Surface}, err
	}
	if method == "ui_locate" || method == "ui_verify" && request.Visual {
		return m.visualUI(ctx, state, workspace, turn, generation, method, request, record, vision)
	}
	if method == "ui_verify" {
		data, err := m.uiBackend(ctx, state, request.Surface, method, request)
		if err != nil {
			return nil, err
		}
		var verification UIVerification
		if err := json.Unmarshal(data, &verification); err != nil {
			return nil, err
		}
		result := &UIResult{Verification: &verification, Backend: request.Surface}
		result.Observation, _ = m.observeUI(ctx, state, workspace, turn, generation, UIRequest{Surface: request.Surface, SurfaceID: request.SurfaceID})
		return result, nil
	}
	if method != "ui_act" {
		return nil, uiFailure("invalid_arguments", "Unknown UI operation")
	}
	if callID == "" {
		return nil, uiFailure("ui_request_id_required", "UI actions require the host tool-call ID")
	}
	data, _ := json.Marshal(request)
	signature := sha256.Sum256(data)
	key := turn + "\x00" + callID
	m.mu.Lock()
	prior, exists := m.uiStateLocked(workspace).requests[key]
	journalFull := len(m.uiStateLocked(workspace).requests) >= 10000
	m.mu.Unlock()
	if exists {
		if prior.signature != hex.EncodeToString(signature[:]) {
			return nil, uiFailure("ui_request_conflict", "Tool-call ID already used with different arguments")
		}
		return prior.result, prior.err
	}
	if journalFull {
		return nil, uiFailure("ui_request_limit", "Turn action journal is full; start a new turn before more UI actions")
	}
	var params map[string]any
	_ = json.Unmarshal(data, &params)
	params["requestId"] = uuid.NewSHA1(uuid.NameSpaceOID, []byte(workspace+"\x00"+key)).String()
	params["epoch"] = record.observation.Surface.Epoch
	for _, target := range record.observation.Targets {
		if target.Ref == request.Ref && target.Grounding == "visual" {
			switch request.Action {
			case "click", "hover", "type", "press", "scroll", "drag":
			default:
				return nil, uiFailure("ui_unsupported_action", "Visual targets support click, hover, type, press, scroll, and drag")
			}
			if err := m.validateVisualTarget(ctx, state, workspace, turn, generation, record, target); err != nil {
				return nil, err
			}
			x, y := sourcePoint(record.observation.Screenshot, *target.Bounds)
			delete(params, "ref")
			params["action"], params["x"], params["y"] = "point", x, y
			params["pointAction"] = request.Action
			if request.Action == "drag" {
				found := false
				for _, destination := range record.observation.Targets {
					if destination.Ref != request.ToRef || destination.Grounding != "visual" {
						continue
					}
					if err := m.validateVisualTarget(ctx, state, workspace, turn, generation, record, destination); err != nil {
						return nil, err
					}
					params["toX"], params["toY"] = sourcePoint(record.observation.Screenshot, *destination.Bounds)
					found = true
				}
				if !found {
					return nil, uiFailure("ui_unobserved_target", "Visual drag requires a visual destination from the same observation")
				}
			}
		}
	}
	if err := m.checkAIControlContext(ctx, workspace); err != nil {
		return nil, err
	}
	result := &UIResult{Execution: "unknown", Verification: &UIVerification{Status: "unverified", Method: "none"}, Backend: request.Surface, Recovery: "none"}
	data, err = m.uiBackend(ctx, state, request.Surface, method, params)
	if err != nil {
		result.Error = &Error{Code: ErrorCode(err), Message: "Action response was interrupted; execution is unknown. Observe before deciding whether another action is needed."}
		if ErrorCode(err) == "ui_capability_unavailable" {
			result.Execution = "not_started"
			result.Error = &Error{Code: ErrorCode(err), Message: err.Error()}
		}
	} else if err := json.Unmarshal(data, result); err != nil {
		result.Execution = "unknown"
		result.Error = &Error{Code: "ui_protocol_error", Message: "Action response was invalid; execution is unknown. Observe before continuing."}
	}
	// Preserve outcome evidence even if cancellation prevents a fresh observation.
	if ctx.Err() == nil {
		result.Observation, _ = m.observeUI(ctx, state, workspace, turn, generation, UIRequest{Surface: request.Surface, SurfaceID: request.SurfaceID})
	}
	m.mu.Lock()
	cached := *result
	cached.Observation = nil // Outcome deduplication need not retain whole old trees.
	m.uiStateLocked(workspace).requests[key] = uiMemo{hex.EncodeToString(signature[:]), &cached, nil}
	m.mu.Unlock()
	return result, nil
}

func (m *Manager) clearUITurnLocked(runtime *runtimeState, turn string) {
	if runtime.ui == nil {
		return
	}
	for id, record := range runtime.ui.observations {
		if record.turn == turn || strings.HasPrefix(record.turn, turn+":research:") {
			delete(runtime.ui.observations, id)
		}
	}
	for id := range runtime.ui.requests {
		if strings.HasPrefix(id, turn+"\x00") || strings.HasPrefix(id, turn+":research:") {
			delete(runtime.ui.requests, id)
		}
	}
}

func (b UIBounds) String() string {
	return fmt.Sprintf("%.1f,%.1f %.1fx%.1f", b.X, b.Y, b.Width, b.Height)
}
