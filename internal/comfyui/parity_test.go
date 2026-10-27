package comfyui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultWorkflowUsesAPILinksAndNonnegativeRandomSeeds(t *testing.T) {
	for _, image := range []string{"", "input.png"} {
		workflow := BuildDefaultWorkflow(TemplateParams{Prompt: "test", Model: "model.safetensors", Seed: -1, Image: image})
		foundImage := false
		for _, raw := range workflow {
			node := raw.(map[string]any)
			inputs := node["inputs"].(map[string]any)
			if node["class_type"] == "LoadImage" {
				foundImage = true
				if inputs["image"] != image {
					t.Fatal(inputs)
				}
			}
			if node["class_type"] == "KSampler" {
				seed := inputs["seed"].(int64)
				if seed < 0 || seed >= 1<<53 {
					t.Fatal(seed)
				}
			}
			for _, key := range []string{"model", "clip", "vae", "positive", "negative", "latent_image", "samples", "images", "pixels"} {
				if value, exists := inputs[key]; exists {
					link, ok := value.([]any)
					if !ok || len(link) != 2 {
						t.Fatal("invalid API link", key, value)
					}
					if _, exists := workflow[link[0].(string)]; !exists {
						t.Fatal("missing linked node", link)
					}
					if key == "clip" && link[1] != 1 {
						t.Fatal("CLIP must use checkpoint output 1", link)
					}
				}
			}
		}
		if foundImage != (image != "") {
			t.Fatal("fallback ignored input image")
		}
	}
}

func TestTemplateTextIsNotReinterpreted(t *testing.T) {
	params := TemplateParams{Prompt: "{{SEED}}", Seed: 42}
	if value := applyReplacements("{{PROMPT}}", buildReplaceMap(params)); value != "{{SEED}}" {
		t.Fatal(value)
	}
	params.Prompt = "123"
	if value := applyReplacements("{{PROMPT}}", buildReplaceMap(params)); value != "123" {
		t.Fatal(value)
	}
	if value := applyReplacements("{{NEGATIVE_PROMPT}}", buildReplaceMap(params)); value != "" {
		t.Fatal(value)
	}
}

func TestVideoHistoryKeysAndExecutionErrorTuples(t *testing.T) {
	for _, key := range []string{"images", "videos", "video", "gifs", "animated_gifs"} {
		t.Run(key, func(t *testing.T) {
			typ := "output"
			if key != "images" {
				typ = "custom"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"job": map[string]any{"outputs": map[string]any{"1": map[string]any{key: []any{map[string]any{"filename": "clip.webm", "subfolder": "a/b", "type": typ}}}}}})
			}))
			defer server.Close()
			result, err := (&Client{BaseURL: server.URL}).GetHistory(context.Background(), "job")
			if err != nil || len(result.OutputVideos) != 1 || result.OutputVideos[0] != "a/b/clip.webm" || result.MediaTypes["a/b/clip.webm"] != typ {
				t.Fatal(result, err)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"job":{"status":{"status_str":"error","messages":[["execution_error",{"node_id":"9","exception_type":"ModelError","exception_message":"missing checkpoint"}]]}}}`)
	}))
	defer server.Close()
	_, err := (&Client{BaseURL: server.URL}).WaitForCompletionPoll(context.Background(), "job")
	var execution *ExecutionError
	if !errors.As(err, &execution) || execution.NodeID != "9" || !strings.Contains(err.Error(), "missing checkpoint") {
		t.Fatal(err)
	}
}

func TestFetchMediaRejectsTruncatedDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write([]byte("partial"))
	}))
	defer server.Close()
	if _, err := (&Client{BaseURL: server.URL}).FetchVideoBytes(context.Background(), "test.mp4", "", "output"); err == nil {
		t.Fatal("accepted truncated media")
	}
}
