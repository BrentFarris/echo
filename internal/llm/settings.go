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
	DefaultEndpointID    = "default"
	DefaultEndpointName  = "Default"
	DefaultContextLength = 262144
	DefaultMaxTokens     = 32168
	DefaultSearxngURL    = searxng.DefaultURL
	defaultTimout        = 600
)

type Interaction string

const (
	InteractionChat            Interaction = "chat"
	InteractionKanbanDecompose Interaction = "kanbanDecompose"
	InteractionKanban          Interaction = "kanban"
	InteractionInlineCode      Interaction = "inlineCode"
)

type LLMEndpoint struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	Endpoint            string  `json:"endpoint"`
	Model               string  `json:"model"`
	Temperature         float64 `json:"temperature"`
	TopK                int     `json:"topK"`
	TopP                float64 `json:"topP"`
	MinP                float64 `json:"minP"`
	ContextLength       int     `json:"contextLength"`
	MaxTokens           int     `json:"maxTokens"`
	FrequencyPenalty    float64 `json:"frequencyPenalty"`
	PresencePenalty     float64 `json:"presencePenalty"`
	RepetitionPenalty   float64 `json:"repetitionPenalty"`
	TimeoutSeconds      int     `json:"timeoutSeconds"`
	ThinkingTokenBudget int     `json:"thinkingTokenBudget"`
	ThinkingCorrection  bool    `json:"thinkingCorrection,omitempty"`
}

type EndpointSelection struct {
	Chat            string `json:"chat"`
	KanbanDecompose string `json:"kanbanDecompose"`
	Kanban          string `json:"kanban"`
	InlineCode      string `json:"inlineCode"`
}

type Settings struct {
	Endpoint                        string            `json:"endpoint"`
	Model                           string            `json:"model"`
	Endpoints                       []LLMEndpoint     `json:"endpoints,omitempty"`
	EndpointSelection               EndpointSelection `json:"endpointSelection,omitempty"`
	Temperature                     float64           `json:"temperature"`
	TopK                            int               `json:"topK"`
	TopP                            float64           `json:"topP"`
	MinP                            float64           `json:"minP"`
	ContextLength                   int               `json:"contextLength"`
	MaxTokens                       int               `json:"maxTokens"`
	FrequencyPenalty                float64           `json:"frequencyPenalty"`
	PresencePenalty                 float64           `json:"presencePenalty"`
	RepetitionPenalty               float64           `json:"repetitionPenalty"`
	TimeoutSeconds                  int               `json:"timeoutSeconds"`
	SearxngURL                      string            `json:"searxngUrl"`
	ThinkingTokenBudget             int               `json:"thinkingTokenBudget"`
	ThinkingCorrection              bool              `json:"thinkingCorrection,omitempty"`
	HideLeadingWhitespaceIndicators bool              `json:"hideLeadingWhitespaceIndicators,omitempty"`
	DisableNotificationSounds       bool              `json:"disableNotificationSounds,omitempty"`
	EnableChatCompletionNotifications bool            `json:"enableChatCompletionNotifications,omitempty"`
	EnableKanbanCompleteNotifications   bool            `json:"enableKanbanCompleteNotifications,omitempty"`
	LimitKanbanConcurrency          bool              `json:"limitKanbanConcurrency,omitempty"`
	DisableGitSplitDiffView         bool              `json:"disableGitSplitDiffView,omitempty"`
	ComfyuiURL                      string            `json:"comfyuiUrl"`
	ComfyuiDefaultCheckpoint        string            `json:"comfyuiDefaultCheckpoint"`
	ComfyuiTxt2imgWorkflow          string            `json:"comfyuiTxt2imgWorkflow"`
	ComfyuiImg2imgWorkflow          string            `json:"comfyuiImg2imgWorkflow"`
	Theme                           Theme             `json:"theme,omitempty"`
}

type Theme struct {
	Light map[string]string `json:"light,omitempty"`
	Dark  map[string]string `json:"dark,omitempty"`
}

