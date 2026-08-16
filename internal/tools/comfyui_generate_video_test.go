package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/llm"
)

// --- Tests for comfyui_generate_video tool ---

func TestComfyuiGenerateVideoRejectsMissingPrompt(t *testing.T) {
	result := Execute(ExecutionContext{Context: context.Background()}, "comfyui_generate_video", mustJSON(t, map[string]any{}))
	if result.Error == nil || result.Error.Code != "invalid_arguments" {
		t.Fatalf("expected missing prompt to fail, got %#v", result)
	}
}

func TestComfyuiGenerateVideoMissingURL(t *testing.T) {
	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: "", // Empty URL
	}, "comfyui_generate_video", mustJSON(t, map[string]any{
		"prompt": "a test video",
	}))

	if result.Error == nil {
		t.Fatal("expected error when ComfyUI URL is not configured")
	}
	if result.Error.Code != "missing_comfyui_url" {
		t.Fatalf("expected missing_comfyui_url, got %s", result.Error.Code)
	}
}

func TestComfyuiGenerateVideoParameterValidation(t *testing.T) {
	t.Run("defaults applied when not specified", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]string{"prompt_id": "vid-1"})
			case strings.HasPrefix(r.URL.Path, "/history/vid-1"):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{
					"vid-1": map[string]any{
						"status": map[string]any{},
						"outputs": map[string]any{},
					},
				})
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		// Provide a workflow so we don't hit missing_video_workflow error
		videoWorkflow := `{"10": {"class_type": "VHS_VideoCombine", "inputs": {"frame_rate": "{{FPS}}", "format": "{{FORMAT}}", "pingpong": false}}}`
		result := Execute(ExecutionContext{
			Context:    context.Background(),
			ComfyuiURL: server.URL,
		}, "comfyui_generate_video", mustJSON(t, map[string]any{
			"prompt":       "test video",
			"workflowJSON": videoWorkflow,
		}))

		if result.Error != nil {
			// We expect an error about no output videos, not validation errors
			// This means params passed through correctly
			if !strings.Contains(result.Error.Message, "no output") && result.Error.Code == "missing_video_workflow" {
				t.Fatalf("expected workflow to be used, got: %v", result.Error)
			}
		}
	})

	t.Run("custom values passed through", func(t *testing.T) {
		var receivedWorkflow map[string]any
		server3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
				var payload struct {
					Prompt map[string]any `json:"prompt"`
				}
				json.NewDecoder(r.Body).Decode(&payload)
				receivedWorkflow = payload.Prompt
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]string{"prompt_id": "vid-2"})
			case strings.HasPrefix(r.URL.Path, "/history/vid-2"):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{
					"vid-2": map[string]any{
						"status": map[string]any{},
						"outputs": map[string]any{
							"10": map[string]any{
								"videos": []any{
									map[string]any{
										"filename":  "test.mp4",
										"subfolder": "",
										"type":      "output",
									},
								},
							},
						},
					},
				})
			case r.URL.Path == "/view":
				w.Header().Set("Content-Type", "video/mp4")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte{0x00, 0x00, 0x00, 0x1C, 0x66, 0x74, 0x79, 0x70})
			default:
				http.NotFound(w, r)
			}
		}))
		defer server3.Close()

		videoWorkflow := `{"20": {"class_type": "AnimateDiff", "inputs": {"value": "{{FRAMES}}"}}}`
		result := Execute(ExecutionContext{
			Context:    context.Background(),
			ComfyuiURL: server3.URL,
		}, "comfyui_generate_video", mustJSON(t, map[string]any{
			"prompt":       "test video with custom params",
			"workflowJSON": videoWorkflow,
			"frames":       48,
			"fps":          24.0,
			"format":       "gif",
			"seed":         123456,
		}))

		if result.Error != nil {
			t.Fatalf("unexpected error: %v", result.Error)
		}

		// Verify frames was substituted in workflow
		if receivedWorkflow == nil {
			t.Fatal("expected workflow to be received")
		}
		node20, ok := receivedWorkflow["20"].(map[string]any)
		if !ok {
			t.Fatal("expected node 20")
		}
		inputs20 := node20["inputs"].(map[string]any)
		framesVal := inputs20["value"]
		if f, ok := framesVal.(float64); !ok || int(f) != 48 {
			t.Errorf("expected FRAMES=48 in workflow, got %v (%T)", framesVal, framesVal)
		}
	})
}

