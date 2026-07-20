package comfyui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientGenerateSubmitsPrompt(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompt"):
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Fatalf("expected Content-Type application/json, got %s", ct)
			}
			json.NewDecoder(r.Body).Decode(&receivedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "test-prompt-123"})
		case strings.HasPrefix(r.URL.Path, "/history/test-prompt-123"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"test-prompt-123": map[string]any{
					"status": map[string]any{}, // no error
					"outputs": map[string]any{
						"9": map[string]any{
							"images": []any{
								map[string]any{
									"filename":  "ComfyUI_test.png",
									"subfolder": "",
									"type":      "output",
								},
							},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	params := TemplateParams{
		Prompt:   "a cat",
		Width:    512,
		Height:   512,
		Steps:    20,
		CfgScale: 7.5,
		Seed:     42,
	}

	result, err := client.Generate(context.Background(), params, nil)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if result.PromptID != "test-prompt-123" {
		t.Fatalf("expected prompt_id test-prompt-123, got %s", result.PromptID)
	}

	promptData, ok := receivedBody["prompt"].(map[string]any)
	if !ok {
		t.Fatal("expected prompt field to be an object")
	}
	if len(promptData) == 0 {
		t.Fatal("expected non-empty workflow prompt")
	}
}

func TestGetHistoryDetectsExecutionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"test-prompt-123": map[string]any{
				"status": map[string]any{
					"error": "execution_failed",
				},
				"outputs": map[string]any{},
			},
		})
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	_, err := client.GetHistory(context.Background(), "test-prompt-123")
	if err == nil {
		t.Fatal("expected error for execution failure")
	}
}

func TestGetHistoryDetectsNodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"test-prompt-123": map[string]any{
				"outputs": map[string]any{
					"5": map[string]any{
						"error":          "CheckpointNotFound",
						"error_message":  "Model not found: checkpoint1",
						"traceback":      []string{},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	_, err := client.GetHistory(context.Background(), "test-prompt-123")
	if err == nil {
		t.Fatal("expected error for node failure")
	}
	execErr, ok := err.(*ExecutionError)
	if !ok {
		t.Fatalf("expected *ExecutionError, got %T", err)
	}
	if execErr.NodeID != "5" {
		t.Fatalf("expected node ID 5, got %s", execErr.NodeID)
	}
}

func TestClientGenerateRejectsInvalidBaseURL(t *testing.T) {
	client := &Client{BaseURL: "not-a-url"}
	_, err := client.Generate(context.Background(), TemplateParams{}, nil)
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestGetHistoryReturnsImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/history/") {
			t.Fatalf("expected /history/ path, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"test-prompt-123": map[string]any{
				"outputs": map[string]any{
					"9": map[string]any{
						"images": []any{
							map[string]any{
								"filename":  "ComfyUI_test.png",
								"subfolder": "",
								"type":      "output",
							},
							map[string]any{
								"filename":  "preview.png",
								"subfolder": "",
								"type":      "temp",
							},
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	result, err := client.GetHistory(context.Background(), "test-prompt-123")
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}
	if len(result.OutputImages) != 1 {
		t.Fatalf("expected 1 output image, got %d", len(result.OutputImages))
	}
	if result.OutputImages[0] != "ComfyUI_test.png" {
		t.Fatalf("expected ComfyUI_test.png, got %s", result.OutputImages[0])
	}
}

func TestGetHistoryPromptNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	_, err := client.GetHistory(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing prompt ID")
	}
}

func TestFetchImageBytes(t *testing.T) {
	pngData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0xAA, 0xBB}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filename") != "test.png" {
			t.Fatalf("expected filename=test.png, got %s", r.URL.Query().Get("filename"))
		}
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		w.Write(pngData)
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	data, err := client.FetchImageBytes(context.Background(), "test.png", "", "output")
	if err != nil {
		t.Fatalf("FetchImageBytes failed: %v", err)
	}
	if len(data) != len(pngData) {
		t.Fatalf("expected %d bytes, got %d", len(pngData), len(data))
	}
	for i := range pngData {
		if data[i] != pngData[i] {
			t.Fatalf("byte mismatch at index %d: expected %02x, got %02x", i, pngData[i], data[i])
		}
	}
}

func TestUploadImage(t *testing.T) {
	imageData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/upload/image") {
			t.Fatalf("expected /upload/image path, got %s", r.URL.Path)
		}
		if !strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Fatalf("expected multipart form, got Content-Type: %s", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"name":      "my_image.png",
			"subfolder": "",
			"type":      "input",
		})
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	name, err := client.UploadImage(context.Background(), "my_image.png", imageData)
	if err != nil {
		t.Fatalf("UploadImage failed: %v", err)
	}
	if name != "my_image.png" {
		t.Fatalf("expected my_image.png, got %s", name)
	}
}

