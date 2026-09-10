package ctest

import "testing"

func TestCoverageStateAggregation(t *testing.T) {
	for _, test := range []struct {
		value lineAccumulator
		want  string
	}{
		{value: lineAccumulator{covered: true, count: 2}, want: "covered"},
		{value: lineAccumulator{uncovered: true}, want: "uncovered"},
		{value: lineAccumulator{covered: true, uncovered: true}, want: "partial"},
		{value: lineAccumulator{covered: true, partial: true}, want: "partial"},
	} {
		if got := stateFor(&test.value); got != test.want {
			t.Fatalf("stateFor(%#v) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestCoverageFormatsRejectMalformedVersionsAndTrailingJSON(t *testing.T) {
	if supportedGcovVersion("") || supportedGcovVersion("garbage") || !supportedGcovVersion("2.0") {
		t.Fatal("gcov version validation is incorrect")
	}
	var destination map[string]any
	if err := decodeJSON([]byte(`{"one":1}{"two":2}`), &destination); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
}

func TestExecutionCountAdditionSaturates(t *testing.T) {
	if got := addExecutionCounts(^uint64(0)-1, 5); got != ^uint64(0) {
		t.Fatalf("saturated count = %d", got)
	}
}