func TestComfyuiGenerateVideoWorkflowResolution(t *testing.T) {
	t.Run("explicit workflowJSON takes precedence", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]string{"prompt_id": "wf-json-1"})
			case strings.HasPrefix(r.URL.Path, "/history/wf-json-1"):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{
					"wf-json-1": map[string]any{
						"status":  map[string]any{},
						"outputs": map[string]any{},
					},
				})
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		inlineWorkflow := `{"1": {"class_type": "InlineVideoNode", "inputs": {"text": "{{PROMPT}}"}}}`
		_ = filepath.Join(t.TempDir(), "ignored.json") // workflowPath that won't be used

		result := Execute(ExecutionContext{
			Context:              context.Background(),
			ComfyuiURL:           server.URL,
			ComfyuiTxt2imgWorkflow: "/nonexistent/default.json", // should NOT be loaded
		}, "comfyui_generate_video", mustJSON(t, map[string]any{
			"prompt":       "test workflow resolution",
			"workflowJSON": inlineWorkflow,
			"workflowPath": "ignored.json", // should be ignored because workflowJSON has priority
		}))

		// Should fail with no output videos (not missing_video_workflow) since inline JSON was used
		if result.Error != nil && result.Error.Code == "missing_video_workflow" {
			t.Fatal("expected inline JSON to be used, not fall through to default workflow")
		}
	})

	t.Run("workflowPath used when no workflowJSON", func(t *testing.T) {
		tmpDir := t.TempDir()
		wfFile := filepath.Join(tmpDir, "custom_video.json")
		os.WriteFile(wfFile, []byte(`{"1": {"class_type": "CustomVideoNode", "inputs": {}}}`), 0644)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]string{"prompt_id": "wf-path-1"})
			case strings.HasPrefix(r.URL.Path, "/history/wf-path-1"):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{
					"wf-path-1": map[string]any{
						"status":  map[string]any{},
						"outputs": map[string]any{},
					},
				})
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		result := Execute(ExecutionContext{
			Context:       context.Background(),
			WorkspacePath: tmpDir,
			ComfyuiURL:    server.URL,
		}, "comfyui_generate_video", mustJSON(t, map[string]any{
			"prompt":       "test workflow path",
			"workflowPath": "custom_video.json",
		}))

		if result.Error != nil && result.Error.Code == "load_workflow_failed" {
			t.Fatalf("expected workflow file to load: %v", result.Error)
		}
		if result.Error != nil && result.Error.Code == "missing_video_workflow" {
			t.Fatal("expected workflowPath to be used, not fall through")
		}
	})

	t.Run("error when no workflow configured", func(t *testing.T) {
		result := Execute(ExecutionContext{
			Context:    context.Background(),
			ComfyuiURL: "http://localhost:8188",
		}, "comfyui_generate_video", mustJSON(t, map[string]any{
			"prompt": "test no workflow",
		}))

		if result.Error == nil {
			t.Fatal("expected error when no video workflow configured")
		}
		if result.Error.Code != "missing_video_workflow" {
			t.Fatalf("expected missing_video_workflow, got %s", result.Error.Code)
		}
	})
}

func TestComfyuiGenerateVideoImageUploadForVideo(t *testing.T) {
	workspace := t.TempDir()

	// Create a PNG input image in the workspace.
	pngData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0xAA, 0xBB}
	imgPath := filepath.Join(workspace, "input.png")
	if err := os.WriteFile(imgPath, pngData, 0644); err != nil {
		t.Fatal(err)
	}

	var uploadCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/upload/image":
			uploadCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"name": "echo_input_test.png"})

		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "img2vid-1"})

		case strings.HasPrefix(r.URL.Path, "/history/img2vid-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"img2vid-1": map[string]any{
					"status":  map[string]any{},
					"outputs": map[string]any{},
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	videoWorkflow := `{"10": {"class_type": "Img2VideoNode", "inputs": {"image": "{{IMAGE}}"}}}`

	result := Execute(ExecutionContext{
		Context:       context.Background(),
		WorkspacePath: workspace,
		ComfyuiURL:    server.URL,
	}, "comfyui_generate_video", mustJSON(t, map[string]any{
		"prompt":       "image-to-video test",
		"workflowJSON": videoWorkflow,
		"imagePath":    "input.png",
	}))

	if result.Error != nil && result.Error.Code == "upload_image_failed" {
		t.Fatalf("upload should succeed: %v", result.Error)
	}
	if !uploadCalled {
		t.Fatal("expected /upload/image to be called for image-to-video")
	}
}

