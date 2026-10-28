package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

func acceptedDispatch(id string) *dispatch {
	done := make(chan struct{})
	close(done)
	return &dispatch{id: id, done: done}
}

func validateMessageContent(content string) error {
	if strings.TrimSpace(content) == "" {
		return errors.New("message is empty")
	}
	if len(content) > 1<<20 {
		return errors.New("message is too large")
	}
	return nil
}

// Caller holds mu, sharing the admission boundary with send and stopChat.
func (s *Server) messageMutationGuard(chatID string) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.maintenance {
		return errMaintenance
	}
	if s.closingChats[chatID] > 0 {
		return errors.New("chat is stopping or being deleted")
	}
	for _, d := range s.dispatches {
		if d.chatID == chatID {
			return errors.New("Wait for the current response to finish before editing or deleting messages.")
		}
	}
	c, err := s.Store.Chat(chatID)
	if err != nil {
		return err
	}
	if c.Projection {
		return errors.New("edit messages on the chat's home machine")
	}
	return nil
}

func (s *Server) actionMessage(chatID, id string) (domain.Message, error) {
	messages, err := s.Store.Messages(chatID)
	if err != nil {
		return domain.Message{}, err
	}
	for _, m := range messages {
		if m.ID == id {
			return m, nil
		}
	}
	return domain.Message{}, errors.New("message was not found")
}

func (s *Server) mutateMessage(chatID, id string, content *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.messageMutationGuard(chatID); err != nil {
		return err
	}
	m, err := s.actionMessage(chatID, id)
	if err != nil {
		return err
	}
	if m.Role != "user" && m.Role != "assistant" || m.Status == "running" || m.Role == "user" && m.RunID != "" {
		return errors.New("only finished user and assistant messages can be changed")
	}
	if content != nil {
		if m.Role != "assistant" {
			return errors.New("user messages must be edited with Save & Resubmit")
		}
		if err = validateMessageContent(*content); err != nil {
			return err
		}
		err = s.Store.UpdateMessageContent(chatID, id, *content)
	} else {
		err = s.Store.DeleteMessage(chatID, id)
	}
	if err == nil {
		s.emitLocked("changed", chatID, "", "", map[string]bool{"messagesChanged": true})
	}
	return err
}

func (s *Server) prepareResubmit(chatID string, req SendRequest) (SendRequest, error) {
	if err := validateMessageContent(req.Content); err != nil {
		return req, err
	}
	if req.RequestID == "" || len(req.RequestID) > 128 {
		return req, errors.New("resubmit requires a request id of at most 128 characters")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.messageMutationGuard(chatID); err != nil {
		return req, err
	}
	m, err := s.actionMessage(chatID, req.replaceMessageID)
	if err != nil {
		return req, err
	}
	if m.Role != "user" || m.RunID != "" {
		return req, errors.New("only user messages can be resubmitted")
	}
	// Retain uploads; acceptance transfers ownership before deleting the old row.
	req.replacementAttachments = m.Attachments
	c, err := s.Store.Chat(chatID)
	if err != nil {
		return req, err
	}
	req.Generation, req.GenerationByNode = c.Generation, c.GenerationByNode
	return req, nil
}

func (s *Server) sendAttachments(chatID string, req SendRequest) ([]domain.Attachment, error) {
	if req.replaceMessageID != "" {
		return req.replacementAttachments, nil
	}
	return s.resolveAttachments(chatID, req.Attachments)
}

func (s *Server) messageActionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/chats/{id}/messages/{messageId}/fork", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RequestID string `json:"requestId"`
		}
		if err := decode(w, r, &body); err != nil {
			respond(w, nil, err)
			return
		}
		id, err := s.forkChat(r.Context(), r.PathValue("id"), r.PathValue("messageId"), body.RequestID)
		respond(w, map[string]string{"chatId": id}, err)
	})
	mux.HandleFunc("DELETE /api/v1/chats/{id}/messages/{messageId}", func(w http.ResponseWriter, r *http.Request) {
		err := s.mutateMessage(r.PathValue("id"), r.PathValue("messageId"), nil)
		respond(w, map[string]bool{"deleted": err == nil}, err)
	})
	mux.HandleFunc("PATCH /api/v1/chats/{id}/messages/{messageId}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Content string `json:"content"`
		}
		if err := decode(w, r, &body); err != nil {
			respond(w, nil, err)
			return
		}
		err := s.mutateMessage(r.PathValue("id"), r.PathValue("messageId"), &body.Content)
		respond(w, map[string]bool{"updated": err == nil}, err)
	})
	mux.HandleFunc("POST /api/v1/chats/{id}/messages/{messageId}/resubmit", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Content   string `json:"content"`
			RequestID string `json:"requestId"`
		}
		if err := decode(w, r, &body); err != nil {
			respond(w, nil, err)
			return
		}
		req := SendRequest{Content: body.Content, RequestID: body.RequestID, replaceMessageID: r.PathValue("messageId"), OriginNodeID: s.Swarm.Node().ID}
		if caller, ok := swarm.Caller(r.Context()); ok {
			req.OriginNodeID = caller.NodeID
		}
		d, err := s.send(r.PathValue("id"), req)
		if err != nil {
			respond(w, nil, err)
			return
		}
		respond(w, map[string]string{"dispatchId": d.id}, nil)
	})
}
