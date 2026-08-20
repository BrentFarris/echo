package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Live smoke test against a real ComfyUI server. Skipped unless
// ECHO_LIVE_COMFYUI is set so ordinary CI/local runs never depend on one.
func TestLiveComfyUIGenerationSmoke(t *testing.T) {
	baseURL := os.Getenv("ECHO_LIVE_COMFYUI")
	if baseURL == "" {
		t.Skip("set ECHO_LIVE_COMFYUI=<url> to run against a live server")
	}
	checkpoint := os.Getenv("ECHO_LIVE_CHECKPOINT")
	runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ctx := ExecutionContext{
		Context:                  runCtx,
		ComfyuiURL:               baseURL,
		ComfyuiDefaultCheckpoint: checkpoint,
	}
	args := map[string]any{
		"prompt":         "a single red cube on a plain white background, studio lighting",
		"negativePrompt": "blurry, low quality",
		"width":          512, "height": 512, "steps": 8,
	}
	if workflowJSON := os.Getenv("ECHO_LIVE_WORKFLOW_JSON"); workflowJSON != "" {
		args["workflowJSON"] = workflowJSON
	}
	rawArgs, _ := json.Marshal(args)
	result := Execute(ctx, "comfyui_generate", rawArgs)
	if !result.Success {
		msg := ""
		if result.Error != nil {
			msg = result.Error.Message
		}
		t.Fatalf("generation failed: %s", msg)
	}
	output, ok := result.Output.(interface {
		LLMImageContent() (LLMImageContent, bool)
		GetImageID() string
	})
	if !ok {
		t.Fatalf("unexpected output type %T", result.Output)
	}
	image, ok := output.LLMImageContent()
	if !ok || !strings.HasPrefix(image.DataURL, "data:image/") {
		t.Fatalf("expected image data URL content, got ok=%v mediaType=%q", ok, image.MediaType)
	}
	if image.Bytes <= 0 {
		t.Fatalf("expected positive byte count, got %d", image.Bytes)
	}
	if id := output.GetImageID(); id == "" {
		t.Fatal("expected a non-empty image ID for save_image compatibility")
	}
	t.Logf("generated %s (%s, %d bytes), imageId=%s", image.Name, image.MediaType, image.Bytes, output.GetImageID())
}
