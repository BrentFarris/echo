package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/brent/echo/internal/development"
)

// ShutdownRequested is consumed by the executable host, never by chat tools.
func (s *Server) ShutdownRequested() <-chan struct{} { return s.shutdownRequested }

func (s *Server) developmentRoutes(mux *http.ServeMux) {
	s.developmentUpdateRoutes(mux)
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]string{"instanceId": s.Development.InstanceID(), "revision": s.Development.Revision()}, nil)
	})
	mux.HandleFunc("GET /api/v1/development/update-status", func(w http.ResponseWriter, r *http.Request) {
		respond(w, s.Development.UpdateNotice(), nil)
	})
	mux.HandleFunc("GET /api/v1/development", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		respond(w, s.Development.Snapshot(ctx), nil)
	})
	mux.HandleFunc("PUT /api/v1/development", func(w http.ResponseWriter, r *http.Request) {
		var config development.Config
		if err := decode(w, r, &config); err != nil {
			respond(w, nil, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		err := s.Development.Save(ctx, config, func(c development.Config) error { return s.Store.Put("settings", "development", c) })
		if err != nil {
			developmentError(w, err)
			return
		}
		respond(w, s.Development.Snapshot(ctx), nil)
	})
	mux.HandleFunc("GET /api/v1/development/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		op, ok := s.Development.Operation(r.PathValue("id"))
		if !ok {
			http.Error(w, "Development operation is no longer available", http.StatusNotFound)
			return
		}
		respond(w, op, nil)
	})
	for _, action := range []string{"check-updates", "update-relaunch", "build-relaunch", "relaunch", "terminate"} {
		mux.HandleFunc("POST /api/v1/development/"+action, func(w http.ResponseWriter, r *http.Request) {
			op, err := s.Development.Start(action)
			if err != nil {
				developmentError(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			respond(w, map[string]any{"operation": op, "instanceId": s.Development.InstanceID()}, nil)
		})
	}
}

func developmentError(w http.ResponseWriter, err error) {
	if errors.Is(err, development.ErrBusy) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		// respond's error branch writes 400; encode the existing error shape here.
		respond(w, map[string]string{"error": err.Error()}, nil)
		return
	}
	respond(w, nil, err)
}
