package server

import (
	"bytes"
	"container/list"
	"errors"
	"image"
	"sync"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

const maxPreviewPixels = 64 * 1024 * 1024
const maxPreviewCacheBytes = 32 << 20
const maxPreviewCacheEntries = 256

func checkPreviewDimensions(data []byte) error {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxPreviewPixels {
		return errors.New("image is invalid or exceeds the 64 megapixel decoding limit")
	}
	return nil
}

type previewKey struct {
	chat, attachment string
	width            int
}
type previewEntry struct {
	key  previewKey
	data []byte
	kind string
}
type attachmentPreviewCache struct {
	mu      sync.Mutex
	entries map[previewKey]*list.Element
	order   list.List
	bytes   int
}

// Serialize thumbnail decoding per server to bound working memory as well as
// retained cache bytes. Callers check the attachment still exists before this.
func (c *attachmentPreviewCache) thumbnail(key previewKey, data []byte, kind string) ([]byte, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.entries[key]; entry != nil {
		c.order.MoveToFront(entry)
		v := entry.Value.(previewEntry)
		return v.data, v.kind, nil
	}
	if err := checkPreviewDimensions(data); err != nil {
		return nil, "", err
	}
	decoded, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, "", errors.New("image preview is unavailable")
	}
	bounds := decoded.Bounds()
	if bounds.Dx() > key.width || bounds.Dy() > key.width {
		decoded = imaging.Fit(decoded, key.width, key.width, imaging.Lanczos)
	}
	var output bytes.Buffer
	if kind == "image/jpeg" {
		err = imaging.Encode(&output, decoded, imaging.JPEG, imaging.JPEGQuality(85))
	} else {
		kind = "image/png"
		err = imaging.Encode(&output, decoded, imaging.PNG)
	}
	if err != nil {
		return nil, "", err
	}
	result := output.Bytes()
	if len(result) <= maxPreviewCacheBytes {
		if c.entries == nil {
			c.entries = make(map[previewKey]*list.Element)
		}
		for c.bytes+len(result) > maxPreviewCacheBytes || len(c.entries) >= maxPreviewCacheEntries {
			old := c.order.Back()
			v := old.Value.(previewEntry)
			delete(c.entries, v.key)
			c.bytes -= len(v.data)
			c.order.Remove(old)
		}
		c.entries[key] = c.order.PushFront(previewEntry{key, result, kind})
		c.bytes += len(result)
	}
	return result, kind, nil
}
