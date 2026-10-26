package tools

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

const (
	WorkerAgentsSpawnToolName  = "worker_agents_spawn"
	WorkerAgentSendToolName    = "worker_agent_send"
	WorkerAgentsWaitToolName   = "worker_agents_wait"
	WorkerAgentsCancelToolName = "worker_agents_cancel"
)

func init() {
	Register(ToolFunc{Meta: Metadata{
		Name:        WorkerAgentsSpawnToolName,
		Description: "Spawn one or more implementation worker subagents concurrently. Each worker keeps a private transcript, can edit workspace files and run commands, and returns a bounded report. Assign non-overlapping file scopes across workers.",
		Parameters: Schema{
			"type": "object", "additionalProperties": false, "required": []any{"workers"},
			"properties": map[string]any{
				"workers": map[string]any{
					"type": "array", "minItems": 1, "maxItems": MaxResearchAgentsPerTurn,
					"items": map[string]any{
						"type": "object", "additionalProperties": false, "required": []any{"task"},
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "description": "Short optional display name."},
							"task": map[string]any{"type": "string", "description": "Focused implementation task including the files the worker may touch."},
						},
					},
				},
			},
		},
	}, Run: runWorkerAgentsSpawn})

	Register(ToolFunc{Meta: Metadata{
		Name:        WorkerAgentSendToolName,
		Description: "Queue a focused follow-up instruction for an existing worker subagent. The worker retains its private transcript.",
		Parameters: Schema{
			"type": "object", "additionalProperties": false, "required": []any{"workerId", "message"},
			"properties": map[string]any{
				"workerId": map[string]any{"type": "string"},
				"message":  map[string]any{"type": "string"},
			},
		},
	}, Run: runWorkerAgentSend})

	Register(ToolFunc{Meta: Metadata{
		Name:        WorkerAgentsWaitToolName,
		Description: "Wait for worker subagents and collect bounded reports. Repeated calls return current status without exposing private transcripts.",
		Parameters: Schema{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"workerIds":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Workers to wait for; omit for all workers in this turn."},
				"waitFor":        map[string]any{"type": "string", "enum": []any{"any", "all"}, "description": "Defaults to all."},
				"timeoutSeconds": map[string]any{"type": "integer", "minimum": 1, "maximum": MaxResearchWaitSeconds, "description": "Defaults to 60 seconds."},
			},
		},
	}, Run: runWorkerAgentsWait})

	Register(ToolFunc{Meta: Metadata{
		Name:        WorkerAgentsCancelToolName,
		Description: "Cancel selected worker subagents, or all workers in this turn when workerIds is omitted.",
		Parameters: Schema{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"workerIds": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
		},
	}, Run: runWorkerAgentsCancel})
}

func workerCoordinator(ctx ExecutionContext) (WorkerAgentCoordinator, error) {
	if ctx.WorkerAgents == nil {
		return nil, SafeError{Code: "worker_agents_unavailable", Message: "worker agents are unavailable in this tool context"}
	}
	return ctx.WorkerAgents, nil
}

func runWorkerAgentsSpawn(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	coordinator, err := workerCoordinator(ctx)
	if err != nil {
		return nil, err
	}
	var args struct {
		Workers []WorkerAgentSpec `json:"workers"`
	}
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	if len(args.Workers) == 0 || len(args.Workers) > MaxResearchAgentsPerTurn {
		return nil, SafeError{Code: "invalid_arguments", Message: "workers must contain between 1 and 8 items"}
	}
	for index := range args.Workers {
		args.Workers[index].Name = strings.TrimSpace(args.Workers[index].Name)
		args.Workers[index].Task = strings.TrimSpace(args.Workers[index].Task)
		if args.Workers[index].Task == "" {
			return nil, SafeError{Code: "invalid_arguments", Message: "each worker requires a task"}
		}
	}
	return coordinator.SpawnWorkerAgents(ctx.context(), args.Workers)
}

func runWorkerAgentSend(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	coordinator, err := workerCoordinator(ctx)
	if err != nil {
		return nil, err
	}
	var args struct {
		WorkerID string `json:"workerId"`
		Message  string `json:"message"`
	}
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	args.WorkerID, args.Message = strings.TrimSpace(args.WorkerID), strings.TrimSpace(args.Message)
	if args.WorkerID == "" || args.Message == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "workerId and message are required"}
	}
	return coordinator.SendWorkerAgentMessage(ctx.context(), args.WorkerID, args.Message)
}

func runWorkerAgentsWait(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	coordinator, err := workerCoordinator(ctx)
	if err != nil {
		return nil, err
	}
	var args struct {
		WorkerIDs      []string `json:"workerIds"`
		WaitFor        string   `json:"waitFor"`
		TimeoutSeconds *int     `json:"timeoutSeconds"`
	}
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	args.WaitFor = strings.ToLower(strings.TrimSpace(args.WaitFor))
	if args.WaitFor == "" {
		args.WaitFor = "all"
	}
	if args.WaitFor != "all" && args.WaitFor != "any" {
		return nil, SafeError{Code: "invalid_arguments", Message: "waitFor must be any or all"}
	}
	timeoutSeconds := 60
	if args.TimeoutSeconds != nil {
		timeoutSeconds = *args.TimeoutSeconds
		if timeoutSeconds < 1 || timeoutSeconds > MaxResearchWaitSeconds {
			return nil, SafeError{Code: "invalid_arguments", Message: "timeoutSeconds must be between 1 and 120"}
		}
	}
	return coordinator.WaitWorkerAgents(ctx.context(), args.WorkerIDs, args.WaitFor, time.Duration(timeoutSeconds)*time.Second)
}

func runWorkerAgentsCancel(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	coordinator, err := workerCoordinator(ctx)
	if err != nil {
		return nil, err
	}
	var args struct {
		WorkerIDs []string `json:"workerIds"`
	}
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	return coordinator.CancelWorkerAgents(context.WithoutCancel(ctx.context()), args.WorkerIDs)
}
