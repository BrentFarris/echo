package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPNGBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
}

func TestFilesystemReadImageReturnsLLMImageContent(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "screen.png"), testPNGBytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_image",
		mustJSON(t, map[string]any{"path": "screen.png", "detail": "low"}),
	)
	if !result.Success {
		t.Fatalf("read image failed: %#v", result)
	}
	output, ok := result.Output.(readImageFileOutput)
	if !ok {
		t.Fatalf("unexpected read image output type: %#v", result.Output)
	}
	if output.Path != "screen.png" || output.MediaType != "image/png" || output.ContentType != "image_url" || output.Detail != "low" {
		t.Fatalf("unexpected image output: %#v", output)
	}
	image, ok := output.LLMImageContent()
	if !ok || !strings.HasPrefix(image.DataURL, "data:image/png;base64,") {
		t.Fatalf("expected image_url data URL content, got ok=%v image=%#v", ok, image)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "data:image") {
		t.Fatalf("expected serialized tool result to omit image data URL, got %s", data)
	}
}

func TestFilesystemReadImageRejectsUnsupportedImage(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "vector.svg"), []byte("<svg></svg>"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_image",
		mustJSON(t, map[string]any{"path": "vector.svg"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "unsupported_image" {
		t.Fatalf("expected unsupported image error, got %#v", result)
	}
}

func TestFilesystemReadImageRejectsInvalidDetail(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "screen.png"), testPNGBytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_image",
		mustJSON(t, map[string]any{"path": "screen.png", "detail": "ultra"}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected invalid detail error, got %#v", result)
	}
}

func TestFilesystemReadImageUsesWorkspaceRootLabels(t *testing.T) {
	base := t.TempDir()
	appRoot := filepath.Join(base, "app")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appRoot, "pic.png"), testPNGBytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := ExecutionContext{
		Context:        context.Background(),
		WorkspaceRoots: []WorkspaceRoot{{Label: "app", Path: appRoot}},
	}

	result := Execute(ctx, "filesystem_read_image", mustJSON(t, map[string]any{"path": "app/pic.png"}))
	if !result.Success {
		t.Fatalf("read labeled image failed: %#v", result)
	}
	output := result.Output.(readImageFileOutput)
	if output.Path != "app/pic.png" {
		t.Fatalf("expected labeled path, got %q", output.Path)
	}
}
