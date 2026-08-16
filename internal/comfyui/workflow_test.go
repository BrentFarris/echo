package comfyui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubstituteTemplateVariables_ReplacesPrompt(t *testing.T) {
	workflow := map[string]any{
		"3": map[string]any{
			"class_type": "CLIPTextEncode",
			"inputs": map[string]any{
				"text": "{{PROMPT}}",
				"clip": "6",
			},
		},
	}

	params := TemplateParams{Prompt: "a beautiful sunset"}
	result := SubstituteTemplateVariables(workflow, params)

	node := result["3"].(map[string]any)
	inputs := node["inputs"].(map[string]any)
	text := inputs["text"].(string)
	if text != "a beautiful sunset" {
		t.Errorf("expected prompt substitution, got %q", text)
	}
}

func TestSubstituteTemplateVariables_ReplacesMultipleVars(t *testing.T) {
	workflow := map[string]any{
		"3": map[string]any{
			"class_type": "CLIPTextEncode",
			"inputs": map[string]any{
				"text": "{{PROMPT}}",
			},
		},
		"5": map[string]any{
			"class_type": "KSampler",
			"inputs": map[string]any{
				"seed":  "{{SEED}}",
				"steps": "{{STEPS}}",
				"cfg":   "{{CFG_SCALE}}",
			},
		},
		"7": map[string]any{
			"class_type": "EmptyLatentImage",
			"inputs": map[string]any{
				"width":  "{{WIDTH}}",
				"height": "{{HEIGHT}}",
			},
		},
	}

	params := TemplateParams{
		Prompt:   "portrait",
		Seed:     42,
		Steps:    30,
		CfgScale: 8.0,
		Width:    768,
		Height:   1024,
	}
	result := SubstituteTemplateVariables(workflow, params)

	checkStr := func(nodeID, key, expected string) {
		t.Helper()
		node := result[nodeID].(map[string]any)
		inputs := node["inputs"].(map[string]any)
		val := inputs[key]
		actual, ok := val.(string)
		if !ok || actual != expected {
			t.Errorf("node %s.%s: expected %q (string), got %v (%T)", nodeID, key, expected, val, val)
		}
	}

	checkFloat := func(nodeID, key string, expected float64) {
		t.Helper()
		node := result[nodeID].(map[string]any)
		inputs := node["inputs"].(map[string]any)
		val := inputs[key]
		actual, ok := val.(float64)
		if !ok || actual != expected {
			t.Errorf("node %s.%s: expected %v (float64), got %v (%T)", nodeID, key, expected, val, val)
		}
	}

	checkStr("3", "text", "portrait")
	checkFloat("5", "seed", 42)
	checkFloat("5", "steps", 30)
	checkFloat("5", "cfg", 8)
	checkFloat("7", "width", 768)
	checkFloat("7", "height", 1024)
}

func TestSubstituteTemplateVariables_LeavesNonTemplateStrings(t *testing.T) {
	workflow := map[string]any{
		"1": map[string]any{
			"class_type": "CheckpointLoaderSimple",
			"inputs": map[string]any{
				"ckpt_name": "my-model.safetensors",
			},
		},
	}

	params := TemplateParams{Prompt: "test"}
	result := SubstituteTemplateVariables(workflow, params)

	node := result["1"].(map[string]any)
	inputs := node["inputs"].(map[string]any)
	name := inputs["ckpt_name"].(string)
	if name != "my-model.safetensors" {
		t.Errorf("expected unchanged string, got %q", name)
	}
}

func TestSubstituteTemplateVariables_NegativePrompt(t *testing.T) {
	workflow := map[string]any{
		"4": map[string]any{
			"class_type": "CLIPTextEncode",
			"inputs": map[string]any{
				"text": "{{NEGATIVE_PROMPT}}",
			},
		},
	}

	params := TemplateParams{NegativePrompt: "blurry, low quality"}
	result := SubstituteTemplateVariables(workflow, params)

	node := result["4"].(map[string]any)
	inputs := node["inputs"].(map[string]any)
	text := inputs["text"].(string)
	if text != "blurry, low quality" {
		t.Errorf("expected negative prompt substitution, got %q", text)
	}
}

