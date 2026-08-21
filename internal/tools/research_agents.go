package tools

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

const (
	MaxResearchAgentsPerTurn = 8
	MaxResearchWaitSeconds   = 120
)

const (
	ResearchAgentsSpawnToolName  = "research_agents_spawn"
	ResearchAgentSendToolName    = "research_agent_send"
	ResearchAgentsWaitToolName   = "research_agents_wait"
	ResearchAgentsCancelToolName = "research_agents_cancel"
)

func init() {
	Register(ToolFunc{Meta: Metadata{
		Name:        ResearchAgentsSpawnToolName,
		Description: "Spawn one or more focused read-only research agents concurrently. Each agent keeps a private transcript and returns a bounded report.",
		Parameters: Schema{
			"type": "object", "additionalProperties": false, "required": []any{"agents"},
			"properties": map[string]any{
				"agents": map[string]any{
					"type": "array", "minItems": 1, "maxItems": MaxResearchAgentsPerTurn,
					"items": map[string]any{
						"type": "object", "additionalProperties": false, "required": []any{"task"},
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "description": "Short optional display name."},
							"task": map[string]any{"type": "string", "description": "Focused research question and expected evidence."},
						},
					},
				},
			},
		},
	}, Run: runResearchAgentsSpawn})

	Register(ToolFunc{Meta: Metadata{
		Name:        ResearchAgentSendToolName,
		Description: "Queue a focused follow-up question for an existing research agent. The agent retains its private research transcript.",
		Parameters: Schema{
			"type": "object", "additionalProperties": false, "required": []any{"agentId", "message"},
			"properties": map[string]any{
				"agentId": map[string]any{"type": "string"},
				"message": map[string]any{"type": "string"},
			},
		},
	}, Run: runResearchAgentSend})

	Register(ToolFunc{Meta: Metadata{
		Name:        ResearchAgentsWaitToolName,
		Description: "Wait for research agents and collect bounded reports. Repeated calls return current status without exposing private transcripts.",
		Parameters: Schema{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"agentIds":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Agents to wait for; omit for all agents in this turn."},
				"waitFor":        map[string]any{"type": "string", "enum": []any{"any", "all"}, "description": "Defaults to all."},
				"timeoutSeconds": map[string]any{"type": "integer", "minimum": 1, "maximum": MaxResearchWaitSeconds, "description": "Defaults to 60 seconds."},
			},
		},
	}, Run: runResearchAgentsWait})

	Register(ToolFunc{Meta: Metadata{
		Name:        ResearchAgentsCancelToolName,
		Description: "Cancel selected research agents, or all agents in this turn when agentIds is omitted.",
		Parameters: Schema{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"agentIds": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
		},
	}, Run: runResearchAgentsCancel})
}

func researchCoordinator(ctx ExecutionContext) (ResearchAgentCoordinator, error) {
	if ctx.ResearchAgents == nil {
		return nil, SafeError{Code: "research_agents_unavailable", Message: "research agents are unavailable in this tool context"}
	}
	return ctx.ResearchAgents, nil
}

func runResearchAgentsSpawn(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	coordinator, err := researchCoordinator(ctx)
	if err != nil {
		return nil, err
	}
	var args struct {
		Agents []ResearchAgentSpec `json:"agents"`
	}
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	if len(args.Agents) == 0 || len(args.Agents) > MaxResearchAgentsPerTurn {
		return nil, SafeError{Code: "invalid_arguments", Message: "agents must contain between 1 and 8 items"}
	}
	for index := range args.Agents {
		args.Agents[index].Name = strings.TrimSpace(args.Agents[index].Name)
		args.Agents[index].Task = strings.TrimSpace(args.Agents[index].Task)
		if args.Agents[index].Task == "" {
			return nil, SafeError{Code: "invalid_arguments", Message: "each research agent requires a task"}
		}
	}
	return coordinator.SpawnResearchAgents(ctx.context(), args.Agents)
}

func runResearchAgentSend(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	coordinator, err := researchCoordinator(ctx)
	if err != nil {
		return nil, err
	}
	var args struct {
		AgentID string `json:"agentId"`
		Message string `json:"message"`
	}
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	args.AgentID, args.Message = strings.TrimSpace(args.AgentID), strings.TrimSpace(args.Message)
	if args.AgentID == "" || args.Message == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "agentId and message are required"}
	}
	return coordinator.SendResearchAgentMessage(ctx.context(), args.AgentID, args.Message)
}

func runResearchAgentsWait(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	coordinator, err := researchCoordinator(ctx)
	if err != nil {
		return nil, err
	}
	var args struct {
		AgentIDs       []string `json:"agentIds"`
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
	return coordinator.WaitResearchAgents(ctx.context(), args.AgentIDs, args.WaitFor, time.Duration(timeoutSeconds)*time.Second)
}

func runResearchAgentsCancel(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	coordinator, err := researchCoordinator(ctx)
	if err != nil {
		return nil, err
	}
	var args struct {
		AgentIDs []string `json:"agentIds"`
	}
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	return coordinator.CancelResearchAgents(context.WithoutCancel(ctx.context()), args.AgentIDs)
}
