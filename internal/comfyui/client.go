package comfyui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// Client communicates with a ComfyUI server.
type Client struct {
	BaseURL    string // e.g., "http://localhost:8188"
	HTTPClient *http.Client
}

const defaultHTTPTimeout = 30 * time.Second

func (c *Client) httpDoer() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: defaultHTTPTimeout}
}

// GenerateResult holds the response from a ComfyUI generation.
type GenerateResult struct {
	PromptID      string   `json:"promptId"`
	OutputImages  []string `json:"outputImages,omitempty"`
	StatusMessage string   `json:"statusMessage,omitempty"`
}

// ExecutionError represents a ComfyUI execution error returned in history.
type ExecutionError struct {
	NodeID        string `json:"node_id"`
	ExceptionType string `json:"exception_type"`
	ErrorMessage  string `json:"exception_message"`
}

func (e *ExecutionError) Error() string {
	return fmt.Sprintf("comfyui execution error on node %s (%s): %s", e.NodeID, e.ExceptionType, e.ErrorMessage)
}

// Generate sends a workflow to ComfyUI and waits for the result.
// If workflow is nil, BuildDefaultWorkflow(params) is used.
func (c *Client) Generate(ctx context.Context, params TemplateParams, workflow map[string]any) (*GenerateResult, error) {
	if workflow == nil {
		workflow = BuildDefaultWorkflow(params)
	}

	// Substitute template variables in the workflow.
	workflow = SubstituteTemplateVariables(workflow, params)

	// Build the prompt payload: ComfyUI expects {"prompt": <workflow>}.
	payload := map[string]any{
		"prompt": workflow,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// POST to /prompt
	url := strings.TrimRight(c.BaseURL, "/") + "/prompt"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := c.httpDoer()

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ComfyUI returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	// Wait for generation to complete by polling history.
	genResult, err := c.WaitForCompletionPoll(ctx, result.PromptID)
	if err != nil {
		return nil, fmt.Errorf("wait for completion: %w", err)
	}
	genResult.StatusMessage = "Generated successfully"
	return genResult, nil
}

// UploadImage uploads an image file to ComfyUI's /upload/image endpoint.
// Returns the filename as stored on the server (used for LoadImage "image" input).
func (c *Client) UploadImage(ctx context.Context, filename string, data []byte) (string, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("image", filepath.Base(filename))
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("write form file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart writer: %w", err)
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/upload/image"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := c.httpDoer()
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ComfyUI upload returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse upload response: %w", err)
	}
	return result.Name, nil
}

// GetHistory fetches the execution history for a prompt ID from ComfyUI.
// Returns an *ExecutionError if the workflow failed during execution.
func (c *Client) GetHistory(ctx context.Context, promptID string) (*GenerateResult, error) {
	url := strings.TrimRight(c.BaseURL, "/") + "/history/" + promptID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	client := c.httpDoer()

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ComfyUI history returned status %d", resp.StatusCode)
	}

	var history map[string]map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		return nil, fmt.Errorf("parse history: %w", err)
	}

	entry, ok := history[promptID]
	if !ok {
		return nil, fmt.Errorf("prompt ID %s not found in history", promptID)
	}

	// Check for node-level errors in outputs first — they contain the real failure details.
	outputs, _ := entry["outputs"].(map[string]any)
	var firstExecError *ExecutionError
	for nodeID, nodeOutputs := range outputs {
		nodeMap, ok := nodeOutputs.(map[string]any)
		if !ok {
			continue
		}
		if excTypeAny, hasExc := nodeMap["error"]; hasExc {
			excType, _ := excTypeAny.(string)
			msgAny, _ := nodeMap["error_message"]
			msg, _ := msgAny.(string)
			firstExecError = &ExecutionError{
				NodeID:        nodeID,
				ExceptionType: excType,
				ErrorMessage:  msg,
			}
			break
		}
	}

	// Check status-level errors.
	if statusAny, exists := entry["status"]; exists {
		statusMap, _ := statusAny.(map[string]any)
		if statusMap != nil {
			// ComfyUI puts execution errors in status.messages as [event_type, data] tuples.
			if msgsAny, hasMsgs := statusMap["messages"]; hasMsgs {
				msgList, _ := msgsAny.([]any)
				for _, msgItem := range msgList {
					arr, ok := msgItem.([]any)
					if !ok || len(arr) < 2 {
						continue
					}
					eventType, _ := arr[0].(string)
					if eventType != "execution_error" {
						continue
					}
					dataMap, ok := arr[1].(map[string]any)
					if !ok {
						continue
					}
					nodeID, _ := dataMap["node_id"].(string)
					excType, _ := dataMap["exception_type"].(string)
					msg, _ := dataMap["exception_message"].(string)
					return nil, &ExecutionError{
						NodeID:        nodeID,
						ExceptionType: excType,
						ErrorMessage:  msg,
					}
				}
			}

			if errAny, exists := statusMap["error"]; exists && errAny != nil {
				switch v := errAny.(type) {
				case string:
					if v != "" {
						return nil, &ExecutionError{
							NodeID:        "status",
							ExceptionType: v,
							ErrorMessage:  fmt.Sprintf("execution error: %s", v),
						}
					}
				case map[string]any:
					msg := ""
					if m, ok := v["message"].(string); ok {
						msg = m
					}
					if msg == "" {
						if tb, ok := v["traceback"].([]any); ok && len(tb) > 0 {
							if s, ok := tb[0].(string); ok {
								msg = s
							}
						}
					}
					return nil, &ExecutionError{
						NodeID:        "status",
						ExceptionType: "execution_error",
						ErrorMessage:  msg,
					}
				}
			}

			if statusStr, ok := statusMap["status_str"].(string); ok && statusStr == "error" {
				if firstExecError != nil {
					return nil, firstExecError
				}
				return nil, &ExecutionError{
					NodeID:        "status",
					ExceptionType: "execution_error",
					ErrorMessage:  "execution status is error (no details available)",
				}
			}
		}
	}

	// Fall back to node-level errors if status didn't report one.
	if firstExecError != nil {
		return nil, firstExecError
	}

	// No errors — collect output images.
	var images []string
	for _, nodeOutputs := range outputs {
		nodeMap, ok := nodeOutputs.(map[string]any)
		if !ok {
			continue
		}
		imgList, ok := nodeMap["images"].([]any)
		if !ok {
			continue
		}
		for _, imgAny := range imgList {
			imgMap, ok := imgAny.(map[string]any)
			if !ok {
				continue
			}
			filename, _ := imgMap["filename"].(string)
			subfolder, _ := imgMap["subfolder"].(string)
			typ, _ := imgMap["type"].(string)
			if typ == "output" && filename != "" {
				path := filename
				if subfolder != "" {
					path = subfolder + "/" + path
				}
				images = append(images, path)
			}
		}
	}

	return &GenerateResult{
		PromptID:     promptID,
		OutputImages: images,
	}, nil
}
