package domain

import "time"

// ChatAlert IDs are based on home and message identity, never websocket sequence.
type ChatAlert struct {
	ID         string    `json:"id"`
	Sequence   int64     `json:"sequence"`
	Kind       string    `json:"kind"`
	HomeNodeID string    `json:"homeNodeId"`
	ChatID     string    `json:"chatId"`
	BotID      string    `json:"botId"`
	MessageID  string    `json:"messageId"`
	OccurredAt time.Time `json:"occurredAt"`
	Title      string    `json:"title"`
	Preview    string    `json:"preview"`
	Active     bool      `json:"active"`
}

type NotificationPreferences struct {
	Completion      bool `json:"completion"`
	Questions       bool `json:"questions"`
	CompletionSound bool `json:"completionSound"`
	QuestionSound   bool `json:"questionSound"`
}

func (p NotificationPreferences) Popup(kind string) bool {
	if kind == "question" {
		return p.Questions
	}
	return p.Completion
}
