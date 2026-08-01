package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComfyuiGenerateRejectsMissingPrompt(t *testing.T) {
	result := Execute(ExecutionContext{Context: context.Background()}, "comfyui_generate", mustJSON(t, map[string]any{}))
	if result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected missing prompt to fail, got %#v", result)
	}
}

func TestComfyuiGenerateUsesContextURL(t *testing.T) {
	calledPrompt := false
	calledHistory := false
	calledView := false
	pngData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0xAA, 0xBB}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			calledPrompt = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "test-123"})
		case strings.HasPrefix(r.URL.Path, "/history/test-123"):
			calledHistory = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"test-123": map[string]any{
					"status": map[string]any{},
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
		case r.URL.Path == "/view":
			calledView = true
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			w.Write(pngData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Execute(ExecutionContext{
		Context:  context.Background(),
		ComfyuiURL: server.URL,
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt": "a test image",
	}))

	if !calledPrompt {
		t.Fatal("expected /prompt to be called")
	}
	if !calledHistory {
		t.Fatal("expected /history to be called")
	}
	if !calledView {
		t.Fatal("expected /view to be called")
	}
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	output, ok := result.Output.(comfyuiOutput)
	if !ok {
		t.Fatalf("expected comfyuiOutput, got %T", result.Output)
	}
	if output.PromptID != "test-123" {
		t.Fatalf("expected prompt ID test-123, got %s", output.PromptID)
	}
	if output.Name != "ComfyUI_test.png" {
		t.Fatalf("expected image name ComfyUI_test.png, got %s", output.Name)
	}
	if output.dataURL == "" {
		t.Fatal("expected dataURL to be populated")
	}
}

func TestComfyuiGenerateUsesDefaultURLWhenEmpty(t *testing.T) {
	calledPrompt := false
	pngData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0xCC, 0xDD}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			calledPrompt = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "test-456"})
		case strings.HasPrefix(r.URL.Path, "/history/test-456"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"test-456": map[string]any{
					"status": map[string]any{},
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
		case r.URL.Path == "/view":
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			w.Write(pngData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Execute(ExecutionContext{
		Context:  context.Background(),
		ComfyuiURL: server.URL,
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt": "test",
	}))

	if !calledPrompt {
		t.Fatal("expected /prompt to be called")
	}
	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	output, ok := result.Output.(comfyuiOutput)
	if !ok {
		t.Fatalf("expected comfyuiOutput, got %T", result.Output)
	}
	if output.dataURL == "" {
		t.Fatal("expected dataURL to be populated")
	}
}

func TestComfyuiGenerateReturnsExecutionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "fail-123"})
		case strings.HasPrefix(r.URL.Path, "/history/fail-123"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"fail-123": map[string]any{
					"status": map[string]any{
						"error": "execution_failed",
					},
					"outputs": map[string]any{},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Execute(ExecutionContext{
		Context:  context.Background(),
		ComfyuiURL: server.URL,
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt": "test",
	}))

	if result.Error == nil {
		t.Fatal("expected error for failed execution")
	}
	if result.Error.Code != "comfyui_error" {
		t.Fatalf("expected comfyui_error, got %s", result.Error.Code)
	}
}

func TestComfyuiGenerateExecutionErrorMapFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "fail-map-123"})
		case strings.HasPrefix(r.URL.Path, "/history/fail-map-123"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Map-format status error — this was the original bug
			json.NewEncoder(w).Encode(map[string]any{
				"fail-map-123": map[string]any{
					"status": map[string]any{
						"error": map[string]any{
							"message": "Checkpoint file not found",
							"traceback": []any{
								"Traceback (most recent call last):",
								"  File \"nodes.py\", line 10, in check_exists\n    raise FileNotFoundError()",
							},
						},
					},
					"outputs": map[string]any{},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: server.URL,
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt": "test",
	}))

	if result.Error == nil {
		t.Fatal("expected error for map-format execution failure")
	}
	if result.Error.Code != "comfyui_error" {
		t.Fatalf("expected comfyui_error, got %s", result.Error.Code)
	}
	if !strings.Contains(result.Error.Message, "Checkpoint file not found") {
		t.Fatalf("expected error message to contain checkpoint message, got %q", result.Error.Message)
	}
}

