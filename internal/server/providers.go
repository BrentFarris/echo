package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"strings"
)

type providerKey struct{}
type providerInvocation struct {
	Scope      domain.ExecutionScope
	Endpoint   domain.Endpoint
	Generation domain.Generation
	Credential string
	Emit       func(model.Delta)
	Bytes      int
	Err        error
}

func (s *Server) complete(ctx context.Context, endpoint domain.Endpoint, generation domain.Generation, messages []model.Message, tools []domain.Tool, key string, emit func(model.Delta)) (model.Result, error) {
	if endpoint.Provider == "" {
		return s.Model.Complete(ctx, endpoint, generation, messages, tools, key, emit)
	}
	parent, ok := ctx.Value(providerKey{}).(*providerInvocation)
	if !ok {
		return model.Result{}, errors.New("custom provider requires a bot execution scope")
	}
	manifest, e := s.Plugins.Provider(parent.Scope.Bot, endpoint.Provider)
	if e != nil {
		return model.Result{}, e
	}
	if manifest.Builtin {
		return s.Model.Complete(ctx, endpoint, generation, messages, tools, key, emit)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	invocation := *parent
	invocation.Endpoint = endpoint
	invocation.Generation = generation
	invocation.Credential = key
	invocation.Bytes = 0
	invocation.Err = nil
	invocation.Emit = func(delta model.Delta) {
		invocation.Bytes += len(delta.Content) + len(delta.Reasoning)
		if invocation.Bytes > 8<<20 {
			invocation.Err = errors.New("provider stream exceeds 8 MiB")
			cancel()
			return
		}
		if emit != nil {
			emit(delta)
		}
	}
	if tools == nil {
		tools = []domain.Tool{}
	}
	input, _ := json.Marshal(map[string]any{"messages": messages, "tools": tools, "generation": generation})
	if len(input) > 4<<20 {
		return model.Result{}, errors.New("provider input exceeds 4 MiB; reduce the context window")
	}
	value, e := s.Plugins.Invoke(context.WithValue(ctx, providerKey{}, &invocation), invocation.Scope, endpoint.Provider, input)
	if invocation.Err != nil {
		return model.Result{}, invocation.Err
	}
	if e != nil {
		return model.Result{}, e
	}
	var result model.Result
	raw, e := json.Marshal(value)
	if e == nil {
		e = json.Unmarshal(raw, &result)
	}
	if e != nil {
		return result, e
	}
	if key != "" {
		result.Content = strings.ReplaceAll(result.Content, key, "[redacted]")
		result.Reasoning = strings.ReplaceAll(result.Reasoning, key, "[redacted]")
		if result.Overflow != nil {
			result.Overflow.Message = strings.ReplaceAll(result.Overflow.Message, key, "[redacted]")
		}
	}
	if result.Overflow != nil {
		return result, result.Overflow
	}
	seen := map[string]bool{}
	for i := range result.Calls {
		call := &result.Calls[i]
		if call.ID == "" {
			call.ID = domain.ID()
		}
		if call.Function.Arguments == "" {
			call.Function.Arguments = "{}"
		}
		if seen[call.ID] || !json.Valid([]byte(call.Function.Arguments)) || call.Function.Name == "" {
			return result, errors.New("provider returned invalid or duplicate tool calls")
		}
		seen[call.ID] = true
		call.Type = "function"
	}
	return result, nil
}

func (s *Server) providerOperation(ctx context.Context, scope domain.ExecutionScope, method string, args json.RawMessage) (any, error) {
	invocation, ok := ctx.Value(providerKey{}).(*providerInvocation)
	if !ok {
		return nil, errors.New("model capabilities are only available during a provider invocation")
	}
	manifest, e := s.Plugins.Provider(scope.Bot, invocation.Endpoint.Provider)
	if e != nil || manifest.ID != scope.PluginID {
		return nil, errors.New("provider does not own this model invocation")
	}
	switch method {
	case "model.request":
		return s.Model.ProviderRequest(ctx, invocation.Endpoint, invocation.Credential, args)
	case "model.emit":
		var d model.Delta
		var value struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
		}
		if e = json.Unmarshal(args, &value); e != nil {
			return nil, e
		}
		d.Content = value.Content
		d.Reasoning = value.Reasoning
		if invocation.Emit != nil {
			invocation.Emit(d)
		}
		return true, nil
	case "model.complete":
		var input struct {
			Messages []model.Message `json:"messages"`
			Tools    []domain.Tool   `json:"tools"`
		}
		if e = json.Unmarshal(args, &input); e != nil {
			return nil, e
		}
		result, err := s.Model.Complete(ctx, invocation.Endpoint, invocation.Generation, input.Messages, input.Tools, invocation.Credential, invocation.Emit)
		// WASI errors are text on the guest wire; retain typed overflow at the host boundary.
		if model.IsContextOverflow(err) {
			invocation.Err = err
		}
		return result, err
	}
	return nil, errors.New("unknown model capability")
}
