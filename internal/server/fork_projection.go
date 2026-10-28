package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
	"github.com/brent/echo/internal/swarm"
)

func checkForkWorkers(chat domain.Chat, participants []swarmParticipant, self string, peers []swarm.Peer) error {
	if chat.ForkedFrom == nil {
		return nil
	}
	for _, participant := range participants {
		if participant.Ref.NodeID == self {
			continue
		}
		supported := slices.ContainsFunc(peers, func(peer swarm.Peer) bool {
			return peer.Node.ID == participant.Ref.NodeID && slices.Contains(peer.Node.Capabilities, "fork-history/1")
		})
		if !supported {
			return fmt.Errorf("update %s before continuing a fork: fork history is not supported", participant.Handle)
		}
	}
	return nil
}

// Only history addressed to this host is imported. Rebuild local reference
// grants rather than trusting historical permissions asserted by the home.
func (s *Server) seedForkHistory(ctx context.Context, req workerRequest, chat domain.Chat, transcript []domain.Message) error {
	seeded, err := s.Store.ForkProjectionSeeded(chat.ID)
	if err != nil || seeded {
		return err
	}
	dir, err := os.MkdirTemp("", "echo-fork-history-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	self := s.Swarm.Node().ID
	localID := func(id string) string {
		node, raw := swarm.Split(id)
		if node == self {
			return raw
		}
		return ""
	}
	copy := store.HistoryCopy{Chat: chat}
	for _, m := range transcript {
		if m.ID == req.Message.ID {
			continue
		}
		if m.RunID == "" && m.Role == "user" {
			ids := []string{}
			for _, id := range m.Recipients {
				if raw := localID(id); raw != "" {
					ids = append(ids, raw)
				}
			}
			if len(ids) == 0 {
				continue
			}
			m.Recipients = ids
		} else {
			bot := localID(m.BotID)
			if bot == "" {
				continue
			}
			m.ID = localID(m.ID)
			if m.ID == "" {
				continue
			}
			m.BotID = bot
			ids := []string{}
			for _, id := range m.Recipients {
				if raw := localID(id); raw != "" {
					ids = append(ids, raw)
				}
			}
			m.Recipients = ids
		}
		m.ChatID, m.NodeID = chat.ID, ""
		m.Segments = append([]domain.Segment(nil), m.Segments...)
		m.Attachments = append([]domain.Attachment(nil), m.Attachments...)
		if m.Status == "running" || m.Status == "waiting" || m.Status == "queued" {
			m.Status = "interrupted"
		}
		for i := range m.Segments {
			seg := &m.Segments[i]
			if seg.Type == "bot" {
				bot := localID(seg.BotID)
				if seg.NodeID == self {
					_, bot = swarm.Split(seg.BotID)
				}
				if bot == "" {
					seg.Type, seg.BotID = "text", ""
				} else {
					seg.BotID = bot
				}
			}
			if seg.Type != "file" {
				continue
			}
			seg.AllowedBots, seg.Kind = nil, ""
			if seg.NodeID != self {
				continue
			}
			for _, id := range m.Recipients {
				bot, err := s.Bot(id)
				if err != nil {
					continue
				}
				if kind, err := s.Files.ReferenceKind(bot, seg.Path); err == nil {
					seg.AllowedBots = append(seg.AllowedBots, id)
					seg.Kind = kind
				}
			}
		}
		urls := map[string]string{}
		for i := range m.Attachments {
			a := &m.Attachments[i]
			path := filepath.Join(dir, a.ID)
			if err := s.stageHistoryAttachment(ctx, req.Home, req.Chat.ID, *a, path); err != nil {
				return err
			}
			urls["/api/v1/chats/"+req.Chat.ID+"/attachments/"+a.ID] = "/api/v1/chats/" + chat.ID + "/attachments/" + a.ID
			a.ChatID, a.NodeID, a.OriginChatID = chat.ID, "", ""
			copy.Attachments = append(copy.Attachments, store.HistoryAttachment{Metadata: *a, MessageID: m.ID, Path: path})
		}
		if m.Role == "tool" {
			m.Content = rewriteMediaJSON(m.Content, nil, urls)
		}
		copy.Messages = append(copy.Messages, m)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Store.SeedForkProjection(copy)
}
