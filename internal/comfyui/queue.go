package comfyui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxMediaBytes = 256 << 20

var ErrPending = errors.New("prompt not found in history")

// Polling works with ComfyUI installations that do not proxy WebSockets.
func (c *Client) WaitForCompletionPoll(ctx context.Context, promptID string) (*GenerateResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		result, err := c.GetHistory(ctx, promptID)
		if err == nil {
			if len(result.OutputImages)+len(result.OutputVideos) == 0 {
				return nil, errors.New("generation completed but produced no output images or videos")
			}
			return result, nil
		}
		if !errors.Is(err, ErrPending) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("generation cancelled or timed out: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func (c *Client) FetchImageBytes(ctx context.Context, filename, subfolder, storageType string) ([]byte, error) {
	q := url.Values{"filename": {filename}, "subfolder": {subfolder}, "type": {storageType}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/view?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpDoer().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ComfyUI /view returned status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxMediaBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxMediaBytes {
		return nil, errors.New("generated media exceeds 256 MiB")
	}
	if len(data) == 0 {
		return nil, errors.New("generated media is empty")
	}
	return data, nil
}

func (c *Client) FetchVideoBytes(ctx context.Context, filename, subfolder, storageType string) ([]byte, error) {
	return c.FetchImageBytes(ctx, filename, subfolder, storageType)
}
