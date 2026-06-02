package llm

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	DefaultEndpoint      = "http://localhost:11434/v1"
	DefaultModel         = "Qwen3.6-35B-A3B"
	DefaultContextLength = 262144
	DefaultMaxTokens     = 32168
)

type Settings struct {
	Endpoint          string  `json:"endpoint"`
	Model             string  `json:"model"`
	Temperature       float64 `json:"temperature"`
	TopK              int     `json:"topK"`
	TopP              float64 `json:"topP"`
	MinP              float64 `json:"minP"`
	ContextLength     int     `json:"contextLength"`
	MaxTokens         int     `json:"maxTokens"`
	FrequencyPenalty  float64 `json:"frequencyPenalty"`
	PresencePenalty   float64 `json:"presencePenalty"`
	RepetitionPenalty float64 `json:"repetitionPenalty"`
	TimeoutSeconds    int     `json:"timeoutSeconds"`
}

func DefaultSettings() Settings {
	return Settings{
		Endpoint:          DefaultEndpoint,
		Model:             DefaultModel,
		Temperature:       0.6,
		TopK:              20,
		TopP:              0.95,
		MinP:              0,
		ContextLength:     DefaultContextLength,
		MaxTokens:         DefaultMaxTokens,
		PresencePenalty:   1.5,
		RepetitionPenalty: 1,
		TimeoutSeconds:    120,
	}
}

func (s Settings) Normalized() Settings {
	s.Endpoint = strings.TrimSpace(s.Endpoint)
	s.Model = strings.TrimSpace(s.Model)
	if s.ContextLength == 0 {
		s.ContextLength = DefaultContextLength
	}
	if s.MaxTokens == 0 {
		s.MaxTokens = DefaultMaxTokens
	}
	if s.RepetitionPenalty == 0 {
		s.RepetitionPenalty = 1
	}
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = 120
	}
	return s
}

func (s Settings) Validate() error {
	s = s.Normalized()
	if s.Endpoint == "" {
		return fmt.Errorf("endpoint is required")
	}
	if s.Model == "" {
		return fmt.Errorf("model is required")
	}

	parsed, err := url.ParseRequestURI(s.Endpoint)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("endpoint must be a valid HTTP or HTTPS URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("endpoint must use http or https")
	}

	if s.Temperature < 0 || s.Temperature > 2 {
		return fmt.Errorf("temperature must be between 0 and 2")
	}
	if s.TopK < 0 {
		return fmt.Errorf("top-k cannot be negative")
	}
	if s.TopP < 0 || s.TopP > 1 {
		return fmt.Errorf("top-p must be between 0 and 1")
	}
	if s.MinP < 0 || s.MinP > 1 {
		return fmt.Errorf("min-p must be between 0 and 1")
	}
	if s.ContextLength < 1 {
		return fmt.Errorf("context length must be at least 1")
	}
	if s.MaxTokens < 1 {
		return fmt.Errorf("max tokens must be at least 1")
	}
	if s.FrequencyPenalty < -2 || s.FrequencyPenalty > 2 {
		return fmt.Errorf("frequency penalty must be between -2 and 2")
	}
	if s.PresencePenalty < -2 || s.PresencePenalty > 2 {
		return fmt.Errorf("presence penalty must be between -2 and 2")
	}
	if s.RepetitionPenalty < 0 {
		return fmt.Errorf("repetition penalty cannot be negative")
	}
	if s.TimeoutSeconds < 1 {
		return fmt.Errorf("timeout must be at least 1 second")
	}
	return nil
}
