package debugger

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type stepFrame = Frame
type stepOperation struct {
	ctx        context.Context
	cancel     context.CancelFunc
	events     chan dapEnvelope
	connection *dapConnection
	threadID   int
	source     func(context.Context, Source) (string, error)
}

func (s *Service) startSourceStep(current *session, command string, args map[string]any) (RequestResponse, bool) {
	s.mu.Lock()
	if current.launch == nil || current.launch.Step == nil || (command != "stepIn" && command != "stepOut") || args["granularity"] == "instruction" || !boolCapability(current.capabilities, "supportsSteppingGranularity") || current.status != StatusStopped || current.step != nil {
		s.mu.Unlock()
		return RequestResponse{}, false
	}
	ctx, cancel := context.WithCancel(current.ctx)
	op := &stepOperation{ctx: ctx, cancel: cancel, events: make(chan dapEnvelope, 8), connection: current.conn, threadID: intValue(args["threadId"]), source: func(ctx context.Context, source Source) (string, error) {
		return s.readAdapterSource(ctx, current.workspaceID, source.Path)
	}}
	current.step = op
	setSessionRunning(current)
	result := RequestResponse{WorkspaceID: current.workspaceID, SessionID: current.id, Revision: current.revision, StopGeneration: current.stopGeneration, Body: json.RawMessage(`{}`)}
	s.mu.Unlock()
	s.publishSession(current.workspaceID, current.id, "continued", nil, "")
	go func() {
		defer cancel()
		stop, message, err := current.launch.Step(ctx, op, command, args)
		if err != nil {
			if current.ctx.Err() != nil {
				return
			}
			message = "Source stepping stopped: " + err.Error()
			if ctx.Err() != nil {
				message = "Source stepping paused."
			}
			if stop == nil {
				pauseCtx, done := context.WithTimeout(current.ctx, 2*time.Second)
				_, _ = op.Request(pauseCtx, "pause", map[string]any{"threadId": op.threadID})
				select {
				case event := <-op.events:
					stop = decodeStop(event)
				case <-pauseCtx.Done():
				}
				done()
			}
		}
		s.mu.Lock()
		if current.step != op || isStoppingOrTerminal(current.status) {
			s.mu.Unlock()
			return
		}
		current.step = nil
		s.mu.Unlock()
		if message != "" {
			s.appendOutput(current.workspaceID, current.id, "adapter", message+"\n", nil)
		}
		if stop != nil {
			body := stop.Body
			if message != "" {
				var value map[string]any
				_ = json.Unmarshal(body, &value)
				if value == nil {
					value = map[string]any{}
				}
				value["description"] = message
				body = mustJSON(value)
			}
			s.handleDAPEvent(current.workspaceID, current.id, dapEnvelope{Event: "stopped", Body: body})
		}
	}()
	return result, true
}
func (s *Service) routeStepEvent(id, sessionID string, event dapEnvelope) bool {
	if event.Event != "stopped" && event.Event != "continued" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.sessionLocked(id, sessionID)
	if err != nil || current.step == nil {
		return false
	}
	if event.Event == "stopped" {
		select {
		case current.step.events <- event:
		default:
			current.step.cancel()
		}
	}
	return true
}
func decodeStop(event dapEnvelope) *StopEvent {
	var stop StopEvent
	_ = json.Unmarshal(event.Body, &stop)
	stop.Body = event.Body
	return &stop
}
func (op *stepOperation) Request(ctx context.Context, command string, args map[string]any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	response, err := op.connection.request(ctx, command, args)
	return response.Body, err
}
func (op *stepOperation) Execute(ctx context.Context, command string, args map[string]any) (*StopEvent, error) {
	if _, err := op.Request(ctx, command, args); err != nil {
		return nil, err
	}
	select {
	case event := <-op.events:
		return decodeStop(event), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (op *stepOperation) Frames(ctx context.Context, thread, levels int) ([]Frame, error) {
	raw, err := op.Request(ctx, "stackTrace", map[string]any{"threadId": thread, "startFrame": 0, "levels": levels})
	if err != nil {
		return nil, err
	}
	var body struct {
		Frames []Frame `json:"stackFrames"`
	}
	if err = json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	if len(body.Frames) == 0 {
		return nil, fmt.Errorf("debugger returned no stack frames")
	}
	return body.Frames, nil
}
func (op *stepOperation) Source(ctx context.Context, source Source) (string, error) {
	return op.source(ctx, source)
}
func mustJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
