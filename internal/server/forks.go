package server

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
	"github.com/brent/echo/internal/swarm"
)

type forkSnapshot struct {
	Chat     domain.Chat
	Messages []domain.Message
	Runs     []domain.Run
	Partial  map[string]bool
	Inputs   map[string]string
}

func (s *Server) captureFork(chatID, target string) (forkSnapshot, error) {
	var snapshot forkSnapshot
	if err := s.messageMutationGuard(chatID); err != nil {
		return snapshot, fmt.Errorf("cannot fork: %w", err)
	}
	c, err := s.Store.Chat(chatID)
	if err != nil {
		return snapshot, err
	}
	all, err := s.Store.Messages(chatID)
	if err != nil {
		return snapshot, err
	}
	end := -1
	for i, m := range all {
		if m.ID == target {
			end = i
			break
		}
	}
	if end < 0 {
		return snapshot, errors.New("message was not found")
	}
	m := all[end]
	finished := m.Status == "completed" || m.Status == "failed" || m.Status == "cancelled" || m.Status == "interrupted"
	if m.Role != "user" && m.Role != "assistant" || !finished || m.Role == "user" && m.RunID != "" {
		return snapshot, errors.New("fork a finished user message or final assistant response")
	}
	if m.Role == "assistant" && m.RunID != "" {
		if m.Status != "completed" || len(m.Calls) > 0 || strings.TrimSpace(m.Content) == "" {
			return snapshot, errors.New("fork the final assistant response")
		}
		for _, later := range all[end+1:] {
			if later.RunID == m.RunID && later.BotID == m.BotID {
				return snapshot, errors.New("fork the final assistant response")
			}
		}
	}
	c.LastReadAt = nil // Opening the source in another browser is not a content change.
	snapshot = forkSnapshot{Chat: c, Messages: all[:end+1], Partial: map[string]bool{}}
	seen := map[string]bool{}
	for _, m := range snapshot.Messages {
		if m.RunID == "" || seen[m.RunID] {
			continue
		}
		seen[m.RunID] = true
		var run domain.Run
		if err := s.Store.Get("runs", m.RunID, &run); err != nil {
			return snapshot, err
		}
		if run.ChatID != chatID {
			return snapshot, errors.New("run belongs to another chat")
		}
		if run.ID == all[end].RunID && run.Status != "completed" {
			return snapshot, errors.New("fork a completed response")
		}
		snapshot.Runs = append(snapshot.Runs, run)
	}
	for _, m := range all[end+1:] {
		if seen[m.RunID] {
			snapshot.Partial[m.RunID] = true
		}
	}
	runs, err := s.Store.ChatRuns(chatID)
	if err != nil {
		return snapshot, err
	}
	inputs := rerunInputs(all, runs)
	snapshot.Inputs = map[string]string{}
	for _, run := range snapshot.Runs {
		snapshot.Inputs[run.ID] = inputs[run.ID].ID
	}
	return snapshot, nil
}

func forkFingerprint(snapshot forkSnapshot) [32]byte {
	data, _ := json.Marshal(snapshot)
	return sha256.Sum256(data)
}

func prepareFork(snapshot forkSnapshot, target, home string) store.HistoryCopy {
	// Copy slices/maps before remapping; the captured snapshot is used again at commit.
	data, _ := json.Marshal(snapshot)
	var cloned forkSnapshot
	_ = json.Unmarshal(data, &cloned)
	now := domain.Now()
	c := cloned.Chat
	c.ID, c.Title, c.OwnerNodeID = domain.ID(), c.Title+" (fork)", home
	c.CreatedAt, c.UpdatedAt, c.LastReadAt = now, now, &now
	c.ForkedFrom = &domain.ForkOrigin{ChatID: snapshot.Chat.ID, MessageID: target}
	c.Bots = map[string]string{}
	runIDs, dispatchIDs := map[string]string{}, map[string]string{}
	messageIDs := map[string]string{}
	for i := range cloned.Runs {
		r := &cloned.Runs[i]
		old := r.ID
		runIDs[old] = domain.ID()
		if dispatchIDs[r.DispatchID] == "" {
			dispatchIDs[r.DispatchID] = domain.ID()
		}
		r.ID, r.DispatchID, r.ChatID = runIDs[old], dispatchIDs[r.DispatchID], c.ID
		if cloned.Partial[old] || r.Status == "running" || r.Status == "waiting" || r.Status == "queued" {
			r.Status, r.Error = "interrupted", "History ends at the fork point."
		}
	}
	for i := range cloned.Messages {
		m := &cloned.Messages[i]
		oldID := m.ID
		owner, _ := swarm.Split(m.ID)
		m.ID = domain.ID()
		if owner != "" {
			m.ID = swarm.Key(owner, m.ID)
		}
		messageIDs[oldID] = m.ID
		m.ChatID, m.RunID = c.ID, runIDs[m.RunID]
		if m.Status == "running" || m.Status == "waiting" || m.Status == "queued" {
			m.Status = "interrupted"
		}
		ids := append(append([]string{}, m.Recipients...), m.BotID)
		for _, id := range ids {
			if id == "" || id == "echo" {
				continue
			}
			name := snapshot.Chat.Bots[id]
			if name == "" {
				name = id
				if id == m.BotID && m.Role == "assistant" {
					name = m.Author
				}
			}
			c.Bots[id] = name
		}
	}
	for i := range cloned.Runs {
		cloned.Runs[i].UserMessageID = messageIDs[snapshot.Inputs[snapshot.Runs[i].ID]]
		if cloned.Runs[i].UserMessageID == "" {
			// Retain a missing relationship instead of guessing another input.
			cloned.Runs[i].UserMessageID = domain.ID()
		}
	}
	return store.HistoryCopy{Chat: c, Messages: cloned.Messages, Runs: cloned.Runs}
}