func DefaultSettings() Settings {
	endpoint := defaultLLMEndpoint()
	return Settings{
		Endpoint:            DefaultEndpoint,
		Model:               DefaultModel,
		Endpoints:           []LLMEndpoint{endpoint},
		EndpointSelection:   defaultEndpointSelection(endpoint.ID),
		Temperature:         0.6,
		TopK:                20,
		TopP:                0.95,
		MinP:                0,
		ContextLength:       DefaultContextLength,
		MaxTokens:           DefaultMaxTokens,
		PresencePenalty:     1.5,
		RepetitionPenalty:   1.05,
		TimeoutSeconds:      defaultTimout,
		SearxngURL:          DefaultSearxngURL,
		ThinkingTokenBudget: -1,
	}
}

func (s Settings) Normalized() Settings {
	s.Endpoint = strings.TrimSpace(s.Endpoint)
	s.Model = strings.TrimSpace(s.Model)
	s.SearxngURL = strings.TrimSpace(s.SearxngURL)
	if s.SearxngURL == "" {
		s.SearxngURL = DefaultSearxngURL
	}
	s = normalizeSettingsGeneration(s)
	s.Endpoints = normalizeLLMEndpoints(s.Endpoints, s)
	s.EndpointSelection = normalizeEndpointSelection(s.EndpointSelection, s.Endpoints)
	if s.Endpoint != "" || s.Model != "" {
		s.Endpoints = applyLegacyEndpointFields(s.Endpoints, s.EndpointSelection.Chat, s)
	}
	if endpoint, ok := endpointByID(s.Endpoints, s.EndpointSelection.Chat); ok {
		s = endpoint.ApplyToSettings(s)
	}
	s.SearxngURL = strings.TrimSpace(s.SearxngURL)
	if s.SearxngURL == "" {
		s.SearxngURL = DefaultSearxngURL
	}
	s.ComfyuiURL = strings.TrimSpace(s.ComfyuiURL)
	s.ComfyuiTxt2imgWorkflow = strings.TrimSpace(s.ComfyuiTxt2imgWorkflow)
	s.ComfyuiImg2imgWorkflow = strings.TrimSpace(s.ComfyuiImg2imgWorkflow)
	s.Theme = s.Theme.Normalized()
	return s
}

func normalizeSettingsGeneration(s Settings) Settings {
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
	return s
}

func (s Settings) Clone() Settings {
	s.Endpoints = append([]LLMEndpoint(nil), s.Endpoints...)
	s.Theme = s.Theme.Clone()
	return s
}

