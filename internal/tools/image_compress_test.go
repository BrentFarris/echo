package tools

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// generateJPEG creates a solid-color JPEG of the given dimensions.
func generateJPEG(w, h, quality int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func TestCompressImageForLLMResizesLargeImage(t *testing.T) {
	large := generateJPEG(4096, 4096, 95)
	compressed, mediaType, err := compressImageForLLM(large, DefaultImageCompressionMaxDimension, DefaultImageCompressionJPEGQuality)
	if err != nil {
		t.Fatalf("compress failed: %v", err)
	}
	if mediaType != "image/jpeg" {
		t.Fatalf("expected jpeg, got %s", mediaType)
	}

	// Verify dimensions were reduced
	decoded, err := jpeg.Decode(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("decode compressed: %v", err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() > DefaultImageCompressionMaxDimension || bounds.Dy() > DefaultImageCompressionMaxDimension {
		t.Fatalf("image not resized: got %dx%d, max %d", bounds.Dx(), bounds.Dy(), DefaultImageCompressionMaxDimension)
	}

	// Verify significant size reduction (large 4096px at q95 vs compressed at q75)
	if len(compressed) >= len(large) {
		t.Fatalf("expected compression to reduce size: raw=%d, compressed=%d", len(large), len(compressed))
	}
}

func TestCompressImageForLLMPassesSmallImageThrough(t *testing.T) {
	small := generateJPEG(100, 100, 95)
	compressed, _, err := compressImageForLLM(small, DefaultImageCompressionMaxDimension, DefaultImageCompressionJPEGQuality)
	if err != nil {
		t.Fatalf("compress failed: %v", err)
	}

	// Small images should still re-encode (quality change) but not resize
	decoded, err := jpeg.Decode(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("decode compressed: %v", err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != 100 || bounds.Dy() != 100 {
		t.Fatalf("small image resized unexpectedly: got %dx%d", bounds.Dx(), bounds.Dy())
	}
}

func TestCompressImageForLLMUsesDefaultsWhenZero(t *testing.T) {
	data := generateJPEG(2048, 2048, 95)
	compressed, _, err := compressImageForLLM(data, 0, 0)
	if err != nil {
		t.Fatalf("compress failed: %v", err)
	}

	decoded, err := jpeg.Decode(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("decode compressed: %v", err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() > DefaultImageCompressionMaxDimension || bounds.Dy() > DefaultImageCompressionMaxDimension {
		t.Fatalf("default resize not applied: got %dx%d, max %d", bounds.Dx(), bounds.Dy(), DefaultImageCompressionMaxDimension)
	}
}

func TestCompressImageForLLMPreservesGIF(t *testing.T) {
	gifData := validGIFBytes()
	compressed, mediaType, err := compressImageForLLM(gifData, 1024, 85)
	if err != nil {
		t.Fatalf("compress failed: %v", err)
	}
	if mediaType != "image/gif" {
		t.Fatalf("expected GIF passthrough, got %s", mediaType)
	}
	if !bytes.Equal(compressed, gifData) {
		t.Fatal("GIF should pass through unchanged")
	}
}

func TestCompressImageForLLMRejectsInvalid(t *testing.T) {
	_, _, err := compressImageForLLM([]byte("not an image"), 1024, 85)
	if err == nil {
		t.Fatal("expected error for invalid data")
	}
}
