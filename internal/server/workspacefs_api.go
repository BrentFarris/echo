package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/brent/echo/internal/workspacefs"
)

func (s *Server) handleFSRoots(w http.ResponseWriter, r *http.Request) {
	roots, err := s.fs.Roots(r.PathValue("id"))
	if err != nil {
		writeWorkspaceFSError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"roots": roots})
}

func (s *Server) handleFSEntries(w http.ResponseWriter, r *http.Request) {
	entries, err := s.fs.List(r.PathValue("id"), workspacefs.FileRef{
		RootID: r.URL.Query().Get("rootId"), Path: r.URL.Query().Get("path"),
	})
	if err != nil {
		writeWorkspaceFSError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) handleFSReadFile(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.fs.Read(r.PathValue("id"), workspacefs.FileRef{
		RootID: r.URL.Query().Get("rootId"), Path: r.URL.Query().Get("path"),
	})
	if err != nil {
		writeWorkspaceFSError(w, err)
		return
	}
	writeData(w, http.StatusOK, snapshot)
}

func (s *Server) handleFSSaveFile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref              workspacefs.FileRef `json:"ref"`
		Content          string              `json:"content"`
		ExpectedRevision string              `json:"expectedRevision"`
		CreateOnly       bool                `json:"createOnly"`
		HasBOM           bool                `json:"hasBom"`
	}
	if err := decodeLimitedJSON(w, r, &body, workspacefs.MaxEditableBytes*6+(1<<20)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, err := s.fs.Save(r.PathValue("id"), workspacefs.SaveRequest{
		Ref: body.Ref, Content: body.Content, ExpectedRevision: body.ExpectedRevision,
		CreateOnly: body.CreateOnly, HasBOM: body.HasBOM,
	})
	if err != nil {
		writeWorkspaceFSError(w, err)
		return
	}
	writeData(w, http.StatusOK, snapshot)
}

func (s *Server) handleFSCreateEntry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Parent  workspacefs.FileRef `json:"parent"`
		Name    string              `json:"name"`
		Kind    string              `json:"kind"`
		Content string              `json:"content"`
		HasBOM  bool                `json:"hasBom"`
	}
	if err := decodeLimitedJSON(w, r, &body, workspacefs.MaxEditableBytes*6+(1<<20)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	entry, snapshot, err := s.fs.Create(r.PathValue("id"), workspacefs.CreateRequest{
		Parent: body.Parent, Name: body.Name, Kind: body.Kind, Content: body.Content, HasBOM: body.HasBOM,
	})
	if err != nil {
		writeWorkspaceFSError(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"entry": entry, "file": snapshot})
}

func (s *Server) handleFSRenameEntry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref               workspacefs.FileRef  `json:"ref"`
		NewName           string               `json:"newName"`
		DestinationParent *workspacefs.FileRef `json:"destinationParent"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var entry workspacefs.Entry
	var err error
	if body.DestinationParent != nil {
		entry, err = s.fs.Move(r.PathValue("id"), body.Ref, *body.DestinationParent)
	} else {
		entry, err = s.fs.Rename(r.PathValue("id"), body.Ref, body.NewName)
	}
	if err != nil {
		writeWorkspaceFSError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"entry": entry, "previousRef": body.Ref})
}

func (s *Server) handleFSTrashEntry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref workspacefs.FileRef `json:"ref"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	item, err := s.fs.Trash(r.PathValue("id"), body.Ref)
	if err != nil {
		writeWorkspaceFSError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"trash": item})
}

func (s *Server) handleFSListTrash(w http.ResponseWriter, r *http.Request) {
	items, err := s.fs.ListTrash(r.PathValue("id"))
	if err != nil {
		writeWorkspaceFSError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleFSRestoreTrash(w http.ResponseWriter, r *http.Request) {
	entry, err := s.fs.Restore(r.PathValue("id"), r.PathValue("trashId"))
	if err != nil {
		writeWorkspaceFSError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"entry": entry})
}

func (s *Server) handleFSPurgeTrash(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("confirmed") != "true" {
		writeCodedError(w, http.StatusBadRequest, "confirmation_required", "permanent deletion requires explicit confirmation", nil)
		return
	}
	if err := s.fs.PurgeTrash(r.PathValue("id"), r.PathValue("trashId")); err != nil {
		writeWorkspaceFSError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleFSReveal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref workspacefs.FileRef `json:"ref"`
	}
	if err := decodeLimitedJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.fs.Reveal(r.PathValue("id"), body.Ref); err != nil {
		writeWorkspaceFSError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"revealed": true})
}

