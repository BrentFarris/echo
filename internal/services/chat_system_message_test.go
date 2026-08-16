package services

import (
	"strings"
	"testing"
)

// TestChatSystemMessageGuidesVideoWorkflowDefault ensures the chat system
// prompt tells the model to rely on the configured default video workflow
// instead of inventing its own inline workflow JSON.
func TestChatSystemMessageGuidesVideoWorkflowDefault(t *testing.T) {
	msg := chatSystemMessage(Workspace{}, AgentMode{ID: AgentModeIDGeneral}, nil, false)

	const guidance = "When the user does not specify a particular workflow, call comfyui_generate_video without workflowPath or workflowJSON so it uses the default video workflow configured in settings"
	if !strings.Contains(msg.Content, guidance) {
		t.Errorf("general mode system message is missing default video workflow guidance.\nwant substring: %q\ncontent:\n%s", guidance, msg.Content)
	}

	if !strings.Contains(msg.Content, "comfyui_generate_video generates short videos") {
		t.Errorf("general mode system message lost the comfyui_generate_video guidance sentence:\n%s", msg.Content)
	}

	const durationGuidance = "When the user asks for a specific duration, compute frames = duration × fps and pass both frames and fps"
	if !strings.Contains(msg.Content, durationGuidance) {
		t.Errorf("general mode system message is missing video duration/frames guidance.\nwant substring: %q\ncontent:\n%s", durationGuidance, msg.Content)
	}
}

// TestChatSystemMessageRequiresExplicitVideoSave ensures the chat system
// prompt only instructs the model to call save_video when the user
// explicitly asks, rather than saving every generated video by default.
func TestChatSystemMessageRequiresExplicitVideoSave(t *testing.T) {
	msg := chatSystemMessage(Workspace{}, AgentMode{ID: AgentModeIDGeneral}, nil, false)

	const guidance = "Do not call save_video automatically after generating a video; only call save_video with the returned videoId when the user explicitly asks to save or download the video"
	if !strings.Contains(msg.Content, guidance) {
		t.Errorf("general mode system message is missing explicit-only video save guidance.\nwant substring: %q\ncontent:\n%s", guidance, msg.Content)
	}

	const legacy = "After generating a video, use save_video with the returned videoId to persist it to disk"
	if strings.Contains(msg.Content, legacy) {
		t.Errorf("general mode system message still contains default-save video guidance.\nfound substring: %q\ncontent:\n%s", legacy, msg.Content)
	}
}
