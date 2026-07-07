package services

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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
	Direction          string                   `json:"direction,omitempty"`
	AcceptanceCriteria []string                 `json:"acceptanceCriteria"`
	Dependencies       []string                 `json:"dependencies,omitempty"`
	DependencyStatuses []KanbanDependencyStatus `json:"dependencyStatuses,omitempty"`
	BlockedBy          []string                 `json:"blockedBy,omitempty"`
	Eligible           bool                     `json:"eligible"`
	Lane               string                   `json:"lane"`
	Status             string                   `json:"status"`
	ProgressTranscript []KanbanProgressEntry    `json:"progressTranscript,omitempty"`
	AutoRetriesUsed    int                      `json:"autoRetriesUsed,omitempty"`
	RecoveryType       string                   `json:"recoveryType,omitempty"`
	StalledAt          *time.Time               `json:"stalledAt,omitempty"`
	WatchdogChecked    bool                     `json:"watchdogChecked,omitempty"`
}

type KanbanDependencyStatus struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Done   bool   `json:"done"`
}

type KanbanProgressEntry struct {
	Type      string    `json:"type"`
	Title     string    `json:"title,omitempty"`
	Content   string    `json:"content"`
	Status    string    `json:"status,omitempty"`
	Timestamp time.Time `json:"timestamp"`
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

func (s *SystemService) ClearDoneKanbanCards(workspaceID string) (KanbanBoard, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	s.mu.Lock()
	doneIDsToKeep := doneKanbanCardsWithUnfinishedDependents(workspaceID, s.state.KanbanCards)
	deletedIDs := make(map[string]struct{})
	next := s.state.KanbanCards[:0]
	for _, card := range s.state.KanbanCards {
		if card.WorkspaceID != workspaceID {
			next = append(next, card)
			continue
		}
		if effectiveKanbanLane(card) != KanbanLaneDone {
			next = append(next, card)
			continue
		}
		if _, keep := doneIDsToKeep[card.ID]; keep {
			next = append(next, card)
			continue
		}
		deletedIDs[card.ID] = struct{}{}
	}
	s.state.KanbanCards = next
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()

	if len(deletedIDs) > 0 {
		s.clearKanbanDetailViewIfDeleted(workspaceID, deletedIDs)
	}
	return board, nil
}

func (s *SystemService) DeleteKanbanCard(workspaceID string, cardID string) (KanbanBoard, error) {
	cardID = strings.TrimSpace(cardID)
	if cardID == "" {
		return KanbanBoard{}, fmt.Errorf("kanban card id is required")
	}
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	s.mu.Lock()
	target, found := kanbanCardByIDForWorkspace(s.state.KanbanCards, workspaceID, cardID)
	if !found {
		s.mu.Unlock()
		return KanbanBoard{}, fmt.Errorf("kanban card was not found")
	}
	lane := effectiveKanbanLane(target)
	if lane != KanbanLaneReady && lane != KanbanLaneDone {
		s.mu.Unlock()
		return KanbanBoard{}, fmt.Errorf("only Ready or Done kanban cards can be deleted")
	}

	deletedIDs := map[string]struct{}{cardID: {}}
	if lane == KanbanLaneReady {
		collectDependentKanbanCardIDs(workspaceID, s.state.KanbanCards, deletedIDs)
	}

	next := s.state.KanbanCards[:0]
	for _, card := range s.state.KanbanCards {
		if card.WorkspaceID == workspaceID {
			if _, deleted := deletedIDs[card.ID]; deleted {
				continue
			}
			card.Dependencies = removeDeletedKanbanDependencies(card.Dependencies, deletedIDs)
		}
		next = append(next, card)
	}
	s.state.KanbanCards = next
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()

	s.clearKanbanDetailViewIfDeleted(workspaceID, deletedIDs)
	return board, nil
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

func (s *SystemService) CreateReadyKanbanCard(workspaceID string, title string, description string, acceptanceCriteria []string) (KanbanBoard, error) {
	title = strings.TrimSpace(title)
	description = strings.TrimSpace(description)
	if title == "" {
		return KanbanBoard{}, fmt.Errorf("title is required")
	}
	if description == "" {
		return KanbanBoard{}, fmt.Errorf("description is required")
	}
	criteria := make([]string, 0, len(acceptanceCriteria))
	for _, criterion := range acceptanceCriteria {
		if criterion = strings.TrimSpace(criterion); criterion != "" {
			criteria = append(criteria, criterion)
		}
	}
	if len(criteria) == 0 {
		return KanbanBoard{}, fmt.Errorf("at least one acceptance criterion is required")
	}
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	s.chatMu.Lock()
	if _, running := s.kanbanRuns[workspaceID]; running {
		s.chatMu.Unlock()
		return KanbanBoard{}, fmt.Errorf("kanban cards cannot be created while agents are running")
	}

	s.mu.Lock()
	card := KanbanCard{
		ID:                 fmt.Sprintf("card-%d", s.nextKanbanCardNumberLocked()),
		WorkspaceID:        workspaceID,
		Title:              title,
		Description:        description,
		AcceptanceCriteria: criteria,
		Lane:               KanbanLaneReady,
		Status:             KanbanLaneReady,
		ProgressTranscript: []KanbanProgressEntry{{
				Type:      "message",
				Title:     "Card created",
				Content:   "Created manually in the Ready lane.",
				Status:    KanbanLaneReady,
				Timestamp: time.Now(),
			}},
		}
	s.state.KanbanCards = append(s.state.KanbanCards, card)
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()
	s.chatMu.Unlock()

	s.emitKanbanEvent(KanbanEvent{WorkspaceID: workspaceID, CardID: card.ID, Type: "card_created", Board: board})
	return board, nil
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

	cardIndex := -1
	for index := range s.state.KanbanCards {
		if s.state.KanbanCards[index].WorkspaceID == workspaceID && s.state.KanbanCards[index].ID == cardID {
			cardIndex = index
			break
		}
	}
	if cardIndex < 0 {
		s.mu.Unlock()
		return KanbanBoard{}, fmt.Errorf("kanban card was not found")
	}

	if lane == KanbanLaneInProgress {
		blockedBy := blockedDependenciesForCard(s.state.KanbanCards[cardIndex], s.state.KanbanCards)
		if len(blockedBy) > 0 {
			s.mu.Unlock()
			return KanbanBoard{}, fmt.Errorf("kanban card is blocked by dependencies: %s", strings.Join(blockedBy, ", "))
		}
	}

	card := &s.state.KanbanCards[cardIndex]
	card.Lane = lane
	card.Status = lane
	card.ProgressTranscript = append(card.ProgressTranscript, KanbanProgressEntry{
		Type:      "status",
		Title:     "Status changed",
		Content:   fmt.Sprintf("Moved to %s.", kanbanLaneLabel(lane)),
		Status:    lane,
		Timestamp: time.Now(),
	})

	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	complete := kanbanBoardComplete(board)
	s.mu.Unlock()
	if complete {
		_ = s.persistWorkspaceAutosave(workspaceID)
	}
	return board, nil
}

func kanbanBoardComplete(board KanbanBoard) bool {
	return len(board.Done) > 0 &&
		len(board.Ready) == 0 &&
		len(board.InProgress) == 0 &&
		len(board.Blocked) == 0
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
			Type:      "message",
			Title:     "Description updated",
			Content:   "User edited the card description before execution.",
			Status:    KanbanLaneReady,
			Timestamp: time.Now(),
		})
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

func (s *SystemService) UpdateKanbanCardDirection(workspaceID string, cardID string, direction string) (KanbanBoard, error) {
	direction = strings.TrimSpace(direction)
	if direction == "" {
		return KanbanBoard{}, fmt.Errorf("direction is required")
	}
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	s.chatMu.Lock()
	if _, running := s.kanbanRuns[workspaceID]; running {
		s.chatMu.Unlock()
		return KanbanBoard{}, fmt.Errorf("kanban directions cannot be edited while cards are running")
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
			return KanbanBoard{}, fmt.Errorf("only Ready card directions can be edited")
		}
		card.Direction = direction
		card.ProgressTranscript = append(card.ProgressTranscript, KanbanProgressEntry{
			Type:      "message",
			Title:     "Direction updated",
			Content:   "User edited the card direction before execution.",
			Status:    KanbanLaneReady,
			Timestamp: time.Now(),
		})
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
					Type:      "message",
					Title:     "Card created",
					Content:   "Created from the approved chat plan.",
					Status:    KanbanLaneReady,
					Timestamp: time.Now(),
				}},
			}
			for _, dependency := range card.Dependencies {
			runtimeCard.Dependencies = append(runtimeCard.Dependencies, idMap[dependency])
		}
		s.state.KanbanCards = append(s.state.KanbanCards, runtimeCard)
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
			Type:      "message",
			Title:     "Card created",
			Content:   "Created directly from an Echo chat message.",
			Status:    KanbanLaneReady,
			Timestamp: time.Now(),
		}},
	}
	s.state.KanbanCards = append(s.state.KanbanCards, card)

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
	if card.StalledAt != nil {
		t := *card.StalledAt
		card.StalledAt = &t
	}
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