// handleFSMedia streams a previewable image or video file straight to the
// browser for the code editor's media surface. The path boundary comes from
// the same confined resolver as every other filesystem endpoint; symlinks
// leaving the workspace are rejected. Oversized images stream their first
// MaxMediaBytes chunk (browsers fail decoding the remainder gracefully),
// while oversized videos are refused outright because partial video files do
// not play.
func (s *Server) handleFSMedia(w http.ResponseWriter, r *http.Request) {
	ref := workspacefs.FileRef{RootID: r.URL.Query().Get("rootId"), Path: r.URL.Query().Get("path")}
	path, size, mediaType, truncated, err := s.fs.MediaMeta(r.PathValue("id"), ref)
	if err != nil {
		writeWorkspaceFSError(w, err)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeCodedError(w, http.StatusNotFound, "not_found", "file not found", nil)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "no-store")
	limit := size
	if truncated {
		limit = workspacefs.MaxMediaBytes
	}
	w.Header().Set("Content-Length", strconv.FormatInt(limit, 10))
	_, _ = io.CopyN(w, file, limit)
}

func (s *Server) handleFSSearch(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	includeDirectories, _ := strconv.ParseBool(r.URL.Query().Get("includeDirectories"))
	result := s.fs.SearchEntries(r.PathValue("id"), r.URL.Query().Get("q"), limit, includeDirectories)
	writeData(w, http.StatusOK, result)
}

func (s *Server) handleFSTextSearch(w http.ResponseWriter, r *http.Request) {
	var body workspacefs.TextSearchRequest
	if err := decodeLimitedJSON(w, r, &body, workspacefs.MaxEditableBytes*6+(1<<20)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.fs.SearchText(r.Context(), r.PathValue("id"), body)
	if err != nil {
		writeWorkspaceFSError(w, err)
		return
	}
	writeData(w, http.StatusOK, result)
}

func (s *Server) handleFSTextReplace(w http.ResponseWriter, r *http.Request) {
	var body workspacefs.TextReplaceRequest
	if err := decodeLimitedJSON(w, r, &body, workspacefs.MaxEditableBytes*6+(1<<20)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.fs.ReplaceText(r.Context(), r.PathValue("id"), body)
	if err != nil {
		var partial *workspacefs.PartialReplaceError
		if errors.As(err, &partial) {
			writeCodedError(w, http.StatusInternalServerError, "replace_partial", "replacement completed only partially", partial.Response)
			return
		}
		writeWorkspaceFSError(w, err)
		return
	}
	writeData(w, http.StatusOK, result)
}

func writeWorkspaceFSError(w http.ResponseWriter, err error) {
	var fsError *workspacefs.Error
	if !errors.As(err, &fsError) {
		writeCodedError(w, http.StatusInternalServerError, "filesystem_error", err.Error(), nil)
		return
	}
	status := http.StatusBadRequest
	switch fsError.Code {
	case "not_found", "workspace_not_found", "root_not_found", "trash_not_found", "parent_not_found":
		status = http.StatusNotFound
	case "path_outside_workspace", "root_mutation_forbidden":
		status = http.StatusForbidden
	case "revision_conflict", "already_exists", "restore_collision":
		status = http.StatusConflict
	case "search_conflict":
		status = http.StatusConflict
	case "file_too_large":
		status = http.StatusRequestEntityTooLarge
	case "unsupported_file", "invalid_utf8", "unsupported_preview":
		status = http.StatusUnsupportedMediaType
	}
	var details any
	if fsError.Current != nil {
		details = map[string]any{"current": fsError.Current}
	}
	writeCodedError(w, status, fsError.Code, fsError.Message, details)
}
