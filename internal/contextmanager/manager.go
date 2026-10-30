package contextmanager

import (
	"context"
	"errors"
	"fmt"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

type CompleteFunc func(context.Context, domain.Endpoint, domain.Generation, []model.Message, []domain.Tool, func(model.Delta)) (model.Result, error)
type CompressionRequest struct {
	History      []model.Message
	Protected    []bool
	Endpoint     domain.Endpoint
	Generation   domain.Generation
	Tools        []domain.Tool
	TargetTokens int
	Complete     CompleteFunc
}
type Compressor interface {
	Compress(context.Context, CompressionRequest) ([]model.Message, error)
}
type CompressionError struct{ Reason string }

func (e *CompressionError) Error() string { return "context compression: " + e.Reason }

type Request struct {
	History    []model.Message
	Endpoint   domain.Endpoint
	Generation domain.Generation
	Tools      []domain.Tool
	Complete   CompleteFunc
	Emit       func(model.Delta)
	// Reset discards partial assistant output, including persisted stream chunks.
	Reset func() error
	// Omit persists the model-facing replacement without altering the tool output.
	Omit func(model.Message, string) error
}

type Manager struct {
	Compressor Compressor
	Accounting Accountant
}

func protected(history []model.Message) []bool {
	keep := make([]bool, len(history))
	lastUser, lastCall, currentStart := -1, -1, -1
	for i, m := range history {
		keep[i] = m.Protected || m.Role == "system"
		if m.Role == "user" && m.ToolResultID == "" {
			lastUser = i
			if m.Protected && currentStart < 0 {
				currentStart = i
			}
		}
		if len(m.Calls) > 0 {
			lastCall = i
		}
	}
	if lastUser >= 0 {
		keep[lastUser] = true
	}
	if currentStart < 0 {
		currentStart = lastUser
	}
	if lastCall > currentStart {
		for i := lastCall; i < len(history); i++ {
			keep[i] = true
		}
	}
	return keep
}

func selected(history []model.Message, keep []bool) []model.Message {
	out := make([]model.Message, 0, len(history))
	for i, m := range history {
		if keep[i] {
			out = append(out, m)
		}
	}
	return out
}

func recoverableCompression(err error) bool {
	var failure *CompressionError
	return errors.As(err, &failure) || model.IsContextOverflow(err)
}

func (m *Manager) Complete(ctx context.Context, r Request) (model.Result, []model.Message, error) {
	history := append([]model.Message(nil), r.History...)
	config := []any{r.Endpoint, r.Generation, r.Tools}
	effectiveEndpoint := r.Endpoint
	_, _, target := Limits(effectiveEndpoint)
	compressor := m.Compressor
	if compressor == nil {
		compressor = Summarizer{}
	}
	compress := func() ([]model.Message, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		keep := protected(history)
		next, err := compressor.Compress(ctx, CompressionRequest{History: history, Protected: keep, Endpoint: effectiveEndpoint, Generation: r.Generation, Tools: r.Tools, TargetTokens: target, Complete: r.Complete})
		if err != nil {
			return nil, err
		}
		if Estimate(next, r.Tools) >= Estimate(history, r.Tools) {
			return nil, &CompressionError{"strategy did not reduce context"}
		}
		// Strategies may change how history is compressed, never the protected continuation.
		cursor := 0
		for i, item := range history {
			if !keep[i] {
				continue
			}
			for cursor < len(next) && (fingerprint(next[cursor]) != fingerprint(item) || next[cursor].ID != item.ID || next[cursor].Protected != item.Protected || next[cursor].ToolResultID != item.ToolResultID || next[cursor].Omitted != item.Omitted) {
				cursor++
			}
			if cursor == len(next) {
				return nil, &CompressionError{"strategy discarded protected context"}
			}
			cursor++
		}
		return next, nil
	}
	if r.Endpoint.Compact && m.Accounting.Count(config, history, r.Tools) >= target {
		next, err := compress()
		if err != nil && !recoverableCompression(err) {
			return model.Result{}, history, err
		}
		if err == nil {
			history = next
			m.Accounting.Reset()
		}
	}
	seen := map[[32]byte]bool{}
	needsReset := false
	reset := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if needsReset && r.Reset != nil {
			if err := r.Reset(); err != nil {
				return err
			}
		}
		needsReset = false
		return nil
	}
	attempt := func(next []model.Message) (model.Result, error, bool) {
		if err := ctx.Err(); err != nil {
			return model.Result{}, err, true
		}
		hash := fingerprint(next)
		if seen[hash] {
			return model.Result{}, nil, false
		}
		if err := reset(); err != nil {
			return model.Result{}, err, true
		}
		seen[hash] = true
		history = next
		result, err := r.Complete(ctx, r.Endpoint, r.Generation, history, r.Tools, r.Emit)
		if err == nil && result.Overflow != nil {
			err = result.Overflow
		}
		needsReset = model.IsContextOverflow(err)
		var overflow *model.ContextOverflowError
		if errors.As(err, &overflow) && overflow.LimitTokens > 0 {
			window, _, _ := Limits(effectiveEndpoint)
			effectiveEndpoint.ContextTokens = min(window, overflow.LimitTokens)
			_, _, target = Limits(effectiveEndpoint)
		}
		if err == nil {
			m.Accounting.Observe(config, history, result.Usage)
		}
		return result, err, true
	}
	result, err, _ := attempt(history)
	if !model.IsContextOverflow(err) {
		return result, history, err
	}
	m.Accounting.Reset()
	// Check persistence/cancellation and retract rejected output before summary work.
	if failure := reset(); failure != nil {
		return model.Result{}, history, failure
	}
	// Compression is attempted once per overflow, independently of proactive settings.
	if next, failure := compress(); failure == nil {
		if value, callErr, tried := attempt(next); tried {
			result, err = value, callErr
			if !model.IsContextOverflow(err) {
				return result, history, err
			}
		}
	} else if !recoverableCompression(failure) {
		return model.Result{}, history, failure
	}
	clean := selected(history, protected(history))
	if value, callErr, tried := attempt(clean); tried {
		result, err = value, callErr
		if !model.IsContextOverflow(err) {
			return result, history, err
		}
	}
	// A rejected minimal request is authoritative evidence, unlike a token estimate.
	toolCount := 0
	for _, item := range history {
		if item.Role == "tool" && !item.Omitted {
			toolCount++
		}
	}
	for removed := 0; removed < toolCount; removed++ {
		if err := ctx.Err(); err != nil {
			return model.Result{}, history, err
		}
		index, largest := -1, -1
		for i, item := range history {
			if item.Role != "tool" || item.Omitted {
				continue
			}
			size := EstimateMessage(item)
			for _, media := range history {
				if item.ID != "" && media.ToolResultID == item.ID {
					size += EstimateMessage(media)
				}
			}
			if size > largest {
				index, largest = i, size
			}
		}
		if index < 0 {
			break
		}
		item := history[index]
		description := "The combined tool responses exceed the available context, even with older conversation removed. This result was omitted to allow continuation."
		if toolCount == 1 {
			description = "This action's response exceeds the available context, even with older conversation removed. Its response cannot be included."
		}
		notice := fmt.Sprintf("%s Request a narrower query, pagination, or smaller output. The action already executed; effects may have occurred. Inspect before repeating any action with side effects.", description)
		if r.Omit != nil {
			if err := r.Omit(item, notice); err != nil {
				return model.Result{}, history, err
			}
		}
		next := make([]model.Message, 0, len(history))
		for i, current := range history {
			if item.ID != "" && current.ToolResultID == item.ID {
				continue
			}
			if i == index {
				current.Content, current.Parts, current.Omitted = notice, nil, true
			}
			next = append(next, current)
		}
		if value, callErr, tried := attempt(next); tried {
			result, err = value, callErr
			if !model.IsContextOverflow(err) {
				return result, history, err
			}
		}
	}
	return model.Result{}, history, fmt.Errorf("required instructions, current request, or tool-call metadata exceed the model context even after recovery; reduce the request or select a larger context window: %w", err)
}
