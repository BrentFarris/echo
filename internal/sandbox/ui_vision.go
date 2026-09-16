package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/google/uuid"
)

func decodeUIImage(shot *UIImage) (image.Image, error) {
	if shot == nil || len(shot.DataBase64) > 8<<20 {
		return nil, uiFailure("ui_image_invalid", "Screenshot is missing or too large")
	}
	data, err := base64.StdEncoding.DecodeString(shot.DataBase64)
	if err != nil {
		return nil, uiFailure("ui_image_invalid", "Screenshot encoding is invalid")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 16_000_000 {
		return nil, uiFailure("ui_image_invalid", "Screenshot dimensions are invalid or too large")
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	return decoded, err
}

func prepareUIImage(shot *UIImage) error {
	decoded, err := decodeUIImage(shot)
	if err != nil {
		return err
	}
	width, height := decoded.Bounds().Dx(), decoded.Bounds().Dy()
	if shot.Width != 0 && shot.Width != width || shot.Height != 0 && shot.Height != height {
		return uiFailure("ui_image_invalid", "Screenshot metadata does not match source pixels")
	}
	shot.Width, shot.Height = width, height
	if shot.ScaleX <= 0 || shot.ScaleY <= 0 {
		return uiFailure("ui_image_invalid", "Screenshot coordinate transform is invalid")
	}
	for _, value := range []float64{shot.OriginX, shot.OriginY, shot.ScaleX, shot.ScaleY} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return uiFailure("ui_image_invalid", "Screenshot coordinate transform is not finite")
		}
	}
	shot.RenderedWidth, shot.RenderedHeight = transportDimensions(width, height)
	return nil
}

func transportDimensions(width, height int) (int, int) {
	scale := math.Min(1, math.Min(1280/float64(width), 800/float64(height)))
	return max(1, int(math.Round(float64(width)*scale))), max(1, int(math.Round(float64(height)*scale)))
}

func validBounds(box UIBounds, width, height float64) bool {
	for _, value := range []float64{box.X, box.Y, box.Width, box.Height} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return box.X >= 0 && box.Y >= 0 && box.Width >= 1 && box.Height >= 1 && box.X+box.Width <= width && box.Y+box.Height <= height
}

type visionTransform struct {
	region        UIBounds
	width, height int
}

func specialistImage(shot *UIImage, region *UIBounds) (string, visionTransform, error) {
	decoded, err := decodeUIImage(shot)
	if err != nil {
		return "", visionTransform{}, err
	}
	box := UIBounds{Width: float64(shot.Width), Height: float64(shot.Height)}
	if region != nil {
		if !validBounds(*region, box.Width, box.Height) {
			return "", visionTransform{}, uiFailure("invalid_arguments", "Zoom region is outside source screenshot pixels")
		}
		// Integral crop boundaries retain an exact transform back to source pixels.
		rect := image.Rect(int(math.Floor(region.X)), int(math.Floor(region.Y)), int(math.Ceil(region.X+region.Width)), int(math.Ceil(region.Y+region.Height)))
		decoded = imaging.Crop(decoded, rect)
		box = UIBounds{X: float64(rect.Min.X), Y: float64(rect.Min.Y), Width: float64(rect.Dx()), Height: float64(rect.Dy())}
	}
	width, height := transportDimensions(decoded.Bounds().Dx(), decoded.Bounds().Dy())
	if width != decoded.Bounds().Dx() || height != decoded.Bounds().Dy() {
		decoded = imaging.Resize(decoded, width, height, imaging.Lanczos)
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, decoded); err != nil {
		return "", visionTransform{}, err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buffer.Bytes()), visionTransform{box, width, height}, nil
}

type visionAnswer struct {
	Status   string    `json:"status"`
	Box      *UIBounds `json:"box,omitempty"`
	Verdict  string    `json:"verdict,omitempty"`
	Evidence string    `json:"evidence,omitempty"`
}

