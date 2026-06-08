package services

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	KanbanLaneReady      = "ready"
	KanbanLaneInProgress = "inProgress"
	KanbanLaneBlocked    = "blocked"
	KanbanLaneDone       = "done"
)

type KanbanCard struct {
	ID                 string                   `json:"id"`
	WorkspaceID        string                   `json:"workspaceId"`
	Title              string                   `json:"title"`
	Description        string                   `json:"description"`
	AcceptanceCriteria []string                 `json:"acceptanceCriteria"`
	Dependencies       []string                 `json:"dependencies,omitempty"`
	DependencyStatuses []KanbanDependencyStatus `json:"dependencyStatuses,omitempty"`
	BlockedBy          []string                 `json:"blockedBy,omitempty"`
	Eligible           bool                     `json:"eligible"`
	Lane               string                   `json:"lane"`
	Status             string                   `json:"status"`
	ProgressTranscript []KanbanProgressEntry    `json:"progressTranscript,omitempty"`
}

type KanbanDependencyStatus struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Done   bool   `json:"done"`
}

type KanbanProgressEntry struct {
	Type    string `json:"type"`
	Title   string `json:"title,omitempty"`
	Content string `json:"content"`
	Status  string `json:"status,omitempty"`
}

type KanbanBoard struct {
	WorkspaceID string       `json:"workspaceId"`
	Ready       []KanbanCard `json:"ready"`
	InProgress  []KanbanCard `json:"inProgress"`
	Blocked     []KanbanCard `json:"blocked"`
	Done        []KanbanCard `json:"done"`
}

func (s *SystemService) LoadKanbanBoard(workspaceID string) (KanbanBoard, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return boardForWorkspace(workspaceID, s.state.KanbanCards), nil
}

func (s *SystemService) CreateKanbanCardFromChatMessage(workspaceID string, messageID string) (KanbanBoard, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return KanbanBoard{}, fmt.Errorf("message id is required")
	}
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	s.chatMu.Lock()
	session := s.chatSessions[workspaceID]
	if session == nil {
		s.chatMu.Unlock()
		return KanbanBoard{}, fmt.Errorf("message was not found")
	}

	found := false
	content := ""
	for _, message := range session.Messages {
		if message.ID != messageID {
			continue
		}
		found = true
		if message.Role != "assistant" {
			s.chatMu.Unlock()
			return KanbanBoard{}, fmt.Errorf("can only create cards from assistant messages")
		}
		if message.Status != "complete" {
			s.chatMu.Unlock()
			return KanbanBoard{}, fmt.Errorf("can only create cards from complete assistant messages")
		}
		content = strings.TrimSpace(message.Content)
		break
	}
	s.chatMu.Unlock()

	if !found {
		return KanbanBoard{}, fmt.Errorf("message was not found")
	}
	if content == "" {
		return KanbanBoard{}, fmt.Errorf("message content is required")
	}

	return s.appendAssistantMessageReadyCard(workspaceID, content)
}

func (s *SystemService) MoveKanbanCard(workspaceID string, cardID string, lane string) (KanbanBoard, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}
	lane = normalizeKanbanLane(lane)
	if lane == "" {
		return KanbanBoard{}, fmt.Errorf("kanban lane is invalid")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cardIndex := -1
	for index := range s.state.KanbanCards {
		if s.state.KanbanCards[index].WorkspaceID == workspaceID && s.state.KanbanCards[index].ID == cardID {
			cardIndex = index
			break
		}
	}
	if cardIndex < 0 {
		return KanbanBoard{}, fmt.Errorf("kanban card was not found")
	}

	if lane == KanbanLaneInProgress {
		blockedBy := blockedDependenciesForCard(s.state.KanbanCards[cardIndex], s.state.KanbanCards)
		if len(blockedBy) > 0 {
			return KanbanBoard{}, fmt.Errorf("kanban card is blocked by dependencies: %s", strings.Join(blockedBy, ", "))
		}
	}

	card := &s.state.KanbanCards[cardIndex]
	card.Lane = lane
	card.Status = lane
	card.ProgressTranscript = append(card.ProgressTranscript, KanbanProgressEntry{
		Type:    "status",
		Title:   "Status changed",
		Content: fmt.Sprintf("Moved to %s.", kanbanLaneLabel(lane)),
		Status:  lane,
	})

	if err := s.saveLocked(); err != nil {
		return KanbanBoard{}, err
	}
	return boardForWorkspace(workspaceID, s.state.KanbanCards), nil
}

