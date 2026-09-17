package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/brent/echo/internal/plugins"
	"github.com/brent/echo/internal/workspacefs"
)

func (s *Server) handleBookmarks(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspaceId"))
	if s.plugins == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin management is unavailable")
		return
	}
	if _, ok, err := s.workspaces.Get(workspaceID); err != nil || !ok {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	var state plugins.BookmarkState
	var err error
	if r.Method == http.MethodGet {
		state, err = s.plugins.Bookmarks(workspaceID)
	} else {
		var action plugins.BookmarkAction
		if err := decodeLimitedJSON(w, r, &action, 256<<10); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// New bookmarks must name an actual confined workspace file. Other
		// operations can still rename/remove bookmarks whose file is missing.
		if action.Action == "toggle" {
			if _, err := s.fs.ResolveExistingHostPath(workspaceID, workspacefs.FileRef{RootID: action.Ref.RootID, Path: action.Ref.Path}, false); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		state, err = s.plugins.MutateBookmarks(workspaceID, action)
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, plugins.ErrBookmarksUnavailable) {
			status = http.StatusForbidden
		}
		writeError(w, status, err.Error())
		return
	}
	if r.Method != http.MethodGet {
		s.hub.Broadcast(map[string]any{"type": "bookmarks_changed", "workspaceId": workspaceID, "state": state})
	}
	writeData(w, http.StatusOK, state)
}
