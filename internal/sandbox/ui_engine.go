package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
)

// Optional additive protocol-3 capability; older/custom engines keep their
// legacy browser and desktop operations without implementing this interface.
type NativeUIEngine interface {
	NativeUICall(context.Context, MachineState, string, json.RawMessage) (json.RawMessage, error)
}

func (e *DockerEngine) NativeUICall(ctx context.Context, state MachineState, method string, params json.RawMessage) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]any{"method": method, "params": params})
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	token := e.secret[state.WorkspaceID].RuntimeAgentToken
	e.mu.Unlock()
	data, _, err := e.serviceRequest(ctx, state, "runtime", agentPort, http.MethodPost, "/v1/ui/call", token, payload, 4<<20)
	if err != nil {
		return nil, err
	}
	var response struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Code  string          `json:"code"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, Wrap("ui_protocol_error", "Accessibility service returned invalid JSON", err)
	}
	if !response.OK {
		return nil, &Error{Code: response.Code, Message: response.Error}
	}
	return response.Data, nil
}