func TestSubstituteTemplateVariables_PartialSubstitution(t *testing.T) {
	workflow := map[string]any{
		"3": map[string]any{
			"class_type": "CLIPTextEncode",
			"inputs": map[string]any{
				"text": "{{PROMPT}} with {{UNKNOWN_VAR}}",
			},
		},
	}

	params := TemplateParams{Prompt: "cat"}
	result := SubstituteTemplateVariables(workflow, params)

	node := result["3"].(map[string]any)
	inputs := node["inputs"].(map[string]any)
	text := inputs["text"].(string)
	if text != "cat with {{UNKNOWN_VAR}}" {
		t.Errorf("expected partial substitution, got %q", text)
	}
}

func TestSubstituteTemplateVariables_NestedStructure(t *testing.T) {
	workflow := map[string]any{
		"10": map[string]any{
			"class_type": "SomeCustomNode",
			"inputs": map[string]any{
				"control_net_conditioning": []any{
					map[string]any{
						"text": "{{PROMPT}}",
					},
				},
			},
		},
	}

	params := TemplateParams{Prompt: "landscape"}
	result := SubstituteTemplateVariables(workflow, params)

	node := result["10"].(map[string]any)
	inputs := node["inputs"].(map[string]any)
	arr := inputs["control_net_conditioning"].([]any)
	nested := arr[0].(map[string]any)
	text := nested["text"].(string)
	if text != "landscape" {
		t.Errorf("expected nested substitution, got %q", text)
	}
}

func TestBuildDefaultWorkflow_HasRequiredNodes(t *testing.T) {
	params := TemplateParams{Prompt: "test"}
	workflow := BuildDefaultWorkflow(params)

	classTypes := make(map[string]int)
	for _, nodeAny := range workflow {
		node := nodeAny.(map[string]any)
		ct, _ := node["class_type"].(string)
		classTypes[ct]++
	}

	required := map[string]bool{
		"CheckpointLoaderSimple": false,
		"CLIPTextEncode":         false,
		"KSampler":               false,
		"EmptyLatentImage":       false,
		"VAEDecode":              false,
		"SaveImage":              false,
	}

	for ct := range classTypes {
		delete(required, ct)
	}

	if len(required) > 0 {
		t.Errorf("missing required node types: %v", required)
	}

	// Should have exactly 2 CLIPTextEncode nodes (positive and negative).
	if classTypes["CLIPTextEncode"] != 2 {
		t.Errorf("expected 2 CLIPTextEncode nodes, got %d", classTypes["CLIPTextEncode"])
	}
}

func TestBuildDefaultWorkflow_ParametersApplied(t *testing.T) {
	params := TemplateParams{
		Prompt:   "custom prompt",
		Seed:     12345,
		Steps:    28,
		CfgScale: 9.0,
		Width:    1024,
		Height:   768,
	}
	workflow := BuildDefaultWorkflow(params)

	// Find KSampler node.
	for _, nodeAny := range workflow {
		node := nodeAny.(map[string]any)
		if node["class_type"] == "KSampler" {
			inputs := node["inputs"].(map[string]any)
			if inputs["seed"] != int64(12345) {
				t.Errorf("expected seed 12345, got %v", inputs["seed"])
			}
			if inputs["steps"] != 28 {
				t.Errorf("expected steps 28, got %v", inputs["steps"])
			}
			if inputs["cfg"] != 9.0 {
				t.Errorf("expected cfg 9.0, got %v", inputs["cfg"])
			}
			return
		}
	}
	t.Fatal("KSampler node not found")
}

func TestBuildDefaultWorkflow_DefaultsWhenEmpty(t *testing.T) {
	workflow := BuildDefaultWorkflow(TemplateParams{})

	// Find KSampler and check defaults.
	for _, nodeAny := range workflow {
		node := nodeAny.(map[string]any)
		if node["class_type"] == "KSampler" {
			inputs := node["inputs"].(map[string]any)
			if inputs["seed"] != int64(-1) {
				t.Errorf("expected default seed -1, got %v", inputs["seed"])
			}
			if inputs["steps"] != 20 {
				t.Errorf("expected default steps 20, got %v", inputs["steps"])
			}
			if inputs["cfg"] != 7.5 {
				t.Errorf("expected default cfg 7.5, got %v", inputs["cfg"])
			}
			return
		}
	}
	t.Fatal("KSampler node not found")
}

