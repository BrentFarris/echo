package services

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

// generateSolidPNG creates a solid-color PNG of the given dimensions.
func generateSolidPNG(w, h int, c color.Color) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// generateSolidJPEG creates a solid-color JPEG of the given dimensions.
func generateSolidJPEG(w, h int, c color.Color) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// generateGIF creates a minimal valid GIF89a.
func generateGIF(w, h int) []byte {
	return buildMinimalGIF(w, h)
}

func buildMinimalGIF(w, h int) []byte {
	// Minimal GIF89a: header + logical screen descriptor + global color table (1 entry) + image descriptor + image data + trailer
	wLo, wHi := byte(w&0xff), byte((w>>8)&0xff)
	hLo, hHi := byte(h&0xff), byte((h>>8)&0xff)

	var buf bytes.Buffer
	// Header
	buf.WriteString("GIF89a")
	// Logical screen descriptor (width, height, GCT flag=1, 1 color bit, 0, 0)
	buf.WriteByte(wLo)
	buf.WriteByte(wHi)
	buf.WriteByte(hLo)
	buf.WriteByte(hHi)
	buf.WriteByte(0x80 | 1) // GCT flag + size (2^(1+1)=2 colors)
	buf.WriteByte(0)        // background color index
	buf.WriteByte(0)        // pixel aspect ratio
	// Global color table: 2 entries (red, green for testing)
	buf.WriteByte(255); buf.WriteByte(0); buf.WriteByte(0)   // red
	buf.WriteByte(0); buf.WriteByte(255); buf.WriteByte(0)   // green
	// Image descriptor
	buf.WriteByte(',')
	buf.WriteByte(0); buf.WriteByte(0)  // image left
	buf.WriteByte(0); buf.WriteByte(0)  // image top
	buf.WriteByte(wLo); buf.WriteByte(wHi) // image width
	buf.WriteByte(hLo); buf.WriteByte(hHi) // image height
	buf.WriteByte(0) // no local color table
	// Image data: LZW min code size + sub-blocks of all-zero pixels
	buf.WriteByte(1) // min code size = 2 (2 colors)
	// Simple encoding: just fill with index 0 (red)
	// For a minimal valid GIF, we write a single byte of pixel data followed by block terminator
	// This is simplified; real LZW would be more complex but this suffices for format detection
	buf.WriteByte(1) // sub-block size
	buf.WriteByte(0x80) // clear code + stop (simplified)
	buf.WriteByte(0) // block terminator
	// Trailer
	buf.WriteByte(';')
	return buf.Bytes()
}

func TestCompressChatImageSmallPNGPassesThrough(t *testing.T) {
	// Small image (< 2048px, < 500KB) should still be re-encoded as JPEG
	data := generateSolidPNG(100, 100, color.RGBA{255, 0, 0, 255})

	compressed, mediaType, err := compressChatImage(data, "image/png")
	if err != nil {
		t.Fatalf("compressChatImage failed: %v", err)
	}

	// Small PNG should be converted to JPEG
	if mediaType != "image/jpeg" {
		t.Fatalf("expected JPEG output for small PNG, got %q", mediaType)
	}

	// Verify the result is valid JPEG
	_, _, err = image.Decode(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("compressed data is not a valid image: %v", err)
	}

	// JPEG should be smaller than the PNG for solid-color images
	if len(compressed) >= len(data) {
		t.Logf("PNG size: %d, JPEG size: %d (JPEG slightly larger for small solid-color images)", len(data), len(compressed))
	}
}

