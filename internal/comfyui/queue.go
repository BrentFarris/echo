package comfyui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// WaitForCompletion connects to ComfyUI's WebSocket endpoint and waits for
// the generation identified by promptID to complete. It returns a result
// containing image paths from the server history.
func (c *Client) WaitForCompletion(ctx context.Context, clientID string, promptID string) (*GenerateResult, error) {
	if clientID == "" {
		clientID = uuid.New().String()
	}

	wsURL := buildWSURL(c.BaseURL, clientID)
	conn, err := connectWS(ctx, wsURL)
	if err != nil {
		return nil, fmt.Errorf("websocket connect: %w", err)
	}
	defer conn.Close()

	completed := make(chan struct{}, 1)
	var lastError error

	go func() {
		defer close(completed)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				lastError = fmt.Errorf("websocket read: %w", err)
				return
			}

			var event wsEvent
			if err := json.Unmarshal(msg, &event); err != nil {
				continue
			}

			switch event.Type {
			case "executing":
				// executing with null payload means the worker is idle = done
				if event.Data.Payload == nil {
					completed <- struct{}{}
					return
				}
			case "execution_error":
				lastError = errors.New("comfyui execution error")
				return
			case "status":
				// Status update — check if queue is empty and we're not executing
				if status, ok := event.Data.Payload.(map[string]interface{}); ok {
					info, _ := status["exec_info"].(map[string]interface{})
					if info != nil {
						executing, _ := info["executing"].(bool)
						if !executing {
							// Check if our prompt is done by polling history
							result, histErr := c.GetHistory(ctx, promptID)
							if histErr == nil && len(result.OutputImages) > 0 {
								return
							}
						}
					}
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-completed:
		if lastError != nil {
			return nil, lastError
		}
		// Fetch final result from history
		result, err := c.GetHistory(ctx, promptID)
		if err != nil {
			return nil, fmt.Errorf("fetch history after completion: %w", err)
		}
		return result, nil
	case <-time.After(5 * time.Minute):
		return nil, errors.New("generation timed out after 5 minutes")
	}
}

// WaitForCompletionPoll is a non-WebSocket alternative that polls the history
// endpoint until the prompt completes or times out.
func (c *Client) WaitForCompletionPoll(ctx context.Context, promptID string) (*GenerateResult, error) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	timeout := 5 * time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	if timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Safety net: limit total polls to avoid infinite loops
	maxPolls := int(timeout.Seconds()) + 1
	pollCount := 0

	for {
		select {
		case <-ctxTimeout.Done():
			return nil, errors.New("generation timed out")
		case <-ticker.C:
			pollCount++
			if pollCount > maxPolls {
				return nil, errors.New("generation timed out: exceeded maximum poll count")
			}

			result, err := c.GetHistory(ctxTimeout, promptID)
			if err != nil {
				// Execution errors are fatal — don't retry.
				if _, isExecErr := err.(*ExecutionError); isExecErr {
					return nil, err
				}
				// "not found in history" means still running — keep polling.
				continue
			}
			// GetHistory returned successfully, meaning the prompt is in history
			// (execution has completed). If there are images, we're done.
			if len(result.OutputImages) > 0 {
				return result, nil
			}
			// Completed but produced no output images.
			// This can happen if the workflow has no image-saving nodes,
			// or execution finished without errors but also without output.
			return nil, errors.New("generation completed but produced no output images")
		}
	}
}

type wsEvent struct {
	Type    string            `json:"type"`
	Data    wsEventData       `json:"data"`
	Origin  string            `json:"origin"`
	Time    float64           `json:"time"`
	Message map[string]any    `json:"message,omitempty"`
}

type wsEventData struct {
	SID     string      `json:"sid,omitempty"`
	PromptID string    `json:"prompt_id,omitempty"`
	Node    string      `json:"node,omitempty"`
	NodeID  string      `json:"node_id,omitempty"`
	Stage   string      `json:"stage,omitempty"`
	Value   interface{} `json:"value,omitempty"`
	Payload interface{} `json:"payload,omitempty"`
}

func buildWSURL(baseURL string, clientID string) string {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return ""
	}
	scheme := u.Scheme
	if scheme == "https" {
		scheme = "wss"
	} else {
		scheme = "ws"
	}
	u.Scheme = scheme
	u.Path = "/ws"
	u.RawQuery = "clientId=" + clientID
	return u.String()
}

var wsDialer = websocket.Dialer{
	HandshakeTimeout: 10 * time.Second,
}

func connectWS(ctx context.Context, wsURL string) (*websocket.Conn, error) {
	if wsURL == "" {
		return nil, errors.New("invalid websocket URL")
	}

	header := http.Header{}
	header.Set("Origin", strings.TrimRight(wsURL, "/ws"))

	conn, _, err := wsDialer.DialContext(ctx, wsURL, header)
	return conn, err
}

// FetchImageBytes retrieves the actual image bytes from ComfyUI's /view endpoint.
func (c *Client) FetchImageBytes(ctx context.Context, filename, subfolder, imgType string) ([]byte, error) {
	viewURL := strings.TrimRight(c.BaseURL, "/") + "/view"
	query := url.Values{}
	query.Set("filename", filename)
	if subfolder != "" {
		query.Set("subfolder", subfolder)
	}
	if imgType != "" {
		query.Set("type", imgType)
	}
	viewURL += "?" + query.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, viewURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	client := c.httpDoer()

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fetch image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ComfyUI /view returned status %d", resp.StatusCode)
	}

	buf := make([]byte, 0, 1<<20) // Pre-allocate 1MB
	var mu sync.Mutex
	writer := &syncWriter{buf: buf}

	// Read in chunks to handle large images
	buf2 := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf2)
		if n > 0 {
			mu.Lock()
			writer.buf = append(writer.buf, buf2[:n]...)
			mu.Unlock()
		}
		if readErr != nil {
			break
		}
	}

	return writer.buf, nil
}

type syncWriter struct {
	buf []byte
}
