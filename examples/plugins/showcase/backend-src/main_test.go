package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestHandshakeToolAndEvent(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"echo.initialize","params":{"protocol":"echo-jsonrpc-1"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools.echo","params":{"arguments":{"message":"hello"},"scope":{"kind":"workspace"},"config":{"display-name":"Developer","api-token":"do-not-return"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"showcase.emit","params":{"sessionId":"ui-1","params":{"message":"event"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"echo.shutdown","params":{}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := serve(strings.NewReader(input), &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected five protocol messages, got %d: %s", len(lines), output.String())
	}
	if strings.Contains(output.String(), "do-not-return") {
		t.Fatal("secret was reflected into protocol output")
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &event); err != nil {
		t.Fatal(err)
	}
	if event["method"] != "echo.uiEvent" {
		t.Fatalf("expected UI event notification, got %#v", event)
	}
}