func TestComfyuiGenerateUsesDefaultWorkflowFromSettings(t *testing.T) {
	calledPrompt := false
	var receivedBody string

	tmpFile := t.TempDir() + "/my_workflow.json"
	if err := os.WriteFile(tmpFile, []byte(`{"3": {"class_type": "CheckpointLoaderSimple", "inputs": {}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			calledPrompt = true
			body, _ := io.ReadAll(r.Body)
			receivedBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "default-wf-1"})
		case strings.HasPrefix(r.URL.Path, "/history/default-wf-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"default-wf-1": map[string]any{
					"status": map[string]any{},
					"outputs": map[string]any{
						"9": map[string]any{
							"images": []any{
								map[string]any{
									"filename":  "test.png",
									"subfolder": "",
									"type":      "output",
								},
							},
						},
					},
				},
			})
		case r.URL.Path == "/view":
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte{0x89, 'P', 'N', 'G'})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Execute(ExecutionContext{
		Context:              context.Background(),
		ComfyuiURL:           server.URL,
		ComfyuiTxt2imgWorkflow: tmpFile,
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt": "test with default workflow",
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !calledPrompt {
		t.Fatal("expected /prompt to be called")
	}
	// Verify the custom workflow was sent.
	if !strings.Contains(receivedBody, "CheckpointLoaderSimple") {
		t.Fatalf("expected prompt body to contain CheckpointLoaderSimple from custom workflow, got: %s", receivedBody)
	}
}

func TestComfyuiGenerateSelectsImg2imgWorkflowWhenImagePathPresent(t *testing.T) {
	calledPrompt := false
	var receivedBody string

	txt2imgFile := t.TempDir() + "/txt2img.json"
	if err := os.WriteFile(txt2imgFile, []byte(`{"3": {"class_type": "Txt2imgNode", "inputs": {}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	img2imgFile := t.TempDir() + "/img2img.json"
	if err := os.WriteFile(img2imgFile, []byte(`{"3": {"class_type": "Img2imgNode", "inputs": {}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			calledPrompt = true
			body, _ := io.ReadAll(r.Body)
			receivedBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "img2img-select-1"})
		case strings.HasPrefix(r.URL.Path, "/history/img2img-select-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"img2img-select-1": map[string]any{
					"status": map[string]any{},
					"outputs": map[string]any{
						"9": map[string]any{
							"images": []any{
								map[string]any{
									"filename":  "test.png",
									"subfolder": "",
									"type":      "output",
								},
							},
						},
					},
				},
			})
		case r.URL.Path == "/view":
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte{0x89, 'P', 'N', 'G'})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Execute(ExecutionContext{
		Context:              context.Background(),
		ComfyuiURL:           server.URL,
		ComfyuiTxt2imgWorkflow: txt2imgFile,
		ComfyuiImg2imgWorkflow: img2imgFile,
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt": "test with imagePath should use img2img workflow",
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !calledPrompt {
		t.Fatal("expected /prompt to be called")
	}
	// When no imagePath is provided, txt2img workflow should be selected.
	if !strings.Contains(receivedBody, "Txt2imgNode") {
		t.Fatalf("expected prompt body to contain Txt2imgNode (txt2img workflow), got: %s", receivedBody)
	}
	if strings.Contains(receivedBody, "Img2imgNode") {
		t.Fatalf("prompt body should NOT contain Img2imgNode when imagePath is absent, got: %s", receivedBody)
	}
}

func TestComfyuiGenerateSelectsTxt2imgWorkflowWhenNoImagePath(t *testing.T) {
	calledPrompt := false
	var receivedBody string

	txt2imgFile := t.TempDir() + "/txt2img.json"
	if err := os.WriteFile(txt2imgFile, []byte(`{"3": {"class_type": "Txt2imgNode", "inputs": {}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	img2imgFile := t.TempDir() + "/img2img.json"
	if err := os.WriteFile(img2imgFile, []byte(`{"3": {"class_type": "Img2imgNode", "inputs": {}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			calledPrompt = true
			body, _ := io.ReadAll(r.Body)
			receivedBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "txt2img-select-1"})
		case strings.HasPrefix(r.URL.Path, "/history/txt2img-select-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"txt2img-select-1": map[string]any{
					"status": map[string]any{},
					"outputs": map[string]any{
						"9": map[string]any{
							"images": []any{
								map[string]any{
									"filename":  "test.png",
									"subfolder": "",
									"type":      "output",
								},
							},
						},
					},
				},
			})
		case r.URL.Path == "/view":
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte{0x89, 'P', 'N', 'G'})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Execute(ExecutionContext{
		Context:              context.Background(),
		ComfyuiURL:           server.URL,
		ComfyuiTxt2imgWorkflow: txt2imgFile,
		ComfyuiImg2imgWorkflow: img2imgFile,
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt": "test without imagePath should use txt2img workflow",
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !calledPrompt {
		t.Fatal("expected /prompt to be called")
	}
	// When no imagePath is provided, txt2img workflow should be selected.
	if !strings.Contains(receivedBody, "Txt2imgNode") {
		t.Fatalf("expected prompt body to contain Txt2imgNode (txt2img workflow), got: %s", receivedBody)
	}
	if strings.Contains(receivedBody, "Img2imgNode") {
		t.Fatalf("prompt body should NOT contain Img2imgNode when imagePath is absent, got: %s", receivedBody)
	}
}

func TestComfyuiGenerateFallsBackToBuildDefaultWorkflow(t *testing.T) {
	calledPrompt := false
	var receivedBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			calledPrompt = true
			body, _ := io.ReadAll(r.Body)
			receivedBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "fallback-1"})
		case strings.HasPrefix(r.URL.Path, "/history/fallback-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"fallback-1": map[string]any{
					"status": map[string]any{},
					"outputs": map[string]any{
						"9": map[string]any{
							"images": []any{
								map[string]any{
									"filename":  "test.png",
									"subfolder": "",
									"type":      "output",
								},
							},
						},
					},
				},
			})
		case r.URL.Path == "/view":
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte{0x89, 'P', 'N', 'G'})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// Neither ComfyuiTxt2imgWorkflow nor ComfyuiImg2imgWorkflow set — should fall back to BuildDefaultWorkflow.
	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: server.URL,
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt": "test fallback",
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !calledPrompt {
		t.Fatal("expected /prompt to be called")
	}
	// The built-in default workflow should contain KSampler.
	if !strings.Contains(receivedBody, "KSampler") {
		t.Fatalf("expected prompt body to contain KSampler from built-in default workflow, got: %s", receivedBody)
	}
}

func TestComfyuiGenerateRejectsInvalidDefaultWorkflow(t *testing.T) {
	result := Execute(ExecutionContext{
		Context:              context.Background(),
		ComfyuiURL:           "http://localhost:8188",
		ComfyuiTxt2imgWorkflow: "/nonexistent/path/workflow.json",
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt": "test",
	}))

	if result.Error == nil {
		t.Fatal("expected error for nonexistent default workflow")
	}
	if result.Error.Code != "load_workflow_failed" {
		t.Fatalf("expected load_workflow_failed, got %s", result.Error.Code)
	}
}

func TestComfyuiGenerateUploadsImagePath(t *testing.T) {
	workspace := t.TempDir()

	// Create a PNG input image in the workspace.
	pngData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0xAA, 0xBB}
	imgPath := filepath.Join(workspace, "input.png")
	if err := os.WriteFile(imgPath, pngData, 0644); err != nil {
		t.Fatal(err)
	}

	var uploadedFilename string
	calledUpload := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/upload/image":
			calledUpload = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			uploadedFilename = "echo_input_test.png"
			json.NewEncoder(w).Encode(map[string]string{"name": uploadedFilename})

		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "img2img-1"})

		case strings.HasPrefix(r.URL.Path, "/history/img2img-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"img2img-1": map[string]any{
					"status": map[string]any{},
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

		case r.URL.Path == "/view":
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte{0x89, 'P', 'N', 'G'})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := Execute(ExecutionContext{
		Context:     context.Background(),
		WorkspacePath: workspace,
		ComfyuiURL:  server.URL,
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt":    "img2img test",
		"imagePath": "input.png",
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !calledUpload {
		t.Fatal("expected /upload/image to be called")
	}
	if uploadedFilename == "" {
		t.Fatal("expected image to be uploaded")
	}
	output, ok := result.Output.(comfyuiOutput)
	if !ok {
		t.Fatalf("expected comfyuiOutput, got %T", result.Output)
	}
	if output.PromptID != "img2img-1" {
		t.Fatalf("expected prompt ID img2img-1, got %s", output.PromptID)
	}
}

func TestComfyuiGenerateImagePathNotFound(t *testing.T) {
	workspace := t.TempDir()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	result := Execute(ExecutionContext{
		Context:     context.Background(),
		WorkspacePath: workspace,
		ComfyuiURL:  server.URL,
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt":    "test",
		"imagePath": "missing.png",
	}))

	if result.Error == nil {
		t.Fatal("expected error for missing imagePath")
	}
}

// --- Tests for attachedImageIndex support ---

func createComfyUIServer(t *testing.T, promptID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/upload/image":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"name": "uploaded_test.png"})

		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": promptID})

		case strings.HasPrefix(r.URL.Path, "/history/"+promptID):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				promptID: map[string]any{
					"status": map[string]any{},
					"outputs": map[string]any{
						"9": map[string]any{
							"images": []any{
								map[string]any{
									"filename":  "ComfyUI_out.png",
									"subfolder": "",
									"type":      "output",
								},
							},
						},
					},
				},
			})

		case r.URL.Path == "/view":
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte{0x89, 'P', 'N', 'G'})

		default:
			http.NotFound(w, r)
		}
	}))
}

func TestComfyuiGenerateAttachedImageIndexUploadsFromMemory(t *testing.T) {
	server := createComfyUIServer(t, "attached-1")
	defer server.Close()

	// Minimal PNG data encoded as data URL
	pngData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0xAA, 0xBB}
	dataURL := fmt.Sprintf("data:image/png;base64,%s", base64.StdEncoding.EncodeToString(pngData))

	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: server.URL,
		AttachedImages: []AttachedImage{
			{Name: "test.png", MediaType: "image/png", DataURL: dataURL},
		},
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt":             "img2img from attached",
		"attachedImageIndex": 0,
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	output, ok := result.Output.(comfyuiOutput)
	if !ok {
		t.Fatalf("expected comfyuiOutput, got %T", result.Output)
	}
	if output.PromptID != "attached-1" {
		t.Fatalf("expected prompt ID attached-1, got %s", output.PromptID)
	}
}

func TestComfyuiGenerateAttachedImageIndexOutOfBounds(t *testing.T) {
	pngData := []byte{0x89, 'P', 'N', 'G'}
	dataURL := fmt.Sprintf("data:image/png;base64,%s", base64.StdEncoding.EncodeToString(pngData))

	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: "http://localhost:8188",
		AttachedImages: []AttachedImage{
			{Name: "only.png", MediaType: "image/png", DataURL: dataURL},
		},
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt":             "test",
		"attachedImageIndex": 5,
	}))

	if result.Error == nil {
		t.Fatal("expected error for out-of-bounds index")
	}
	if result.Error.Code != "invalid_index" {
		t.Fatalf("expected invalid_index, got %s", result.Error.Code)
	}
}

func TestComfyuiGenerateAttachedImageIndexNegative(t *testing.T) {
	pngData := []byte{0x89, 'P', 'N', 'G'}
	dataURL := fmt.Sprintf("data:image/png;base64,%s", base64.StdEncoding.EncodeToString(pngData))

	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: "http://localhost:8188",
		AttachedImages: []AttachedImage{
			{Name: "only.png", MediaType: "image/png", DataURL: dataURL},
		},
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt":             "test",
		"attachedImageIndex": -1,
	}))

	if result.Error == nil {
		t.Fatal("expected error for negative index")
	}
	if result.Error.Code != "invalid_index" {
		t.Fatalf("expected invalid_index, got %s", result.Error.Code)
	}
}

func TestComfyuiGenerateAttachedImageIndexNoImages(t *testing.T) {
	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: "http://localhost:8188",
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt":             "test",
		"attachedImageIndex": 0,
	}))

	// With no attached images and no imagePath, this should behave like a normal txt2img call
	// (no image upload, just workflow generation) — but since the server is unreachable, it will fail.
	// The important thing is that it doesn't error with invalid_index.
	if result.Error != nil && result.Error.Code == "invalid_index" {
		t.Fatalf("should not return invalid_index when no attached images are present")
	}
}

func TestComfyuiGenerateImagePathTakesPrecedenceOverAttached(t *testing.T) {
	workspace := t.TempDir()

	// Create a PNG input image in the workspace.
	pngData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0xAA, 0xBB}
	imgPath := filepath.Join(workspace, "input.png")
	if err := os.WriteFile(imgPath, pngData, 0644); err != nil {
		t.Fatal(err)
	}

	var uploadCalled bool
	var uploadedContent []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/upload/image":
			uploadCalled = true
			body, _ := io.ReadAll(r.Body)
			uploadedContent = body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"name": "uploaded.png"})

		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "precedence-1"})

		case strings.HasPrefix(r.URL.Path, "/history/precedence-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"precedence-1": map[string]any{
					"status": map[string]any{},
					"outputs": map[string]any{
						"9": map[string]any{
							"images": []any{
								map[string]any{
									"filename":  "out.png",
									"subfolder": "",
									"type":      "output",
								},
							},
						},
					},
				},
			})

		case r.URL.Path == "/view":
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte{0x89, 'P', 'N', 'G'})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// Provide both imagePath AND attached image — imagePath should win
	attachedJpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0} // JPEG magic bytes (different from PNG)
	attachedDataURL := fmt.Sprintf("data:image/jpeg;base64,%s", base64.StdEncoding.EncodeToString(attachedJpegData))

	result := Execute(ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: workspace,
		ComfyuiURL:    server.URL,
		AttachedImages: []AttachedImage{
			{Name: "attached.jpg", MediaType: "image/jpeg", DataURL: attachedDataURL},
		},
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt":             "test precedence",
		"imagePath":          "input.png",
		"attachedImageIndex": 0,
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if !uploadCalled {
		t.Fatal("expected upload to be called")
	}
	// The multipart body contains the PNG magic bytes (0x89 0x50 0x4E 0x47 = \x89PNG)
	// somewhere inside the form. If imagePath took precedence, we'll find those bytes.
	// If attachedImageIndex won, we'd find JPEG magic (0xFF 0xD8).
	hasPngMagic := false
	for i := 0; i+3 < len(uploadedContent); i++ {
		if uploadedContent[i] == 0x89 && uploadedContent[i+1] == 'P' && uploadedContent[i+2] == 'N' && uploadedContent[i+3] == 'G' {
			hasPngMagic = true
			break
		}
	}
	if !hasPngMagic {
		t.Fatal("expected imagePath (PNG) to take precedence: PNG magic bytes not found in upload")
	}
}

func TestComfyuiGenerateSelectsImg2imgWorkflowForAttachedImage(t *testing.T) {
	var receivedBody string

	txt2imgFile := t.TempDir() + "/txt2img.json"
	if err := os.WriteFile(txt2imgFile, []byte(`{"3": {"class_type": "Txt2imgNode", "inputs": {}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	img2imgFile := t.TempDir() + "/img2img.json"
	if err := os.WriteFile(img2imgFile, []byte(`{"3": {"class_type": "Img2imgNode", "inputs": {}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/upload/image":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"name": "uploaded.png"})

		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			body, _ := io.ReadAll(r.Body)
			receivedBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "wf-select-1"})

		case strings.HasPrefix(r.URL.Path, "/history/wf-select-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"wf-select-1": map[string]any{
					"status": map[string]any{},
					"outputs": map[string]any{
						"9": map[string]any{
							"images": []any{
								map[string]any{
									"filename":  "out.png",
									"subfolder": "",
									"type":      "output",
								},
							},
						},
					},
				},
			})

		case r.URL.Path == "/view":
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte{0x89, 'P', 'N', 'G'})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	pngData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	dataURL := fmt.Sprintf("data:image/png;base64,%s", base64.StdEncoding.EncodeToString(pngData))

	result := Execute(ExecutionContext{
		Context:              context.Background(),
		ComfyuiURL:           server.URL,
		ComfyuiTxt2imgWorkflow: txt2imgFile,
		ComfyuiImg2imgWorkflow: img2imgFile,
		AttachedImages: []AttachedImage{
			{Name: "test.png", MediaType: "image/png", DataURL: dataURL},
		},
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt":             "test workflow selection",
		"attachedImageIndex": 0,
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	// Should select img2img workflow when attachedImageIndex is used
	if !strings.Contains(receivedBody, "Img2imgNode") {
		t.Fatalf("expected Img2imgNode in prompt body when attachedImageIndex is used, got: %s", receivedBody)
	}
	if strings.Contains(receivedBody, "Txt2imgNode") {
		t.Fatalf("should NOT contain Txt2imgNode when attachedImageIndex is used, got: %s", receivedBody)
	}
}

func TestComfyuiGenerateAttachedImageMultipleFormats(t *testing.T) {
	server := createComfyUIServer(t, "multi-format-1")
	defer server.Close()

	formats := []struct {
		name      string
		mediaType string
		data      []byte
	}{
		{"png", "image/png", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}},
		{"jpeg", "image/jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}},
		{"webp", "image/webp", []byte{'R', 'I', 'F', 'F', 0x24, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P'}},
		{"gif", "image/gif", []byte{'G', 'I', 'F', '8', '9', 'a'}},
	}

	for _, tc := range formats {
		t.Run(tc.name, func(t *testing.T) {
			dataURL := fmt.Sprintf("data:%s;base64,%s", tc.mediaType, base64.StdEncoding.EncodeToString(tc.data))

			result := Execute(ExecutionContext{
				Context:    context.Background(),
				ComfyuiURL: server.URL,
				AttachedImages: []AttachedImage{
					{Name: tc.name + "." + strings.TrimPrefix(tc.mediaType, "image/"), MediaType: tc.mediaType, DataURL: dataURL},
				},
			}, "comfyui_generate", mustJSON(t, map[string]any{
				"prompt":             "test format " + tc.name,
				"attachedImageIndex": 0,
			}))

			if result.Error != nil {
				t.Fatalf("unexpected error for %s: %v", tc.name, result.Error)
			}
		})
	}
}

func TestComfyuiGenerateAttachedImageInvalidDataUrl(t *testing.T) {
	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: "http://localhost:8188",
		AttachedImages: []AttachedImage{
			{Name: "bad.png", MediaType: "image/png", DataURL: "not-a-data-url"},
		},
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt":             "test bad data URL",
		"attachedImageIndex": 0,
	}))

	if result.Error == nil {
		t.Fatal("expected error for invalid data URL")
	}
	if result.Error.Code != "decode_image_failed" {
		t.Fatalf("expected decode_image_failed, got %s", result.Error.Code)
	}
}

func TestComfyuiGenerateAttachedImageSecondIndex(t *testing.T) {
	server := createComfyUIServer(t, "second-img-1")
	defer server.Close()

	pngData1 := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	pngData2 := []byte{0xFF, 0xD8, 0xFF, 0xE0} // JPEG
	dataURL1 := fmt.Sprintf("data:image/png;base64,%s", base64.StdEncoding.EncodeToString(pngData1))
	dataURL2 := fmt.Sprintf("data:image/jpeg;base64,%s", base64.StdEncoding.EncodeToString(pngData2))

	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: server.URL,
		AttachedImages: []AttachedImage{
			{Name: "first.png", MediaType: "image/png", DataURL: dataURL1},
			{Name: "second.jpg", MediaType: "image/jpeg", DataURL: dataURL2},
		},
	}, "comfyui_generate", mustJSON(t, map[string]any{
		"prompt":             "test second image",
		"attachedImageIndex": 1,
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	output, ok := result.Output.(comfyuiOutput)
	if !ok {
		t.Fatalf("expected comfyuiOutput, got %T", result.Output)
	}
	if output.PromptID != "second-img-1" {
		t.Fatalf("expected prompt ID second-img-1, got %s", output.PromptID)
	}
}
