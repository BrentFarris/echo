package plugins

import "github.com/brent/echo/internal/domain"

func comfyUIPlugin() domain.PluginManifest {
	props := func(video bool) map[string]any {
		p := map[string]any{
			"prompt": S("Positive prompt"), "negativePrompt": S("Negative prompt"),
			"workflowJSON": S("Inline ComfyUI API workflow JSON; overrides workflowPath and configured defaults. Supports {{PROMPT}}, {{NEGATIVE_PROMPT}}, {{MODEL}}, {{IMAGE}}, {{WIDTH}}, {{HEIGHT}}, {{STEPS}}, {{CFG_SCALE}}, {{SEED}}."),
			"workflowPath": S("Workflow JSON path within this bot's folders; overrides configured defaults"),
			"model":        S("Checkpoint name; defaults to plugin setting"),
			"width":        N("Width, default 512"), "height": N("Height, default 512"), "steps": N("Steps, default 20"),
			"cfgScale":           map[string]any{"type": "number", "description": "CFG scale, default 7.5"},
			"seed":               N("Seed; -1 for random"),
			"imagePath":          S("Input image path within this bot's folders; takes precedence over attachedImageIndex"),
			"attachedImageIndex": N("Zero-based image index in the most recent image-bearing user message addressed to this bot; uploads directly from chat"),
		}
		if video {
			p["frames"] = N("{{FRAMES}}, default 16")
			p["fps"] = map[string]any{"type": "number", "description": "{{FPS}}, default 8"}
			p["format"] = S("{{FORMAT}}: mp4 or gif, default mp4")
			p["duration"] = map[string]any{"type": "number", "description": "{{DURATION}} in seconds, default 5"}
			p["aspectRatio"] = S("{{ASPECT_RATIO}}, default 16:9 (Widescreen)")
			p["megapixels"] = map[string]any{"type": "number", "description": "{{MEGAPIXELS}}, default 0.4"}
		}
		return p
	}
	return domain.PluginManifest{ID: "comfyui", Name: "ComfyUI", Tools: []domain.Tool{
		tool("comfyui_generate", "Generate an image with ComfyUI. Uses the configured txt2img or img2img workflow, or a built-in checkpoint workflow. Returns an inline image and imageId for save_image.", "comfyui.generate", props(false), "prompt"),
		tool("comfyui_generate_video", "Generate a video or animated GIF with ComfyUI. Requires a configured video workflow or an explicit workflow. Returns inline media and videoId for save_video.", "comfyui.generate_video", props(true), "prompt"),
		tool("save_image", "Save a generated image to this bot's workspace.", "comfyui.save_image", map[string]any{"imageId": S("Generated image ID"), "path": S("Destination path"), "overwrite": B("Replace an existing file, default false")}, "imageId", "path"),
		tool("save_video", "Save a generated video or GIF to this bot's workspace when the user asks to save it.", "comfyui.save_video", map[string]any{"videoId": S("Generated video ID"), "path": S("Destination path"), "overwrite": B("Replace an existing file, default false")}, "videoId", "path"),
	}, Settings: Schema(map[string]any{
		"url":               S("ComfyUI server URL, e.g. http://127.0.0.1:8188 (set in owner settings)"),
		"defaultCheckpoint": S("Default checkpoint/model filename"),
		"txt2imgWorkflow":   S("Default text-to-image API workflow JSON path, within the bot's folders"),
		"img2imgWorkflow":   S("Default image-to-image API workflow JSON path, within the bot's folders"),
		"videoWorkflow":     S("Default text/image-to-video API workflow JSON path, within the bot's folders"),
	})}
}
