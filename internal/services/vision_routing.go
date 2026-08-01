package services

import (
	"github.com/brent/echo/internal/llm"
)

// visionLLMRouter keeps the surface's normal model and the user-selected
// vision model available for a single agent run. Visual payloads are routed
// without changing the model configured for the rest of the surface.
type visionLLMRouter struct {
	defaultSettings llm.Settings
	defaultClient   *llm.Client
	visionSettings  llm.Settings
	visionClient    *llm.Client
}

func (s *SystemService) newVisionLLMRouter(workspaceID string, defaultSettings llm.Settings) (visionLLMRouter, error) {
	_, visionSettings, err := s.workspaceAndSettingsFor(workspaceID, llm.InteractionVision)
	if err != nil {
		return visionLLMRouter{}, err
	}
	defaultClient, err := s.newLLMClient(defaultSettings)
	if err != nil {
		return visionLLMRouter{}, err
	}
	visionClient, err := s.newLLMClient(visionSettings)
	if err != nil {
		return visionLLMRouter{}, err
	}
	return visionLLMRouter{
		defaultSettings: defaultSettings,
		defaultClient:   defaultClient,
		visionSettings:  visionSettings,
		visionClient:    visionClient,
	}, nil
}

func (r visionLLMRouter) route(messages []llm.Message) (llm.Settings, *llm.Client) {
	if messagesRequireVision(messages) {
		return r.visionSettings, r.visionClient
	}
	return r.defaultSettings, r.defaultClient
}

func messagesRequireVision(messages []llm.Message) bool {
	for _, message := range messages {
		for _, part := range message.ContentParts {
			if part.ImageURL != nil || part.VideoURL != nil {
				return true
			}
		}
	}
	return false
}
