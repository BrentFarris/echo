package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
	"net/http"
	"path"
	"strings"
)

type BookmarkRef struct {
	RootID string `json:"rootId"`
	Path   string `json:"path"`
}

type Bookmark struct {
	ID      string      `json:"id"`
	Ref     BookmarkRef `json:"ref"`
	Line    int         `json:"line"`
	Label   string      `json:"label,omitempty"`
	Preview string      `json:"preview"`
}

type BookmarkState struct {
	Version   int        `json:"version"`
	Revision  int64      `json:"revision"`
	Bookmarks []Bookmark `json:"bookmarks"`
}

type BookmarkPosition struct {
	ID      string      `json:"id"`
	Ref     BookmarkRef `json:"ref"`
	Line    int         `json:"line"`
	Preview string      `json:"preview"`
}

type BookmarkAction struct {
	Action      string             `json:"action"`
	ID          string             `json:"id,omitempty"`
	Ref         BookmarkRef        `json:"ref,omitempty"`
	Line        int                `json:"line,omitempty"`
	Label       string             `json:"label,omitempty"`
	Preview     string             `json:"preview,omitempty"`
	Positions   []BookmarkPosition `json:"positions,omitempty"`
	PreviousRef BookmarkRef        `json:"previousRef,omitempty"`
	NextRef     BookmarkRef        `json:"nextRef,omitempty"`
}

func validBookmarkRef(ref BookmarkRef) bool {
	return ref.RootID != "" && len(ref.RootID) <= 256 && !strings.ContainsAny(ref.RootID, "/\\\x00") &&
		ref.Path != "" && len(ref.Path) <= 4096 && !strings.ContainsAny(ref.Path, "\\\x00\r\n:") &&
		!strings.HasPrefix(ref.Path, "/") && ref.Path != "." && ref.Path != ".." &&
		!strings.HasPrefix(ref.Path, "../") && path.Clean(ref.Path) == ref.Path
}

func validBookmarkID(id string) bool {
	return id != "" && len(id) <= 256 && !strings.ContainsAny(id, "/\\\x00")
}
func mutateEditorBookmarks(state BookmarkState, action BookmarkAction) (BookmarkState, error) {
	switch action.Action {
	case "toggle":
		if !validBookmarkRef(action.Ref) || action.Line < 1 || len(action.Preview) > 4096 {
			return BookmarkState{}, fmt.Errorf("invalid bookmark location")
		}
		kept := make([]Bookmark, 0, len(state.Bookmarks)+1)
		found := false
		for _, mark := range state.Bookmarks {
			if mark.Ref == action.Ref && mark.Line == action.Line {
				found = true
			} else {
				kept = append(kept, mark)
			}
		}
		if !found {
			kept = append(kept, Bookmark{ID: domain.ID(), Ref: action.Ref, Line: action.Line, Preview: action.Preview})
		}
		state.Bookmarks = kept
	case "rename":
		if !validBookmarkID(action.ID) || len(action.Label) > 2048 || strings.ContainsAny(action.Label, "\r\n\x00") {
			return BookmarkState{}, fmt.Errorf("invalid bookmark name")
		}
		for i := range state.Bookmarks {
			if state.Bookmarks[i].ID == action.ID {
				state.Bookmarks[i].Label = strings.TrimSpace(action.Label)
			}
		}
	case "delete":
		if !validBookmarkID(action.ID) {
			return BookmarkState{}, fmt.Errorf("invalid bookmark id")
		}
		kept := make([]Bookmark, 0, len(state.Bookmarks))
		for _, mark := range state.Bookmarks {
			if mark.ID != action.ID {
				kept = append(kept, mark)
			}
		}
		state.Bookmarks = kept
	case "positions":
		for _, position := range action.Positions {
			if !validBookmarkID(position.ID) || !validBookmarkRef(position.Ref) || position.Line < 1 || len(position.Preview) > 4096 {
				return BookmarkState{}, fmt.Errorf("invalid bookmark position")
			}
			for i := range state.Bookmarks {
				mark := &state.Bookmarks[i]
				if mark.ID == position.ID && mark.Ref == position.Ref {
					mark.Line, mark.Preview = position.Line, position.Preview
				}
			}
		}
	case "remap":
		if !validBookmarkRef(action.PreviousRef) || !validBookmarkRef(action.NextRef) {
			return BookmarkState{}, fmt.Errorf("invalid bookmark file move")
		}
		for i := range state.Bookmarks {
			mark := &state.Bookmarks[i]
			if mark.Ref.RootID == action.PreviousRef.RootID && (mark.Ref.Path == action.PreviousRef.Path || strings.HasPrefix(mark.Ref.Path, action.PreviousRef.Path+"/")) {
				mark.Ref = BookmarkRef{RootID: action.NextRef.RootID, Path: action.NextRef.Path + strings.TrimPrefix(mark.Ref.Path, action.PreviousRef.Path)}
			}
		}
	default:
		return BookmarkState{}, fmt.Errorf("unknown bookmark action")
	}
	state.Revision++
	encoded, err := json.Marshal(state)
	if err != nil || len(encoded) > 1<<20 {
		return BookmarkState{}, fmt.Errorf("bookmark storage quota exceeded")
	}
	return state, nil
}
func (s *Server) editorBookmarks(w http.ResponseWriter, r *http.Request) {
	s.editorBookmarkMu.Lock()
	defer s.editorBookmarkMu.Unlock()
	botID := r.PathValue("botId")
	state := BookmarkState{Version: 1, Bookmarks: []Bookmark{}}
	if err := s.Store.Get("editor-bookmarks", botID, &state); err != nil && !errors.Is(err, sql.ErrNoRows) {
		respond(w, nil, err)
		return
	}
	if r.Method == "POST" {
		var action BookmarkAction
		if err := editorDecode(w, r, &action, 256<<10); err != nil {
			respond(w, nil, err)
			return
		}
		if action.Action == "toggle" {
			if _, err := s.EditorFS.ResolveExistingHostPath(botID, editorfs.FileRef{RootID: action.Ref.RootID, Path: action.Ref.Path}, false); err != nil {
				respond(w, nil, err)
				return
			}
		}
		if action.Action == "remap" {
			if _, err := s.EditorFS.ResolveExistingHostPath(botID, editorfs.FileRef{RootID: action.NextRef.RootID, Path: action.NextRef.Path}, true); err != nil {
				respond(w, nil, err)
				return
			}
		}
		if action.Action == "positions" {
			roots, err := s.EditorFS.Roots(botID)
			if err != nil {
				respond(w, nil, err)
				return
			}
			allowed := map[string]bool{}
			for _, root := range roots {
				allowed[root.ID] = root.BlockedReason == ""
			}
			for _, position := range action.Positions {
				if !allowed[position.Ref.RootID] {
					respond(w, nil, fmt.Errorf("bookmark folder is unavailable"))
					return
				}
			}
		}
		var err error
		state, err = mutateEditorBookmarks(state, action)
		if err != nil {
			respond(w, nil, err)
			return
		}
		if err = s.Store.Put("editor-bookmarks", botID, state); err != nil {
			respond(w, nil, err)
			return
		}
		s.editorBroadcast(botID, map[string]any{"type": "bookmarks_changed", "botId": botID, "state": state})
	}
	respond(w, state, nil)
}
