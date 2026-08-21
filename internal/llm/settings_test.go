package llm

import "testing"

func TestStreamIdleTimeoutNormalization(t *testing.T) {
	settings := DefaultSettings()
	if settings.StreamIdleTimeoutSeconds != DefaultStreamIdleTimeoutSeconds {
		t.Fatalf("expected default stream idle timeout, got %d", settings.StreamIdleTimeoutSeconds)
	}

	settings.StreamIdleTimeoutSeconds = 0
	for index := range settings.Endpoints {
		settings.Endpoints[index].StreamIdleTimeoutSeconds = 0
	}
	normalized := settings.NormalizedEndpointProfiles()
	if normalized.StreamIdleTimeoutSeconds != DefaultStreamIdleTimeoutSeconds || normalized.Endpoints[0].StreamIdleTimeoutSeconds != DefaultStreamIdleTimeoutSeconds {
		t.Fatalf("legacy settings did not receive the idle timeout default: %#v", normalized)
	}

	normalized.Endpoints[0].StreamIdleTimeoutSeconds = -1
	normalized = normalized.NormalizedEndpointProfiles()
	if normalized.StreamIdleTimeoutSeconds != -1 {
		t.Fatalf("expected disabled idle timeout to be preserved, got %d", normalized.StreamIdleTimeoutSeconds)
	}
}

func TestStreamIdleTimeoutValidation(t *testing.T) {
	settings := DefaultSettings()
	settings.StreamIdleTimeoutSeconds = -2
	settings.Endpoints[0].StreamIdleTimeoutSeconds = -2
	if err := settings.Validate(); err == nil {
		t.Fatal("expected invalid stream idle timeout to fail validation")
	}
}

func TestContextCompressionDefaultsAndExplicitDisable(t *testing.T) {
	legacy := DefaultSettings()
	legacy.ContextCompressionEnabled = nil
	legacy.ContextCompressionThresholdPercent = 0
	legacy.Endpoints[0].ContextCompressionEnabled = nil
	legacy.Endpoints[0].ContextCompressionThresholdPercent = 0

	normalized := legacy.NormalizedEndpointProfiles()
	if !normalized.CompressionEnabled() || normalized.ContextCompressionThresholdPercent != DefaultCompressionThreshold {
		t.Fatalf("legacy settings did not receive compression defaults: %#v", normalized)
	}
	if normalized.Endpoints[0].ContextCompressionEnabled == nil || !*normalized.Endpoints[0].ContextCompressionEnabled || normalized.Endpoints[0].ContextCompressionThresholdPercent != DefaultCompressionThreshold {
		t.Fatalf("legacy endpoint did not receive compression defaults: %#v", normalized.Endpoints[0])
	}

	disabled := false
	normalized.Endpoints[0].ContextCompressionEnabled = &disabled
	normalized = normalized.NormalizedEndpointProfiles()
	if normalized.CompressionEnabled() || normalized.Endpoints[0].ContextCompressionEnabled == nil || *normalized.Endpoints[0].ContextCompressionEnabled {
		t.Fatalf("explicit compression disablement was not preserved: %#v", normalized)
	}
	cloned := normalized.Clone()
	*cloned.Endpoints[0].ContextCompressionEnabled = true
	if *normalized.Endpoints[0].ContextCompressionEnabled {
		t.Fatal("cloned endpoint compression setting aliases the original")
	}
}

func TestContextCompressionRoutingAndValidation(t *testing.T) {
	settings := DefaultSettings()
	research := settings.Endpoints[0]
	research.ID = "research"
	research.Name = "Research"
	research.Model = "research-model"
	research.ContextCompressionThresholdPercent = 55
	disabled := false
	research.ContextCompressionEnabled = &disabled
	settings.Endpoints = append(settings.Endpoints, research)
	settings.EndpointSelection.Research = research.ID

	routed := settings.ForInteraction(InteractionResearch)
	if routed.Model != "research-model" || routed.CompressionEnabled() || routed.ContextCompressionThresholdPercent != 55 {
		t.Fatalf("research routing lost compression settings: %#v", routed)
	}

	settings.Endpoints[0].ContextCompressionThresholdPercent = -1
	if err := settings.NormalizedEndpointProfiles().Validate(); err == nil {
		t.Fatal("expected a compression threshold below 1 percent to fail validation")
	}
	settings.Endpoints[0].ContextCompressionThresholdPercent = MaxCompressionThreshold + 1
	if err := settings.NormalizedEndpointProfiles().Validate(); err == nil {
		t.Fatal("expected a compression threshold above 99 percent to fail validation")
	}
}
