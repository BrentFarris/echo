package contextmanager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

type compressorFunc func(context.Context, CompressionRequest) ([]model.Message, error)

func (f compressorFunc) Compress(c context.Context, r CompressionRequest) ([]model.Message, error) {
	return f(c, r)
}
func noSummary(context.Context, CompressionRequest) ([]model.Message, error) {
	return nil, &CompressionError{"unavailable"}
}
func overflow() error { return &model.ContextOverflowError{Message: "context_length_exceeded"} }

func toolHistory() []model.Message {
	return []model.Message{
		{Role: "system", Content: "instructions"},
		{Role: "user", Content: "older request"},
		{Role: "assistant", Content: strings.Repeat("old ", 1000)},
		{Role: "user", Content: "current request", Protected: true},
		{Role: "assistant", Calls: []domain.ToolCall{{ID: "a", Type: "function", Function: domain.FunctionCall{Name: "read", Arguments: "{}"}}, {ID: "b", Type: "function", Function: domain.FunctionCall{Name: "read", Arguments: "{}"}}}},
		{ID: "result-a", Role: "tool", CallID: "a", Content: strings.Repeat("large ", 1000)},
		{ID: "result-b", Role: "tool", CallID: "b", Content: "small"},
		{Role: "user", ToolResultID: "result-a", Parts: []model.ContentPart{{Type: "image_url", ImageURL: &model.MediaURL{URL: "data:image/png;base64,abc"}}}},
	}
}

func TestAccountingUsesReportedPrefixAndResets(t *testing.T) {
	a := Accountant{}
	h := []model.Message{{Role: "user", Content: "request"}}
	config := []any{"model-a", []domain.Tool{{Name: "read"}}}
	a.Observe(config, h, &domain.TokenUsage{InputTokens: 100, OutputTokens: 900, TotalTokens: 1000})
	h = append(h, model.Message{Role: "assistant", Content: "short answer"}, model.Message{Role: "tool", Content: "new output"})
	want := 100 + EstimateMessage(h[1]) + EstimateMessage(h[2])
	if got := a.Count(config, h, nil); got != want {
		t.Fatalf("got %d want %d (reasoning/output must not be added twice)", got, want)
	}
	a.Observe(config, h, &domain.TokenUsage{InputTokens: 200})
	if got := a.Count(config, h, nil); got != 200 {
		t.Fatal(got)
	}
	if got := a.Count("model-b", h, nil); got != Estimate(h, nil) {
		t.Fatal("model change reused baseline", got)
	}
	a.Observe(config, h, &domain.TokenUsage{InputTokens: 200})
	h[0].Content = "summary"
	if got := a.Count(config, h, nil); got != Estimate(h, nil) {
		t.Fatal("rewritten context reused baseline", got)
	}
	a.Observe(config, h, nil)
	if a.valid {
		t.Fatal("missing usage kept stale baseline")
	}
	window, reserve, target := Limits(domain.Endpoint{})
	if window != 32768 || reserve != 2048 || target != 22937 {
		t.Fatal(window, reserve, target)
	}
	n := 32000
	_, _, target = Limits(domain.Endpoint{MaxTokens: &n})
	if target != 768 {
		t.Fatal("output not reserved", target)
	}
}

