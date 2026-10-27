package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/editorfs"
)

func (s *Server) handleFSRoots(w http.ResponseWriter, r *http.Request) {
	roots, err := s.EditorFS.Roots(r.PathValue("botId"))
	if err != nil {
		writeEditorFSError(w, err)
		return
	}
	editorData(w, http.StatusOK, map[string]any{"roots": roots})
}

func (s *Server) handleFSEntries(w http.ResponseWriter, r *http.Request) {
	entries, err := s.EditorFS.List(r.PathValue("botId"), editorfs.FileRef{
		RootID: r.URL.Query().Get("rootId"), Path: r.URL.Query().Get("path"),
	})
	if err != nil {
		writeEditorFSError(w, err)
		return
	}
	editorData(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) handleFSReadFile(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.EditorFS.Read(r.PathValue("botId"), editorfs.FileRef{
		RootID: r.URL.Query().Get("rootId"), Path: r.URL.Query().Get("path"),
	})
	if err != nil {
		writeEditorFSError(w, err)
		return
	}
	editorData(w, http.StatusOK, snapshot)
}

func (s *Server) handleFSSaveFile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref              editorfs.FileRef `json:"ref"`
		Content          string           `json:"content"`
		ExpectedRevision string           `json:"expectedRevision"`
		CreateOnly       bool             `json:"createOnly"`
		HasBOM           bool             `json:"hasBom"`
	}
	if err := editorDecode(w, r, &body, editorfs.MaxEditableBytes*6+(1<<20)); err != nil {
		editorError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, err := s.EditorFS.SaveContext(r.Context(), r.PathValue("botId"), editorfs.SaveRequest{
		Ref: body.Ref, Content: body.Content, ExpectedRevision: body.ExpectedRevision,
		CreateOnly: body.CreateOnly, HasBOM: body.HasBOM,
	})
	if err != nil {
		writeEditorFSError(w, err)
		return
	}
	editorData(w, http.StatusOK, snapshot)
}

func (s *Server) handleFSCreateEntry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Parent  editorfs.FileRef `json:"parent"`
		Name    string           `json:"name"`
		Kind    string           `json:"kind"`
		Content string           `json:"content"`
		HasBOM  bool             `json:"hasBom"`
	}
	if err := editorDecode(w, r, &body, editorfs.MaxEditableBytes*6+(1<<20)); err != nil {
		editorError(w, http.StatusBadRequest, err.Error())
		return
	}
	entry, snapshot, err := s.EditorFS.CreateContext(r.Context(), r.PathValue("botId"), editorfs.CreateRequest{
		Parent: body.Parent, Name: body.Name, Kind: body.Kind, Content: body.Content, HasBOM: body.HasBOM,
	})
	if err != nil {
		writeEditorFSError(w, err)
		return
	}
	editorData(w, http.StatusCreated, map[string]any{"entry": entry, "file": snapshot})
}

func (s *Server) handleFSRenameEntry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref               editorfs.FileRef  `json:"ref"`
		NewName           string            `json:"newName"`
		DestinationParent *editorfs.FileRef `json:"destinationParent"`
	}
	if err := editorDecode(w, r, &body, 64<<10); err != nil {
		editorError(w, http.StatusBadRequest, err.Error())
		return
	}
	var entry editorfs.Entry
	var err error
	if body.DestinationParent != nil {
		entry, err = s.EditorFS.MoveContext(r.Context(), r.PathValue("botId"), body.Ref, *body.DestinationParent)
	} else {
		entry, err = s.EditorFS.RenameContext(r.Context(), r.PathValue("botId"), body.Ref, body.NewName)
	}
	if err != nil {
		writeEditorFSError(w, err)
		return
	}
	editorData(w, http.StatusOK, map[string]any{"entry": entry, "previousRef": body.Ref})
}

func (s *Server) handleFSTrashEntry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref editorfs.FileRef `json:"ref"`
	}
	if err := editorDecode(w, r, &body, 64<<10); err != nil {
		editorError(w, http.StatusBadRequest, err.Error())
		return
	}
	item, err := s.EditorFS.TrashContext(r.Context(), r.PathValue("botId"), body.Ref)
	if err != nil {
		writeEditorFSError(w, err)
		return
	}
	editorData(w, http.StatusOK, map[string]any{"trash": item})
}

