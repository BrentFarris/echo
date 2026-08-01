package services

import (
	"testing"

	"github.com/brent/echo/internal/llm"
)

func TestMessagesRequireVisionForImageAndVideoPayloads(t *testing.T) {
	tests := []struct {
		name     string
		messages []llm.Message
		want     bool
	}{
		{
			name:     "text only",
			messages: []llm.Message{{Role: llm.RoleUser, Content: "Review the implementation."}},
		},
		{
			name: "image",
			messages: []llm.Message{{
				Role:         llm.RoleUser,
				ContentParts: []llm.MessageContentPart{llm.ImageURLContentPart("data:image/png;base64,aW1hZ2U=")},
			}},
			want: true,
		},
		{
			name: "video",
			messages: []llm.Message{{
				Role:         llm.RoleUser,
				ContentParts: []llm.MessageContentPart{llm.VideoURLContentPart("data:video/mp4;base64,dmlkZW8=")},
			}},
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := messagesRequireVision(test.messages); got != test.want {
				t.Fatalf("messagesRequireVision() = %v, want %v", got, test.want)
			}
		})
	}
}
