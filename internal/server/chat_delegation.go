package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"regexp"
	"strings"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
)

const delegatedAuthorPrefix = "Delegated by @"

// Swarm may suffix a machine's 48-character namespace with "-" and eight
// hex digits to disambiguate machines with the same name.
var delegationTarget = regexp.MustCompile(`^([a-z][a-z0-9_-]{0,56}\.)?[a-z][a-z0-9_-]{0,47}$`)

type chatStart struct {
	chat domain.Chat
	ctx  context.Context // checked at admission; accepted work runs independently
}

type chatReceipt struct {
	ChatID     string `json:"chatId"`
	DispatchID string `json:"dispatchId"`
	NodeID     string `json:"nodeId"`
}

func (s *Server) startDelegatedChat(ctx context.Context, scope domain.ExecutionScope, to, prompt, taskKey string) (chatReceipt, error) {
	to = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(to)), "@")
	if !delegationTarget.MatchString(to) {
		return chatReceipt{}, errors.New("choose one bot or group by handle, optionally qualified with its machine")
	}
	if err := validateMessageContent(prompt); err != nil {
		return chatReceipt{}, err
	}
	taskKey = strings.TrimSpace(taskKey)
	if len(taskKey) > 128 {
		return chatReceipt{}, errors.New("taskKey exceeds 128 bytes")
	}
	var run domain.Run
	if scope.ChatID == "" || scope.RunID == "" || s.Store.Get("runs", scope.RunID, &run) != nil || run.ChatID != scope.ChatID || run.BotID != scope.Bot.ID {
		return chatReceipt{}, errors.New("chat_start requires a chat run for the delegating bot")
	}
	parent, err := s.Store.Chat(scope.ChatID)
	if err != nil {
		return chatReceipt{}, err
	}
	id := domain.ID()
	if taskKey != "" {
		sum := sha256.Sum256([]byte("chat_start\x00" + scope.Bot.ID + "\x00" + to + "\x00" + taskKey))
		id = hex.EncodeToString(sum[:16])
	}
	now := domain.Now()
	base := parent.Generation
	if selected := parent.GenerationByNode[s.Swarm.Node().ID]; selected.EndpointID != "" {
		base = selected
	} else if parent.Projection {
		base = s.defaultGeneration()
	}
	c := domain.Chat{
		ID: id, Title: truncate(strings.TrimSpace(prompt), 70), Bots: map[string]string{},
		Generation: base, GenerationByNode: maps.Clone(parent.GenerationByNode), CreatedAt: now, UpdatedAt: now,
	}
	req := SendRequest{
		Content: "@" + to + "\n" + prompt, inputAuthor: delegatedAuthorPrefix + scope.Bot.Handle,
		RequestID: "chat_start", newChat: &chatStart{chat: c, ctx: ctx},
		Generation: c.Generation, GenerationByNode: c.GenerationByNode,
	}
	d, err := s.send(id, req)
	if err != nil {
		return chatReceipt{}, err
	}
	return chatReceipt{ChatID: id, DispatchID: d.id, NodeID: s.Swarm.Node().ID}, nil
}

func (s *Server) chatForSend(id string, req SendRequest) (domain.Chat, error) {
	if req.newChat != nil {
		return req.newChat.chat, req.newChat.ctx.Err()
	}
	return s.Store.Chat(id)
}

// The chat, its opening turn (including a queued group assignment), and its
// retry receipt become visible together in the existing acceptance transaction.
func (s *Server) storeTurn(req SendRequest, c domain.Chat, m domain.Message, runs []domain.Run, id string, records map[string]map[string]any) error {
	mode := store.AppendTurn
	if req.newChat != nil {
		if err := req.newChat.ctx.Err(); err != nil {
			return err
		}
		mode = store.CreateChatWithTurn
	} else if req.routing != nil && req.routing.pending != nil {
		mode = store.AssignQueuedTurn
	}
	return s.Store.AcceptTurn(c, m, runs, req.RequestID, id, records, mode, req.replaceMessageID)
}
