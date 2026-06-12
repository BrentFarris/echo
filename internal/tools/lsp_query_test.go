package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeCodeNavigator struct {
	request CodeNavigationRequest
}

func (f *fakeCodeNavigator) QueryCode(ctx context.Context, request CodeNavigationRequest) (CodeNavigationResponse, error) {
	f.request = request
	return CodeNavigationResponse{
		Operation: request.Operation,
		Path:      request.Path,
		Found:     true,
		Locations: []CodeLocation{{
			Path: "app/main.go",
			Range: CodeRange{
				Start: CodePosition{Line: 4, Column: 2},
				End:   CodePosition{Line: 4, Column: 8},
			},
		}},
	}, nil
}

func TestLSPQueryCallsCodeNavigator(t *testing.T) {
	navigator := &fakeCodeNavigator{}
	result := Execute(
		ExecutionContext{Context: context.Background(), CodeNavigator: navigator},
		"lsp_query",
		mustToolJSON(t, map[string]any{
			"operation":  "type-definition",
			"path":       "app/main.go",
			"line":       4,
			"column":     2,
			"maxResults": 5,
		}),
	)

	if !result.Success {
		t.Fatalf("lsp_query failed: %#v", result)
	}
	if navigator.request.Operation != "type_definition" || navigator.request.Path != "app/main.go" {
		t.Fatalf("unexpected navigator request: %#v", navigator.request)
	}
	output, ok := result.Output.(CodeNavigationResponse)
	if !ok {
		t.Fatalf("unexpected output type: %#v", result.Output)
	}
	if !output.Found || len(output.Locations) != 1 || output.Locations[0].Path != "app/main.go" {
		t.Fatalf("unexpected output: %#v", output)
	}
}

func TestLSPQueryRequiresNavigator(t *testing.T) {
	result := Execute(
		ExecutionContext{Context: context.Background()},
		"lsp_query",
		mustToolJSON(t, map[string]any{"operation": "definition", "path": "app/main.go", "line": 1, "column": 1}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "lsp_unavailable" {
		t.Fatalf("expected lsp_unavailable error, got %#v", result)
	}
}

func mustToolJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
