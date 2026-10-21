package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brent/echo/internal/decision"
)

func TestDecisionModelToolDisabled(t *testing.T) {
	result := Execute(ExecutionContext{Context: context.Background()}, "decision_model",
		json.RawMessage(`{"state":"s","questions":[{"name":"q","type":"noul","instructions":"i"}]}`))
	if result.Success || result.Error == nil || result.Error.Code != "decision_model_disabled" {
		t.Fatalf("expected decision_model_disabled, got %+v", result)
	}
}

func TestDecisionModelToolRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"clef-flash","answers":{"team":{"type":"choice","choice":"billing","probabilities":{"billing":0.98,"sales":0.01,"technical":0.01},"confidence":0.97}},"usage":{"input_tokens":10,"output_tokens":0}}`))
	}))
	t.Cleanup(server.Close)

	result := Execute(
		ExecutionContext{Context: context.Background(), DecisionModel: decision.New(server.URL, "")},
		"decision_model",
		json.RawMessage(`{"state":"Refund please","questions":[{"name":"team","type":"choice","instructions":"Which team?","criteria":{"billing":"Payments","technical":"Bugs"}}]}`),
	)
	if !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}
	raw, err := json.Marshal(result.Output)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	var response decision.Response
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	answer, ok := response.Answer("team")
	if !ok || answer.Choice != "billing" {
		t.Fatalf("team answer = %+v", answer)
	}
}

func TestDecisionModelToolValidatesArguments(t *testing.T) {
	client := decision.New("http://localhost:1", "")
	cases := []string{
		`{"questions":[{"name":"q","type":"noul","instructions":"i"}]}`,                                   // missing state
		`{"state":"s","questions":[]}`,                                                                   // no questions
		`{"state":"s","questions":[{"name":"q","type":"bogus","instructions":"i"}]}`,                      // bad type
		`{"state":"s","questions":[{"name":"q","type":"choice","instructions":"i"}]}`,                     // missing criteria
		`{"state":"s","questions":[{"name":"q","type":"score","instructions":"i","criteria":{"a":"b"}}]}`, // object criteria for score
		`{"state":"s","questions":[{"name":"q","type":"noul","instructions":"i"},{"name":"q","type":"noul","instructions":"i"}]}`, // duplicate
	}
	for _, arguments := range cases {
		result := Execute(ExecutionContext{Context: context.Background(), DecisionModel: client}, "decision_model", json.RawMessage(arguments))
		if result.Success || result.Error == nil || result.Error.Code != "invalid_arguments" {
			t.Errorf("arguments %s result = %+v, want invalid_arguments", arguments, result)
		}
	}
}
