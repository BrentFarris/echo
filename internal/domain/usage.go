package domain

import (
	"encoding/json"
	"time"
)

// Missing flags preserve the original numeric wire fields and distinguish unknown
// usage from a provider-reported zero, including older custom providers.
func (u *TokenUsage) UnmarshalJSON(data []byte) error {
	type plain TokenUsage
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var presence struct {
		Input  *int `json:"inputTokens"`
		Output *int `json:"outputTokens"`
	}
	if err := json.Unmarshal(data, &presence); err != nil {
		return err
	}
	*u = TokenUsage(value)
	u.InputMissing = u.InputMissing || presence.Input == nil || u.InputTokens < 0
	u.OutputMissing = u.OutputMissing || presence.Output == nil || u.OutputTokens < 0
	return nil
}

type UsageCounts struct {
	InputTokens   int64 `json:"inputTokens"`
	OutputTokens  int64 `json:"outputTokens"`
	ModelCalls    int64 `json:"modelCalls"`
	InputReports  int64 `json:"inputReports"`
	OutputReports int64 `json:"outputReports"`
}

func (c *UsageCounts) Add(u *TokenUsage) {
	c.ModelCalls++
	if u == nil {
		return
	}
	if !u.InputMissing && u.InputTokens >= 0 {
		c.InputTokens += int64(u.InputTokens)
		c.InputReports++
	}
	if !u.OutputMissing && u.OutputTokens >= 0 {
		c.OutputTokens += int64(u.OutputTokens)
		c.OutputReports++
	}
}

type TurnStatistics struct {
	UsageCounts
	StartedAt   time.Time  `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	ToolCalls   int64      `json:"toolCalls"`
}

type UsageTotals struct {
	UsageCounts
	TrackingStartedAt time.Time `json:"trackingStartedAt"`
	ResetAt           time.Time `json:"resetAt"`
	Revision          int64     `json:"revision"`
}