func TestOverflowCleanRetryAndLargestResultOmission(t *testing.T) {
	for _, rejectTool := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean-fits", true: "oversized-tool"}[rejectTool], func(t *testing.T) {
			manager := Manager{Compressor: compressorFunc(noSummary)}
			history := toolHistory()
			attempts, resets, omissions := 0, 0, 0
			r := Request{History: history, Reset: func() error { resets++; return nil }, Omit: func(item model.Message, notice string) error {
				omissions++
				if item.ID != "result-a" || !strings.Contains(notice, "combined") || !strings.Contains(notice, "already executed") {
					t.Fatal(item, notice)
				}
				return nil
			}, Complete: func(_ context.Context, _ domain.Endpoint, _ domain.Generation, h []model.Message, _ []domain.Tool, _ func(model.Delta)) (model.Result, error) {
				attempts++
				for _, item := range h {
					if strings.Contains(item.Content, "old ") || rejectTool && strings.Contains(item.Content, "large ") {
						return model.Result{}, overflow()
					}
				}
				if rejectTool {
					for _, item := range h {
						if item.ToolResultID == "result-a" {
							t.Fatal("omitted media survived")
						}
					}
				}
				return model.Result{Content: "continued", Usage: &domain.TokenUsage{InputTokens: 400}}, nil
			}}
			result, next, err := manager.Complete(context.Background(), r)
			if err != nil || result.Content != "continued" {
				t.Fatal(result, err)
			}
			want := 2
			if rejectTool {
				want = 3
			}
			if attempts != want || resets != want-1 || omissions != want-2 {
				t.Fatal(attempts, resets, omissions)
			}
			if history[5].Omitted || history[5].Content != strings.Repeat("large ", 1000) || len(history[7].Parts) != 1 {
				t.Fatal("original mutated")
			}
			calls, results := 0, 0
			for _, item := range next {
				calls += len(item.Calls)
				if item.Role == "tool" {
					results++
				}
			}
			if calls != 2 || results != 2 {
				t.Fatal("tool pairing broken", next)
			}
		})
	}
}

