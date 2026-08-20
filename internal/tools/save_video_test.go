package tools

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveVideoHappyPath(t *testing.T) {
	workspace := t.TempDir()
	vidName := "test.mp4"

	mp4Data := []byte{0x00, 0x00, 0x00, 0x1C, 0x66, 0x74, 0x79, 0x70, 0x6D, 0x70, 0x34, 0x32}
	dataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(mp4Data)

	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: workspace,
		WorkspaceRoots: []WorkspaceRoot{
			{ID: "echo", Label: "echo", Path: workspace},
		},
		GeneratedVideos: map[string]AttachedVideo{
			"vid-123": {Name: vidName, MediaType: "video/mp4", DataURL: dataURL},
		},
	}

	result := Execute(ctx, "save_video", mustJSON(t, map[string]any{
		"videoId":   "vid-123",
		"path":      "echo/test_output.mp4",
		"overwrite": false,
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	out, ok := result.Output.(saveVideoOutput)
	if !ok {
		t.Fatalf("expected saveVideoOutput, got %T", result.Output)
	}
	if out.Path == "" {
		t.Fatal("expected path to be set")
	}
	if out.BytesWritten <= 0 {
		t.Fatalf("expected bytes written > 0, got %d", out.BytesWritten)
	}

	// Verify file was written — resolveWorkspaceChildPath writes directly under workspace root
	savedPath := filepath.Join(workspace, "test_output.mp4")
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("failed to read saved file: %v", err)
	}
	if len(data) != len(mp4Data) {
		t.Fatalf("expected %d bytes, got %d", len(mp4Data), len(data))
	}
	for i := range mp4Data {
		if data[i] != mp4Data[i] {
			t.Fatalf("byte mismatch at index %d: expected %02x, got %02x", i, mp4Data[i], data[i])
		}
	}
}

func TestSaveVideoGifHappyPath(t *testing.T) {
	workspace := t.TempDir()

	gifData := []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00}
	dataURL := "data:image/gif;base64," + base64.StdEncoding.EncodeToString(gifData)

	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: workspace,
		WorkspaceRoots: []WorkspaceRoot{
			{ID: "echo", Label: "echo", Path: workspace},
		},
		GeneratedVideos: map[string]AttachedVideo{
			"vid-gif-1": {Name: "animated.gif", MediaType: "image/gif", DataURL: dataURL},
		},
	}

	result := Execute(ctx, "save_video", mustJSON(t, map[string]any{
		"videoId": "vid-gif-1",
		"path":    "echo/animated.gif",
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	out, ok := result.Output.(saveVideoOutput)
	if !ok {
		t.Fatalf("expected saveVideoOutput, got %T", result.Output)
	}
	if out.BytesWritten <= 0 {
		t.Fatalf("expected bytes written > 0, got %d", out.BytesWritten)
	}

	savedPath := filepath.Join(workspace, "animated.gif")
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("failed to read saved file: %v", err)
	}
	if string(data[:3]) != "GIF" {
		t.Fatalf("expected GIF magic bytes, got %q", string(data[:3]))
	}
}

func TestSaveVideoNotFound(t *testing.T) {
	workspace := t.TempDir()

	mp4Data := []byte{0x00, 0x00, 0x00, 0x1C}
	dataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(mp4Data)

	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: workspace,
		WorkspaceRoots: []WorkspaceRoot{
			{ID: "echo", Label: "echo", Path: workspace},
		},
		GeneratedVideos: map[string]AttachedVideo{
			"vid-available-1": {Name: "a.mp4", MediaType: "video/mp4", DataURL: dataURL},
			"vid-available-2": {Name: "b.mp4", MediaType: "video/mp4", DataURL: dataURL},
		},
	}

	result := Execute(ctx, "save_video", mustJSON(t, map[string]any{
		"videoId": "vid-missing",
		"path":    "echo/output.mp4",
	}))

	if result.Error == nil {
		t.Fatal("expected error for missing video")
	}
	if result.Error.Code != "video_not_found" {
		t.Fatalf("expected code video_not_found, got %s", result.Error.Code)
	}
	msg := result.Error.Message
	if !strings.Contains(msg, "vid-missing") {
		t.Fatalf("error message should contain requested ID: %s", msg)
	}
	if !strings.Contains(msg, "Available video IDs") {
		t.Fatalf("error message should list available IDs: %s", msg)
	}
}

func TestSaveVideoEmptyDataURL(t *testing.T) {
	workspace := t.TempDir()

	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: workspace,
		WorkspaceRoots: []WorkspaceRoot{
			{ID: "echo", Label: "echo", Path: workspace},
		},
		GeneratedVideos: map[string]AttachedVideo{
			"vid-empty": {Name: "empty.mp4", MediaType: "video/mp4", DataURL: ""},
		},
	}

	result := Execute(ctx, "save_video", mustJSON(t, map[string]any{
		"videoId": "vid-empty",
		"path":    "echo/output.mp4",
	}))

	if result.Error == nil {
		t.Fatal("expected error for empty DataURL")
	}
	if result.Error.Code != "video_not_found" {
		t.Fatalf("expected code video_not_found, got %s", result.Error.Code)
	}
}

