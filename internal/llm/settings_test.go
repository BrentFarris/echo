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
