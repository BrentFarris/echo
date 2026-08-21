package server

import (
	"encoding/base64"
	"testing"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

type fakeImageOutput struct {
	content tools.LLMImageContent
}

func (f fakeImageOutput) LLMImageContent() (tools.LLMImageContent, bool) { return f.content, true }

type fakeVideoOutput struct {
	content tools.LLMVideoContent
}

func (f fakeVideoOutput) LLMVideoContent() (tools.LLMVideoContent, bool) { return f.content, true }

type fakeBothMediaOutput struct {
	fakeImageOutput
	fakeVideoOutput
}

func pngDataURL() string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png-bytes"))
}

func mp4DataURL() string {
	return "data:video/mp4;base64," + base64.StdEncoding.EncodeToString([]byte("mp4-bytes"))
}

func TestExtractToolMedia(t *testing.T) {
	tests := []struct {
		name     string
		result   tools.ExecutionResult
		exImgs   int
		exVids   int
		wantImgs int
		wantVids int
	}{
		{name: "failed result yields no media", result: tools.ExecutionResult{Success: false}, wantImgs: 0, wantVids: 0},
		{name: "nil output yields no media", result: tools.ExecutionResult{Success: true}, wantImgs: 0, wantVids: 0},
		{name: "non-provider output yields no media", result: tools.ExecutionResult{Success: true, Output: map[string]any{"ok": true}}, wantImgs: 0, wantVids: 0},
		{
			name: "image provider only",
			result: tools.ExecutionResult{
				Success: true,
				Output:  fakeImageOutput{content: tools.LLMImageContent{Name: "ComfyUI_a.png", MediaType: "image/png", Bytes: 9, DataURL: pngDataURL()}},
			},
			wantImgs: 1,
		},
		{
			name: "video provider only",
			result: tools.ExecutionResult{
				Success: true,
				Output:  fakeVideoOutput{content: tools.LLMVideoContent{Name: "clip.mp4", MediaType: "video/mp4", Bytes: 9, DataURL: mp4DataURL()}},
			},
			wantVids: 1,
		},
		{
			name: "both providers",
			result: tools.ExecutionResult{
				Success: true,
				Output: fakeBothMediaOutput{
					fakeImageOutput{content: tools.LLMImageContent{Name: "a.png", MediaType: "image/png", Bytes: 9, DataURL: pngDataURL()}},
					fakeVideoOutput{content: tools.LLMVideoContent{Name: "b.mp4", MediaType: "video/mp4", Bytes: 9, DataURL: mp4DataURL()}},
				},
			},
			wantImgs: 1, wantVids: 1,
		},
		{
			name: "empty data url is skipped",
			result: tools.ExecutionResult{
				Success: true,
				Output:  fakeImageOutput{content: tools.LLMImageContent{Name: "ghost.png", MediaType: "image/png", Bytes: 0, DataURL: "  "}},
			},
			wantImgs: 0,
		},
		{
			name: "name falls back to path label",
			result: tools.ExecutionResult{
				Success: true,
				Output:  fakeImageOutput{content: tools.LLMImageContent{Path: "comfyui_generated", MediaType: "image/png", Bytes: 9, DataURL: pngDataURL()}},
			},
			wantImgs: 1,
		},
		{
			name: "turn budget exhausted drops image",
			result: tools.ExecutionResult{
				Success: true,
				Output:  fakeImageOutput{content: tools.LLMImageContent{Name: "late.png", MediaType: "image/png", Bytes: 9, DataURL: pngDataURL()}},
			},
			exImgs: maxAssistantTurnMedia, exVids: 0, wantImgs: 0,
		},
		{
			name: "budget shared between kinds",
			result: tools.ExecutionResult{
				Success: true,
				Output: fakeBothMediaOutput{
					fakeImageOutput{content: tools.LLMImageContent{Name: "a.png", MediaType: "image/png", Bytes: 9, DataURL: pngDataURL()}},
					fakeVideoOutput{content: tools.LLMVideoContent{Name: "b.mp4", MediaType: "video/mp4", Bytes: 9, DataURL: mp4DataURL()}},
				},
			},
			exImgs: maxAssistantTurnMedia - 1, exVids: 0, wantImgs: 1, wantVids: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			images, videos := extractToolMedia(tt.result, tt.exImgs, tt.exVids)
			if len(images) != tt.wantImgs || len(videos) != tt.wantVids {
				t.Fatalf("got %d image(s), %d video(s); want %d/%d", len(images), len(videos), tt.wantImgs, tt.wantVids)
			}
			for _, attachment := range append(images, videos...) {
				if attachment.ID == "" || attachment.DataURL == "" || attachment.MediaType == "" {
					t.Fatalf("malformed attachment: %#v", attachment)
				}
			}
		})
	}
}

func TestExtractToolMediaFallsBackToDefaultName(t *testing.T) {
	images, _ := extractToolMedia(tools.ExecutionResult{
		Success: true,
		Output:  fakeImageOutput{content: tools.LLMImageContent{MediaType: "image/png", Bytes: 9, DataURL: pngDataURL()}},
	}, 0, 0)
	if len(images) != 1 || images[0].Name != "generated-image" {
		t.Fatalf("expected default name generated-image, got %#v", images)
	}
}

