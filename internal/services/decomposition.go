package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/brent/echo/internal/llm"
)

const decompositionSystemPrompt = `You decompose approved coding plans into Echo Kanban cards for autonomous programming agents.

Use only the visible user and assistant messages supplied. Do not infer hidden reasoning, tool calls, system state, or unavailable history.

Return only strict JSON:
{
  "cards": [
    {
      "id": "card-1",
      "title": "Implement focused code change",
      "description": "Concrete programming work for this isolated slice.",
      "acceptanceCriteria": ["Observable code or product outcome."],
      "dependencies": ["card-1"]
    }
  ]
}

Card rules:
- A card must be isolated programming work a coding agent can complete with repository tools.
- Prefer fewer cards; split only for independently useful code changes or true implementation ordering.
- Do not create cards for opening, navigating, reading, inspecting, or finding files; fold needed investigation into the implementation card.
- Do not create setup, planning, context-gathering, review, summary, build, test, or verify-only cards unless the user explicitly requested that as the product work.
- Echo automatically runs detected verification after each card, so do not add verification instructions as separate work.
- Titles should be short imperative programming tasks.
- Descriptions should be one or two concrete sentences about the code or behavior to change.
- Acceptance criteria should describe the desired code/product outcome, not process steps.
- Every id must be unique within this response.
- Dependencies must contain only ids from this response and only when a card truly requires another card's completed code.
- Do not include markdown, commentary, or extra top-level keys.`

type decompositionResponse struct {
	Cards []decomposedCard `json:"cards"`
}

type decomposedCard struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	Dependencies       []string `json:"dependencies"`
}

func (s *SystemService) ExecutePlan(workspaceID string) (KanbanBoard, error) {
	_, settings, err := s.workspaceAndSettingsFor(workspaceID, llm.InteractionKanbanDecompose)
	if err != nil {
		return KanbanBoard{}, err
	}

	visible, err := s.visiblePlanMessages(workspaceID)
	if err != nil {
		return KanbanBoard{}, err
	}

	client, err := llm.NewClient(settings)
	if err != nil {
		return KanbanBoard{}, err
	}
	request, err := llm.NewChatRequest(settings, []llm.Message{
		{
			Role:    llm.RoleSystem,
			Content: decompositionSystemPrompt,
		},
		{
			Role:    llm.RoleUser,
			Content: decompositionUserPrompt(visible),
		},
	})
	if err != nil {
		return KanbanBoard{}, err
	}

	response, err := client.Complete(context.Background(), request)
	if err != nil {
		return KanbanBoard{}, errors.New(userFacingLLMError(err))
	}
	if len(response.Choices) == 0 {
		return KanbanBoard{}, fmt.Errorf("decomposition returned no choices")
	}

	cards, err := parseDecomposedCards(response.Choices[0].Message.Content)
	if err != nil {
		return KanbanBoard{}, err
	}
	return s.appendReadyCards(workspaceID, cards)
}

func (s *SystemService) visiblePlanMessages(workspaceID string) ([]llm.Message, error) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()

	session := s.chatSessions[workspaceID]
	if session == nil || len(session.Messages) == 0 {
		return nil, fmt.Errorf("ask Echo for a plan before executing it")
	}
	if session.Busy {
		return nil, fmt.Errorf("wait for the current chat response to finish before executing the plan")
	}

	visible := make([]llm.Message, 0, len(session.Messages))
	hasUser := false
	hasAssistantPlan := false
	for _, message := range session.Messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		switch message.Role {
		case llm.RoleUser:
			hasUser = true
			visible = append(visible, llm.Message{Role: llm.RoleUser, Content: content})
		case llm.RoleAssistant:
			if message.Status != "complete" {
				continue
			}
			hasAssistantPlan = true
			visible = append(visible, llm.Message{Role: llm.RoleAssistant, Content: content})
		}
	}
	if !hasUser || !hasAssistantPlan {
		return nil, fmt.Errorf("ask Echo to produce a visible plan before executing it")
	}
	return visible, nil
}

func decompositionUserPrompt(visible []llm.Message) string {
	var builder strings.Builder
	builder.WriteString("Convert the visible transcript into coding Kanban cards. Use USER MESSAGE blocks for constraints and ASSISTANT MESSAGE blocks for the approved plan. Return only the requested JSON.\n")

	userCount := 0
	assistantCount := 0
	for _, message := range visible {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		label := ""
		switch message.Role {
		case llm.RoleUser:
			userCount++
			label = fmt.Sprintf("USER MESSAGE %d", userCount)
		case llm.RoleAssistant:
			assistantCount++
			label = fmt.Sprintf("ASSISTANT MESSAGE %d", assistantCount)
		default:
			continue
		}
		builder.WriteString("\n--- ")
		builder.WriteString(label)
		builder.WriteString(" ---\n")
		builder.WriteString(content)
		builder.WriteString("\n")
	}
	builder.WriteString("\n--- END VISIBLE CHAT TRANSCRIPT ---")
	return builder.String()
}

