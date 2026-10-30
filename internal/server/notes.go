package server

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
)

var noteNodeID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (s *Server) notesRoutes(mux *http.ServeMux) {
	for _, route := range []string{"GET /notes", "POST /notes", "GET /notes/{noteId}", "PUT /notes/{noteId}", "DELETE /notes/{noteId}"} {
		method, path, _ := cutEditorRoute(route)
		mux.HandleFunc(method+" /api/v1/plugins/notes"+path, s.notesRequest)
	}
}

func (s *Server) notesRequest(w http.ResponseWriter, r *http.Request) {
	// Handler's owner-session/swarm authentication applies to all these routes.
	bot, err := s.Bot("echo")
	if err != nil || !s.Plugins.Enabled(bot, "notes") {
		editorError(w, http.StatusForbidden, "Notes is disabled")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("noteId")
	if r.Method == "GET" {
		if id == "" {
			notes, err := s.Store.List("notes")
			respond(w, notes, err)
			return
		}
		note, err := s.Store.Note(id)
		s.noteResponse(w, note, err)
		return
	}
	var input struct {
		Title    string               `json:"title"`
		Markdown string               `json:"markdown"`
		Mentions []domain.NoteMention `json:"mentions"`
		Revision int64                `json:"revision"`
	}
	if r.Method == "DELETE" {
		input.Revision, err = strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	} else {
		err = decode(w, r, &input)
	}
	if err != nil {
		respond(w, nil, err)
		return
	}
	create, remove := r.Method == "POST", r.Method == "DELETE"
	if (!create && input.Revision < 1) || (create && input.Revision != 0) {
		editorError(w, http.StatusBadRequest, "A matching note revision is required")
		return
	}
	note := domain.Note{ID: id, Title: strings.TrimSpace(input.Title), Markdown: input.Markdown, Mentions: input.Mentions, Revision: input.Revision}
	if !remove {
		if note.Title == "" {
			note.Title = "Untitled note"
		}
		if err := validateNote(note); err != nil {
			respond(w, nil, err)
			return
		}
		if note.Mentions == nil {
			note.Mentions = []domain.NoteMention{}
		}
	}
	note, err = s.Store.MutateNote(note, create, remove)
	s.noteResponse(w, note, err)
}

func validateNote(note domain.Note) error {
	if !utf8.ValidString(note.Title) || !utf8.ValidString(note.Markdown) || utf8.RuneCountInString(note.Title) > 120 || len(note.Markdown) > 1<<20 {
		return errors.New("Notes allow a 120-character title and up to 1 MiB of Markdown")
	}
	if len(note.Mentions) > 4096 {
		return errors.New("Too many bot mentions")
	}
	body := utf16.Encode([]rune(note.Markdown))
	end := 0
	for _, mention := range note.Mentions {
		if !noteNodeID.MatchString(mention.NodeID) || mention.BotID == "" || len(mention.BotID) > 128 || len(mention.Label) > 256 || !strings.HasPrefix(mention.Label, "@") || mention.Start < end || mention.End <= mention.Start || mention.End > len(body) {
			return errors.New("Invalid bot mention")
		}
		if string(utf16.Decode(body[mention.Start:mention.End])) != mention.Label {
			return errors.New("Bot mention does not match Markdown")
		}
		end = mention.End
	}
	return nil
}

func (s *Server) noteResponse(w http.ResponseWriter, note domain.Note, err error) {
	if errors.Is(err, store.ErrNoteMissing) {
		editorError(w, http.StatusNotFound, "Note was deleted or does not exist")
		return
	}
	if errors.Is(err, store.ErrNoteConflict) {
		editorError(w, http.StatusConflict, err.Error())
		return
	}
	respond(w, note, err)
}
