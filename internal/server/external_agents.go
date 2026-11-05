package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/externalagent"
)

type externalSession struct {
	ID            string   `json:"id"`
	ChatID        string   `json:"chatId"`
	BotID         string   `json:"botId"`
	EndpointID    string   `json:"endpointId"`
	SessionID     string   `json:"sessionId"`
	Fingerprint   string   `json:"fingerprint"`
	LastMessageID string   `json:"lastMessageId"`
	PrefixHash    string   `json:"prefixHash"`
	Ready         bool     `json:"ready"`
	Delivered     []string `json:"delivered"`
}

func externalHash(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func externalVisible(messages []domain.Message, bot string) []domain.Message {
	var out []domain.Message
	for _, m := range messages {
		if m.Role == "user" && slices.Contains(m.Recipients, bot) || m.Role != "user" && m.BotID == bot {
			out = append(out, m)
		}
	}
	return out
}

func (s *Server) externalPrompt(bot domain.Bot, chat string, messages []domain.Message) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "You are responding for Echo bot @%s (%s).\nBot instructions:\n%s\nProject locations (context, not access restrictions): %s\nUse your own tools and configuration. Echo's tools and plugins are not available in this session.\nThe following transcript is context; answer the latest request. Inspect the current filesystem before repeating any interrupted work.\n", bot.Handle, bot.Name, bot.Prompt, strings.Join(bot.Roots, ", "))
	for _, m := range messages {
		fmt.Fprintf(&b, "\n--- %s / %s [%s] ---\n%s\n", m.Role, m.Author, m.Status, groupModelContent(m))
		for _, call := range m.Calls {
			fmt.Fprintf(&b, "Prior Echo tool request (historical context): %s\n%s\n", call.Function.Name, call.Function.Arguments)
		}
		for _, seg := range m.Segments {
			if seg.Type == "file" && slices.Contains(seg.AllowedBots, bot.ID) {
				fmt.Fprintf(&b, "Referenced path: %s\n", seg.Path)
			}
		}
		for _, a := range m.Attachments {
			path, err := s.Store.ExternalAttachment(chat, a.ID)
			if err != nil {
				return "", fmt.Errorf("attachment %s: %w", a.Name, err)
			}
			fmt.Fprintf(&b, "Attached file %q (%s): %s\n", a.Name, a.MediaType, path)
		}
	}
	if b.Len() > 4<<20 {
		return "", errors.New("external agent context exceeds 4 MiB; start a new chat with a concise task")
	}
	return b.String(), nil
}

