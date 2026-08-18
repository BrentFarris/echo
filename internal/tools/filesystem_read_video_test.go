package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemReadVideoReturnsLLMVideoContent(t *testing.T) {
	workspace := t.TempDir()
	mp4Bytes := []byte{0x00, 0x00, 0x00, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	if err := os.WriteFile(filepath.Join(workspace, "clip.mp4"), mp4Bytes, 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_video",
		mustJSON(t, map[string]any{"path": "clip.mp4"}),
	)

	if !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}
	output, ok := result.Output.(readVideoFileOutput)
	if !ok {
		t.Fatalf("unexpected output type: %#v", result.Output)
	}
	if output.Name != "clip.mp4" || output.MediaType != "video/mp4" {
		t.Fatalf("unexpected video metadata: %#v", output)
	}
	if output.Bytes != int64(len(mp4Bytes)) {
		t.Fatalf("unexpected byte count: %d", output.Bytes)
	}
	if !strings.HasPrefix(output.dataURL, "data:video/mp4;base64,") {
		t.Fatalf("expected data URL prefix, got %q", output.dataURL)
	}
	provider := result.Output.(LLMVideoContentProvider)
	video, ok := provider.LLMVideoContent()
	if !ok || video.DataURL == "" {
		t.Fatalf("expected LLMVideoContent to be available")
	}
}

func TestFilesystemReadVideoRejectsUnsupportedFormat(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "clip.avi"), []byte("RIFF....AVI "), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_video",
		mustJSON(t, map[string]any{"path": "clip.avi"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "unsupported_video" {
		t.Fatalf("expected unsupported video error, got %#v", result)
	}
}

func TestFilesystemReadVideoRejectsInvalidDetail(t *testing.T) {
	workspace := t.TempDir()
	mp4Bytes := []byte{0x00, 0x00, 0x00, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	if err := os.WriteFile(filepath.Join(workspace, "clip.mp4"), mp4Bytes, 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_video",
		mustJSON(t, map[string]any{"path": "clip.mp4", "detail": "ultra"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected invalid detail error, got %#v", result)
	}
}

func TestFilesystemReadVideoDetectsWebMAndMOV(t *testing.T) {
	webm := []byte{0x1a, 0x45, 0xdf, 0xa3, 0x01, 0x00, 0x00, 0x00}
	if got, err := detectVideoMediaType(webm); err != nil || got != "video/webm" {
		t.Fatalf("expected video/webm, got %q err=%v", got, err)
	}
	mov := []byte{0x00, 0x00, 0x00, 0x20, 'm', 'o', 'o', 'v'}
	if got, err := detectVideoMediaType(mov); err != nil || got != "video/quicktime" {
		t.Fatalf("expected video/quicktime, got %q err=%v", got, err)
	}
}

func TestFilesystemReadVideoUsesWorkspaceRootLabels(t *testing.T) {
	base := t.TempDir()
	appRoot := filepath.Join(base, "app")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	mp4Bytes := []byte{0x00, 0x00, 0x00, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	if err := os.WriteFile(filepath.Join(appRoot, "clip.mp4"), mp4Bytes, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := ExecutionContext{
		Context:        context.Background(),
		WorkspaceRoots: []WorkspaceRoot{{Label: "app", Path: appRoot}},
	}

	result := Execute(ctx, "filesystem_read_video", mustJSON(t, map[string]any{"path": "app/clip.mp4"}))
	if !result.Success {
		t.Fatalf("read labeled video failed: %#v", result)
	}
	output := result.Output.(readVideoFileOutput)
	if output.Path != "app/clip.mp4" {
		t.Fatalf("expected labeled path, got %q", output.Path)
	}
}