func TestRecoveryFailuresAndBound(t *testing.T) {
	for _, kind := range []string{"unrelated", "persistence", "cancel", "exhausted"} {
		t.Run(kind, func(t *testing.T) {
			manager := Manager{Compressor: compressorFunc(noSummary)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			r := Request{History: toolHistory(), Complete: func(_ context.Context, _ domain.Endpoint, _ domain.Generation, _ []model.Message, _ []domain.Tool, _ func(model.Delta)) (model.Result, error) {
				calls++
				if kind == "unrelated" {
					return model.Result{}, errors.New("HTTP 500")
				}
				if kind == "cancel" {
					cancel()
				}
				return model.Result{}, overflow()
			}, Reset: func() error {
				if kind == "persistence" {
					return errors.New("disk full")
				}
				return nil
			}}
			_, _, err := manager.Complete(ctx, r)
			if err == nil {
				t.Fatal("failure hidden")
			}
			if kind == "exhausted" {
				if calls != 4 || !strings.Contains(err.Error(), "required instructions") {
					t.Fatal(calls, err)
				}
			} else if calls != 1 {
				t.Fatal("unexpected retry", calls, err)
			}
		})
	}
}

func TestCompressionIsReplaceableAndUsageTriggersProactive(t *testing.T) {
	count := 0
	manager := Manager{Compressor: compressorFunc(func(_ context.Context, r CompressionRequest) ([]model.Message, error) {
		count++
		return selected(r.History, r.Protected), nil
	})}
	h := toolHistory()
	e := domain.Endpoint{Compact: true}
	r := Request{History: h, Endpoint: e, Complete: func(_ context.Context, _ domain.Endpoint, _ domain.Generation, _ []model.Message, _ []domain.Tool, _ func(model.Delta)) (model.Result, error) {
		return model.Result{Usage: &domain.TokenUsage{InputTokens: 25000}}, nil
	}}
	_, next, err := manager.Complete(context.Background(), r)
	if err != nil || count != 0 {
		t.Fatal(count, err)
	}
	r.History = append(next, model.Message{Role: "assistant", Content: "reply"})
	_, _, err = manager.Complete(context.Background(), r)
	if err != nil || count != 1 {
		t.Fatal("reported usage did not trigger compaction", count, err)
	}
}

func TestSummariesBoundInputShrinkAndPreserveMedia(t *testing.T) {
	h := toolHistory()
	// Large historical content, with a small protected continuation.
	h = h[:4]
	h[2].Content = strings.Repeat("😀 historical detail ", 4000)
	h[3].Parts = []model.ContentPart{{Type: "image_url", ImageURL: &model.MediaURL{URL: "data:image/png;base64," + strings.Repeat("a", 20000)}}}
	calls, rejections := 0, 0
	r := CompressionRequest{History: h, Protected: protected(h), Endpoint: domain.Endpoint{ContextTokens: 4096}, TargetTokens: 1800,
		Complete: func(_ context.Context, e domain.Endpoint, g domain.Generation, m []model.Message, _ []domain.Tool, _ func(model.Delta)) (model.Result, error) {
			calls++
			if g.Thinking != "none" || e.MaxTokens == nil || *e.MaxTokens > 1024 {
				t.Fatal("unbounded summary", e, g)
			}
			if len(m[1].Content) > 2000 {
				rejections++
				return model.Result{}, overflow()
			}
			return model.Result{Content: "Earlier task facts."}, nil
		}}
	next, err := (Summarizer{}).Compress(context.Background(), r)
	if err != nil || calls < 2 || rejections == 0 {
		t.Fatal(calls, rejections, err)
	}
	if len(next[len(next)-1].Parts) != 1 || Estimate(next, nil) >= Estimate(h, nil) {
		t.Fatal("media lost or no reduction")
	}
	r.Complete = func(_ context.Context, _ domain.Endpoint, _ domain.Generation, m []model.Message, _ []domain.Tool, _ func(model.Delta)) (model.Result, error) {
		return model.Result{Content: m[1].Content}, nil
	}
	if _, err = (Summarizer{}).Compress(context.Background(), r); err == nil {
		t.Fatal("nonreducing summary accepted")
	}
}

func TestReportedLimitAndProtectedStrategyBoundary(t *testing.T) {
	history := toolHistory()
	manager := Manager{Compressor: compressorFunc(func(_ context.Context, r CompressionRequest) ([]model.Message, error) {
		if r.Endpoint.ContextTokens != 4096 {
			t.Fatal("provider limit ignored", r.Endpoint.ContextTokens)
		}
		// An invalid replacement cannot discard the current request or tools.
		return []model.Message{{Role: "system", Content: "instructions"}}, nil
	})}
	calls := 0
	_, next, err := manager.Complete(context.Background(), Request{History: history, Complete: func(_ context.Context, _ domain.Endpoint, _ domain.Generation, m []model.Message, _ []domain.Tool, _ func(model.Delta)) (model.Result, error) {
		calls++
		if calls == 1 {
			return model.Result{}, &model.ContextOverflowError{Message: "too large", LimitTokens: 4096}
		}
		if len(m) < 4 {
			t.Fatal("invalid strategy accepted", m)
		}
		return model.Result{Content: "continued"}, nil
	}})
	if err != nil || calls != 2 || len(next) != 6 {
		t.Fatal(calls, len(next), err)
	}
}

func TestShortHistoryStillSummarizesOlderMessages(t *testing.T) {
	h := []model.Message{{Role: "system", Content: "instructions"}, {Role: "user", Content: strings.Repeat("earlier ", 500)}, {Role: "user", Content: "current", Protected: true}}
	next, err := (Summarizer{}).Compress(context.Background(), CompressionRequest{History: h, Protected: protected(h), TargetTokens: 4000, Endpoint: domain.Endpoint{ContextTokens: 8192}, Complete: func(_ context.Context, _ domain.Endpoint, _ domain.Generation, _ []model.Message, _ []domain.Tool, _ func(model.Delta)) (model.Result, error) {
		return model.Result{Content: "Earlier task facts."}, nil
	}})
	if err != nil || next[len(next)-1].Content != "current" || Estimate(next, nil) >= Estimate(h, nil) {
		t.Fatal(next, err)
	}
}

func TestCleanContextRetainsToolGroupBeforeNewHandoff(t *testing.T) {
	h := append(toolHistory(), model.Message{Role: "user", Content: "new handoff"})
	clean := selected(h, protected(h))
	tools, calls := 0, 0
	for _, m := range clean {
		calls += len(m.Calls)
		if m.Role == "tool" {
			tools++
		}
	}
	if tools != 2 || calls != 2 || clean[len(clean)-1].Content != "new handoff" {
		t.Fatal(clean)
	}
	// A subsequent user turn can summarize old tool exchanges normally.
	for i := range h {
		h[i].Protected = false
	}
	h = append(h, model.Message{Role: "user", Content: "next user turn", Protected: true})
	clean = selected(h, protected(h))
	if len(clean) != 2 {
		t.Fatal("old exchange was pinned into a new turn", clean)
	}
}