func TestComfyuiGenerateVideoTemplateParamPassThrough(t *testing.T) {
	var receivedWorkflow map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			var payload struct {
				Prompt map[string]any `json:"prompt"`
			}
			json.NewDecoder(r.Body).Decode(&payload)
			receivedWorkflow = payload.Prompt
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "params-1"})

		case strings.HasPrefix(r.URL.Path, "/history/params-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"params-1": map[string]any{
					"status":  map[string]any{},
					"outputs": map[string]any{},
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	videoWorkflow := `{
		"10": {"class_type": "VHS_VideoCombine", "inputs": {
			"frame_rate": "{{FPS}}",
			"format": "{{FORMAT}}"
		}},
		"20": {"class_type": "AnimateDiff", "inputs": {
			"value": "{{FRAMES}}"
		}}
	}`

		result := Execute(ExecutionContext{
			Context:    context.Background(),
			ComfyuiURL: server.URL,
		}, "comfyui_generate_video", mustJSON(t, map[string]any{
			"prompt":       "test param passthrough",
			"workflowJSON": videoWorkflow,
			"frames":       32,
			"fps":          15.0,
			"format":       "gif",
		}))

	if result.Error != nil && result.Error.Code == "missing_video_workflow" {
		t.Fatal("expected workflow to be parsed")
	}

	// Verify template substitution passed through correctly
	if receivedWorkflow == nil {
		t.Fatal("expected workflow to be sent to ComfyUI")
	}

	// Check frames
	node20, ok := receivedWorkflow["20"].(map[string]any)
	if !ok {
		t.Fatal("expected node 20 in workflow")
	}
	inputs20 := node20["inputs"].(map[string]any)
	framesVal := inputs20["value"]
	if f, ok := framesVal.(float64); !ok || int(f) != 32 {
		t.Errorf("expected FRAMES=32 in submitted workflow, got %v (%T)", framesVal, framesVal)
	}

	// Check fps and format
	node10, ok := receivedWorkflow["10"].(map[string]any)
	if !ok {
		t.Fatal("expected node 10 in workflow")
	}
	inputs10 := node10["inputs"].(map[string]any)
	fpsVal := inputs10["frame_rate"]
	if f, ok := fpsVal.(float64); !ok || f != 15.0 {
		t.Errorf("expected FPS=15 in submitted workflow, got %v (%T)", fpsVal, fpsVal)
	}
	formatVal, ok := inputs10["format"].(string)
	if !ok || formatVal != "gif" {
		t.Errorf("expected FORMAT=gif in submitted workflow, got %v (%T)", inputs10["format"], inputs10["format"])
	}
}

func TestComfyuiGenerateVideoFullPipeline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "full-vid-1"})

		case strings.HasPrefix(r.URL.Path, "/history/full-vid-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"full-vid-1": map[string]any{
					"status": map[string]any{},
					"outputs": map[string]any{
						"10": map[string]any{
							"videos": []any{
								map[string]any{
									"filename":  "result.mp4",
									"subfolder": "",
									"type":      "output",
								},
							},
						},
					},
				},
			})

		case r.URL.Path == "/view":
			mp4Data := []byte{0x00, 0x00, 0x00, 0x1C, 0x66, 0x74, 0x79, 0x70}
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusOK)
			w.Write(mp4Data)

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	videoWorkflow := `{"10": {"class_type": "VHS_VideoCombine", "inputs": {"format": "{{FORMAT}}"}}}`

	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: server.URL,
	}, "comfyui_generate_video", mustJSON(t, map[string]any{
		"prompt":       "a walking cat video",
		"workflowJSON": videoWorkflow,
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error in full pipeline: %v", result.Error)
	}

	output, ok := result.Output.(comfyuiVideoOutput)
	if !ok {
		t.Fatalf("expected comfyuiVideoOutput, got %T", result.Output)
	}

	if output.PromptID != "full-vid-1" {
		t.Fatalf("expected prompt ID full-vid-1, got %s", output.PromptID)
	}
	if output.Name != "result.mp4" {
		t.Fatalf("expected video name result.mp4, got %s", output.Name)
	}
	if output.MediaType != "video/mp4" {
		t.Fatalf("expected media type video/mp4, got %s", output.MediaType)
	}
	if output.VideoID() == "" {
		t.Fatal("expected VideoID to be set")
	}
	if output.dataURL == "" {
		t.Fatal("expected dataURL to be populated")
	}
}

func TestComfyuiGenerateVideoGifOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "gif-1"})

		case strings.HasPrefix(r.URL.Path, "/history/gif-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"gif-1": map[string]any{
					"status": map[string]any{},
					"outputs": map[string]any{
						"10": map[string]any{
							"gifs": []any{
								map[string]any{
									"filename":  "animated.gif",
									"subfolder": "",
									"type":      "output",
								},
							},
						},
					},
				},
			})

		case r.URL.Path == "/view":
			gifData := []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61} // GIF magic bytes
			w.Header().Set("Content-Type", "image/gif")
			w.WriteHeader(http.StatusOK)
			w.Write(gifData)

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	videoWorkflow := `{"10": {"class_type": "SaveAnimatedGIF", "inputs": {"format": "{{FORMAT}}"}}}`

	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: server.URL,
	}, "comfyui_generate_video", mustJSON(t, map[string]any{
		"prompt":       "animated gif test",
		"workflowJSON": videoWorkflow,
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error in GIF pipeline: %v", result.Error)
	}

	output, ok := result.Output.(comfyuiVideoOutput)
	if !ok {
		t.Fatalf("expected comfyuiVideoOutput, got %T", result.Output)
	}

	if output.Name != "animated.gif" {
		t.Fatalf("expected video name animated.gif, got %s", output.Name)
	}
	if output.MediaType != "image/gif" {
		t.Fatalf("expected media type image/gif for GIF, got %s", output.MediaType)
	}
}

func TestComfyuiGenerateVideoWithSubfolder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "sub-vid-1"})

		case strings.HasPrefix(r.URL.Path, "/history/sub-vid-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"sub-vid-1": map[string]any{
					"status": map[string]any{},
					"outputs": map[string]any{
						"10": map[string]any{
							"videos": []any{
								map[string]any{
									"filename":  "output.mp4",
									"subfolder": "animations",
									"type":      "output",
								},
							},
						},
					},
				},
			})

		case r.URL.Path == "/view":
			if r.URL.Query().Get("subfolder") != "animations" {
				t.Errorf("expected subfolder=animations in /view query, got %s", r.URL.Query().Get("subfolder"))
			}
			mp4Data := []byte{0x00, 0x00, 0x00, 0x1C, 0x66, 0x74, 0x79, 0x70}
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusOK)
			w.Write(mp4Data)

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	videoWorkflow := `{"10": {"class_type": "VHS_VideoCombine", "inputs": {}}}`

	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: server.URL,
	}, "comfyui_generate_video", mustJSON(t, map[string]any{
		"prompt":       "video with subfolder test",
		"workflowJSON": videoWorkflow,
	}))

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}

	output, ok := result.Output.(comfyuiVideoOutput)
	if !ok {
		t.Fatalf("expected comfyuiVideoOutput, got %T", result.Output)
	}
	if output.Name != "output.mp4" {
		t.Fatalf("expected video name output.mp4, got %s", output.Name)
	}
}

func TestComfyuiGenerateVideoNoVideoOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "no-vid-1"})

		case strings.HasPrefix(r.URL.Path, "/history/no-vid-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"no-vid-1": map[string]any{
					"status":  map[string]any{},
					"outputs": map[string]any{}, // No video outputs, no image outputs either
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	videoWorkflow := `{"10": {"class_type": "NoOutputNode", "inputs": {}}}`

	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: server.URL,
	}, "comfyui_generate_video", mustJSON(t, map[string]any{
		"prompt":       "no video output test",
		"workflowJSON": videoWorkflow,
	}))

	if result.Error == nil {
		t.Fatal("expected error when generation produces no output")
	}
	if !strings.Contains(result.Error.Message, "no output images or videos") {
		t.Fatalf("expected 'no output' in error message, got: %s", result.Error.Message)
	}
}