func TestBuildDefaultWorkflow(t *testing.T) {
	params := TemplateParams{
		Prompt:         "a cat",
		NegativePrompt: "blurry",
		Model:          "test-model.safetensors",
		Width:          768,
		Height:         512,
		Steps:          30,
		CfgScale:       8.0,
		Seed:           12345,
	}

	workflow := BuildDefaultWorkflow(params)

	foundCheckpoint := false
	foundCLIPTextEncode := 0
	foundKSampler := false
	foundSaveImage := false
	foundLatent := false
	foundVAEDecode := false

	for _, nodeAny := range workflow {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}
		classType, _ := node["class_type"].(string)
		switch classType {
		case "CheckpointLoaderSimple":
			foundCheckpoint = true
		case "CLIPTextEncode":
			foundCLIPTextEncode++
		case "KSampler":
			foundKSampler = true
		case "SaveImage":
			foundSaveImage = true
		case "EmptyLatentImage":
			foundLatent = true
		case "VAEDecode":
			foundVAEDecode = true
		}
	}

	if !foundCheckpoint {
		t.Error("expected CheckpointLoaderSimple node")
	}
	if foundCLIPTextEncode != 2 {
		t.Errorf("expected 2 CLIPTextEncode nodes, got %d", foundCLIPTextEncode)
	}
	if !foundKSampler {
		t.Error("expected KSampler node")
	}
	if !foundSaveImage {
		t.Error("expected SaveImage node")
	}
	if !foundLatent {
		t.Error("expected EmptyLatentImage node")
	}
	if !foundVAEDecode {
		t.Error("expected VAEDecode node")
	}
}

func TestValidateWorkflow(t *testing.T) {
	tests := []struct {
		name    string
		workflow map[string]any
		wantErr bool
	}{
		{
			name: "valid workflow",
			workflow: map[string]any{
				"1": map[string]any{"class_type": "CheckpointLoaderSimple"},
			},
			wantErr: false,
		},
		{
			name:     "empty workflow",
			workflow: map[string]any{},
			wantErr:  true,
		},
		{
			name: "missing class_type",
			workflow: map[string]any{
				"1": map[string]any{"inputs": map[string]any{}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWorkflow(tt.workflow)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateWorkflow() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetHistoryDetectsMapFormatStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"test-prompt-123": map[string]any{
				"status": map[string]any{
					"error": map[string]any{
						"message": "Something went wrong during execution",
						"traceback": []any{
							"Traceback (most recent call last):",
							"  File \"nodes.py\", line 42, in execute\n    raise RuntimeError(\"boom\")",
						},
						"current_inputs": map[string]any{},
					},
				},
				"outputs": map[string]any{},
			},
		})
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	_, err := client.GetHistory(context.Background(), "test-prompt-123")
	if err == nil {
		t.Fatal("expected error for map-format status error")
	}
	execErr, ok := err.(*ExecutionError)
	if !ok {
		t.Fatalf("expected *ExecutionError, got %T", err)
	}
	if execErr.NodeID != "status" {
		t.Fatalf("expected node ID 'status', got %s", execErr.NodeID)
	}
	if execErr.ErrorMessage != "Something went wrong during execution" {
		t.Fatalf("expected message from map, got %q", execErr.ErrorMessage)
	}
}

func TestGetHistoryDetectsMapFormatStatusErrorTracebackOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"test-prompt-123": map[string]any{
				"status": map[string]any{
					"error": map[string]any{
						"traceback": []any{
							"Traceback (most recent call last):",
							"RuntimeError: checkpoint not found",
						},
					},
				},
				"outputs": map[string]any{},
			},
		})
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	_, err := client.GetHistory(context.Background(), "test-prompt-123")
	if err == nil {
		t.Fatal("expected error when traceback is present but message is empty")
	}
	execErr, ok := err.(*ExecutionError)
	if !ok {
		t.Fatalf("expected *ExecutionError, got %T", err)
	}
	if execErr.ErrorMessage != "Traceback (most recent call last):" {
		t.Fatalf("expected first traceback line as message, got %q", execErr.ErrorMessage)
	}
}

func TestGetHistoryDetectsStatusStrError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"test-prompt-123": map[string]any{
				"status": map[string]any{
					"status_str": "error",
				},
				"outputs": map[string]any{},
			},
		})
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	_, err := client.GetHistory(context.Background(), "test-prompt-123")
	if err == nil {
		t.Fatal("expected error when status_str is 'error'")
	}
	execErr, ok := err.(*ExecutionError)
	if !ok {
		t.Fatalf("expected *ExecutionError, got %T", err)
	}
	if execErr.NodeID != "status" {
		t.Fatalf("expected node ID 'status', got %s", execErr.NodeID)
	}
}

func TestWaitForCompletionPollFailsOnCompletedNoImages(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/history/test-prompt-123"):
			callCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Prompt IS in history (execution completed), but no output images
			json.NewEncoder(w).Encode(map[string]any{
				"test-prompt-123": map[string]any{
					"status":  map[string]any{}, // no error
					"outputs": map[string]any{}, // empty outputs
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	_, err := client.WaitForCompletionPoll(context.Background(), "test-prompt-123")
	if err == nil {
		t.Fatal("expected error when execution completes with no images")
	}
	if !strings.Contains(err.Error(), "no output images") {
		t.Fatalf("expected 'no output images' in error, got %q", err.Error())
	}
	// Should only poll once — history returned immediately
	if callCount != 1 {
		t.Fatalf("expected 1 history call, got %d (should not keep polling)", callCount)
	}
}

func TestWaitForCompletionPollMaxPollCount(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/history/test-prompt-123"):
			callCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Prompt never appears in history — simulate still running forever
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL}
	// Use a short deadline so the max poll count is small
	ctxShort, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := client.WaitForCompletionPoll(ctxShort, "test-prompt-123")
	if err == nil {
		t.Fatal("expected error when prompt never appears in history")
	}
	// Should eventually time out rather than spin forever
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %q", err.Error())
	}
	// Poll count should be bounded by the timeout seconds + 1
	if callCount > 5 {
		t.Fatalf("expected at most ~4 polls for a 3-second timeout, got %d", callCount)
	}
}