func (s *SystemService) UpdateKanbanCardDescription(workspaceID string, cardID string, description string) (KanbanBoard, error) {
	description = strings.TrimSpace(description)
	if description == "" {
		return KanbanBoard{}, fmt.Errorf("description is required")
	}
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	s.chatMu.Lock()
	if _, running := s.kanbanRuns[workspaceID]; running {
		s.chatMu.Unlock()
		return KanbanBoard{}, fmt.Errorf("kanban descriptions cannot be edited while cards are running")
	}

	s.mu.Lock()
	for index := range s.state.KanbanCards {
		card := &s.state.KanbanCards[index]
		if card.WorkspaceID != workspaceID || card.ID != cardID {
			continue
		}
		if normalizeKanbanLane(card.Lane) != KanbanLaneReady {
			s.mu.Unlock()
			s.chatMu.Unlock()
			return KanbanBoard{}, fmt.Errorf("only Ready card descriptions can be edited")
		}
		card.Description = description
		card.ProgressTranscript = append(card.ProgressTranscript, KanbanProgressEntry{
			Type:    "message",
			Title:   "Description updated",
			Content: "User edited the card description before execution.",
			Status:  KanbanLaneReady,
		})
		if err := s.saveLocked(); err != nil {
			s.mu.Unlock()
			s.chatMu.Unlock()
			return KanbanBoard{}, err
		}
		board := boardForWorkspace(workspaceID, s.state.KanbanCards)
		s.mu.Unlock()
		s.chatMu.Unlock()
		s.emitKanbanEvent(KanbanEvent{WorkspaceID: workspaceID, CardID: cardID, Type: "card_updated", Board: board})
		return board, nil
	}
	s.mu.Unlock()
	s.chatMu.Unlock()
	return KanbanBoard{}, fmt.Errorf("kanban card was not found")
}

func (s *SystemService) ResetKanbanCard(workspaceID string, cardID string) (KanbanBoard, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	key := kanbanAgentKey(workspaceID, cardID)
	s.chatMu.Lock()
	agent := s.kanbanAgents[key]
	delete(s.kanbanAgents, key)
	if agent != nil {
		agent.cancel()
	}

	s.mu.Lock()

	found := false
	for index := range s.state.KanbanCards {
		card := &s.state.KanbanCards[index]
		if card.WorkspaceID != workspaceID || card.ID != cardID {
			continue
		}
		card.Lane = KanbanLaneReady
		card.Status = KanbanLaneReady
		card.ProgressTranscript = nil
		found = true
		break
	}
	if !found {
		s.mu.Unlock()
		s.chatMu.Unlock()
		return KanbanBoard{}, fmt.Errorf("kanban card was not found")
	}
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		s.chatMu.Unlock()
		return KanbanBoard{}, err
	}
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()
	s.chatMu.Unlock()
	return board, nil
}

func boardForWorkspace(workspaceID string, cards []KanbanCard) KanbanBoard {
	board := KanbanBoard{
		WorkspaceID: workspaceID,
		Ready:       []KanbanCard{},
		InProgress:  []KanbanCard{},
		Blocked:     []KanbanCard{},
		Done:        []KanbanCard{},
	}
	byID := kanbanCardsByID(cards)
	for _, card := range cards {
		if card.WorkspaceID != workspaceID {
			continue
		}
		card = enrichKanbanCard(cloneKanbanCard(card), byID)
		switch card.Lane {
		case KanbanLaneInProgress:
			board.InProgress = append(board.InProgress, card)
		case KanbanLaneBlocked:
			board.Blocked = append(board.Blocked, card)
		case KanbanLaneDone:
			board.Done = append(board.Done, card)
		default:
			card.Lane = KanbanLaneReady
			board.Ready = append(board.Ready, card)
		}
	}
	return board
}

