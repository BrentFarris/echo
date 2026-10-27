package server

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
)

func (s *Server) appDock() ([]string, error) {
	ids := []string{}
	err := s.Store.Get("settings", "app-dock", &ids)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, err
}

func (s *Server) setAppDock(id string, docked bool) ([]string, error) {
	s.appDockMu.Lock()
	defer s.appDockMu.Unlock()
	if docked {
		m, err := s.Plugins.Get(id)
		if err != nil {
			return nil, err
		}
		echo, err := s.Bot("echo")
		if err != nil {
			return nil, err
		}
		if m.App == nil || !s.Plugins.Enabled(echo, id) {
			return nil, errors.New("only available registered apps can be docked")
		}
	}
	ids, err := s.appDock()
	if err != nil {
		return nil, err
	}
	index := slices.Index(ids, id)
	if docked && index == -1 {
		ids = append(ids, id)
	} else if !docked && index != -1 {
		ids = slices.Delete(ids, index, index+1)
	} else {
		return ids, nil
	}
	if err := s.Store.Put("settings", "app-dock", ids); err != nil {
		return nil, err
	}
	s.emit("changed", "", "", "", nil)
	return ids, nil
}

func (s *Server) appRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/v1/plugins/{id}/app/dock", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Docked *bool `json:"docked"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		if input.Docked == nil {
			respond(w, nil, errors.New("docked is required"))
			return
		}
		ids, err := s.setAppDock(r.PathValue("id"), *input.Docked)
		respond(w, ids, err)
	})
}