func TestBuildDefaultWorkflow_ValidJSONStructure(t *testing.T) {
	params := TemplateParams{Prompt: "test"}
	workflow := BuildDefaultWorkflow(params)

	// Should marshal to valid JSON.
	data, err := json.Marshal(workflow)
	if err != nil {
		t.Fatalf("workflow should marshal to JSON: %v", err)
	}

	// Should unmarshal back.
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("marshaled workflow should parse back: %v", err)
	}

	if err := ValidateWorkflow(parsed); err != nil {
		t.Errorf("default workflow should be valid: %v", err)
	}
}

func TestValidateWorkflow_RejectsEmpty(t *testing.T) {
	err := ValidateWorkflow(map[string]any{})
	if err == nil {
		t.Error("expected error for empty workflow")
	}
}

func TestValidateWorkflow_RejectsMissingClassType(t *testing.T) {
	workflow := map[string]any{
		"1": map[string]any{
			"inputs": map[string]any{"text": "hello"},
		},
	}
	err := ValidateWorkflow(workflow)
	if err == nil {
		t.Error("expected error for missing class_type")
	}
}

func TestValidateWorkflow_AcceptsValid(t *testing.T) {
	workflow := map[string]any{
		"1": map[string]any{
			"class_type": "CLIPTextEncode",
			"inputs":     map[string]any{"text": "{{PROMPT}}"},
		},
	}
	if err := ValidateWorkflow(workflow); err != nil {
		t.Errorf("valid workflow rejected: %v", err)
	}
}

