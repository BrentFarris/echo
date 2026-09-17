package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

var ErrBookmarksUnavailable = errors.New("built-in Bookmarks is not enabled for this workspace")

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

func (m *Manager) requireBookmarks(workspaceID string) error {
	if !validOpaqueID(workspaceID) || m.workspacePath == nil {
		return ErrBookmarksUnavailable
	}
	if _, err := m.workspacePath(workspaceID); err != nil {
		return ErrBookmarksUnavailable
	}
	installed, ok, err := m.Installed("bookmarks")
	if err != nil || !ok || installed.Source.Type != "builtin" || installed.Source.Builtin != "bookmarks" || !m.IsEnabled("bookmarks", workspaceID) {
		return ErrBookmarksUnavailable
	}
	view, ok := installed.Manifest.View("bookmarks")
	if !ok || view.Kind != "code-sidebar" {
		return ErrBookmarksUnavailable
	}
	return m.verifyInstalledSnapshot(installed)
}

// bookmarkStorage must be called with storageMu held.
func (m *Manager) bookmarkStorage(workspaceID string) (BookmarkState, map[string]any, string, error) {
	state := BookmarkState{Version: 1, Bookmarks: []Bookmark{}}
	values, storagePath, err := m.loadStorage("bookmarks", filepath.Join("workspaces", workspaceID))
	if err != nil {
		return state, nil, "", err
	}
	if value, found := values["bookmarks.v1"]; found {
		data, err := json.Marshal(value)
		if err != nil {
			return state, nil, "", err
		}
		if err = json.Unmarshal(data, &state); err != nil {
			return state, nil, "", fmt.Errorf("invalid bookmark data: %w", err)
		}
		if state.Version != 1 || state.Revision < 0 || state.Bookmarks == nil {
			return state, nil, "", fmt.Errorf("invalid bookmark data")
		}
		seen := map[string]bool{}
		for _, mark := range state.Bookmarks {
			if !validOpaqueID(mark.ID) || seen[mark.ID] || !validBookmarkRef(mark.Ref) || mark.Line < 1 {
				return state, nil, "", fmt.Errorf("invalid bookmark data")
			}
			seen[mark.ID] = true
		}
	}
	return state, values, storagePath, nil
}

func (m *Manager) Bookmarks(workspaceID string) (BookmarkState, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if err := m.requireBookmarks(workspaceID); err != nil {
		return BookmarkState{}, err
	}
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	state, _, _, err := m.bookmarkStorage(workspaceID)
	return state, err
}

// MutateBookmarks applies individual operations to the latest server state.
// Position updates never recreate deleted IDs or overwrite custom names.
func (m *Manager) MutateBookmarks(workspaceID string, action BookmarkAction) (BookmarkState, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if err := m.requireBookmarks(workspaceID); err != nil {
		return BookmarkState{}, err
	}
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	state, values, storagePath, err := m.bookmarkStorage(workspaceID)
	if err != nil {
		return BookmarkState{}, err
	}
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
			kept = append(kept, Bookmark{ID: randomID("bookmark-"), Ref: action.Ref, Line: action.Line, Preview: action.Preview})
		}
		state.Bookmarks = kept
	case "rename":
		if !validOpaqueID(action.ID) || len(action.Label) > 2048 || strings.ContainsAny(action.Label, "\r\n\x00") {
			return BookmarkState{}, fmt.Errorf("invalid bookmark name")
		}
		for i := range state.Bookmarks {
			if state.Bookmarks[i].ID == action.ID {
				state.Bookmarks[i].Label = strings.TrimSpace(action.Label)
			}
		}
	case "delete":
		if !validOpaqueID(action.ID) {
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
			if !validOpaqueID(position.ID) || !validBookmarkRef(position.Ref) || position.Line < 1 || len(position.Preview) > 4096 {
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
	if err != nil || len(encoded) > maxStorageValue {
		return BookmarkState{}, fmt.Errorf("bookmark storage quota exceeded")
	}
	values["bookmarks.v1"] = state
	data, err := json.Marshal(values)
	if err != nil || len(data) > maxStorageBytes {
		return BookmarkState{}, fmt.Errorf("plugin storage quota exceeded")
	}
	if err := writeAtomic(storagePath, data, 0o600); err != nil {
		return BookmarkState{}, err
	}
	return state, nil
}