func (s *SystemService) appendReadyCards(workspaceID string, cards []decomposedCard) (KanbanBoard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.refreshWorkspaceStatusesLocked() {
		_ = s.saveLocked()
	}
	if !workspaceExists(s.state.Workspaces, workspaceID) {
		return KanbanBoard{}, fmt.Errorf("workspace was not found")
	}
	for _, workspace := range s.state.Workspaces {
		if workspace.ID == workspaceID && workspace.Missing {
			return KanbanBoard{}, fmt.Errorf("workspace folder is unavailable")
		}
	}

	nextID := s.nextKanbanCardNumberLocked()
	idMap := make(map[string]string, len(cards))
	for index, card := range cards {
		idMap[card.ID] = fmt.Sprintf("card-%d", nextID+index)
	}

	for _, card := range cards {
		runtimeCard := KanbanCard{
			ID:                 idMap[card.ID],
			WorkspaceID:        workspaceID,
			Title:              card.Title,
			Description:        card.Description,
			AcceptanceCriteria: append([]string(nil), card.AcceptanceCriteria...),
			Lane:               KanbanLaneReady,
			Status:             KanbanLaneReady,
			ProgressTranscript: []KanbanProgressEntry{{
				Type:    "message",
				Title:   "Card created",
				Content: "Created from the approved chat plan.",
				Status:  KanbanLaneReady,
			}},
		}
		for _, dependency := range card.Dependencies {
			runtimeCard.Dependencies = append(runtimeCard.Dependencies, idMap[dependency])
		}
		s.state.KanbanCards = append(s.state.KanbanCards, runtimeCard)
	}

	if err := s.saveLocked(); err != nil {
		return KanbanBoard{}, err
	}
	return boardForWorkspace(workspaceID, s.state.KanbanCards), nil
}

func (s *SystemService) appendAssistantMessageReadyCard(workspaceID string, content string) (KanbanBoard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.refreshWorkspaceStatusesLocked() {
		_ = s.saveLocked()
	}
	if !workspaceExists(s.state.Workspaces, workspaceID) {
		return KanbanBoard{}, fmt.Errorf("workspace was not found")
	}
	for _, workspace := range s.state.Workspaces {
		if workspace.ID == workspaceID && workspace.Missing {
			return KanbanBoard{}, fmt.Errorf("workspace folder is unavailable")
		}
	}

	card := KanbanCard{
		ID:                 fmt.Sprintf("card-%d", s.nextKanbanCardNumberLocked()),
		WorkspaceID:        workspaceID,
		Title:              assistantMessageKanbanTitle(content),
		Description:        content,
		AcceptanceCriteria: []string{"Complete the work described in the assistant message."},
		Lane:               KanbanLaneReady,
		Status:             KanbanLaneReady,
		ProgressTranscript: []KanbanProgressEntry{{
			Type:    "message",
			Title:   "Card created",
			Content: "Created directly from an Echo chat message.",
			Status:  KanbanLaneReady,
		}},
	}
	s.state.KanbanCards = append(s.state.KanbanCards, card)

	if err := s.saveLocked(); err != nil {
		return KanbanBoard{}, err
	}
	return boardForWorkspace(workspaceID, s.state.KanbanCards), nil
}

func assistantMessageKanbanTitle(content string) string {
	const maxTitleRunes = 80
	for _, line := range strings.Split(content, "\n") {
		title := strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
		if title == "" || strings.HasPrefix(title, "```") {
			continue
		}
		title = strings.TrimSpace(strings.TrimLeft(title, "#"))
		for _, prefix := range []string{"- ", "* ", "+ "} {
			if strings.HasPrefix(title, prefix) {
				title = strings.TrimSpace(strings.TrimPrefix(title, prefix))
				break
			}
		}
		title = strings.Trim(title, "`*_")
		if title == "" {
			continue
		}
		runes := []rune(title)
		if len(runes) <= maxTitleRunes {
			return title
		}
		return strings.TrimSpace(string(runes[:maxTitleRunes-3])) + "..."
	}
	return "Echo message"
}