func TestSaveVideoMissingVideoID(t *testing.T) {
	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: t.TempDir(),
	}

	result := Execute(ctx, "save_video", mustJSON(t, map[string]any{
		"path": "echo/output.mp4",
	}))

	if result.Error == nil {
		t.Fatal("expected error for missing videoId")
	}
	if result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected code invalid_arguments, got %s", result.Error.Code)
	}
}

func TestSaveVideoMissingPath(t *testing.T) {
	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: t.TempDir(),
	}

	result := Execute(ctx, "save_video", mustJSON(t, map[string]any{
		"videoId": "vid-123",
	}))

	if result.Error == nil {
		t.Fatal("expected error for missing path")
	}
	if result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected code invalid_arguments, got %s", result.Error.Code)
	}
}

func TestSaveVideoFileExists(t *testing.T) {
	workspace := t.TempDir()
	vidPath := filepath.Join(workspace, "existing.mp4")
	os.WriteFile(vidPath, []byte("existing"), 0o644)

	mp4Data := []byte{0x00, 0x00, 0x00, 0x1C}
	dataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(mp4Data)

	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: workspace,
		WorkspaceRoots: []WorkspaceRoot{
			{ID: "echo", Label: "echo", Path: workspace},
		},
		GeneratedVideos: map[string]AttachedVideo{
			"vid-123": {Name: "test.mp4", MediaType: "video/mp4", DataURL: dataURL},
		},
	}

	result := Execute(ctx, "save_video", mustJSON(t, map[string]any{
		"videoId":   "vid-123",
		"path":      "echo/existing.mp4",
		"overwrite": false,
	}))

	if result.Error == nil {
		t.Fatal("expected error when file exists without overwrite")
	}
	if result.Error.Code != "file_exists" {
		t.Fatalf("expected code file_exists, got %s", result.Error.Code)
	}
}

func TestSaveVideoOverwrite(t *testing.T) {
	workspace := t.TempDir()
	vidPath := filepath.Join(workspace, "existing.mp4")
	os.WriteFile(vidPath, []byte("old content"), 0o644)

	mp4Data := []byte{0x00, 0x00, 0x00, 0x1C, 0x66, 0x74, 0x79, 0x70}
	dataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(mp4Data)

	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: workspace,
		WorkspaceRoots: []WorkspaceRoot{
			{ID: "echo", Label: "echo", Path: workspace},
		},
		GeneratedVideos: map[string]AttachedVideo{
			"vid-123": {Name: "test.mp4", MediaType: "video/mp4", DataURL: dataURL},
		},
	}

	result := Execute(ctx, "save_video", mustJSON(t, map[string]any{
		"videoId":   "vid-123",
		"path":      "echo/existing.mp4",
		"overwrite": true,
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	out, ok := result.Output.(saveVideoOutput)
	if !ok {
		t.Fatalf("expected saveVideoOutput, got %T", result.Output)
	}
	if !out.Overwritten {
		t.Fatal("expected overwritten to be true")
	}

	// Verify new content was written
	savedPath := filepath.Join(workspace, "existing.mp4")
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("failed to read saved file: %v", err)
	}
	if string(data) == "old content" {
		t.Fatal("file was not overwritten")
	}
}

func TestSaveVideoMalformedDataURL(t *testing.T) {
	workspace := t.TempDir()

	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: workspace,
		WorkspaceRoots: []WorkspaceRoot{
			{ID: "echo", Label: "echo", Path: workspace},
		},
		GeneratedVideos: map[string]AttachedVideo{
			"vid-bad": {Name: "bad.mp4", MediaType: "video/mp4", DataURL: "not-a-data-url"},
		},
	}

	result := Execute(ctx, "save_video", mustJSON(t, map[string]any{
		"videoId": "vid-bad",
		"path":    "echo/output.mp4",
	}))

	if result.Error == nil {
		t.Fatal("expected error for malformed DataURL")
	}
	if result.Error.Code != "invalid_video_data" {
		t.Fatalf("expected code invalid_video_data, got %s", result.Error.Code)
	}
}

func TestSaveVideoEmptyBase64Content(t *testing.T) {
	workspace := t.TempDir()

	ctx := ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: workspace,
		WorkspaceRoots: []WorkspaceRoot{
			{ID: "echo", Label: "echo", Path: workspace},
		},
		GeneratedVideos: map[string]AttachedVideo{
			"vid-empty-b64": {Name: "empty.mp4", MediaType: "video/mp4", DataURL: "data:video/mp4;base64,"},
		},
	}

	result := Execute(ctx, "save_video", mustJSON(t, map[string]any{
		"videoId": "vid-empty-b64",
		"path":    "echo/output.mp4",
	}))

	if result.Error == nil {
		t.Fatal("expected error for empty base64 content")
	}
	if result.Error.Code != "invalid_video_data" {
		t.Fatalf("expected code invalid_video_data, got %s", result.Error.Code)
	}
}
