package contextmanager

import (
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"testing"
)

func TestOutputOnlyUsageDoesNotCalibrateContext(t *testing.T) {
	history := []model.Message{{Role: "user", Content: "Some input text"}}
	var accountant Accountant
	accountant.Observe("config", history, &domain.TokenUsage{OutputTokens: 12, InputMissing: true})
	if got := accountant.Count("config", history, nil); got != Estimate(history, nil) {
		t.Fatal(got)
	}
	accountant.Observe("config", history, &domain.TokenUsage{InputTokens: 0, OutputMissing: true})
	if got := accountant.Count("config", history, nil); got != 0 {
		t.Fatal(got)
	}
}