func (s Settings) ForInteraction(interaction Interaction) Settings {
	s = s.Normalized()
	endpointID := s.EndpointSelection.Chat
	switch interaction {
	case InteractionKanbanDecompose:
		endpointID = s.EndpointSelection.KanbanDecompose
	case InteractionKanban:
		endpointID = s.EndpointSelection.Kanban
	case InteractionInlineCode:
		endpointID = s.EndpointSelection.InlineCode
	}
	if endpoint, ok := endpointByID(s.Endpoints, endpointID); ok {
		s = endpoint.ApplyToSettings(s)
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
	if len(s.Endpoints) == 0 {
		return fmt.Errorf("endpoint is required")
	}
	seenNames := map[string]struct{}{}
	for _, endpoint := range s.Endpoints {
		if endpoint.Name == "" {
			return fmt.Errorf("endpoint name is required")
		}
		if endpoint.Endpoint == "" {
			return fmt.Errorf("endpoint is required")
		}
		if endpoint.Model == "" {
			return fmt.Errorf("model is required")
		}
		if err := validateHTTPURL(endpoint.Endpoint, "endpoint"); err != nil {
			return err
		}
		if err := endpoint.ValidateGeneration(); err != nil {
			return err
		}
		nameKey := strings.ToLower(endpoint.Name)
		if _, exists := seenNames[nameKey]; exists {
			return fmt.Errorf("endpoint names must be unique")
		}
		seenNames[nameKey] = struct{}{}
	}
	if err := validateHTTPURL(s.SearxngURL, "searxng url"); err != nil {
		return err
	}
	if s.ComfyuiURL != "" {
		if err := validateHTTPURL(s.ComfyuiURL, "comfyui url"); err != nil {
			return err
		}
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
	if s.ThinkingTokenBudget < -1 {
		return fmt.Errorf("thinking token budget must be -1 or greater")
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

func defaultLLMEndpoint() LLMEndpoint {
	return LLMEndpoint{
		ID:       DefaultEndpointID,
		Name:     DefaultEndpointName,
		Endpoint: DefaultEndpoint,
		Model:    DefaultModel,
	}.WithGenerationFromSettings(Settings{
		Temperature:         0.6,
		TopK:                20,
		TopP:                0.95,
		MinP:                0,
		ContextLength:       DefaultContextLength,
		MaxTokens:           DefaultMaxTokens,
		PresencePenalty:     1.5,
		RepetitionPenalty:   1.05,
		TimeoutSeconds:      defaultTimout,
		ThinkingTokenBudget: -1,
	})
}

func defaultEndpointSelection(endpointID string) EndpointSelection {
	return EndpointSelection{
		Chat:            endpointID,
		KanbanDecompose: endpointID,
		Kanban:          endpointID,
		InlineCode:      endpointID,
	}
}

func normalizeLLMEndpoints(endpoints []LLMEndpoint, fallback Settings) []LLMEndpoint {
	normalized := make([]LLMEndpoint, 0, len(endpoints))
	usedIDs := map[string]struct{}{}
	for index, endpoint := range endpoints {
		endpoint = endpoint.Normalized(fallback)
		if endpoint.ID == "" {
			endpoint.ID = fmt.Sprintf("endpoint-%d", index+1)
		}
		endpoint.ID = uniqueEndpointID(endpoint.ID, usedIDs)
		if endpoint.Name == "" {
			endpoint.Name = fmt.Sprintf("Endpoint %d", index+1)
		}
		normalized = append(normalized, endpoint)
	}
	if len(normalized) == 0 && (fallback.Endpoint != "" || fallback.Model != "") {
		endpoint := LLMEndpoint{
			ID:       DefaultEndpointID,
			Name:     DefaultEndpointName,
			Endpoint: fallback.Endpoint,
			Model:    fallback.Model,
		}.Normalized(fallback)
		normalized = append(normalized, endpoint)
	}
	return normalized
}

func (e LLMEndpoint) Normalized(fallback Settings) LLMEndpoint {
	e.ID = strings.TrimSpace(e.ID)
	e.Name = strings.TrimSpace(e.Name)
	e.Endpoint = strings.TrimSpace(e.Endpoint)
	e.Model = strings.TrimSpace(e.Model)
	if !e.hasGenerationConfig() {
		e = e.WithGenerationFromSettings(fallback)
	}
	e = normalizeEndpointGeneration(e)
	return e
}

func (e LLMEndpoint) WithGenerationFromSettings(settings Settings) LLMEndpoint {
	settings = normalizeSettingsGeneration(settings)
	e.Temperature = settings.Temperature
	e.TopK = settings.TopK
	e.TopP = settings.TopP
	e.MinP = settings.MinP
	e.ContextLength = settings.ContextLength
	e.MaxTokens = settings.MaxTokens
	e.FrequencyPenalty = settings.FrequencyPenalty
	e.PresencePenalty = settings.PresencePenalty
	e.RepetitionPenalty = settings.RepetitionPenalty
	e.TimeoutSeconds = settings.TimeoutSeconds
	e.ThinkingTokenBudget = settings.ThinkingTokenBudget
	e.ThinkingCorrection = settings.ThinkingCorrection
	return e
}

func (e LLMEndpoint) ApplyToSettings(settings Settings) Settings {
	settings.Endpoint = e.Endpoint
	settings.Model = e.Model
	settings.Temperature = e.Temperature
	settings.TopK = e.TopK
	settings.TopP = e.TopP
	settings.MinP = e.MinP
	settings.ContextLength = e.ContextLength
	settings.MaxTokens = e.MaxTokens
	settings.FrequencyPenalty = e.FrequencyPenalty
	settings.PresencePenalty = e.PresencePenalty
	settings.RepetitionPenalty = e.RepetitionPenalty
	settings.TimeoutSeconds = e.TimeoutSeconds
	settings.ThinkingTokenBudget = e.ThinkingTokenBudget
	settings.ThinkingCorrection = e.ThinkingCorrection
	return settings
}

func (e LLMEndpoint) ValidateGeneration() error {
	settings := e.ApplyToSettings(Settings{})
	settings = normalizeSettingsGeneration(settings)
	if settings.Temperature < 0 || settings.Temperature > 2 {
		return fmt.Errorf("temperature must be between 0 and 2")
	}
	if settings.TopK < 0 {
		return fmt.Errorf("top-k cannot be negative")
	}
	if settings.TopP < 0 || settings.TopP > 1 {
		return fmt.Errorf("top-p must be between 0 and 1")
	}
	if settings.MinP < 0 || settings.MinP > 1 {
		return fmt.Errorf("min-p must be between 0 and 1")
	}
	if settings.ContextLength < 1 {
		return fmt.Errorf("context length must be at least 1")
	}
	if settings.MaxTokens < 1 {
		return fmt.Errorf("max tokens must be at least 1")
	}
	if settings.ThinkingTokenBudget < -1 {
		return fmt.Errorf("thinking token budget must be -1 or greater")
	}
	if settings.FrequencyPenalty < -2 || settings.FrequencyPenalty > 2 {
		return fmt.Errorf("frequency penalty must be between -2 and 2")
	}
	if settings.PresencePenalty < -2 || settings.PresencePenalty > 2 {
		return fmt.Errorf("presence penalty must be between -2 and 2")
	}
	if settings.RepetitionPenalty < 0 {
		return fmt.Errorf("repetition penalty cannot be negative")
	}
	if settings.TimeoutSeconds < 1 {
		return fmt.Errorf("timeout must be at least 1 second")
	}
	return nil
}

func (e LLMEndpoint) hasGenerationConfig() bool {
	return e.Temperature != 0 ||
		e.TopK != 0 ||
		e.TopP != 0 ||
		e.MinP != 0 ||
		e.ContextLength != 0 ||
		e.MaxTokens != 0 ||
		e.FrequencyPenalty != 0 ||
		e.PresencePenalty != 0 ||
		e.RepetitionPenalty != 0 ||
		e.TimeoutSeconds != 0 ||
		e.ThinkingTokenBudget != 0 ||
		e.ThinkingCorrection
}

func normalizeEndpointGeneration(e LLMEndpoint) LLMEndpoint {
	if e.ContextLength == 0 {
		e.ContextLength = DefaultContextLength
	}
	if e.MaxTokens == 0 {
		e.MaxTokens = DefaultMaxTokens
	}
	if e.RepetitionPenalty == 0 {
		e.RepetitionPenalty = 1
	}
	if e.TimeoutSeconds == 0 {
		e.TimeoutSeconds = defaultTimout
	}
	return e
}

func uniqueEndpointID(id string, used map[string]struct{}) string {
	base := strings.TrimSpace(id)
	if base == "" {
		base = "endpoint"
	}
	candidate := base
	for suffix := 2; ; suffix++ {
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, suffix)
	}
}

func normalizeEndpointSelection(selection EndpointSelection, endpoints []LLMEndpoint) EndpointSelection {
	fallback := ""
	if len(endpoints) > 0 {
		fallback = endpoints[0].ID
	}
	selection.Chat = normalizeSelectedEndpointID(selection.Chat, fallback, endpoints)
	selection.Kanban = normalizeSelectedEndpointID(selection.Kanban, fallback, endpoints)
	selection.KanbanDecompose = normalizeSelectedEndpointID(selection.KanbanDecompose, selection.Kanban, endpoints)
	selection.InlineCode = normalizeSelectedEndpointID(selection.InlineCode, fallback, endpoints)
	return selection
}

func normalizeSelectedEndpointID(id string, fallback string, endpoints []LLMEndpoint) string {
	id = strings.TrimSpace(id)
	if _, ok := endpointByID(endpoints, id); ok {
		return id
	}
	return fallback
}

func applyLegacyEndpointFields(endpoints []LLMEndpoint, selectedID string, settings Settings) []LLMEndpoint {
	output := append([]LLMEndpoint(nil), endpoints...)
	for index := range output {
		if output[index].ID == selectedID {
			output[index].Endpoint = settings.Endpoint
			output[index].Model = settings.Model
			output[index] = output[index].WithGenerationFromSettings(settings)
			return output
		}
	}
	return output
}

func endpointByID(endpoints []LLMEndpoint, id string) (LLMEndpoint, bool) {
	for _, endpoint := range endpoints {
		if endpoint.ID == id {
			return endpoint, true
		}
	}
	return LLMEndpoint{}, false
}

func (t Theme) Normalized() Theme {
	return Theme{
		Light: normalizeThemePalette(t.Light),
		Dark:  normalizeThemePalette(t.Dark),
	}
}

func (t Theme) Clone() Theme {
	return Theme{
		Light: cloneStringMap(t.Light),
		Dark:  cloneStringMap(t.Dark),
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

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
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
