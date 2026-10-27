package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/brent/echo/internal/calendar"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/plugins"
	"github.com/brent/echo/internal/store"
)

func (s *Server) routes(mux *http.ServeMux) {
	s.developmentRoutes(mux)
	s.sourceControlRoutes(mux)
	s.attachmentRoutes(mux)
	mux.HandleFunc("GET /api/v1/state", func(w http.ResponseWriter, r *http.Request) {
		bots, e := s.Bots()
		if e != nil {
			respond(w, nil, e)
			return
		}
		chats, e := s.Store.Chats()
		if e != nil {
			respond(w, nil, e)
			return
		}
		endpoints, e := s.Store.List("endpoints")
		if e != nil {
			respond(w, nil, e)
			return
		}
		ps, e := s.Plugins.List()
		if e != nil {
			respond(w, nil, e)
			return
		}
		schedules, e := s.Calendar.List()
		if e != nil {
			respond(w, nil, e)
			return
		}
		jobs, e := s.Calendar.Occurrences()
		if e != nil {
			respond(w, nil, e)
			return
		}
		runs, e := s.Store.List("runs")
		respond(w, map[string]any{"bots": bots, "chats": chats, "endpoints": endpoints, "plugins": ps, "schedules": schedules, "occurrences": jobs, "runs": runs}, e)
	})
	mux.HandleFunc("POST /api/v1/bots", func(w http.ResponseWriter, r *http.Request) {
		var b domain.Bot
		if e := decode(w, r, &b); e != nil {
			respond(w, nil, e)
			return
		}
		if b.ID != "" {
			finish := s.stopBot(b.ID)
			defer finish()
		}
		value, e := s.saveBot(b)
		if e == nil {
			e = s.Calendar.ReconcileActivation(domain.Now())
		}
		s.emit("changed", "", "", "", nil)
		respond(w, value, e)
	})
	mux.HandleFunc("DELETE /api/v1/bots/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "echo" {
			respond(w, nil, errors.New("Echo cannot be deleted"))
			return
		}
		finish := s.stopBot(id)
		defer finish()
		if e := s.Calendar.DeleteBot(id); e != nil {
			respond(w, nil, e)
			return
		}
		e := s.Store.Delete("bots", id)
		s.emit("changed", "", "", "", nil)
		respond(w, map[string]bool{"deleted": e == nil}, e)
	})
	mux.HandleFunc("POST /api/v1/endpoints", func(w http.ResponseWriter, r *http.Request) {
		var a struct {
			Endpoint domain.Endpoint `json:"endpoint"`
			APIKey   *string         `json:"apiKey"`
		}
		if e := decode(w, r, &a); e != nil {
			respond(w, nil, e)
			return
		}
		e := a.Endpoint
		if e.ID == "" {
			e.ID = domain.ID()
		}
		var old domain.Endpoint
		s.Store.Get("endpoints", e.ID, &old)
		e.Credential = old.Credential
		if e.URL != old.URL {
			e.Credential = ""
		}
		if err := model.ValidateEndpoint(e); err != nil {
			respond(w, nil, err)
			return
		}
		if e.Provider != "" {
			echo, _ := s.Bot("echo")
			if _, err := s.Plugins.Provider(echo, e.Provider); err != nil {
				respond(w, nil, err)
				return
			}
		}
		if a.APIKey != nil {
			if *a.APIKey == "" {
				if old.Credential != "" {
					s.Secrets.Delete(r.Context(), old.Credential)
				}
				e.Credential = ""
			} else {
				e.Credential = "Echo2/" + s.Dir + "/models/" + e.ID
				if err := s.Secrets.Set(r.Context(), e.Credential, *a.APIKey); err != nil {
					respond(w, nil, err)
					return
				}
			}
		}
		if e.ContextTokens == 0 {
			e.ContextTokens = 32768
		}
		err := s.Store.Put("endpoints", e.ID, e)
		s.emit("changed", "", "", "", nil)
		respond(w, e, err)
	})
	mux.HandleFunc("DELETE /api/v1/endpoints/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var endpoint domain.Endpoint
		s.Store.Get("endpoints", id, &endpoint)
		if endpoint.Credential != "" {
			s.Secrets.Delete(r.Context(), endpoint.Credential)
		}
		e := s.Store.Delete("endpoints", id)
		s.emit("changed", "", "", "", nil)
		respond(w, true, e)
	})
	mux.HandleFunc("GET /api/v1/endpoints/{id}/models", func(w http.ResponseWriter, r *http.Request) {
		var e domain.Endpoint
		if err := s.Store.Get("endpoints", r.PathValue("id"), &e); err != nil {
			respond(w, nil, err)
			return
		}
		key, err := s.key(r.Context(), e)
		if err != nil {
			respond(w, nil, err)
			return
		}
		v, err := s.Model.Models(r.Context(), e, key)
		respond(w, v, err)
	})
	mux.HandleFunc("POST /api/v1/chats", func(w http.ResponseWriter, r *http.Request) {
		c := domain.Chat{ID: domain.ID(), Title: "New chat", Bots: map[string]string{}, CreatedAt: domain.Now(), UpdatedAt: domain.Now(), Generation: domain.Generation{Temperature: 0.6}}
		endpoints, _ := s.Store.List("endpoints")
		if len(endpoints) > 0 {
			var ep domain.Endpoint
			json.Unmarshal(endpoints[0], &ep)
			c.Generation.EndpointID = ep.ID
			c.Generation.Model = ep.Model
			if ep.Temperature != nil {
				c.Generation.Temperature = *ep.Temperature
			}
		}
		e := s.Store.CreateChat(c)
		s.emit("changed", c.ID, "", "", nil)
		respond(w, c, e)
	})
	mux.HandleFunc("DELETE /api/v1/chats/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		finish := s.stopChat(id)
		defer finish()
		e := s.Store.DeleteChat(id)
		s.emit("changed", id, "", "", nil)
		respond(w, true, e)
	})
	mux.HandleFunc("PUT /api/v1/chats/{id}/generation", func(w http.ResponseWriter, r *http.Request) {
		var g domain.Generation
		if e := decode(w, r, &g); e != nil {
			respond(w, nil, e)
			return
		}
		var endpoint domain.Endpoint
		if e := s.Store.Get("endpoints", g.EndpointID, &endpoint); e != nil {
			respond(w, nil, errors.New("choose an available model connection"))
			return
		}
		if e := model.ValidateSettings(domain.Endpoint{Temperature: &g.Temperature, ReasoningEffort: g.Thinking}); e != nil {
			respond(w, nil, e)
			return
		}
		e := s.Store.SetChatGeneration(r.PathValue("id"), g)
		respond(w, g, e)
	})
	mux.HandleFunc("POST /api/v1/chats/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		finish := s.stopChat(r.PathValue("id"))
		defer finish()
		respond(w, true, nil)
	})
	mux.HandleFunc("GET /api/v1/chats/{id}/messages", func(w http.ResponseWriter, r *http.Request) { v, e := s.messages(r.PathValue("id")); respond(w, v, e) })
	mux.HandleFunc("POST /api/v1/chats/{id}/files/reveal", s.revealChangedFile)
	mux.HandleFunc("GET /api/v1/chats/{id}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		messages, sequence, e := s.snapshot(r.PathValue("id"))
		respond(w, map[string]any{"messages": messages, "sequence": sequence}, e)
	})
	mux.HandleFunc("POST /api/v1/chats/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		var a SendRequest
		if e := decode(w, r, &a); e != nil {
			respond(w, nil, e)
			return
		}
		fields := strings.Fields(a.Content)
		if len(fields) > 0 && (fields[0] == "/approve" || fields[0] == "/missed") {
			value, e := s.managementMessage(r.PathValue("id"), a)
			respond(w, value, e)
			return
		}
		d, e := s.send(r.PathValue("id"), a)
		if e != nil {
			respond(w, nil, e)
			return
		}
		respond(w, map[string]any{"dispatchId": d.id}, nil)
	})
	mux.HandleFunc("POST /api/v1/references", func(w http.ResponseWriter, r *http.Request) {
		var a struct {
			Content  string           `json:"content"`
			Query    string           `json:"query"`
			Segments []domain.Segment `json:"segments"`
		}
		if e := decode(w, r, &a); e != nil {
			respond(w, nil, e)
			return
		}
		bots, e := s.recipients(SendRequest{Content: a.Content, Segments: a.Segments})
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, status, e := s.fileReferences(r.Context(), bots, a.Query)
		w.Header().Set("X-Echo-Reference-Indexing", strconv.FormatBool(status.Indexing))
		w.Header().Set("X-Echo-Reference-Incomplete", strconv.FormatBool(status.Incomplete))
		respond(w, out, e)
	})
	mux.HandleFunc("POST /api/v1/references/validate", func(w http.ResponseWriter, r *http.Request) {
		var a SendRequest
		if e := decode(w, r, &a); e != nil {
			respond(w, nil, e)
			return
		}
		bots, e := s.recipients(a)
		if e != nil {
			respond(w, nil, e)
			return
		}
		if e := s.validateFileSegments(&a, bots, false); e != nil {
			respond(w, nil, e)
			return
		}
		out := []domain.Segment{}
		for _, ref := range a.Segments {
			if ref.Type != "file" {
				continue
			}
			out = append(out, ref)
		}
		respond(w, out, nil)
	})
	mux.HandleFunc("POST /api/v1/references/reveal", s.revealDraftReference)
	mux.HandleFunc("POST /api/v1/chats/{id}/references/reveal", s.revealMessageReference)
	mux.HandleFunc("PATCH /api/v1/plugins/{id}", func(w http.ResponseWriter, r *http.Request) {
		m, e := s.Plugins.Get(r.PathValue("id"))
		if e != nil {
			respond(w, nil, e)
			return
		}
		var a struct {
			Enabled bool           `json:"enabled"`
			Config  map[string]any `json:"config"`
		}
		if e = decode(w, r, &a); e != nil {
			respond(w, nil, e)
			return
		}
		if a.Enabled && m.Pending {
			respond(w, nil, errors.New("approve the requested capabilities first"))
			return
		}
		oldCredential := text(m.Config, "credential")
		if sourceControl(m.ID) {
			a.Config, e = s.sourceControlConfig(m, a.Config, true)
		} else {
			b, _ := json.Marshal(a.Config)
			e = plugins.Validate(m.Settings, b)
		}
		if e != nil {
			respond(w, nil, e)
			return
		}
		m.Enabled = a.Enabled
		m.Config = a.Config
		e = s.Plugins.Save(m)
		if e == nil && m.ID == "p4" && oldCredential != "" && oldCredential != text(m.Config, "credential") {
			s.Secrets.Delete(r.Context(), oldCredential)
		}
		if e == nil {
			e = s.Calendar.ReconcileActivation(domain.Now())
		}
		s.emit("changed", "", "", "", nil)
		respond(w, m, e)
	})
	mux.HandleFunc("POST /api/v1/plugins/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		var approval struct {
			Digest string `json:"digest"`
		}
		if e := decode(w, r, &approval); e != nil {
			respond(w, nil, e)
			return
		}
		m, e := s.Plugins.ApproveDigest(r.PathValue("id"), approval.Digest)
		s.emit("changed", "", "", "", nil)
		respond(w, m, e)
	})
	mux.HandleFunc("POST /api/v1/plugins/{id}/rollback", func(w http.ResponseWriter, r *http.Request) {
		e := s.Plugins.Rollback(r.PathValue("id"))
		s.emit("changed", "", "", "", nil)
		respond(w, true, e)
	})
	mux.HandleFunc("GET /api/v1/plugins/{id}/views/{view}", func(w http.ResponseWriter, r *http.Request) {
		m, e := s.Plugins.Get(r.PathValue("id"))
		if e != nil {
			respond(w, nil, e)
			return
		}
		if !m.Enabled || m.Pending {
			respond(w, nil, errors.New("plugin is inactive"))
			return
		}
		for _, view := range m.Views {
			if view.ID == r.PathValue("view") {
				respond(w, view, nil)
				return
			}
		}
		respond(w, nil, errors.New("view not found"))
	})
	mux.HandleFunc("POST /api/v1/plugins/{id}/bridge", func(w http.ResponseWriter, r *http.Request) {
		var a struct {
			BotID  string          `json:"botId"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if e := decode(w, r, &a); e != nil {
			respond(w, nil, e)
			return
		}
		if a.BotID == "" {
			a.BotID = "echo"
		}
		b, e := s.Bot(a.BotID)
		if e != nil {
			respond(w, nil, e)
			return
		}
		if !strings.HasPrefix(a.Method, "storage.") {
			respond(w, nil, errors.New("UI bridge supports only namespaced storage"))
			return
		}
		v, e := s.pluginCall(r.Context(), domain.ExecutionScope{Bot: b, PluginID: r.PathValue("id")}, a.Method, a.Params)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /api/v1/schedules", func(w http.ResponseWriter, r *http.Request) {
		var a calendar.Schedule
		if e := decode(w, r, &a); e != nil {
			respond(w, nil, e)
			return
		}
		if _, e := s.Bot(a.BotID); e != nil {
			respond(w, nil, e)
			return
		}
		v, e := s.Calendar.Save(a, domain.Now())
		s.emit("changed", "", "", "", nil)
		respond(w, v, e)
	})
	mux.HandleFunc("DELETE /api/v1/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		e := s.Calendar.Delete(r.PathValue("id"))
		s.emit("changed", "", "", "", nil)
		respond(w, true, e)
	})
	mux.HandleFunc("POST /api/v1/missed/review", func(w http.ResponseWriter, r *http.Request) {
		var a struct {
			IDs         []string `json:"ids"`
			Concurrency int      `json:"concurrency"`
		}
		if e := decode(w, r, &a); e != nil {
			respond(w, nil, e)
			return
		}
		e := s.Calendar.Review(s.ctx, a.IDs, a.Concurrency)
		s.emit("changed", "", "", "", nil)
		respond(w, true, e)
	})
}
func (s *Server) managementMessage(chatID string, req SendRequest) (any, error) {
	if prior := s.Store.Accepted(chatID, req.RequestID); prior != "" {
		return map[string]any{"ownerCommand": true, "dispatchId": prior}, nil
	}
	c, e := s.Store.Chat(chatID)
	if e != nil {
		return nil, e
	}
	if c.Title == "New chat" {
		c.Title = truncate(req.Content, 70)
	}
	c.UpdatedAt = domain.Now()
	run := domain.Run{ID: domain.ID(), DispatchID: domain.ID(), ChatID: c.ID, BotID: "echo", Status: "running", CreatedAt: domain.Now()}
	user := domain.Message{ID: domain.ID(), ChatID: c.ID, Author: "You", Role: "user", Content: req.Content, Recipients: []string{"echo"}, Segments: []domain.Segment{{Type: "text", Text: req.Content}}, Status: "completed", CreatedAt: domain.Now()}
	if e = s.Store.Accept(c, user, []domain.Run{run}, req.RequestID, run.DispatchID); e != nil {
		return nil, e
	}
	s.emit("message", c.ID, run.ID, "echo", user)
	_, operationErr := s.ownerCommand(req.Content)
	response := "Saved your selection."
	run.Status = "completed"
	if operationErr != nil {
		response = operationErr.Error()
		run.Status = "failed"
		run.Error = response
	} else if strings.HasPrefix(req.Content, "/approve ") {
		response = "Approved and activated the reviewed plugin package."
	}
	message := domain.Message{ID: domain.ID(), ChatID: c.ID, RunID: run.ID, BotID: "echo", Author: "Echo", Role: "assistant", Content: response, Status: run.Status, CreatedAt: domain.Now()}
	if e = s.Store.AddMessage(message); e != nil {
		return nil, e
	}
	run.UpdatedAt = domain.Now()
	if e = s.Store.Put("runs", run.ID, run); e != nil {
		return nil, e
	}
	s.emit("message", c.ID, run.ID, "echo", message)
	s.emit("changed", c.ID, run.ID, "echo", nil)
	return map[string]any{"ownerCommand": true, "dispatchId": run.DispatchID}, nil
}
func (s *Server) ownerCommand(content string) (bool, error) {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return false, nil
	}
	if fields[0] == "/approve" {
		if len(fields) != 3 {
			return true, errors.New("usage: /approve plugin-id digest (from the requested package)")
		}
		_, e := s.Plugins.ApproveDigest(fields[1], fields[2])
		s.emit("changed", "", "", "", nil)
		return true, e
	}
	if fields[0] == "/missed" {
		if len(fields) == 2 && fields[1] == "skip-all" {
			e := s.Calendar.Review(s.ctx, nil, 1)
			s.emit("changed", "", "", "", nil)
			return true, e
		}
		if len(fields) >= 4 && fields[1] == "run" {
			n, e := strconv.Atoi(fields[2])
			if e != nil {
				return true, e
			}
			e = s.Calendar.Review(s.ctx, fields[3:], n)
			s.emit("changed", "", "", "", nil)
			return true, e
		}
		return true, errors.New("usage: /missed run <concurrency> <job IDs>, or /missed skip-all")
	}
	return false, nil
}

var _ = store.Decode[domain.Endpoint]
