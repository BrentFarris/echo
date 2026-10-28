package server

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

// New runs record their input explicitly. For legacy runs, use the latest
// input before admission only if no other dispatch intervenes. Run records
// survive message deletion, so a deleted input cannot select an older turn.
func rerunInputs(messages []domain.Message, runs []domain.Run) map[string]domain.Message {
	users := map[string]domain.Message{}
	for _, m := range messages {
		if m.Role == "user" && m.RunID == "" {
			users[m.ID] = m
		}
	}
	inputs := map[string]domain.Message{}
	for _, run := range runs {
		if run.UserMessageID != "" {
			if m, ok := users[run.UserMessageID]; ok {
				inputs[run.ID] = m
			}
			continue
		}
		var candidate domain.Message
		for _, m := range users {
			if !m.CreatedAt.IsZero() && !m.CreatedAt.After(run.CreatedAt) && (candidate.ID == "" || m.CreatedAt.After(candidate.CreatedAt)) {
				candidate = m
			}
		}
		if candidate.ID == "" || run.DispatchID == "" {
			continue
		}
		for _, other := range runs {
			if other.DispatchID != run.DispatchID && !other.CreatedAt.Before(candidate.CreatedAt) && !other.CreatedAt.After(run.CreatedAt) {
				candidate = domain.Message{}
				break
			}
		}
		if candidate.ID != "" {
			inputs[run.ID] = candidate
		}
	}
	return inputs
}

func transcriptFingerprint(messages []domain.Message) [32]byte {
	data, _ := json.Marshal(messages)
	return sha256.Sum256(data)
}

// Caller holds the admission lock. The normal send pipeline validates model,
// recipients and current file permissions before atomically replacing history.
func (s *Server) prepareRerunLocked(chatID string, req SendRequest) (SendRequest, error) {
	messages, err := s.Store.Messages(chatID)
	if err != nil {
		return req, err
	}
	runs, err := s.Store.ChatRuns(chatID)
	if err != nil {
		return req, err
	}
	inputs := rerunInputs(messages, runs)
	var target domain.Message
	for _, m := range messages {
		if m.ID == req.rerunMessageID {
			target = m
			break
		}
	}
	finished := target.Status == "completed" || target.Status == "failed" || target.Status == "cancelled" || target.Status == "interrupted"
	if !finished || target.Role != "user" && target.Role != "assistant" || target.Role == "user" && target.RunID != "" || len(target.Calls) != 0 {
		return req, errors.New("rerun a finished user message or final assistant response")
	}
	input := target
	if target.Role == "assistant" {
		input = inputs[target.RunID]
		if input.ID == "" {
			return req, errors.New("cannot rerun without the original user message")
		}
	}
	if strings.TrimSpace(input.Content) == "" && len(input.Attachments) == 0 {
		return req, errors.New("the original user message is empty")
	}
	c, err := s.Store.Chat(chatID)
	if err != nil {
		return req, err
	}
	req.rerunFingerprint = transcriptFingerprint(messages)
	req.replaceMessageID, req.Content = input.ID, input.Content
	req.replacementAttachments = input.Attachments
	for _, a := range input.Attachments {
		req.Attachments = append(req.Attachments, a.ID)
	}
	req.rerunRecipients = append([]string(nil), input.Recipients...)
	if len(req.rerunRecipients) == 0 {
		req.rerunRecipients = []string{"echo"}
	}
	req.Segments = append([]domain.Segment(nil), input.Segments...)
	for i := range req.Segments {
		seg := &req.Segments[i]
		if seg.Type == "file" {
			seg.AllowedBots, seg.Kind = nil, ""
			if seg.NodeID == "" {
				seg.NodeID = s.Swarm.Node().ID
			}
		}
	}
	req.Generation = c.Generation
	req.GenerationByNode = map[string]domain.Generation{}
	for node, g := range c.GenerationByNode {
		req.GenerationByNode[node] = g
	}
	for _, run := range runs {
		if inputs[run.ID].ID != input.ID || run.Generation.EndpointID == "" {
			continue
		}
		node, _ := swarm.Split(run.BotID)
		if node == "" || node == s.Swarm.Node().ID {
			req.Generation = run.Generation
			node = s.Swarm.Node().ID
		}
		req.GenerationByNode[node] = run.Generation
	}
	return req, nil
}

func (s *Server) checkRerunSnapshot(chatID string, req SendRequest) error {
	if req.rerunMessageID == "" {
		return nil
	}
	messages, err := s.Store.Messages(chatID)
	if err != nil {
		return err
	}
	if transcriptFingerprint(messages) != req.rerunFingerprint {
		return errors.New("the conversation changed before it could be rerun; try again")
	}
	return nil
}
