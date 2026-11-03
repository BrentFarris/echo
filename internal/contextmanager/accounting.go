// Package contextmanager owns model context budgets and overflow recovery.
// Compression strategies do not own retry policy or execute tools.
package contextmanager

import (
	"crypto/sha256"
	"encoding/json"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

func EstimateMessage(m model.Message) int {
	raw, _ := json.Marshal(m.Summary())
	n := (len(raw)+2)/3 + 4
	for _, part := range m.Parts {
		if part.Type != "text" {
			n += 1024
		}
	}
	return n
}

func Estimate(messages []model.Message, tools []domain.Tool) int {
	n := 3
	for _, m := range messages {
		n += EstimateMessage(m)
	}
	if len(tools) > 0 {
		raw, _ := json.Marshal(tools)
		n += (len(raw)+2)/3 + len(tools)*16
	}
	return n
}

func Limits(e domain.Endpoint) (window, reserve, target int) {
	window = e.ContextTokens
	if window <= 0 {
		window = 32768
	}
	reserve = 2048
	if e.MaxTokens != nil {
		reserve = *e.MaxTokens
	}
	threshold := e.CompressionThreshold
	if threshold == 0 {
		threshold = 70
	}
	target = max(0, min(window-reserve, window*threshold/100))
	return
}

func fingerprint(v any) [32]byte { raw, _ := json.Marshal(v); return sha256.Sum256(raw) }

// Accountant is scoped to one conversation run. Only an unchanged request prefix
// may reuse its reported input count; generated reasoning is never added here.
type Accountant struct {
	config [32]byte
	prefix [][32]byte
	input  int
	valid  bool
}

func (a *Accountant) Reset() { *a = Accountant{} }
func (a *Accountant) Observe(config any, messages []model.Message, usage *domain.TokenUsage) {
	a.Reset()
	if usage == nil || usage.InputMissing || usage.InputTokens < 0 {
		return
	}
	a.config, a.input, a.valid = fingerprint(config), usage.InputTokens, true
	for _, m := range messages {
		a.prefix = append(a.prefix, fingerprint(m))
	}
}
func (a *Accountant) Count(config any, messages []model.Message, tools []domain.Tool) int {
	if a.valid && a.config == fingerprint(config) && len(messages) >= len(a.prefix) {
		matches := true
		for i, hash := range a.prefix {
			if fingerprint(messages[i]) != hash {
				matches = false
				break
			}
		}
		if matches {
			n := a.input
			for _, m := range messages[len(a.prefix):] {
				n += EstimateMessage(m)
			}
			return n
		}
	}
	a.Reset()
	return Estimate(messages, tools)
}
