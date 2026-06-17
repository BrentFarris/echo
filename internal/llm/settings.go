package llm

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/brent/echo/internal/searxng"
)

const (
	DefaultEndpoint      = "http://localhost:11434/v1"
	DefaultModel         = "Qwen3.6-35B-A3B"
	DefaultContextLength = 262144
	DefaultMaxTokens     = 32168
	DefaultSearxngURL    = searxng.DefaultURL
	defaultTimout        = 600
)

type Settings struct {
	Endpoint                        string  `json:"endpoint"`
	Model                           string  `json:"model"`
	Temperature                     float64 `json:"temperature"`
	TopK                            int     `json:"topK"`
	TopP                            float64 `json:"topP"`
	MinP                            float64 `json:"minP"`
	ContextLength                   int     `json:"contextLength"`
	MaxTokens                       int     `json:"maxTokens"`
	FrequencyPenalty                float64 `json:"frequencyPenalty"`
	PresencePenalty                 float64 `json:"presencePenalty"`
	RepetitionPenalty               float64 `json:"repetitionPenalty"`
	TimeoutSeconds                  int     `json:"timeoutSeconds"`
	SearxngURL                      string  `json:"searxngUrl"`
	EnableThinking                  bool    `json:"enableThinking"`
	ThinkingCorrection              bool    `json:"thinkingCorrection,omitempty"`
	HideLeadingWhitespaceIndicators bool    `json:"hideLeadingWhitespaceIndicators,omitempty"`
	DisableNotificationSounds       bool    `json:"disableNotificationSounds,omitempty"`
	Theme                           Theme   `json:"theme,omitempty"`
}

type Theme struct {
	Light map[string]string `json:"light,omitempty"`
	Dark  map[string]string `json:"dark,omitempty"`
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
		RepetitionPenalty: 1.05,
		TimeoutSeconds:    defaultTimout,
		SearxngURL:        DefaultSearxngURL,
		EnableThinking:    true,
	}
}

func (s Settings) Normalized() Settings {
	s.Endpoint = strings.TrimSpace(s.Endpoint)
	s.Model = strings.TrimSpace(s.Model)
	s.SearxngURL = strings.TrimSpace(s.SearxngURL)
	if s.SearxngURL == "" {
		s.SearxngURL = DefaultSearxngURL
	}
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
		s.TimeoutSeconds = defaultTimout
	}
	s.Theme = s.Theme.Normalized()
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

	if err := validateHTTPURL(s.Endpoint, "endpoint"); err != nil {
		return err
	}
	if err := validateHTTPURL(s.SearxngURL, "searxng url"); err != nil {
		return err
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
	if err := s.Theme.Validate(); err != nil {
		return err
	}
	return nil
}

func (t Theme) Normalized() Theme {
	return Theme{
		Light: normalizeThemePalette(t.Light),
		Dark:  normalizeThemePalette(t.Dark),
	}
}

func (t Theme) Validate() error {
	if err := validateThemePalette("light", t.Light); err != nil {
		return err
	}
	return validateThemePalette("dark", t.Dark)
}

func normalizeThemePalette(palette map[string]string) map[string]string {
	if len(palette) == 0 {
		return nil
	}
	normalized := make(map[string]string, len(palette))
	for key, value := range palette {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if color, ok := normalizeHexColor(value); ok {
			value = color
		}
		normalized[key] = value
	}
	return normalized
}

func validateThemePalette(name string, palette map[string]string) error {
	for key, value := range palette {
		if !isValidThemeTokenKey(key) {
			return fmt.Errorf("theme %s color token %q is invalid", name, key)
		}
		if _, ok := normalizeHexColor(value); !ok {
			return fmt.Errorf("theme %s color %q must be a 3- or 6-digit hex color", name, key)
		}
	}
	return nil
}

func isValidThemeTokenKey(value string) bool {
	if value == "" {
		return false
	}
	for index, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' {
			continue
		}
		if index > 0 && (char >= '0' && char <= '9' || char == '-' || char == '_') {
			continue
		}
		return false
	}
	return true
}

func normalizeHexColor(value string) (string, bool) {
	if len(value) != 4 && len(value) != 7 {
		return "", false
	}
	if value[0] != '#' {
		return "", false
	}
	var builder strings.Builder
	builder.WriteByte('#')
	for index := 1; index < len(value); index++ {
		char := value[index]
		var normalized byte
		if char >= '0' && char <= '9' {
			normalized = char
		} else if char >= 'a' && char <= 'f' {
			normalized = char
		} else if char >= 'A' && char <= 'F' {
			normalized = char + ('a' - 'A')
		} else {
			return "", false
		}
		builder.WriteByte(normalized)
		if len(value) == 4 {
			builder.WriteByte(normalized)
		}
	}
	return builder.String(), true
}

func validateHTTPURL(value string, label string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" {
		if label == "endpoint" {
			return fmt.Errorf("endpoint must be a valid HTTP or HTTPS URL")
		}
		return fmt.Errorf("%s must be a valid HTTP or HTTPS URL", label)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		if label == "endpoint" {
			return fmt.Errorf("endpoint must use http or https")
		}
		return fmt.Errorf("%s must use http or https", label)
	}
	return nil
}
