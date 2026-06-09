package tools

import (
	"encoding/json"
	"testing"
)

func TestDecodeToolArgumentsRepairsMissingClosingQuoteAndBrace(t *testing.T) {
	var args struct {
		Path string `json:"path"`
	}

	if err := DecodeToolArguments(json.RawMessage(`{"path":"README.md`), &args); err != nil {
		t.Fatalf("decode repaired arguments: %v", err)
	}
	if args.Path != "README.md" {
		t.Fatalf("expected repaired path, got %#v", args)
	}
}

func TestRepairToolArgumentsJSONInsertsMissingQuoteBeforeObjectClose(t *testing.T) {
	repaired, ok := RepairToolArgumentsJSON(json.RawMessage(`{"path":"README.md}`))
	if !ok {
		t.Fatal("expected arguments to be repaired")
	}
	if string(repaired) != `{"path":"README.md"}` {
		t.Fatalf("unexpected repair: %s", repaired)
	}
}

func TestRepairToolArgumentsJSONRejectsUnrepairableInput(t *testing.T) {
	if repaired, ok := RepairToolArgumentsJSON(json.RawMessage(`{"path":`)); ok {
		t.Fatalf("expected input to remain unrepaired, got %s", repaired)
	}
}
