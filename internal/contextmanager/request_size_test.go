package contextmanager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

func TestSummaryByteRejectionIsActionableAndDoesNotRetry(t *testing.T) {
	manager := Manager{}
	calls := 0
	maxOutput := 128
	_, _, err := manager.Complete(context.Background(), Request{
		Endpoint: domain.Endpoint{Compact: true, ContextTokens: 2000, MaxTokens: &maxOutput},
		History:  []model.Message{{Role: "system", Content: "instructions"}, {Role: "user", Content: strings.Repeat("old history ", 2000)}, {Role: "user", Content: "current request", Protected: true}},
		Complete: func(_ context.Context, _ domain.Endpoint, _ domain.Generation, m []model.Message, _ []domain.Tool, _ func(model.Delta)) (model.Result, error) {
			calls++
			if !strings.HasPrefix(m[0].Content, "Summarize") {
				t.Error("expected summary request")
			}
			return model.Result{}, model.ProviderError(413, []byte(`<html><body>Request Entity Too Large</body></html>`))
		},
	})
	var size *model.RequestSizeError
	if calls != 1 || !errors.As(err, &size) || strings.Contains(err.Error(), "<html") {
		t.Fatal(calls, err)
	}
}
