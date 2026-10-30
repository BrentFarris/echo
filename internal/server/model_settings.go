package server

import "github.com/brent/echo/internal/plugins"

func modelSettingsProperties() map[string]any {
	props := map[string]any{
		"id": plugins.S("Existing endpoint id"), "name": plugins.S("Display name"), "url": plugins.S("Base URL"), "model": plugins.S("Model identifier"),
		"provider": plugins.S("Optional installed provider name; empty uses the compatible client"), "cloud": plugins.B("Cloud endpoint"),
		"contextTokens": plugins.N("Context length in tokens"), "compact": plugins.B("Proactive context compaction; context overflow recovery always remains enabled"),
		"compressionThreshold":     plugins.N("Context compression threshold, 1–99 percent; default 70"),
		"reasoningEffort":          plugins.S("Default named reasoning effort: none, minimal, low, medium, high, xhigh or max; empty uses provider default"),
		"requestTimeoutSeconds":    plugins.N("Overall request timeout in seconds; 0 uses the default"),
		"streamIdleTimeoutSeconds": plugins.N("Seconds without arriving stream data; -1 disables, 0 uses the default"),
	}
	for _, name := range []string{"temperature", "topP", "minP", "frequencyPenalty", "presencePenalty", "repetitionPenalty"} {
		props[name] = map[string]any{"type": []string{"number", "null"}, "description": "Endpoint sampling default; null omits this parameter"}
	}
	for _, name := range []string{"topK", "maxTokens", "thinkingTokenBudget"} {
		props[name] = map[string]any{"type": []string{"integer", "null"}, "description": "Endpoint default; null omits this parameter. Named reasoning effort overrides the thinking token budget"}
	}
	return props
}