func parseVisionAnswer(text string, transform visionTransform, verify bool) (visionAnswer, error) {
	var answer visionAnswer
	if len(text) > 8192 {
		return answer, uiFailure("ui_vision_invalid", "Specialist response exceeded its size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(text)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&answer); err != nil {
		return answer, uiFailure("ui_vision_invalid", "Specialist must return only the requested JSON object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return answer, uiFailure("ui_vision_invalid", "Specialist response contains extra content")
	}
	if verify {
		if answer.Box != nil || answer.Status != "" || (answer.Verdict != "pass" && answer.Verdict != "fail" && answer.Verdict != "unknown") {
			return answer, uiFailure("ui_vision_invalid", "Verification requires verdict pass, fail, or unknown")
		}
	} else {
		if answer.Verdict != "" || (answer.Status != "found" && answer.Status != "absent" && answer.Status != "ambiguous") {
			return answer, uiFailure("ui_vision_invalid", "Location requires found, absent, or ambiguous")
		}
		if answer.Status == "found" {
			if answer.Box == nil || !validBounds(*answer.Box, float64(transform.width), float64(transform.height)) {
				return answer, uiFailure("ui_vision_invalid", "Target rectangle is outside the supplied image")
			}
		} else if answer.Box != nil {
			return answer, uiFailure("ui_vision_invalid", "Absent or ambiguous targets must not supply coordinates")
		}
	}
	return answer, nil
}

func (m *Manager) visualUI(ctx context.Context, state MachineState, workspace, turn string, generation uint64, method string, request UIRequest, record uiRecord, vision UIVision) (*UIResult, error) {
	if vision == nil {
		return nil, uiFailure("ui_vision_unavailable", "Configure an image-capable Vision endpoint to use visual assistance; semantic UI tools remain available")
	}
	if strings.TrimSpace(request.Target) == "" {
		return nil, uiFailure("invalid_arguments", "Describe the visual target or condition explicitly")
	}
	observation := record.observation
	if method == "ui_verify" {
		var err error
		observation, err = m.observeUI(ctx, state, workspace, turn, generation, UIRequest{Surface: request.Surface, SurfaceID: request.SurfaceID, Screenshot: true})
		if err != nil {
			return nil, err
		}
	}
	dataURL, transform, err := specialistImage(observation.Screenshot, request.Region)
	if err != nil {
		return nil, err
	}
	visionRequest := UIVisionRequest{ImageDataURL: dataURL, Width: transform.width, Height: transform.height, Target: request.Target, Verify: method == "ui_verify"}
	usage := &UISpecialistUsage{}
	var answer visionAnswer
	for attempt := 0; attempt < 2; attempt++ {
		if err := m.checkAIControlContext(ctx, workspace); err != nil {
			return nil, err
		}
		text, used, callErr := vision(ctx, visionRequest)
		usage.Calls++
		usage.PromptTokens += used.PromptTokens
		usage.CompletionTokens += used.CompletionTokens
		usage.Model = used.Model
		if callErr != nil {
			return &UIResult{Observation: observation, Backend: "vision", Specialist: usage, Error: &Error{Code: "ui_vision_unavailable", Message: "Vision endpoint could not inspect this image; semantic tools remain available"}}, nil
		}
		answer, err = parseVisionAnswer(text, transform, visionRequest.Verify)
		if err == nil {
			break
		}
		visionRequest.Correction = err.Error()
	}
	if err != nil {
		return &UIResult{Observation: observation, Backend: "vision", Specialist: usage, Error: &Error{Code: "ui_vision_invalid", Message: err.Error()}}, nil
	}
	if err := m.checkAIControlContext(ctx, workspace); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	result := &UIResult{Observation: observation, Backend: "vision", Specialist: usage}
	if visionRequest.Verify {
		status := map[string]string{"pass": "passed", "fail": "failed", "unknown": "unknown"}[answer.Verdict]
		result.Verification = &UIVerification{Status: status, Method: "model_assessed", Evidence: answer.Evidence}
		return result, nil
	}
	if answer.Status != "found" {
		result.Error = &Error{Code: "ui_target_" + answer.Status, Message: "Visual target " + answer.Status + "; refine the description or inspect a smaller region"}
		return result, nil
	}
	box := answer.Box
	source := UIBounds{X: transform.region.X + box.X*transform.region.Width/float64(transform.width), Y: transform.region.Y + box.Y*transform.region.Height/float64(transform.height), Width: box.Width * transform.region.Width / float64(transform.width), Height: box.Height * transform.region.Height / float64(transform.height)}
	target := UITarget{Ref: "visual-" + uuid.NewString(), Role: "visual", Name: request.Target, Bounds: &source, Grounding: "visual", Actions: []string{"click", "hover", "type", "press", "scroll", "drag"}}
	copy := *observation
	copy.Targets = append(append([]UITarget(nil), observation.Targets...), target)
	m.saveObservation(workspace, turn, generation, &copy)
	result.Observation, result.Target = &copy, &target
	return result, nil
}

func sourcePoint(shot *UIImage, box UIBounds) (float64, float64) {
	return shot.OriginX + (box.X+box.Width/2)*shot.ScaleX, shot.OriginY + (box.Y+box.Height/2)*shot.ScaleY
}

func targetRegionUnchanged(before, after *UIImage, box UIBounds) bool {
	if before == nil || after == nil || before.Width != after.Width || before.Height != after.Height || before.Space != after.Space || before.ScaleX != after.ScaleX || before.ScaleY != after.ScaleY || before.OriginX != after.OriginX || before.OriginY != after.OriginY {
		return false
	}
	if !validBounds(box, float64(before.Width), float64(before.Height)) {
		return false
	}
	a, err := decodeUIImage(before)
	if err != nil {
		return false
	}
	b, err := decodeUIImage(after)
	if err != nil {
		return false
	}
	// Compare the target plus an 8px context margin, not unrelated clocks/spinners.
	x0, y0 := max(0, int(box.X)-8), max(0, int(box.Y)-8)
	x1, y1 := min(before.Width, int(math.Ceil(box.X+box.Width))+8), min(before.Height, int(math.Ceil(box.Y+box.Height))+8)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, bb, ba := b.At(x, y).RGBA()
			if ar != br || ag != bg || ab != bb || aa != ba {
				return false
			}
		}
	}
	return true
}

func (m *Manager) validateVisualTarget(ctx context.Context, state MachineState, workspace, turn string, generation uint64, record uiRecord, target UITarget) error {
	observation := record.observation
	fresh, err := m.observeUI(ctx, state, workspace, turn, generation, UIRequest{Surface: observation.Surface.Kind, SurfaceID: observation.Surface.ID, Screenshot: true, Limit: 1})
	if err != nil {
		return err
	}
	if fresh.Surface != observation.Surface || target.Bounds == nil || !targetRegionUnchanged(observation.Screenshot, fresh.Screenshot, *target.Bounds) {
		return uiFailure("ui_visual_target_stale", "Target region or surface geometry changed. Observe and locate again before clicking.")
	}
	return nil
}
