package tools

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveImageHappyPath(t *testing.T) {
	workspace := t.TempDir()
	imgName := "test.png"

	pngData := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0xaa, 0xbb}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData)

	ctx := ExecutionContext{
		Context: context.Background(),
		WorkspacePath: workspace,
		WorkspaceRoots: []WorkspaceRoot{
			{ID: "echo", Label: "echo", Path: workspace},
		},
		GeneratedImages: map[string]AttachedImage{
			"img-123": {Name: imgName, MediaType: "image/png", DataURL: dataURL},
		},
	}

	result := Execute(ctx, "save_image", mustJSON(t, map[string]any{
		"imageId":   "img-123",
		"path":      "echo/test_output.png",
		"overwrite": false,
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	out, ok := result.Output.(saveImageOutput)
	if !ok {
		t.Fatalf("expected saveImageOutput, got %T", result.Output)
	}
	if out.Path == "" {
		t.Fatal("expected path to be set")
	}
	if out.BytesWritten <= 0 {
		t.Fatalf("expected bytes written > 0, got %d", out.BytesWritten)
	}

	// Verify file was written — resolveWorkspaceChildPath writes directly under workspace root
	savedPath := filepath.Join(workspace, "test_output.png")
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("failed to read saved file: %v", err)
	}
	if len(data) != len(pngData) {
		t.Fatalf("expected %d bytes, got %d", len(pngData), len(data))
	}
}

func TestSaveImageNotFound(t *testing.T) {
	workspace := t.TempDir()

	pngData := []byte{0x89, 'P', 'N', 'G'}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData)

	ctx := ExecutionContext{
		Context: context.Background(),
		WorkspacePath: workspace,
		WorkspaceRoots: []WorkspaceRoot{
			{ID: "echo", Label: "echo", Path: workspace},
		},
		GeneratedImages: map[string]AttachedImage{
			"img-available-1": {Name: "a.png", MediaType: "image/png", DataURL: dataURL},
			"img-available-2": {Name: "b.png", MediaType: "image/png", DataURL: dataURL},
		},
	}

	result := Execute(ctx, "save_image", mustJSON(t, map[string]any{
		"imageId":   "img-missing",
		"path":      "echo/output.png",
	}))

	if result.Error == nil {
		t.Fatal("expected error for missing image")
	}
	if result.Error.Code != "image_not_found" {
		t.Fatalf("expected code image_not_found, got %s", result.Error.Code)
	}
	msg := result.Error.Message
	if !strings.Contains(msg, "img-missing") {
		t.Fatalf("error message should contain requested ID: %s", msg)
	}
	if !strings.Contains(msg, "Available image IDs") {
		t.Fatalf("error message should list available IDs: %s", msg)
	}
}

func TestSaveImageEmptyDataURL(t *testing.T) {
	workspace := t.TempDir()

	ctx := ExecutionContext{
		Context: context.Background(),
		WorkspacePath: workspace,
		WorkspaceRoots: []WorkspaceRoot{
			{ID: "echo", Label: "echo", Path: workspace},
		},
		GeneratedImages: map[string]AttachedImage{
			"img-empty": {Name: "empty.png", MediaType: "image/png", DataURL: ""},
		},
	}

	result := Execute(ctx, "save_image", mustJSON(t, map[string]any{
		"imageId":   "img-empty",
		"path":      "echo/output.png",
	}))

	if result.Error == nil {
		t.Fatal("expected error for empty DataURL")
	}
	if result.Error.Code != "image_not_found" {
		t.Fatalf("expected code image_not_found, got %s", result.Error.Code)
	}
}

func TestSaveImageMissingImageID(t *testing.T) {
	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: t.TempDir(),
	}

	result := Execute(ctx, "save_image", mustJSON(t, map[string]any{
		"path": "echo/output.png",
	}))

	if result.Error == nil {
		t.Fatal("expected error for missing imageId")
	}
	if result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected code invalid_arguments, got %s", result.Error.Code)
	}
}

func TestSaveImageMissingPath(t *testing.T) {
	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: t.TempDir(),
	}

	result := Execute(ctx, "save_image", mustJSON(t, map[string]any{
		"imageId": "img-123",
	}))

	if result.Error == nil {
		t.Fatal("expected error for missing path")
	}
	if result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected code invalid_arguments, got %s", result.Error.Code)
	}
}

func TestSaveImageFileExists(t *testing.T) {
	workspace := t.TempDir()
	imgPath := filepath.Join(workspace, "existing.png")
	os.WriteFile(imgPath, []byte("existing"), 0o644)

	pngData := []byte{0x89, 'P', 'N', 'G'}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData)

	ctx := ExecutionContext{
		Context: context.Background(),
		WorkspacePath: workspace,
		WorkspaceRoots: []WorkspaceRoot{
			{ID: "echo", Label: "echo", Path: workspace},
		},
		GeneratedImages: map[string]AttachedImage{
			"img-123": {Name: "test.png", MediaType: "image/png", DataURL: dataURL},
		},
	}

	result := Execute(ctx, "save_image", mustJSON(t, map[string]any{
		"imageId":   "img-123",
		"path":      "echo/existing.png",
		"overwrite": false,
	}))

	if result.Error == nil {
		t.Fatal("expected error when file exists without overwrite")
	}
	if result.Error.Code != "file_exists" {
		t.Fatalf("expected code file_exists, got %s", result.Error.Code)
	}
}

func TestSaveImageOverwrite(t *testing.T) {
	workspace := t.TempDir()
	imgPath := filepath.Join(workspace, "existing.png")
	os.WriteFile(imgPath, []byte("old content"), 0o644)

	pngData := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData)

	ctx := ExecutionContext{
		Context: context.Background(),
		WorkspacePath: workspace,
		WorkspaceRoots: []WorkspaceRoot{
			{ID: "echo", Label: "echo", Path: workspace},
		},
		GeneratedImages: map[string]AttachedImage{
			"img-123": {Name: "test.png", MediaType: "image/png", DataURL: dataURL},
		},
	}

	result := Execute(ctx, "save_image", mustJSON(t, map[string]any{
		"imageId":   "img-123",
		"path":      "echo/existing.png",
		"overwrite": true,
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	out, ok := result.Output.(saveImageOutput)
	if !ok {
		t.Fatalf("expected saveImageOutput, got %T", result.Output)
	}
	if !out.Overwritten {
		t.Fatal("expected overwritten to be true")
	}

	// Verify new content was written
	savedPath := filepath.Join(workspace, "existing.png")
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("failed to read saved file: %v", err)
	}
	if string(data) == "old content" {
		t.Fatal("file was not overwritten")
	}
}