func (s *SystemService) nextKanbanCardNumberLocked() int {
	maxID := 0
	for _, card := range s.state.KanbanCards {
		if value, ok := strings.CutPrefix(card.ID, "card-"); ok {
			if id, err := strconv.Atoi(value); err == nil && id > maxID {
				maxID = id
			}
		}
	}
	return maxID + 1
}

func cardsWithoutWorkspace(cards []KanbanCard, workspaceID string) []KanbanCard {
	next := cards[:0]
	for _, card := range cards {
		if card.WorkspaceID != workspaceID {
			next = append(next, card)
		}
	}
	return next
}

func cloneKanbanCards(cards []KanbanCard) []KanbanCard {
	clone := append([]KanbanCard{}, cards...)
	for i := range clone {
		clone[i] = cloneKanbanCard(clone[i])
	}
	return clone
}

func cloneKanbanCard(card KanbanCard) KanbanCard {
	card.AcceptanceCriteria = append([]string(nil), card.AcceptanceCriteria...)
	card.Dependencies = append([]string(nil), card.Dependencies...)
	card.DependencyStatuses = append([]KanbanDependencyStatus(nil), card.DependencyStatuses...)
	card.BlockedBy = append([]string(nil), card.BlockedBy...)
	card.ProgressTranscript = append([]KanbanProgressEntry(nil), card.ProgressTranscript...)
	return card
}

func normalizeKanbanLane(lane string) string {
	switch strings.TrimSpace(lane) {
	case KanbanLaneReady:
		return KanbanLaneReady
	case KanbanLaneInProgress:
		return KanbanLaneInProgress
	case KanbanLaneBlocked:
		return KanbanLaneBlocked
	case KanbanLaneDone:
		return KanbanLaneDone
	default:
		return ""
	}
}

func kanbanLaneLabel(lane string) string {
	switch lane {
	case KanbanLaneInProgress:
		return "In Progress"
	case KanbanLaneBlocked:
		return "Blocked"
	case KanbanLaneDone:
		return "Done"
	default:
		return "Ready"
	}
}

func kanbanCardsByID(cards []KanbanCard) map[string]KanbanCard {
	byID := make(map[string]KanbanCard, len(cards))
	for _, card := range cards {
		byID[card.ID] = card
	}
	return byID
}

func enrichKanbanCard(card KanbanCard, byID map[string]KanbanCard) KanbanCard {
	card.Lane = normalizeKanbanLane(card.Lane)
	if card.Lane == "" {
		card.Lane = normalizeKanbanLane(card.Status)
	}
	if card.Lane == "" {
		card.Lane = KanbanLaneReady
	}
	card.Status = card.Lane
	card.DependencyStatuses = nil
	card.BlockedBy = nil
	for _, dependencyID := range card.Dependencies {
		dependency, ok := byID[dependencyID]
		status := KanbanLaneReady
		title := dependencyID
		done := false
		if ok {
			status = normalizeKanbanLane(dependency.Lane)
			if status == "" {
				status = normalizeKanbanLane(dependency.Status)
			}
			if status == "" {
				status = KanbanLaneReady
			}
			title = dependency.Title
			done = status == KanbanLaneDone
		}
		card.DependencyStatuses = append(card.DependencyStatuses, KanbanDependencyStatus{
			ID:     dependencyID,
			Title:  title,
			Status: status,
			Done:   done,
		})
		if !done {
			card.BlockedBy = append(card.BlockedBy, dependencyID)
		}
	}
	card.Eligible = card.Lane == KanbanLaneReady && len(card.BlockedBy) == 0
	return card
}

func blockedDependenciesForCard(card KanbanCard, cards []KanbanCard) []string {
	enriched := enrichKanbanCard(card, kanbanCardsByID(cards))
	return enriched.BlockedBy
}
