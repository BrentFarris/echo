package domain

import (
	"encoding/json"
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