func TestComfyuiGenerateVideoDetectMediaType(t *testing.T) {
	tests := []struct {
		name      string
		filename  string
		data      []byte
		expected  string
	}{
		{
			name:     "mp4 by extension",
			filename: "video.mp4",
			data:     []byte("random bytes"),
			expected: "video/mp4",
		},
		{
			name:     "gif by extension",
			filename: "animated.gif",
			data:     []byte("random bytes"),
			expected: "image/gif",
		},
		{
			name:     "webm by extension",
			filename: "video.webm",
			data:     []byte("random bytes"),
			expected: "video/webm",
		},
		{
			name:     "gif by magic bytes",
			filename: "noext.xyz",
			data:     []byte{'G', 'I', 'F', '8', '9', 'a', 0x00, 0x01},
			expected: "image/gif",
		},
		{
			name:     "mp4 by ftyp bytes",
			filename: "noext.xyz",
			data:     []byte{0x00, 0x00, 0x00, 0x1C, 'f', 't', 'y', 'p'},
			expected: "video/mp4",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := detectComfyuiVideoMediaType(tc.filename, tc.data)
			if result != tc.expected {
				t.Errorf("expected %s, got %s", tc.expected, result)
			}
		})
	}
}

func TestComfyuiGenerateVideoAttachedImageUpload(t *testing.T) {
	var uploadCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/upload/image":
			uploadCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"name": "uploaded.png"})

		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "attached-vid-1"})

		case strings.HasPrefix(r.URL.Path, "/history/attached-vid-1"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{
				"attached-vid-1": map[string]any{
					"status":  map[string]any{},
					"outputs": map[string]any{},
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	pngData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0xAA, 0xBB}
	dataURL := fmt.Sprintf("data:image/png;base64,%s", base64.StdEncoding.EncodeToString(pngData))

	videoWorkflow := `{"10": {"class_type": "Img2VideoNode", "inputs": {"image": "{{IMAGE}}"}}}`

	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: server.URL,
		AttachedImages: []AttachedImage{
			{Name: "frame.png", MediaType: "image/png", DataURL: dataURL},
		},
	}, "comfyui_generate_video", mustJSON(t, map[string]any{
		"prompt":             "image-to-video from attached",
		"workflowJSON":       videoWorkflow,
		"attachedImageIndex": 0,
	}))

	if result.Error != nil && result.Error.Code == "upload_image_failed" {
		t.Fatalf("upload should succeed: %v", result.Error)
	}
	if !uploadCalled {
		t.Fatal("expected /upload/image to be called for attached image")
	}
}

func TestComfyuiGenerateVideoInvalidWorkflowJSON(t *testing.T) {
	result := Execute(ExecutionContext{
		Context:    context.Background(),
		ComfyuiURL: "http://localhost:8188",
	}, "comfyui_generate_video", mustJSON(t, map[string]any{
		"prompt":       "test bad json",
		"workflowJSON": "{invalid json}",
	}))

	if result.Error == nil {
		t.Fatal("expected error for invalid workflow JSON")
	}
	if result.Error.Code != "invalid_workflow_json" {
		t.Fatalf("expected invalid_workflow_json, got %s", result.Error.Code)
	}
}

func TestComfyuiGenerateVideoSchemaDocumentsDefaultWorkflow(t *testing.T) {
	var tool llm.Tool
	found := false
	for _, candidate := range LLMSchema() {
		if candidate.Function.Name == "comfyui_generate_video" {
			tool = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatal("comfyui_generate_video not found in LLMSchema")
	}

	if !strings.Contains(tool.Function.Description, "default video workflow configured in settings") {
		t.Errorf("tool description does not mention the configured default video workflow: %q", tool.Function.Description)
	}

	properties, ok := tool.Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map in schema, got %#v", tool.Function.Parameters)
	}
	for _, key := range []string{"workflowPath", "workflowJSON"} {
		prop, ok := properties[key].(map[string]any)
		if !ok {
			t.Fatalf("expected %s property in schema", key)
		}
		desc, _ := prop["description"].(string)
		if !strings.Contains(desc, "Overrides the configured default video workflow") &&
			!strings.Contains(desc, "overrides the configured default video workflow") {
			t.Errorf("%s description does not mention overriding the configured default: %q", key, desc)
		}
	}
}
