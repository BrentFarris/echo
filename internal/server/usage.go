package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/brent/echo/internal/domain"
)

type completionAccountingKey struct{}
type completionAccounting struct{ delegated int }

func (s *Server) recordCompletion(ctx context.Context, requestID string, usage *domain.TokenUsage) error {
	var next *domain.TurnStatistics
	var runID string
	parent, _ := ctx.Value(providerKey{}).(*providerInvocation)
	if parent != nil && parent.Statistics != nil {
		copy := *parent.Statistics
		copy.Add(usage)
		next, runID = &copy, parent.Scope.RunID
	}
	if err := s.Store.RecordModelUsage(requestID, usage, runID, next); err != nil {
		return err
	}
	if next != nil {
		*parent.Statistics = *next
	}
	return nil
}

func startRunStatistics(run *domain.Run) {
	if run.Statistics == nil {
		run.Statistics = &domain.TurnStatistics{StartedAt: domain.Now()}
	}
	run.Statistics.CompletedAt = nil
}

func (s *Server) usageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/usage", func(w http.ResponseWriter, r *http.Request) {
		value, err := s.Store.Usage()
		respond(w, value, err)
	})
	mux.HandleFunc("POST /api/v1/usage/reset", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			RequestID string `json:"requestId"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		if strings.TrimSpace(input.RequestID) == "" || len(input.RequestID) > 128 {
			respond(w, nil, errors.New("a requestId of 1–128 characters is required"))
			return
		}
		value, err := s.Store.ResetUsage(input.RequestID)
		respond(w, value, err)
	})
}
