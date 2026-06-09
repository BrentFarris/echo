package tools

import (
	"bytes"
	"encoding/json"
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
	trimmed := bytes.TrimSpace(arguments)
	if len(trimmed) == 0 || json.Valid(trimmed) || !looksLikeJSONArguments(trimmed) {
		return nil, false
	}

	candidates := [][]byte{}
	if repaired, ok := repairToolArgumentsQuoteBeforeTrailingClosers(trimmed); ok {
		candidates = append(candidates, repaired)
		if closed, ok := repairToolArgumentsAppendClosers(repaired); ok {
			candidates = append(candidates, closed)
		}
	}
	if repaired, ok := repairToolArgumentsAppendClosers(trimmed); ok {
		candidates = append(candidates, repaired)
	}

	seen := map[string]bool{}
	for _, candidate := range candidates {
		candidate = bytes.TrimSpace(candidate)
		key := string(candidate)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if json.Valid(candidate) {
			return append(json.RawMessage(nil), candidate...), true
		}
	}
	return nil, false
}

func looksLikeJSONArguments(arguments []byte) bool {
	return arguments[0] == '{' || arguments[0] == '['
}

func repairToolArgumentsQuoteBeforeTrailingClosers(arguments []byte) ([]byte, bool) {
	state := scanJSONArgumentState(arguments)
	if state.mismatched || !state.inString {
		return nil, false
	}

	split := len(arguments)
	for split > 0 && isJSONWhitespace(arguments[split-1]) {
		split--
	}
	closeEnd := split
	for split > 0 && (arguments[split-1] == '}' || arguments[split-1] == ']') {
		split--
	}
	if split == closeEnd {
		return nil, false
	}

	repaired := make([]byte, 0, len(arguments)+1)
	repaired = append(repaired, arguments[:split]...)
	repaired = append(repaired, '"')
	repaired = append(repaired, arguments[split:]...)
	return repaired, true
}

func repairToolArgumentsAppendClosers(arguments []byte) ([]byte, bool) {
	state := scanJSONArgumentState(arguments)
	if state.mismatched || (!state.inString && !state.escaping && len(state.expectedClosers) == 0) {
		return nil, false
	}

	repaired := append([]byte(nil), arguments...)
	if state.inString {
		if state.escaping {
			repaired = append(repaired, '\\')
		}
		repaired = append(repaired, '"')
	}
	for i := len(state.expectedClosers) - 1; i >= 0; i-- {
		repaired = append(repaired, state.expectedClosers[i])
	}
	return repaired, true
}

type jsonArgumentState struct {
	expectedClosers []byte
	inString        bool
	escaping        bool
	mismatched      bool
}

func scanJSONArgumentState(arguments []byte) jsonArgumentState {
	var state jsonArgumentState
	for _, b := range arguments {
		if state.inString {
			if state.escaping {
				state.escaping = false
				continue
			}
			switch b {
			case '\\':
				state.escaping = true
			case '"':
				state.inString = false
			}
			continue
		}

		switch b {
		case '"':
			state.inString = true
		case '{':
			state.expectedClosers = append(state.expectedClosers, '}')
		case '[':
			state.expectedClosers = append(state.expectedClosers, ']')
		case '}', ']':
			if len(state.expectedClosers) == 0 || state.expectedClosers[len(state.expectedClosers)-1] != b {
				state.mismatched = true
				return state
			}
			state.expectedClosers = state.expectedClosers[:len(state.expectedClosers)-1]
		}
	}
	return state
}

func isJSONWhitespace(b byte) bool {
	return b == ' ' || b == '\n' || b == '\r' || b == '\t'
}