// Known structured media references are remapped; ordinary prose is untouched.
func rewriteMediaJSON(content string, ids, urls map[string]string) string {
	var value any
	if json.Unmarshal([]byte(content), &value) != nil {
		return content
	}
	changed := false
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, item := range v {
				if text, ok := item.(string); ok {
					replacement := ""
					if key == "imageId" || key == "videoId" {
						replacement = ids[text]
					}
					if key == "url" {
						replacement = urls[text]
					}
					if replacement != "" {
						v[key] = replacement
						changed = true
					}
				}
				walk(item)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(value)
	if !changed {
		return content
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (s *Server) stageHistoryAttachment(ctx context.Context, sourceHome, sourceChat string, a domain.Attachment, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	owner := a.NodeID
	if owner == "" {
		owner = sourceHome
	}
	chat := a.OriginChatID
	if chat == "" {
		chat = sourceChat
	}
	if owner == s.Swarm.Node().ID {
		return s.Store.StageAttachment(chat, a.ID, path)
	}
	if a.Size < 0 || a.Size > store.MaxGeneratedAttachmentBytes {
		return errors.New("invalid fork attachment size")
	}
	child, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	response, err := s.Swarm.RoundTrip(child, owner, "GET", "/api/v1/chats/"+url.PathEscape(chat)+"/attachments/"+url.PathEscape(a.ID), nil)
	if err != nil {
		return fmt.Errorf("cannot copy %s: %w", a.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("cannot copy %s: media source returned %d", a.Name, response.StatusCode)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	zip := gzip.NewWriter(file)
	n, copyErr := io.Copy(zip, io.LimitReader(response.Body, a.Size+1))
	zipErr, fileErr := zip.Close(), file.Close()
	if copyErr != nil {
		return copyErr
	}
	if zipErr != nil {
		return zipErr
	}
	if fileErr != nil {
		return fileErr
	}
	if n != a.Size {
		return errors.New("fork attachment size changed during transfer")
	}
	return child.Err()
}

func (s *Server) copyForkAttachments(ctx context.Context, snapshot forkSnapshot, copy *store.HistoryCopy, dir string) error {
	ids, urls := map[string]string{}, map[string]string{}
	for i := range copy.Messages {
		m := &copy.Messages[i]
		for j := range m.Attachments {
			source := snapshot.Messages[i].Attachments[j]
			a := &m.Attachments[j]
			a.ID, a.ChatID, a.NodeID, a.OriginChatID = domain.ID(), copy.Chat.ID, "", ""
			path := filepath.Join(dir, a.ID)
			if err := s.stageHistoryAttachment(ctx, s.Swarm.Node().ID, snapshot.Chat.ID, source, path); err != nil {
				return err
			}
			copy.Attachments = append(copy.Attachments, store.HistoryAttachment{Metadata: *a, MessageID: m.ID, Path: path})
			ids[source.ID] = a.ID
			origin := source.OriginChatID
			if origin == "" {
				origin = snapshot.Chat.ID
			}
			urls["/api/v1/chats/"+origin+"/attachments/"+source.ID] = "/api/v1/chats/" + copy.Chat.ID + "/attachments/" + a.ID
		}
	}
	for i := range copy.Messages {
		m := &copy.Messages[i]
		if m.Role == "tool" {
			m.Content = rewriteMediaJSON(m.Content, ids, urls)
		}
		for j := range m.Calls {
			m.Calls[j].Function.Arguments = rewriteMediaJSON(m.Calls[j].Function.Arguments, ids, urls)
		}
	}
	return nil
}

func (s *Server) forkChat(ctx context.Context, source, target, request string) (string, error) {
	if strings.TrimSpace(request) == "" || len(request) > 128 {
		return "", errors.New("fork requires a request id of at most 128 characters")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	s.mu.Lock()
	id, err := s.Store.ForkResult(source, target, request)
	if id != "" || err != nil {
		s.mu.Unlock()
		return id, err
	}
	snapshot, err := s.captureFork(source, target)
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "echo-fork-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	copy := prepareFork(snapshot, target, s.Swarm.Node().ID)
	if err = s.copyForkAttachments(ctx, snapshot, &copy, dir); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, err := s.Store.ForkResult(source, target, request); id != "" || err != nil {
		return id, err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	current, err := s.captureFork(source, target)
	if err != nil {
		return "", err
	}
	if forkFingerprint(current) != forkFingerprint(snapshot) {
		return "", errors.New("source history changed while copying; retry the fork")
	}
	id, err = s.Store.CommitFork(source, target, request, copy)
	if err == nil {
		s.emitLocked("changed", id, "", "", nil)
	}
	return id, err
}
