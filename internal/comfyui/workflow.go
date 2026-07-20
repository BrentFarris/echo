package comfyui

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// TemplateParams holds all substitutable values for ComfyUI workflow templates.
type TemplateParams struct {
	Prompt         string
	NegativePrompt string
	Model          string
	Image          string // filename on ComfyUI server for img2img workflows
	Width          int
	Height         int
	Steps          int
	CfgScale       float64
	Seed           int64
}

// MaxWorkflowJSONBytes caps inline workflowJSON at 500KB.
const MaxWorkflowJSONBytes = 500 * 1024

// LoadWorkflowJSON reads and validates a ComfyUI workflow JSON file.
// It returns the parsed prompt map (node-id → node object).
func LoadWorkflowJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow file: %w", err)
	}
	if len(data) > MaxWorkflowJSONBytes {
		return nil, fmt.Errorf("workflow file exceeds %d bytes", MaxWorkflowJSONBytes)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse workflow JSON: %w", err)
	}

	if err := ValidateWorkflow(raw); err != nil {
		return nil, err
	}

	return raw, nil
}

// ParseWorkflowJSON parses inline workflow JSON bytes.
func ParseWorkflowJSON(data []byte) (map[string]any, error) {
	if len(data) > MaxWorkflowJSONBytes {
		return nil, fmt.Errorf("workflow JSON exceeds %d bytes", MaxWorkflowJSONBytes)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse workflow JSON: %w", err)
	}

	if err := ValidateWorkflow(raw); err != nil {
		return nil, err
	}

	return raw, nil
}

// ValidateWorkflow checks that the workflow has valid ComfyUI structure.
func ValidateWorkflow(workflow map[string]any) error {
	if len(workflow) == 0 {
		return fmt.Errorf("workflow must contain at least one node")
	}
	for id, nodeAny := range workflow {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			return fmt.Errorf("node %q is not an object", id)
		}
		if _, hasClass := node["class_type"]; !hasClass {
			return fmt.Errorf("node %q is missing class_type", id)
		}
	}
	return nil
}

// SubstituteTemplateVariables walks the workflow JSON and replaces {{VAR}}
// template strings with values from params. It operates recursively on the
// entire structure so it works for any ComfyUI workflow.
func SubstituteTemplateVariables(workflow map[string]any, params TemplateParams) map[string]any {
	replaceMap := buildReplaceMap(params)

	// Walk all node inputs recursively.
	for _, nodeAny := range workflow {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}
		inputs, ok := node["inputs"].(map[string]any)
		if !ok {
			continue
		}
		substituteInputs(inputs, replaceMap)
	}

	return workflow
}

// substituteInputs recursively replaces template variables in input values.
func substituteInputs(inputs map[string]any, replaceMap map[string]string) {
	for key, value := range inputs {
		switch v := value.(type) {
		case string:
			inputs[key] = applyReplacements(v, replaceMap)
		case map[string]any:
			substituteInputs(v, replaceMap)
		case []any:
			substituteArray(v, replaceMap)
		}
	}
}

// substituteArray replaces template variables in array string elements.
func substituteArray(arr []any, replaceMap map[string]string) {
	for i, value := range arr {
		switch v := value.(type) {
		case string:
			arr[i] = applyReplacements(v, replaceMap)
		case map[string]any:
			substituteInputs(v, replaceMap)
		case []any:
			substituteArray(v, replaceMap)
		}
	}
}

// buildReplaceMap builds a map of template variable names to their string values.
// Unspecified (zero) numeric params get sensible defaults so {{VAR}} placeholders
// are always substituted even when the caller omits them.
func buildReplaceMap(params TemplateParams) map[string]string {
	m := make(map[string]string)
	if params.Prompt != "" {
		m["PROMPT"] = params.Prompt
	}
	if params.NegativePrompt != "" {
		m["NEGATIVE_PROMPT"] = params.NegativePrompt
	}
	modelName := params.Model
	if modelName == "" {
		modelName = "checkpoint1" // fallback placeholder; ComfyUI will pick the default if available
	}
	m["MODEL"] = modelName

	if params.Image != "" {
		m["IMAGE"] = params.Image
	}

	width := params.Width
	if width <= 0 {
		width = 512
	}
	m["WIDTH"] = strconv.Itoa(width)

	height := params.Height
	if height <= 0 {
		height = 512
	}
	m["HEIGHT"] = strconv.Itoa(height)

	steps := params.Steps
	if steps <= 0 {
		steps = 20
	}
	m["STEPS"] = strconv.Itoa(steps)

	cfgScale := params.CfgScale
	if cfgScale <= 0 {
		cfgScale = 7.5
	}
	m["CFG_SCALE"] = strconv.FormatFloat(cfgScale, 'f', -1, 64)

	// ComfyUI requires seed >= 0; use a large random-ish value instead of -1
	seed := params.Seed
	if seed < 0 {
		seed = 847291053
	}
	m["SEED"] = strconv.FormatInt(seed, 10)
	return m
}

