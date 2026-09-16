package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validPNGBytes returns a minimal 2x2 red PNG that decodes successfully.
func validPNGBytes() []byte {
	return []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 73, 72, 68, 82, 0, 0, 0, 2, 0, 0, 0, 2, 8, 2, 0, 0, 0, 253, 212, 154, 115, 0, 0, 0, 16, 73, 68, 65, 84, 120, 156, 99, 248, 207, 192, 0, 68, 12, 16, 10, 0, 31, 238, 3, 253, 139, 95, 20, 212, 0, 0, 0, 0, 73, 69, 78, 68, 174, 66, 96, 130}
}

// validGIFBytes returns a minimal 1x1 GIF that decodes successfully.
func validGIFBytes() []byte {
	return []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\xff\x00\x00!\xf9\x04\x00\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x01;\x00")
}

func TestFilesystemReadImageReturnsLLMImageContent(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "screen.png"), validPNGBytes(), 0o600); err != nil {
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
	// PNGs are re-encoded as JPEG for context; GIF pass through unchanged
	if output.Path != "screen.png" || output.MediaType != "image/jpeg" || output.ContentType != "image_url" || output.Detail != "low" {
		t.Fatalf("unexpected image output: %#v", output)
	}
	image, ok := output.LLMImageContent()
	if !ok || !strings.HasPrefix(image.DataURL, "data:image/jpeg;base64,") {
		t.Fatalf("expected jpeg data URL content, got ok=%v image=%#v", ok, image)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "data:image") {
		t.Fatalf("expected serialized tool result to omit image data URL, got %s", data)
	}
}

func TestFilesystemReadImagePreservesGIF(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "anim.gif"), validGIFBytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"filesystem_read_image",
		mustJSON(t, map[string]any{"path": "anim.gif"}),
	)
	if !result.Success {
		t.Fatalf("read gif failed: %#v", result)
	}
	output := result.Output.(readImageFileOutput)
	if output.MediaType != "image/gif" {
		t.Fatalf("expected GIF to pass through unchanged, got %s", output.MediaType)
	}
	image, ok := output.LLMImageContent()
	if !ok || !strings.HasPrefix(image.DataURL, "data:image/gif;base64,") {
		t.Fatalf("expected gif data URL content, got ok=%v image=%#v", ok, image)
	}
}

func TestFilesystemReadImageUsesCustomCompressionSettings(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "pic.png"), validPNGBytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	// Explicit settings — should still produce JPEG output
	result := Execute(
		ExecutionContext{
			Context:                     context.Background(),
			WorkspacePath:               workspace,
			ImageCompressionMaxDimension: 1024,
			ImageCompressionJPEGQuality:  90,
		},
		"filesystem_read_image",
		mustJSON(t, map[string]any{"path": "pic.png"}),
	)
	if !result.Success {
		t.Fatalf("read image failed: %#v", result)
	}
	output := result.Output.(readImageFileOutput)
	if output.MediaType != "image/jpeg" {
		t.Fatalf("expected jpeg output, got %s", output.MediaType)
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
	if err := os.WriteFile(filepath.Join(workspace, "screen.png"), validPNGBytes(), 0o600); err != nil {
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
	if err := os.WriteFile(filepath.Join(appRoot, "pic.png"), validPNGBytes(), 0o600); err != nil {
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