func parseDecomposedCards(content string) ([]decomposedCard, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("decomposition returned an empty response")
	}

	candidates := extractJSONObjectCandidates(content)
	if len(candidates) == 0 {
		candidates = []string{content}
	}

	var firstParseErr error
	var firstValidationErr error
	for _, candidate := range candidates {
		var decoded decompositionResponse
		if err := json.Unmarshal([]byte(candidate), &decoded); err != nil {
			if firstParseErr == nil {
				firstParseErr = err
			}
			continue
		}
		if err := validateDecomposedCards(decoded.Cards); err != nil {
			if firstValidationErr == nil {
				firstValidationErr = err
			}
			continue
		}
		return decoded.Cards, nil
	}

	if firstValidationErr != nil {
		return nil, firstValidationErr
	}
	if firstParseErr != nil {
		return nil, fmt.Errorf("decomposition returned invalid JSON: %w", firstParseErr)
	}
	return nil, fmt.Errorf("decomposition returned invalid JSON")
}

func extractJSONObjectCandidates(content string) []string {
	candidates := make([]string, 0, 1)
	for start := 0; start < len(content); start++ {
		if content[start] != '{' {
			continue
		}
		if candidate, ok := balancedJSONObjectCandidate(content, start); ok {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func balancedJSONObjectCandidate(content string, start int) (string, bool) {
	inString := false
	escaped := false
	depth := 0
	for index := start; index < len(content); index++ {
		switch content[index] {
		case '\\':
			if inString {
				escaped = !escaped
				continue
			}
		case '"':
			if !escaped {
				inString = !inString
			}
		case '{':
			if !inString {
				depth++
			}
		case '}':
			if !inString {
				depth--
				if depth == 0 {
					return content[start : index+1], true
				}
			}
		}
		if content[index] != '\\' {
			escaped = false
		}
	}
	return "", false
}

func validateDecomposedCards(cards []decomposedCard) error {
	if len(cards) == 0 {
		return fmt.Errorf("decomposition returned no cards")
	}

	ids := make(map[string]struct{}, len(cards))
	for index := range cards {
		cards[index].ID = strings.TrimSpace(cards[index].ID)
		cards[index].Title = strings.TrimSpace(cards[index].Title)
		cards[index].Description = strings.TrimSpace(cards[index].Description)
		for criteriaIndex := range cards[index].AcceptanceCriteria {
			cards[index].AcceptanceCriteria[criteriaIndex] = strings.TrimSpace(cards[index].AcceptanceCriteria[criteriaIndex])
		}
		for dependencyIndex := range cards[index].Dependencies {
			cards[index].Dependencies[dependencyIndex] = strings.TrimSpace(cards[index].Dependencies[dependencyIndex])
		}

		card := cards[index]
		id := card.ID
		if id == "" {
			return fmt.Errorf("card %d is missing an id", index+1)
		}
		if _, exists := ids[id]; exists {
			return fmt.Errorf("card id %q is duplicated", id)
		}
		ids[id] = struct{}{}

		if card.Title == "" {
			return fmt.Errorf("card %q is missing a title", id)
		}
		if card.Description == "" {
			return fmt.Errorf("card %q is missing a description", id)
		}
		if len(card.AcceptanceCriteria) == 0 {
			return fmt.Errorf("card %q needs at least one acceptance criterion", id)
		}
		for criteriaIndex, criterion := range card.AcceptanceCriteria {
			if criterion == "" {
				return fmt.Errorf("card %q has an empty acceptance criterion at position %d", id, criteriaIndex+1)
			}
		}
	}

	for _, card := range cards {
		for _, dependency := range card.Dependencies {
			dependency = strings.TrimSpace(dependency)
			if dependency == "" {
				return fmt.Errorf("card %q has an empty dependency", card.ID)
			}
			if dependency == card.ID {
				return fmt.Errorf("card %q cannot depend on itself", card.ID)
			}
			if _, exists := ids[dependency]; !exists {
				return fmt.Errorf("card %q depends on unknown card id %q", card.ID, dependency)
			}
		}
	}
	return nil
}