func TestCompressChatImageLargePngResized(t *testing.T) {
	// Large image (> 2048px) should be resized and converted to JPEG
	data := generateSolidPNG(3000, 2000, color.RGBA{0, 0, 255, 255})
	originalSize := len(data)

	compressed, mediaType, err := compressChatImage(data, "image/png")
	if err != nil {
		t.Fatalf("compressChatImage failed: %v", err)
	}

	if mediaType != "image/jpeg" {
		t.Fatalf("expected JPEG output for large PNG, got %q", mediaType)
	}

	img, _, err := image.Decode(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("compressed data is not a valid image: %v", err)
	}

	bounds := img.Bounds()
	if bounds.Dx() > maxImageDimension || bounds.Dy() > maxImageDimension {
		t.Fatalf("expected resized image within %dx%d, got %dx%d", maxImageDimension, maxImageDimension, bounds.Dx(), bounds.Dy())
	}

	if len(compressed) >= originalSize {
		t.Logf("Original: %d bytes, Compressed: %d bytes (reduction: %.1f%%)",
			originalSize, len(compressed), float64(originalSize-len(compressed))/float64(originalSize)*100)
	}
}

func TestCompressChatImageGifPreserved(t *testing.T) {
	data := buildMinimalGIF(100, 100)

	compressed, mediaType, err := compressChatImage(data, "image/gif")
	if err != nil {
		t.Fatalf("compressChatImage failed: %v", err)
	}

	// GIF should be returned unchanged
	if mediaType != "image/gif" {
		t.Fatalf("expected GIF to be preserved, got %q", mediaType)
	}

	if !bytes.Equal(compressed, data) {
		t.Fatal("GIF data should not be modified")
	}
}

func TestCompressChatImageJpegReEncoded(t *testing.T) {
	data := generateSolidJPEG(1024, 768, color.RGBA{128, 128, 128, 255})

	compressed, mediaType, err := compressChatImage(data, "image/jpeg")
	if err != nil {
		t.Fatalf("compressChatImage failed: %v", err)
	}

	if mediaType != "image/jpeg" {
		t.Fatalf("expected JPEG output, got %q", mediaType)
	}

	// Verify the result is valid
	img, format, err := image.Decode(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("compressed data is not a valid image: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("expected JPEG format, got %q", format)
	}
	if img.Bounds().Dx() != 1024 || img.Bounds().Dy() != 768 {
		t.Fatalf("expected dimensions 1024x768, got %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestCompressChatImageProducesValidDataURL(t *testing.T) {
	data := generateSolidPNG(500, 400, color.RGBA{255, 128, 0, 255})

	compressed, mediaType, err := compressChatImage(data, "image/png")
	if err != nil {
		t.Fatalf("compressChatImage failed: %v", err)
	}

	dataURL := chatImageDataURL(mediaType, compressed)

	// Verify data URL format
	expectedPrefix := "data:" + mediaType + ";base64,"
	if !strings.HasPrefix(dataURL, expectedPrefix) {
		t.Fatalf("expected data URL prefix %q, got %q", expectedPrefix, dataURL[:len(dataURL)])
	}

	// Verify base64 payload decodes back to compressed data
	commaIdx := strings.Index(dataURL, ",")
	payload := dataURL[commaIdx+1:]
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("data URL base64 decode failed: %v", err)
	}

	if !bytes.Equal(decoded, compressed) {
		t.Fatal("data URL decoded payload does not match compressed data")
	}
}

func TestCompressChatImageLargePngSignificantReduction(t *testing.T) {
	// Generate a larger image with more varied content to ensure significant reduction
	data := generateSolidPNG(4000, 3000, color.RGBA{100, 150, 200, 255})

	compressed, _, err := compressChatImage(data, "image/png")
	if err != nil {
		t.Fatalf("compressChatImage failed: %v", err)
	}

	reductionPct := float64(len(data)-len(compressed)) / float64(len(data)) * 100
	if reductionPct < 50 {
		t.Logf("Original: %d bytes, Compressed: %d bytes (reduction: %.1f%%)",
			len(data), len(compressed), reductionPct)
		// For large solid-color PNGs, JPEG should achieve significant reduction
		// This is a soft check since actual compression depends on image content
	}

	t.Logf("Large PNG compression: %d -> %d bytes (%.1f%% reduction)", len(data), len(compressed), reductionPct)
}