func TestLoadWorkflowJSON_FromFile(t *testing.T) {
	tmpDir := t.TempDir()
	testWorkflow := map[string]any{
		"3": map[string]any{
			"class_type": "CLIPTextEncode",
			"inputs": map[string]any{
				"text": "{{PROMPT}}",
				"clip": "6",
			},
		},
		"5": map[string]any{
			"class_type": "KSampler",
			"inputs": map[string]any{
				"seed":  "{{SEED}}",
				"steps": "{{STEPS}}",
				"cfg":   "{{CFG_SCALE}}",
			},
		},
	}

	data, err := json.Marshal(testWorkflow)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(tmpDir, "test-workflow.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	result, err := LoadWorkflowJSON(path)
	if err != nil {
		t.Fatalf("failed to load workflow: %v", err)
	}

	// Verify template variables are present.
	node3 := result["3"].(map[string]any)
	inputs := node3["inputs"].(map[string]any)
	if inputs["text"] != "{{PROMPT}}" {
		t.Errorf("expected {{PROMPT}}, got %v", inputs["text"])
	}

	// Now substitute.
	params := TemplateParams{Prompt: "a cat", Seed: 99, Steps: 15, CfgScale: 6.5}
	result = SubstituteTemplateVariables(result, params)

	inputs = node3["inputs"].(map[string]any)
	if inputs["text"] != "a cat" {
		t.Errorf("expected substituted prompt, got %v", inputs["text"])
	}
}

func TestLoadWorkflowJSON_InvalidPath(t *testing.T) {
	_, err := LoadWorkflowJSON("/nonexistent/path/workflow.json")
	if err == nil {
		t.Error("expected error for nonexistent path")
	}
}

func TestLoadWorkflowJSON_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad.json")
	if err := os.WriteFile(path, []byte("{invalid"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadWorkflowJSON(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestLoadWorkflowJSON_InvalidStructure(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad-structure.json")
	if err := os.WriteFile(path, []byte(`{"1": {"inputs": {}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadWorkflowJSON(path)
	if err == nil {
		t.Error("expected error for missing class_type")
	}
}

func TestParseWorkflowJSON_Valid(t *testing.T) {
	jsonStr := `{
		"3": {"class_type": "CLIPTextEncode", "inputs": {"text": "{{PROMPT}}"}},
		"5": {"class_type": "KSampler", "inputs": {"seed": "{{SEED}}"}}
	}`
	result, err := ParseWorkflowJSON([]byte(jsonStr))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	params := TemplateParams{Prompt: "dog", Seed: 7}
	result = SubstituteTemplateVariables(result, params)

	node3 := result["3"].(map[string]any)
	text := node3["inputs"].(map[string]any)["text"].(string)
	if text != "dog" {
		t.Errorf("expected 'dog', got %q", text)
	}
}

func TestParseWorkflowJSON_TooLarge(t *testing.T) {
	large := make([]byte, MaxWorkflowJSONBytes+1)
	for i := range large {
		large[i] = 'x'
	}
	_, err := ParseWorkflowJSON(large)
	if err == nil {
		t.Error("expected error for oversized workflow")
	}
}

func TestApplyReplacements_NoMatch(t *testing.T) {
	repl := map[string]string{"PROMPT": "hello"}
	result := applyReplacements("no template here", repl)
	if result != "no template here" {
		t.Errorf("expected unchanged, got %q", result)
	}
}

func TestApplyReplacements_MultipleInSameString(t *testing.T) {
	repl := map[string]string{"PROMPT": "cat", "NEGATIVE_PROMPT": "blurry"}
	result := applyReplacements("{{PROMPT}} without {{NEGATIVE_PROMPT}}", repl)
	expected := "cat without blurry"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestSubstituteTemplateVariables_Image(t *testing.T) {
	workflow := map[string]any{
		"2": map[string]any{
			"class_type": "LoadImage",
			"inputs": map[string]any{
				"image":  "{{IMAGE}}",
				"upload": "image",
			},
		},
	}

	params := TemplateParams{Image: "input-photo.png"}
	result := SubstituteTemplateVariables(workflow, params)

	node := result["2"].(map[string]any)
	inputs := node["inputs"].(map[string]any)
	image := inputs["image"].(string)
	if image != "input-photo.png" {
		t.Errorf("expected IMAGE substitution, got %q", image)
	}
}

func TestVideoTemplateVariables(t *testing.T) {
	// Simulate a video workflow with AnimateDiff and VHS_VideoCombine nodes
	workflow := map[string]any{
		"10": map[string]any{
			"class_type": "AnimateDiff",
			"inputs": map[string]any{
				"model":  "6_0",
				"motion": "mm_sd_v15.ckpt",
				"value":  "{{FRAMES}}",
			},
		},
		"11": map[string]any{
			"class_type": "VHS_VideoCombine",
			"inputs": map[string]any{
				"frame_rate": "{{FPS}}",
				"format":     "{{FORMAT}}",
				"pingpong":   false,
			},
		},
	}

	params := TemplateParams{
		Prompt:  "a walking cat",
		Frames:  24,
		FPS:     12.0,
		Format:  "mp4",
	}
	result := SubstituteTemplateVariables(workflow, params)

	// Check AnimateDiff frames
	node10 := result["10"].(map[string]any)
	inputs10 := node10["inputs"].(map[string]any)
	framesVal := inputs10["value"]
	if f, ok := framesVal.(float64); !ok || int(f) != 24 {
		t.Errorf("expected FRAMES=24 (numeric), got %v (%T)", framesVal, framesVal)
	}

	// Check VHS_VideoCombine fps and format
	node11 := result["11"].(map[string]any)
	inputs11 := node11["inputs"].(map[string]any)
	fpsVal := inputs11["frame_rate"]
	if f, ok := fpsVal.(float64); !ok || f != 12.0 {
		t.Errorf("expected FPS=12 (numeric), got %v (%T)", fpsVal, fpsVal)
	}
	formatVal, ok := inputs11["format"].(string)
	if !ok || formatVal != "mp4" {
		t.Errorf("expected FORMAT=mp4 (string), got %v (%T)", inputs11["format"], inputs11["format"])
	}
}

func TestVideoDefaults(t *testing.T) {
	// When video params are not specified, buildReplaceMap should use defaults:
	// 16 frames, 8.0 fps, mp4 format
	workflow := map[string]any{
		"10": map[string]any{
			"class_type": "VHS_VideoCombine",
			"inputs": map[string]any{
				"frame_rate": "{{FPS}}",
				"format":     "{{FORMAT}}",
			},
		},
		"20": map[string]any{
			"class_type": "AnimateDiff",
			"inputs": map[string]any{
				"value": "{{FRAMES}}",
			},
		},
	}

	params := TemplateParams{Prompt: "default video"} // No Frames, FPS, Format set
	result := SubstituteTemplateVariables(workflow, params)

	node10 := result["10"].(map[string]any)
	inputs10 := node10["inputs"].(map[string]any)

	// Default FPS should be 8.0
	fpsVal := inputs10["frame_rate"]
	if f, ok := fpsVal.(float64); !ok || f != 8.0 {
		t.Errorf("expected default FPS=8 (numeric), got %v (%T)", fpsVal, fpsVal)
	}

	// Default FORMAT should be "mp4"
	formatVal, ok := inputs10["format"].(string)
	if !ok || formatVal != "mp4" {
		t.Errorf("expected default FORMAT=mp4 (string), got %v (%T)", inputs10["format"], inputs10["format"])
	}

	// Default FRAMES should be 16
	node20 := result["20"].(map[string]any)
	inputs20 := node20["inputs"].(map[string]any)
	framesVal := inputs20["value"]
	if f, ok := framesVal.(float64); !ok || int(f) != 16 {
		t.Errorf("expected default FRAMES=16 (numeric), got %v (%T)", framesVal, framesVal)
	}
}

func TestVideoTemplateVariableGifFormat(t *testing.T) {
	workflow := map[string]any{
		"11": map[string]any{
			"class_type": "SaveAnimatedGIF",
			"inputs": map[string]any{
				"format": "{{FORMAT}}",
			},
		},
	}

	params := TemplateParams{Format: "gif"}
	result := SubstituteTemplateVariables(workflow, params)

	node11 := result["11"].(map[string]any)
	inputs11 := node11["inputs"].(map[string]any)
	formatVal, ok := inputs11["format"].(string)
	if !ok || formatVal != "gif" {
		t.Errorf("expected FORMAT=gif, got %v (%T)", inputs11["format"], inputs11["format"])
	}
}

func TestVideoTemplateVariableFormatUppercaseNormalization(t *testing.T) {
	workflow := map[string]any{
		"11": map[string]any{
			"class_type": "VHS_VideoCombine",
			"inputs": map[string]any{
				"format": "{{FORMAT}}",
			},
		},
	}

	params := TemplateParams{Format: "MP4"} // Uppercase input
	result := SubstituteTemplateVariables(workflow, params)

	node11 := result["11"].(map[string]any)
	inputs11 := node11["inputs"].(map[string]any)
	formatVal, ok := inputs11["format"].(string)
	if !ok || formatVal != "mp4" {
		t.Errorf("expected FORMAT normalized to 'mp4', got %q", formatVal)
	}
}

func TestVideoTemplateVariableInvalidFormatFallback(t *testing.T) {
	workflow := map[string]any{
		"11": map[string]any{
			"class_type": "VHS_VideoCombine",
			"inputs": map[string]any{
				"format": "{{FORMAT}}",
			},
		},
	}

	for _, invalidFormat := range []string{"webm", "avi", "mp3", ""} {
		params := TemplateParams{Format: invalidFormat}
		result := SubstituteTemplateVariables(workflow, params)

		node11 := result["11"].(map[string]any)
		inputs11 := node11["inputs"].(map[string]any)
		formatVal, ok := inputs11["format"].(string)
		if !ok || formatVal != "mp4" {
			t.Errorf("Format=%q: expected invalid format to fall back to 'mp4', got %v (%T)", invalidFormat, inputs11["format"], inputs11["format"])
		}
	}
}

func TestDurationDrivenVideoTemplateVariables(t *testing.T) {
	// MiniMax H3-style workflow: duration feeds a math expression that
	// computes frame count; resolution comes from aspect ratio + megapixels.
	workflow := map[string]any{
		"105:111": map[string]any{
			"class_type": "PrimitiveFloat",
			"inputs":     map[string]any{"value": "{{DURATION}}"},
		},
		"115": map[string]any{
			"class_type": "ResolutionSelector",
			"inputs": map[string]any{
				"aspect_ratio": "{{ASPECT_RATIO}}",
				"megapixels":   "{{MEGAPIXELS}}",
			},
		},
	}

	params := TemplateParams{
		Prompt:      "a test video",
		Duration:    5,
		AspectRatio: "3:4 (Portrait Standard)",
		Megapixels:  0.2,
	}
	result := SubstituteTemplateVariables(workflow, params)

	nodeFloat := result["105:111"].(map[string]any)
	durationVal := nodeFloat["inputs"].(map[string]any)["value"]
	if f, ok := durationVal.(float64); !ok || f != 5.0 {
		t.Errorf("expected DURATION=5 (numeric), got %v (%T)", durationVal, durationVal)
	}

	nodeRes := result["115"].(map[string]any)
	resInputs := nodeRes["inputs"].(map[string]any)
	if ar, ok := resInputs["aspect_ratio"].(string); !ok || ar != "3:4 (Portrait Standard)" {
		t.Errorf("expected ASPECT_RATIO label passthrough, got %v (%T)", resInputs["aspect_ratio"], resInputs["aspect_ratio"])
	}
	if mp, ok := resInputs["megapixels"].(float64); !ok || mp != 0.2 {
		t.Errorf("expected MEGAPIXELS=0.2 (numeric), got %v (%T)", resInputs["megapixels"], resInputs["megapixels"])
	}
}

func TestDurationDrivenVideoDefaults(t *testing.T) {
	workflow := map[string]any{
		"105:111": map[string]any{
			"class_type": "PrimitiveFloat",
			"inputs":     map[string]any{"value": "{{DURATION}}"},
		},
		"115": map[string]any{
			"class_type": "ResolutionSelector",
			"inputs": map[string]any{
				"aspect_ratio": "{{ASPECT_RATIO}}",
				"megapixels":   "{{MEGAPIXELS}}",
			},
		},
	}

	params := TemplateParams{Prompt: "defaults"} // no duration params set
	result := SubstituteTemplateVariables(workflow, params)

	nodeFloat := result["105:111"].(map[string]any)
	durationVal := nodeFloat["inputs"].(map[string]any)["value"]
	if f, ok := durationVal.(float64); !ok || f != 5.0 {
		t.Errorf("expected default DURATION=5 (numeric), got %v (%T)", durationVal, durationVal)
	}

	nodeRes := result["115"].(map[string]any)
	resInputs := nodeRes["inputs"].(map[string]any)
	if ar, ok := resInputs["aspect_ratio"].(string); !ok || ar != "16:9 (Widescreen)" {
		t.Errorf("expected default ASPECT_RATIO=16:9 (Widescreen), got %v (%T)", resInputs["aspect_ratio"], resInputs["aspect_ratio"])
	}
	if mp, ok := resInputs["megapixels"].(float64); !ok || mp != 0.4 {
		t.Errorf("expected default MEGAPIXELS=0.4 (numeric), got %v (%T)", resInputs["megapixels"], resInputs["megapixels"])
	}
}

func TestNegativeSeedIsRandomized(t *testing.T) {
	// Fresh workflow per call: SubstituteTemplateVariables mutates inputs in place,
	// so a reused map would hand back the previously substituted seed.
	freshWorkflow := func() map[string]any {
		return map[string]any{
			"5": map[string]any{
				"class_type": "RandomNoise",
				"inputs":     map[string]any{"noise_seed": "{{SEED}}"},
			},
		}
	}

	first, second := int64(-1), int64(-1)
	for i := 0; i < 20; i++ {
		result := SubstituteTemplateVariables(freshWorkflow(), TemplateParams{Prompt: "x", Seed: first})
		seedVal := result["5"].(map[string]any)["inputs"].(map[string]any)["noise_seed"]
		f, ok := seedVal.(float64)
		if !ok || f < 0 {
			t.Fatalf("expected non-negative numeric seed, got %v (%T)", seedVal, seedVal)
		}
		first = int64(f)

		result = SubstituteTemplateVariables(freshWorkflow(), TemplateParams{Prompt: "x", Seed: second})
		seedVal2 := result["5"].(map[string]any)["inputs"].(map[string]any)["noise_seed"]
		f2, ok := seedVal2.(float64)
		if !ok || f2 < 0 {
			t.Fatalf("expected non-negative numeric seed, got %v (%T)", seedVal2, seedVal2)
		}
		second = int64(f2)

		if first == second {
			t.Fatalf("negative seeds should be randomized, but two consecutive draws both produced %d", first)
		}
	}

	// Seeds must survive JSON float64 round-tripping (ComfyUI API transport).
	result := SubstituteTemplateVariables(freshWorkflow(), TemplateParams{Prompt: "x", Seed: -1})
	seedVal := result["5"].(map[string]any)["inputs"].(map[string]any)["noise_seed"]
	f, ok := seedVal.(float64)
	if !ok {
		t.Fatalf("expected numeric seed, got %T", seedVal)
	}
	if f != float64(int64(f)) {
		t.Errorf("seed %v is not exactly representable as int64 through float64", f)
	}
}

func TestPositiveSeedUnchanged(t *testing.T) {
	workflow := map[string]any{
		"5": map[string]any{
			"class_type": "RandomNoise",
			"inputs":     map[string]any{"noise_seed": "{{SEED}}"},
		},
	}

	result := SubstituteTemplateVariables(workflow, TemplateParams{Prompt: "x", Seed: 123456})
	seedVal := result["5"].(map[string]any)["inputs"].(map[string]any)["noise_seed"]
	if f, ok := seedVal.(float64); !ok || int64(f) != 123456 {
		t.Errorf("expected explicit seed 123456 preserved, got %v (%T)", seedVal, seedVal)
	}
}

func TestMiniMaxH3WorkflowFileSubstitutes(t *testing.T) {
	path := filepath.Join("..", "..", ".comfy", "workflows", "minimax_h3_video.json")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("workflow file not present: %v", err)
	}

	workflow, err := LoadWorkflowJSON(path)
	if err != nil {
		t.Fatalf("failed to load workflow: %v", err)
	}

	params := TemplateParams{
		Prompt:      "a bird flying",
		Duration:    5,
		AspectRatio: "3:4 (Portrait Standard)",
		Megapixels:  0.2,
		Steps:       8,
		Seed:        42,
	}
	result := SubstituteTemplateVariables(workflow, params)

	checks := []struct {
		node  string
		input string
		want  any
	}{
		{"105:104", "prompt", "a bird flying"},
		{"105:111", "value", float64(5)},
		{"115", "aspect_ratio", "3:4 (Portrait Standard)"},
		{"115", "megapixels", 0.2},
		{"105:9", "steps", float64(8)},
		{"105:15", "noise_seed", float64(42)},
	}
	for _, c := range checks {
		node, ok := result[c.node].(map[string]any)
		if !ok {
			t.Fatalf("node %s missing", c.node)
		}
		inputs := node["inputs"].(map[string]any)
		got, ok := inputs[c.input]
		if !ok {
			t.Fatalf("node %s input %s missing", c.node, c.input)
		}
		switch want := c.want.(type) {
		case string:
			if s, isStr := got.(string); !isStr || s != want {
				t.Errorf("node %s %s = %v (%T), want %q", c.node, c.input, got, got, want)
			}
		case float64:
			f, isNum := got.(float64)
			if !isNum || f != want {
				t.Errorf("node %s %s = %v (%T), want %v", c.node, c.input, got, got, want)
			}
		}
	}

	// The frame-count math expression and fixed 24fps output must be preserved
	// verbatim — they are what keep Echo runs in lockstep with manual ComfyUI runs.
	mathNode := result["105:107"].(map[string]any)
	expr, _ := mathNode["inputs"].(map[string]any)["expression"].(string)
	if expr != "max(5, round(a * 24)) + (5 - (max(5, round(a * 24)) % 17)) % 17" {
		t.Errorf("math expression altered: %q", expr)
	}
	videoNode := result["105:91"].(map[string]any)
	if fps, _ := videoNode["inputs"].(map[string]any)["fps"].(float64); fps != 24 {
		t.Errorf("CreateVideo fps = %v, want 24", videoNode["inputs"].(map[string]any)["fps"])
	}

	// No unsubstituted template placeholders may remain anywhere in the graph.
	data, _ := json.Marshal(result)
	if s := string(data); strings.Contains(s, "{{") {
		t.Errorf("unsubstituted template placeholder remains in workflow: %s", s)
	}
}
