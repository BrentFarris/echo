package tools

import (
	"bytes"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

const (
	DefaultImageCompressionMaxDimension = 1536
	DefaultImageCompressionJPEGQuality  = 75
)

// compressImageForLLM decodes image data, optionally resizes if either
// dimension exceeds maxDimension, and re-encodes as JPEG at jpegQuality.
// GIF images pass through unchanged (animated GIF support).
// Returns the compressed bytes and "image/jpeg" media type.
func compressImageForLLM(data []byte, maxDimension, jpegQuality int) ([]byte, string, error) {
	// Default to sensible values if 0 is passed
	if maxDimension <= 0 {
		maxDimension = DefaultImageCompressionMaxDimension
	}
	if jpegQuality <= 0 || jpegQuality > 100 {
		jpegQuality = DefaultImageCompressionJPEGQuality
	}

	// Try GIF first — pass through unchanged to preserve animation
	if isGIF(data) {
		if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
			return nil, "", err
		}
		return data, "image/gif", nil
	}

	decoded, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, "", err
	}

	bounds := decoded.Bounds()
	if bounds.Dx() > maxDimension || bounds.Dy() > maxDimension {
		decoded = imaging.Fit(decoded, maxDimension, maxDimension, imaging.Lanczos)
	}

	var output bytes.Buffer
	if err := jpeg.Encode(&output, decoded, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, "", err
	}
	return output.Bytes(), "image/jpeg", nil
}

func isGIF(data []byte) bool {
	return len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a")
}