func (s *Server) handleFSListTrash(w http.ResponseWriter, r *http.Request) {
	items, err := s.EditorFS.ListTrash(r.PathValue("botId"))
	if err != nil {
		writeEditorFSError(w, err)
		return
	}
	editorData(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleFSRestoreTrash(w http.ResponseWriter, r *http.Request) {
	entry, err := s.EditorFS.RestoreContext(r.Context(), r.PathValue("botId"), r.PathValue("trashId"))
	if err != nil {
		writeEditorFSError(w, err)
		return
	}
	editorData(w, http.StatusOK, map[string]any{"entry": entry})
}

func (s *Server) handleFSPurgeTrash(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("confirmed") != "true" {
		editorCodedError(w, http.StatusBadRequest, "confirmation_required", "permanent deletion requires explicit confirmation", nil)
		return
	}
	if err := s.EditorFS.PurgeTrash(r.PathValue("botId"), r.PathValue("trashId")); err != nil {
		writeEditorFSError(w, err)
		return
	}
	editorData(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleFSReveal(w http.ResponseWriter, r *http.Request) {
	if !localRevealRequest(w, r) {
		return
	}
	var body struct {
		Ref editorfs.FileRef `json:"ref"`
	}
	if err := editorDecode(w, r, &body, 64<<10); err != nil {
		writeEditorFSError(w, err)
		return
	}
	path, err := s.EditorFS.ResolveExistingHostPath(r.PathValue("botId"), body.Ref, true)
	if err != nil {
		writeEditorFSError(w, err)
		return
	}
	bot, err := s.editorBot(r.PathValue("botId"))
	if err != nil {
		writeEditorFSError(w, err)
		return
	}
	target, err := s.Files.RevealTarget(bot, path)
	if err == nil {
		reveal := s.revealFile
		if reveal == nil {
			reveal = broker.RevealInFileBrowser
		}
		err = reveal(target)
	}
	respond(w, target, err)
}

// handleFSMedia streams a previewable image, video, or audio file straight to the
// browser for the code editor's media surface. The path boundary comes from
// the same confined resolver as every other filesystem endpoint; symlinks
// leaving the workspace are rejected. Oversized images stream their first
// MaxMediaBytes chunk (browsers fail decoding the remainder gracefully),
// while oversized videos and audio are refused outright because partial media
// files do not play.
func (s *Server) handleFSMedia(w http.ResponseWriter, r *http.Request) {
	ref := editorfs.FileRef{RootID: r.URL.Query().Get("rootId"), Path: r.URL.Query().Get("path")}
	_, size, mediaType, truncated, err := s.EditorFS.MediaMeta(r.PathValue("botId"), ref)
	if err != nil {
		writeEditorFSError(w, err)
		return
	}
	file, err := s.EditorFS.OpenMedia(r.PathValue("botId"), ref)
	if err != nil {
		editorCodedError(w, http.StatusNotFound, "not_found", "file not found", nil)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "no-store")
	limit := size
	if truncated {
		limit = editorfs.MaxMediaBytes
	}
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	info, err := file.Stat()
	if err != nil {
		return
	}
	http.ServeContent(w, r, ref.Path, info.ModTime(), io.NewSectionReader(file, 0, limit))
}

func (s *Server) handleFSSearch(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	includeDirectories, _ := strconv.ParseBool(r.URL.Query().Get("includeDirectories"))
	result := s.EditorFS.SearchEntries(r.PathValue("botId"), r.URL.Query().Get("q"), limit, includeDirectories)
	editorData(w, http.StatusOK, result)
}

func (s *Server) handleFSTextSearch(w http.ResponseWriter, r *http.Request) {
	var body editorfs.TextSearchRequest
	if err := editorDecode(w, r, &body, editorfs.MaxEditableBytes*6+(1<<20)); err != nil {
		editorError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.EditorFS.SearchText(r.Context(), r.PathValue("botId"), body)
	if err != nil {
		writeEditorFSError(w, err)
		return
	}
	editorData(w, http.StatusOK, result)
}

func (s *Server) handleFSTextReplace(w http.ResponseWriter, r *http.Request) {
	var body editorfs.TextReplaceRequest
	if err := editorDecode(w, r, &body, editorfs.MaxEditableBytes*6+(1<<20)); err != nil {
		editorError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.EditorFS.ReplaceText(r.Context(), r.PathValue("botId"), body)
	if err != nil {
		var partial *editorfs.PartialReplaceError
		if errors.As(err, &partial) {
			editorCodedError(w, http.StatusInternalServerError, "replace_partial", "replacement completed only partially", partial.Response)
			return
		}
		writeEditorFSError(w, err)
		return
	}
	editorData(w, http.StatusOK, result)
}

func writeEditorFSError(w http.ResponseWriter, err error) {
	var fsError *editorfs.Error
	if !errors.As(err, &fsError) {
		editorCodedError(w, http.StatusInternalServerError, "filesystem_error", err.Error(), nil)
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
	editorCodedError(w, status, fsError.Code, fsError.Message, details)
}

func editorData(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func editorCodedError(w http.ResponseWriter, status int, code, message string, details any) {
	editorData(w, status, map[string]any{"error": message, "code": code, "details": details})
}
func editorError(w http.ResponseWriter, status int, message string) {
	editorCodedError(w, status, "editor_error", message, nil)
}
func editorDecode(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(v)
}