// applyReplacements replaces all {{VAR}} patterns in a string and converts
// the result to a numeric type (float64) when it represents a number, since
// ComfyUI expects numeric inputs for fields like seed, steps, cfg, width, height.
func applyReplacements(s string, replaceMap map[string]string) any {
	if !strings.Contains(s, "{{") {
		return s
	}

	result := s
	for key, value := range replaceMap {
		placeholder := "{{" + key + "}}"
		result = strings.ReplaceAll(result, placeholder, value)
	}

	// Try to convert to float64 so ComfyUI receives a number, not a string.
	if f, err := strconv.ParseFloat(result, 64); err == nil {
		return f
	}
	return result
}

// BuildDefaultWorkflow constructs the minimal CLIPTextEncode → KSampler →
// SaveImage workflow using class-type matching. Node IDs are generated
// dynamically rather than hardcoded.
func BuildDefaultWorkflow(params TemplateParams) map[string]any {
	nodeID := 1
	nextID := func() string {
		id := strconv.Itoa(nodeID)
		nodeID++
		return id
	}

	positiveText := params.Prompt
	if positiveText == "" {
		positiveText = "beautiful scene"
	}
	negativeText := params.NegativePrompt
	if negativeText == "" {
		negativeText = "blurry, low quality"
	}
	modelName := params.Model
	if modelName == "" {
		modelName = "checkpoint1"
	}
	clipName := params.Model // In ComfyUI, CLIP comes from the checkpoint loader.
	if clipName == "" {
		clipName = "checkpoint1"
	}
	vaeName := params.Model
	if vaeName == "" {
		vaeName = "checkpoint1"
	}

	width := params.Width
	if width <= 0 {
		width = 512
	}
	height := params.Height
	if height <= 0 {
		height = 512
	}
	steps := params.Steps
	if steps <= 0 {
		steps = 20
	}
	cfgScale := params.CfgScale
	if cfgScale <= 0 {
		cfgScale = 7.5
	}
	seed := params.Seed
	if seed == 0 {
		seed = -1 // Random seed
	}

	samplerName := "euler"
	schedulerName := "normal"

	workflow := make(map[string]any)

	// CheckpointLoaderSimple
	checkpointID := nextID()
	workflow[checkpointID] = map[string]any{
		"class_type": "CheckpointLoaderSimple",
		"inputs":     map[string]any{"ckpt_name": modelName},
	}

	// CLIPTextEncode (positive)
	positiveID := nextID()
	clipRef := checkpointID + "_0" // CheckpointLoaderSimple outputs model, clip, vae as 0, 1, 2
	workflow[positiveID] = map[string]any{
		"class_type": "CLIPTextEncode",
		"inputs": map[string]any{
			"text": positiveText,
			"clip": clipRef,
		},
	}

	// CLIPTextEncode (negative)
	negativeID := nextID()
	workflow[negativeID] = map[string]any{
		"class_type": "CLIPTextEncode",
		"inputs": map[string]any{
			"text": negativeText,
			"clip": clipRef,
		},
	}

	// EmptyLatentImage
	latentID := nextID()
	workflow[latentID] = map[string]any{
		"class_type": "EmptyLatentImage",
		"inputs": map[string]any{
			"width":  float64(width),
			"height": float64(height),
			"batch_size": 1,
		},
	}

	// KSampler
	samplerID := nextID()
	modelRef := checkpointID + "_0" // model output from checkpoint loader
	vaeRef := checkpointID + "_2"    // vae output from checkpoint loader
	workflow[samplerID] = map[string]any{
		"class_type": "KSampler",
		"inputs": map[string]any{
			"model":               modelRef,
			"positive":            positiveID + "_0",
			"negative":            negativeID + "_0",
			"latent_image":        latentID + "_0",
			"seed":                seed,
			"steps":               steps,
			"cfg":                 cfgScale,
			"sampler_name":        samplerName,
			"scheduler":           schedulerName,
			"denoise":             1.0,
		},
	}

	// VAEDecode
	decodeID := nextID()
	workflow[decodeID] = map[string]any{
		"class_type": "VAEDecode",
		"inputs": map[string]any{
			"samples": samplerID + "_0",
			"vae":     vaeRef,
		},
	}

	// SaveImage
	saveID := nextID()
	workflow[saveID] = map[string]any{
		"class_type": "SaveImage",
		"inputs": map[string]any{
			"images":        decodeID + "_0",
			"filename_prefix": "ComfyUI",
		},
	}

	return workflow
}