func kanbanCardByIDForWorkspace(cards []KanbanCard, workspaceID string, cardID string) (KanbanCard, bool) {
	for _, card := range cards {
		if card.WorkspaceID == workspaceID && card.ID == cardID {
			return card, true
		}
	}
	return KanbanCard{}, false
}

func effectiveKanbanLane(card KanbanCard) string {
	lane := normalizeKanbanLane(card.Lane)
	if lane == "" {
		lane = normalizeKanbanLane(card.Status)
	}
	if lane == "" {
		lane = KanbanLaneReady
	}
	return lane
}

func doneKanbanCardsWithUnfinishedDependents(workspaceID string, cards []KanbanCard) map[string]struct{} {
	doneIDs := make(map[string]struct{})
	for _, card := range cards {
		if card.WorkspaceID == workspaceID && effectiveKanbanLane(card) == KanbanLaneDone {
			doneIDs[card.ID] = struct{}{}
		}
	}
	if len(doneIDs) == 0 {
		return doneIDs
	}

	keep := make(map[string]struct{})
	for _, card := range cards {
		if card.WorkspaceID != workspaceID || effectiveKanbanLane(card) == KanbanLaneDone {
			continue
		}
		for _, dependencyID := range card.Dependencies {
			if _, done := doneIDs[dependencyID]; done {
				keep[dependencyID] = struct{}{}
			}
		}
	}
	return keep
}

