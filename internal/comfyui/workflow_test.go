package comfyui

import (
	"encoding/json"
	"os"
	"path/filepath"
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
