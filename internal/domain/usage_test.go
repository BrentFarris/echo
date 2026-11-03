package domain

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestUsagePresenceAndCoverage(t *testing.T) {
	var counts UsageCounts
	for _, raw := range []string{`{"inputTokens":10,"outputTokens":5}`, `{"outputTokens":0}`, `{"inputTokens":3}`, `{"inputTokens":-1,"outputTokens":-2}`, `{}`} {
		var usage TokenUsage
		if err := json.Unmarshal([]byte(raw), &usage); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(usage)
		var roundtrip TokenUsage
		if err := json.Unmarshal(encoded, &roundtrip); err != nil || usage != roundtrip {
			t.Fatal(roundtrip, err)
		}
		counts.Add(&usage)
	}
	counts.Add(nil)
	if counts != (UsageCounts{InputTokens: 13, OutputTokens: 5, ModelCalls: 6, InputReports: 2, OutputReports: 2}) {
		t.Fatal(counts)
	}
}

func TestUsageCacheCoverageAndRoundTrip(t *testing.T) {
	var counts UsageCounts
	for _, raw := range []string{
		`{"inputTokens":100,"cachedInputTokens":60}`,
		`{"inputTokens":40,"cachedInputTokens":0}`,
		`{"inputTokens":20}`,
		`{"inputTokens":10,"cachedInputTokens":null}`,
		`{"inputTokens":5,"cachedInputTokens":-1}`,
		`{"inputTokens":5,"cachedInputTokens":6}`,
		`{"cachedInputTokens":0}`,
		`{"inputTokens":1,"inputMissing":true,"cachedInputTokens":0}`,
	} {
		var usage, roundtrip TokenUsage
		if err := json.Unmarshal([]byte(raw), &usage); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(usage)
		if err := json.Unmarshal(encoded, &roundtrip); err != nil || !reflect.DeepEqual(usage, roundtrip) {
			t.Fatal(string(encoded), roundtrip, err)
		}
		counts.Add(&roundtrip)
	}
	if counts != (UsageCounts{InputTokens: 180, CachedInputTokens: 60, UncachedInputTokens: 80, CacheReports: 2, InputReports: 6, ModelCalls: 8}) {
		t.Fatalf("unexpected coverage: %+v", counts)
	}
}