func TestExtractToolMediaSanitizesNames(t *testing.T) {
	images, _ := extractToolMedia(tools.ExecutionResult{
		Success: true,
		Output:  fakeImageOutput{content: tools.LLMImageContent{Name: "../../etc/passwd", MediaType: "image/png", Bytes: 9, DataURL: pngDataURL()}},
	}, 0, 0)
	if len(images) != 1 || images[0].Name != "passwd" {
		t.Fatalf("expected basename sanitization, got %#v", images)
	}
}

// --- Phase 1: generated-media tracking + context hygiene ---

type fakeTrackedImageOutput struct {
	fakeImageOutput
	id string
}

func (f fakeTrackedImageOutput) GetImageID() string { return f.id }

type fakeTrackedVideoOutput struct {
	fakeVideoOutput
	id string
}

func (f fakeTrackedVideoOutput) VideoID() string { return f.id }

func TestTrackGeneratedMediaRecordsProviderIDs(t *testing.T) {
	s := &chatSession{}
	images := make(map[string]tools.AttachedImage)
	videos := make(map[string]tools.AttachedVideo)

	s.trackGeneratedMediaLocked(images, videos, tools.ExecutionResult{
		Tool:    "comfyui_generate",
		Success: true,
		Output: fakeTrackedImageOutput{
			fakeImageOutput: fakeImageOutput{content: tools.LLMImageContent{Name: "a.png", MediaType: "image/png", Bytes: 9, DataURL: pngDataURL()}},
			id:              "img-1",
		},
	})
	if len(images) != 1 || images["img-1"].Name != "a.png" || images["img-1"].DataURL != pngDataURL() {
		t.Fatalf("expected image tracked under img-1, got %#v", images)
	}

	s.trackGeneratedMediaLocked(images, videos, tools.ExecutionResult{
		Tool:    "comfyui_generate_video",
		Success: true,
		Output: fakeTrackedVideoOutput{
			fakeVideoOutput: fakeVideoOutput{content: tools.LLMVideoContent{Name: "b.mp4", MediaType: "video/mp4", Bytes: 8, DataURL: mp4DataURL()}},
			id:              "vid-1",
		},
	})
	if len(videos) != 1 || videos["vid-1"].Name != "b.mp4" || videos["vid-1"].Bytes != 8 || videos["vid-1"].DataURL != mp4DataURL() {
		t.Fatalf("expected video tracked under vid-1, got %#v", videos)
	}

	// Failed results and non-media outputs are ignored.
	before := len(images)
	s.trackGeneratedMediaLocked(images, videos, tools.ExecutionResult{Tool: "x", Success: false, Error: &tools.ExecutionError{Code: "boom"}})
	s.trackGeneratedMediaLocked(images, videos, tools.ExecutionResult{Tool: "x", Success: true, Output: map[string]any{"ok": true}})
	if len(images) != before {
		t.Fatalf("unexpected tracking for non-media/failed results: %#v", images)
	}
}

func TestStripMediaContentPartsKeepsText(t *testing.T) {
	message := llm.Message{
		Role:         llm.RoleUser,
		Content:      "keep me",
		ToolCallID:   "call-1",
		Name:         "echo-agent-mode",
		ToolCalls:    []llm.ToolCall{{ID: "call-1"}},
		ContentParts: []llm.MessageContentPart{llm.ImageURLContentPart(pngDataURL()), llm.VideoURLContentPart(mp4DataURL())},
	}
	stripped := stripMediaContentParts(message)
	if stripped.Content != "keep me" || stripped.ToolCallID != "call-1" || stripped.Name != "echo-agent-mode" || len(stripped.ToolCalls) != 1 {
		t.Fatalf("text fields not preserved: %#v", stripped)
	}
	if len(stripped.ContentParts) != 0 {
		t.Fatalf("content parts not stripped: %#v", stripped.ContentParts)
	}

	untouched := llm.Message{Role: llm.RoleAssistant, Content: "plain"}
	if out := stripMediaContentParts(untouched); out.Content != untouched.Content || len(out.ContentParts) != 0 {
		t.Fatalf("message without parts should pass through unchanged: %#v", out)
	}
}

func TestHasImageMediaIgnoresVideos(t *testing.T) {
	videoOnly := []llm.Message{{Role: llm.RoleUser, ContentParts: []llm.MessageContentPart{llm.VideoURLContentPart(mp4DataURL())}}}
	if hasImageMedia(videoOnly) {
		t.Fatal("video-only messages must not count as image media")
	}
	withImage := append(videoOnly, llm.Message{Role: llm.RoleUser, ContentParts: []llm.MessageContentPart{llm.ImageURLContentPart(pngDataURL())}})
	if !hasImageMedia(withImage) {
		t.Fatal("image part must be detected")
	}
	if hasImageMedia(nil) {
		t.Fatal("empty message list must report no image media")
	}
}