func collectDependentKanbanCardIDs(workspaceID string, cards []KanbanCard, deletedIDs map[string]struct{}) {
	for {
		changed := false
		for _, card := range cards {
			if card.WorkspaceID != workspaceID {
				continue
			}
			if _, deleted := deletedIDs[card.ID]; deleted {
				continue
			}
			for _, dependencyID := range card.Dependencies {
				if _, deleted := deletedIDs[dependencyID]; deleted {
					deletedIDs[card.ID] = struct{}{}
					changed = true
					break
				}
			}
		}
		if !changed {
			return
		}
	}
}

func removeDeletedKanbanDependencies(dependencies []string, deletedIDs map[string]struct{}) []string {
	next := dependencies[:0]
	for _, dependencyID := range dependencies {
		if _, deleted := deletedIDs[dependencyID]; deleted {
			continue
		}
		next = append(next, dependencyID)
	}
	return next
}

func (s *SystemService) clearKanbanDetailViewIfDeleted(workspaceID string, deletedIDs map[string]struct{}) {
	s.chatMu.Lock()
	if activeCardID := s.kanbanDetailViews[workspaceID]; activeCardID != "" {
		if _, deleted := deletedIDs[activeCardID]; deleted {
			delete(s.kanbanDetailViews, workspaceID)
		}
	}
	s.chatMu.Unlock()
}

func enrichKanbanCard(card KanbanCard, byID map[string]KanbanCard) KanbanCard {
	card.Lane = effectiveKanbanLane(card)
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

func FindEligibleCards(board KanbanBoard, limit int) []KanbanCard {
	if limit <= 0 {
		return nil
	}
	allByID := kanbanBoardCardsByID(board)
	doneIDs := make(map[string]struct{}, len(board.Done))
	for _, card := range board.Done {
		doneIDs[card.ID] = struct{}{}
	}

	result := make([]KanbanCard, 0, limit)
	for _, card := range board.Ready {
		blocked := false
		for _, depID := range card.Dependencies {
			if _, done := doneIDs[depID]; !done {
				// dependency exists but is not Done; check if it's a ghost dep (not in board at all)
				if _, exists := allByID[depID]; !exists {
					// unknown dependency, treat as unblocked
					continue
				}
				blocked = true
				break
			}
		}
		if !blocked {
			result = append(result, card)
			if len(result) == limit {
				return result
			}
		}
	}
	return result
}

func kanbanBoardCardsByID(board KanbanBoard) map[string]KanbanCard {
	all := make([]KanbanCard, 0, len(board.Ready)+len(board.InProgress)+len(board.Blocked)+len(board.Done))
	all = append(all, board.Ready...)
	all = append(all, board.InProgress...)
	all = append(all, board.Blocked...)
	all = append(all, board.Done...)
	return kanbanCardsByID(all)
}

func blockedDependenciesForCard(card KanbanCard, cards []KanbanCard) []string {
	enriched := enrichKanbanCard(card, kanbanCardsByID(cards))
	return enriched.BlockedBy
}