func (s *Server) runExternalAgent(ctx context.Context, d *dispatch, bot domain.Bot, run domain.Run, endpoint domain.Endpoint) (err error) {
	id := externalHash([]string{run.ChatID, bot.ID, endpoint.ID})
	gate, _ := s.externalLocks.LoadOrStore(id, make(chan struct{}, 1))
	lock := gate.(chan struct{})
	select {
	case lock <- struct{}{}:
		defer func() { <-lock }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	dir, err := externalagent.Directory(endpoint, bot.Roots)
	if err != nil {
		return err
	}
	fingerprint := externalHash([]any{endpoint.Kind, endpoint.Executable, dir, bot.Prompt, bot.Name, bot.Handle, bot.Roots, run.Generation.Model, run.Generation.Thinking})
	var saved externalSession
	_ = s.Store.Get("external-sessions", id, &saved)
	var pending []string
	for _, req := range d.group.Drain(bot.ID) {
		pending = append(pending, req.ID)
		m := domain.Message{ID: domain.ID(), ChatID: run.ChatID, RunID: run.ID, BotID: bot.ID, Role: "user", Author: "Bot handoff", Recipients: []string{bot.ID}, Content: fmt.Sprintf("Task from @%s:\n%s", req.From, req.Content), Status: "completed", CreatedAt: domain.Now()}
		if err = s.Store.AddMessage(m); err != nil {
			return err
		}
		s.emit("message", m.ChatID, m.RunID, m.BotID, m)
	}
	select {
	case <-d.wake[bot.ID]:
	default:
	}
	all, err := s.Store.Messages(run.ChatID)
	if err != nil {
		return err
	}
	// A queued user's input can precede the preceding turn's final response in
	// storage. Select by originating input, not by the response's insert order.
	runs, err := s.Store.ChatRuns(run.ChatID)
	if err != nil {
		return err
	}
	inputs := map[string]bool{}
	for _, m := range all {
		inputs[m.ID] = true
		if m.ID == run.UserMessageID {
			break
		}
	}
	allowedRuns := map[string]bool{run.ID: true}
	for _, r := range runs {
		if inputs[r.UserMessageID] {
			allowedRuns[r.ID] = true
		}
	}
	var visible []domain.Message
	for _, m := range externalVisible(all, bot.ID) {
		if inputs[m.ID] || allowedRuns[m.RunID] {
			visible = append(visible, m)
		}
	}
	input := visible
	sessionID := ""
	if saved.Ready && saved.Fingerprint == fingerprint {
		known := map[string]bool{}
		for _, id := range saved.Delivered {
			known[id] = true
		}
		var previous, unseen []domain.Message
		for _, m := range visible {
			if known[m.ID] {
				previous = append(previous, m)
			} else {
				unseen = append(unseen, m)
			}
		}
		if len(previous) == len(saved.Delivered) && externalHash(previous) == saved.PrefixHash {
			sessionID, input = saved.SessionID, unseen
		}
	}
	prompt, err := s.externalPrompt(bot, run.ChatID, input)
	if err != nil {
		return err
	}
	if saved.ID != "" && !saved.Ready {
		prompt = "The previous external invocation failed, was cancelled, or was interrupted. This is a fresh native session. Filesystem changes may remain; inspect the recorded work before continuing.\n\n" + prompt
	}
	next := externalSession{ID: id, ChatID: run.ChatID, BotID: bot.ID, EndpointID: endpoint.ID, Fingerprint: fingerprint}
	// Write before launching: a crash must never resume uncertain native state.
	if err = s.Store.Put("external-sessions", id, next); err != nil {
		return err
	}
	activities := map[string]domain.Message{}
	lastUpdate := map[string]time.Time{}
	emit := func(event externalagent.Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		m, exists := activities[event.ID]
		if !exists {
			m = domain.Message{ID: domain.ID(), ChatID: run.ChatID, RunID: run.ID, BotID: bot.ID, Author: bot.Name + " · " + event.Name, Role: "activity", Status: "running", CreatedAt: domain.Now()}
		}
		if event.Name != "" {
			m.Author = bot.Name + " · " + event.Name
		}
		content := event.Content
		if exists && event.Name == "" {
			content = m.Content + "\n\nResult:\n" + content
		}
		m.Content, m.Status = content, event.Status
		activities[event.ID] = m
		if exists && m.Status == "running" && time.Since(lastUpdate[event.ID]) < 100*time.Millisecond {
			return nil
		}
		if exists {
			err = s.Store.FinishMessage(m)
		} else {
			err = s.Store.AddMessage(m)
		}
		if err != nil {
			return err
		}
		activities[event.ID] = m
		lastUpdate[event.ID] = time.Now()
		s.emit("message", m.ChatID, m.RunID, m.BotID, m)
		return nil
	}
	result, runErr := (externalagent.Runner{Process: s.Process}).Run(ctx, externalagent.Request{Endpoint: endpoint, Directory: dir, Model: run.Generation.Model, Thinking: run.Generation.Thinking, SessionID: sessionID, Prompt: prompt}, emit)
	status := "completed"
	if runErr != nil {
		status = "failed"
	}
	if ctx.Err() != nil {
		status, runErr = "cancelled", ctx.Err()
	}
	for _, m := range activities {
		if m.Status == "running" {
			m.Status = status
			if status == "completed" {
				m.Status = "completed"
			}
			err = errors.Join(err, s.Store.FinishMessage(m))
			s.emit("message", m.ChatID, m.RunID, m.BotID, m)
		}
	}
	if run.Statistics != nil {
		run.Statistics.ToolCalls += result.ToolCalls
		run.Statistics.ExternalAgent = true
	}
	if result.SessionID != "" {
		err = errors.Join(err, s.recordCompletion(ctx, domain.ID(), result.Usage))
	}
	content := result.Content
	if runErr != nil {
		if content != "" {
			content += "\n\n"
		}
		content += "External agent stopped: " + runErr.Error()
	}
	m := domain.Message{ID: domain.ID(), ChatID: run.ChatID, RunID: run.ID, BotID: bot.ID, Author: bot.Name, Role: "assistant", Content: content, Status: status, Usage: result.Usage, CreatedAt: domain.Now()}
	if e := s.Store.AddMessage(m); e != nil {
		return errors.Join(runErr, err, e)
	}
	err = errors.Join(err, s.Store.FinishMessage(m, s.messageAlert(m)...))
	s.emit("message", m.ChatID, m.RunID, m.BotID, m)
	if runErr != nil || err != nil {
		return errors.Join(runErr, err)
	}
	// Capture only the history actually known to this native session. An input
	// queued during this turn belongs to its own later invocation.
	all, err = s.Store.Messages(run.ChatID)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, m := range visible {
		known[m.ID] = true
	}
	var synced []domain.Message
	for _, item := range externalVisible(all, bot.ID) {
		if known[item.ID] || item.RunID == run.ID {
			synced = append(synced, item)
		}
	}
	for _, item := range synced {
		next.Delivered = append(next.Delivered, item.ID)
	}
	next.SessionID, next.LastMessageID, next.PrefixHash, next.Ready = result.SessionID, m.ID, externalHash(synced), true
	if err = s.Store.Put("external-sessions", id, next); err != nil {
		return err
	}
	for _, request := range pending {
		d.group.Reply(bot.ID, request, result.Content)
	}
	return nil
}

func (s *Server) externalAgentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/endpoints/{id}/check", func(w http.ResponseWriter, r *http.Request) {
		var e domain.Endpoint
		if err := s.Store.Get("endpoints", r.PathValue("id"), &e); err != nil {
			respond(w, nil, err)
			return
		}
		dir, err := externalagent.Directory(e, nil)
		if err != nil {
			respond(w, nil, err)
			return
		}
		check, err := (externalagent.Runner{Process: s.Process}).Check(r.Context(), e, dir)
		if err != nil {
			check.Message = err.Error()
		}
		respond(w, check, nil)
	})
	mux.HandleFunc("POST /api/v1/external-agents/reset", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ChatID     string `json:"chatId"`
			HomeNodeID string `json:"homeNodeId"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		id := input.ChatID
		if input.HomeNodeID != "" && input.HomeNodeID != s.Swarm.Node().ID {
			id = projectionID(input.HomeNodeID, id)
		}
		if _, err := s.Store.Chat(id); err != nil {
			respond(w, nil, err)
			return
		}
		finish := s.stopChat(id)
		defer finish()
		raw, err := s.Store.List("external-sessions")
		for _, data := range raw {
			var session externalSession
			if e := json.Unmarshal(data, &session); e != nil {
				err = errors.Join(err, e)
				continue
			}
			if session.ChatID == id {
				err = errors.Join(err, s.Store.Delete("external-sessions", session.ID))
			}
		}
		respond(w, true, err)
	})
}
