package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
)

type uiVisionClient struct{ requests []llm.ChatRequest }

func (f *uiVisionClient) StreamChat(context.Context, llm.ChatRequest) *llm.Stream {
	panic("UI specialist must use an independent completion")
}
func (f *uiVisionClient) Complete(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	f.requests = append(f.requests, request)
	return llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.Message{Content: `{"status":"absent"}`}}}, Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 4}}, nil
}

type guiPreviewFixture struct{ fakeImageOutput }

func (f guiPreviewFixture) GUIPreview() (tools.LLMImageContent, bool) { return f.content, true }

func TestGUIPreviewDoesNotEnterPlannerOrSelectVision(t *testing.T) {
	result := tools.ExecutionResult{Success: true, Output: guiPreviewFixture{fakeImageOutput{content: tools.LLMImageContent{DataURL: pngDataURL(), MediaType: "image/png", Name: "UI"}}}}
	if _, ok := toolResultImageMessage("ui_observe", result); ok {
		t.Fatal("GUI preview became model image input")
	}
	images, _ := extractToolMedia(result, 0, 0)
	if len(images) != 1 {
		t.Fatal("GUI preview missing from UI transcript")
	}
	if images[0].Purpose != "gui_preview" {
		t.Fatal("preview purpose was not persisted")
	}
	hydrated := hydrateChatMediaHistory([]llm.Message{{Role: llm.RoleUser, Content: "Continue"}}, []sessions.Turn{{UserMessageIndex: 0, Images: images}})
	if messagesRequireMedia(hydrated) {
		t.Fatal("preview was rehydrated as user image input")
	}
	chat, vision := &uiVisionClient{}, &uiVisionClient{}
	settings := llm.DefaultSettings()
	s := &Server{llm: chat, visionLLM: vision, llmSettings: settings, visionSettings: settings, visionSeparate: true}
	s.visionSettings.Model = "vision-specialist"
	history := []llm.Message{{Role: llm.RoleUser, Content: "Click Save"}, {Role: llm.RoleTool, Content: `{"observation":{"screenshot":{"width":100,"height":100}}}`}}
	_, streamer := s.routeMediaChat(settings, history, false)
	if streamer != chat {
		t.Fatal("GUI preview rerouted the planner")
	}
	compressed := buildCompressedModelHistory(history, nil)
	if messagesRequireMedia(compressed) {
		t.Fatal("compression introduced GUI media")
	}
}

func TestUIVisionUsesSelectedEndpointWithBoundedIndependentInput(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(map[bool]string{false: "separate", true: "same-model"}[same], func(t *testing.T) {
			client := &uiVisionClient{}
			settings := llm.DefaultSettings()
			s := &Server{visionLLM: client, visionSettings: settings, visionSeparate: !same}
			text, usage, err := s.uiVision(context.Background(), sandbox.UIVisionRequest{ImageDataURL: pngDataURL(), Width: 1280, Height: 800, Target: "Save button"})
			if err != nil {
				t.Fatal(err)
			}
			if text != `{"status":"absent"}` || usage.PromptTokens != 10 || len(client.requests) != 1 {
				t.Fatalf("unexpected specialist response: %s %+v", text, usage)
			}
			request := client.requests[0]
			if len(request.Messages) != 2 || len(request.Tools) != 0 || request.MaxTokens == nil || *request.MaxTokens > 1200 {
				t.Fatal("specialist request was not bounded and independent")
			}
			data, _ := json.Marshal(request)
			if !strings.Contains(string(data), "Save button") || !messagesRequireMedia(request.Messages) {
				t.Fatal("specialist target/image missing")
			}
		})
	}
}
