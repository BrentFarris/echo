package decision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sampleResponse is the exact response body from the Decision Model spec.
const sampleResponse = `{"model":"clef-flash","answers":{"team":{"type":"choice","choice":"billing","probabilities":{"billing":0.9856991693342816,"sales":0.006899823243733864,"technical":0.0074010074219844225},"confidence":0.9785487540014224},"refund":{"type":"noul","noul":0.875798578163359},"urgency":{"type":"score","score":2.1668532824117843,"legend":{"0":"Can wait","1":"This week","2":"Today","3":"Immediately"},"probabilities":{"0":0.012809943242769652,"1":0.02276650004052402,"2":0.7491838877788586,"3":0.2152396689378478},"confidence":0.7363739445360888}},"usage":{"input_tokens":342,"output_tokens":0}}`

func newTestServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(server.URL, "")
}

func TestClassifyRoundTripsSamplePayload(t *testing.T) {
	var receivedPath string
	var receivedBody map[string]any
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, &receivedBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleResponse))
	})

	response, err := client.Ask(context.Background(), "I was charged twice for March. Please refund the duplicate today.").
		Choice("team", "Which team should handle this?", map[string]string{
			"billing":   "Payments and refunds",
			"technical": "Bugs and outages",
			"sales":     "Plans and upgrades",
		}).
		Noul("refund", "Does the customer explicitly request a refund?").
		Score("urgency", "How urgent is this?", []string{"Can wait", "This week", "Today", "Immediately"}).
		Do()
	if err != nil {
		t.Fatalf("Do() error: %v", err)
	}

	if receivedPath != "/systemone" {
		t.Errorf("request path = %q, want /systemone", receivedPath)
	}
	if got := receivedBody["model"]; got != DefaultModel {
		t.Errorf("request model = %v, want %q", got, DefaultModel)
	}
	questions, ok := receivedBody["questions"].(map[string]any)
	if !ok {
		t.Fatalf("request questions missing: %v", receivedBody)
	}
	team, ok := questions["team"].(map[string]any)
	if !ok {
		t.Fatalf("team question missing: %v", questions)
	}
	if team["type"] != "choice" {
		t.Errorf("team type = %v, want choice", team["type"])
	}
	criteria, ok := team["criteria"].(map[string]any)
	if !ok || criteria["billing"] != "Payments and refunds" {
		t.Errorf("team criteria = %v, want object with billing description", team["criteria"])
	}
	urgency, ok := questions["urgency"].(map[string]any)
	if !ok {
		t.Fatalf("urgency question missing: %v", questions)
	}
	legend, ok := urgency["criteria"].([]any)
	if !ok || len(legend) != 4 || legend[0] != "Can wait" {
		t.Errorf("urgency criteria = %v, want ordered legend array", urgency["criteria"])
	}

	if response.Model != "clef-flash" {
		t.Errorf("response model = %q", response.Model)
	}
	if response.Usage.InputTokens != 342 || response.Usage.OutputTokens != 0 {
		t.Errorf("response usage = %+v", response.Usage)
	}

	teamAnswer, ok := response.Answer("team")
	if !ok {
		t.Fatal("team answer missing")
	}
	if teamAnswer.Choice != "billing" {
		t.Errorf("team choice = %q, want billing", teamAnswer.Choice)
	}
	best, prob, found := teamAnswer.Best()
	if !found || best != "billing" || prob < 0.98 {
		t.Errorf("team Best() = (%q, %v, %v)", best, prob, found)
	}
	if teamAnswer.Confidence < 0.97 {
		t.Errorf("team confidence = %v", teamAnswer.Confidence)
	}

	refundAnswer, ok := response.Answer("refund")
	if !ok {
		t.Fatal("refund answer missing")
	}
	if refundAnswer.Noul < 0.87 {
		t.Errorf("refund noul = %v", refundAnswer.Noul)
	}

	urgencyAnswer, ok := response.Answer("urgency")
	if !ok {
		t.Fatal("urgency answer missing")
	}
	if urgencyAnswer.Score < 2.1 || urgencyAnswer.Score > 2.2 {
		t.Errorf("urgency score = %v", urgencyAnswer.Score)
	}
	if urgencyAnswer.Probability("2") < 0.74 {
		t.Errorf("urgency P(2) = %v", urgencyAnswer.Probability("2"))
	}
	if urgencyAnswer.Legend["3"] != "Immediately" {
		t.Errorf("urgency legend = %v", urgencyAnswer.Legend)
	}
}

func TestClassifyAppendsSystemoneOnce(t *testing.T) {
	cases := []string{
		"http://host:11434/v1",
		"http://host:11434/v1/",
		"http://host:11434/v1/systemone",
		"http://host:11434/v1/systemone/",
	}
	for _, base := range cases {
		client := New(base, "m")
		if got := client.classifyURL(); got != "http://host:11434/v1/systemone" {
			t.Errorf("New(%q).classifyURL() = %q", base, got)
		}
	}
}

func TestDisabledClient(t *testing.T) {
	var nilClient *Client
	if nilClient.Enabled() {
		t.Error("nil client should be disabled")
	}
	if _, err := nilClient.Classify(context.Background(), &Request{}); !errors.Is(err, ErrDisabled) {
		t.Errorf("nil client Classify error = %v, want ErrDisabled", err)
	}
	empty := New("", "")
	if empty.Enabled() {
		t.Error("empty-URL client should be disabled")
	}
	if _, err := empty.Ask(context.Background(), "state").Noul("q", "i?").Do(); !errors.Is(err, ErrDisabled) {
		t.Errorf("empty client Do() error = %v, want ErrDisabled", err)
	}
}

func TestBuilderRejectsDuplicateAndEmptyQueries(t *testing.T) {
	client := New("http://localhost:1", "")
	_, err := client.Ask(context.Background(), "state").Noul("q", "a?").Noul("q", "b?").Do()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate question error = %v", err)
	}
	_, err = client.Ask(context.Background(), "state").Do()
	if !errors.Is(err, ErrNoQuestions) {
		t.Errorf("empty query error = %v, want ErrNoQuestions", err)
	}
}

func TestClassifySurfacesHTTPErrors(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model unavailable", http.StatusServiceUnavailable)
	})
	_, err := client.Ask(context.Background(), "state").Noul("q", "i?").Do()
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("HTTP error = %v, want status 503 mention", err)
	}
}

func TestClassifyRespectsCallerDeadline(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(sampleResponse))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Ask(ctx, "state").Noul("q", "i?").Do(); err == nil {
		t.Error("expected deadline-exceeded error")
	}
}

func TestCriteriaUnmarshalBothShapes(t *testing.T) {
	var object Criteria
	if err := json.Unmarshal([]byte(`{"billing":"Payments"}`), &object); err != nil {
		t.Fatalf("object unmarshal: %v", err)
	}
	if object.Descriptions["billing"] != "Payments" || object.Options != nil {
		t.Errorf("object criteria = %+v", object)
	}
	var array Criteria
	if err := json.Unmarshal([]byte(`["Can wait","Today"]`), &array); err != nil {
		t.Fatalf("array unmarshal: %v", err)
	}
	if len(array.Options) != 2 || array.Options[1] != "Today" || array.Descriptions != nil {
		t.Errorf("array criteria = %+v", array)
	}
}
