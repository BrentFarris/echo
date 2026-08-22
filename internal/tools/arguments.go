package tools

import (
	"bytes"
	"encoding/json"

	"github.com/brent/echo/internal/toolargs"
)

// DecodeToolArguments unmarshals tool arguments and retries once with a small
// repair pass for common truncated JSON emitted by some chat-completion models.
func DecodeToolArguments(arguments json.RawMessage, target any) error {
	trimmed := bytes.TrimSpace(arguments)
	if len(trimmed) == 0 {
		return nil
	}
	if err := json.Unmarshal(trimmed, target); err == nil {
		return nil
	} else {
		if repaired, ok := RepairToolArgumentsJSON(trimmed); ok {
			if repairErr := json.Unmarshal(repaired, target); repairErr == nil {
				return nil
			}
		}
		return err
	}
}

// RepairToolArgumentsJSON returns a valid JSON argument payload when a small
// deterministic repair can recover a truncated object or array.
func RepairToolArgumentsJSON(arguments json.RawMessage) (json.RawMessage, bool) {
	return toolargs.RepairJSON(arguments)
}
