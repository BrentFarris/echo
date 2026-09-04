package debugconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DecodeJSONC accepts the comments and trailing commas used by VS Code's JSON
// configuration files without changing strings that contain comment markers.
func DecodeJSONC(data []byte, destination any) error {
	cleaned, err := stripJSONComments(data)
	if err != nil {
		return err
	}
	cleaned = stripTrailingCommas(cleaned)
	if err := json.Unmarshal(cleaned, destination); err != nil {
		return fmt.Errorf("parse JSONC: %w", err)
	}
	return nil
}

func stripJSONComments(data []byte) ([]byte, error) {
	result := make([]byte, 0, len(data))
	inString, escaped, lineComment, blockComment := false, false, false, false
	for index := 0; index < len(data); index++ {
		current := data[index]
		var next byte
		if index+1 < len(data) {
			next = data[index+1]
		}
		if lineComment {
			if current == '\n' || current == '\r' {
				lineComment = false
				result = append(result, current)
			} else {
				result = append(result, ' ')
			}
			continue
		}
		if blockComment {
			if current == '*' && next == '/' {
				result = append(result, ' ', ' ')
				index++
				blockComment = false
			} else if current == '\n' || current == '\r' {
				result = append(result, current)
			} else {
				result = append(result, ' ')
			}
			continue
		}
		if inString {
			result = append(result, current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			result = append(result, current)
			continue
		}
		if current == '/' && next == '/' {
			lineComment = true
			result = append(result, ' ', ' ')
			index++
			continue
		}
		if current == '/' && next == '*' {
			blockComment = true
			result = append(result, ' ', ' ')
			index++
			continue
		}
		result = append(result, current)
	}
	if blockComment {
		return nil, fmt.Errorf("unterminated JSON block comment")
	}
	return result, nil
}

func stripTrailingCommas(data []byte) []byte {
	result := append([]byte(nil), data...)
	inString, escaped := false, false
	for index := 0; index < len(result); index++ {
		current := result[index]
		if inString {
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			continue
		}
		if current != ',' {
			continue
		}
		next := bytes.TrimLeft(result[index+1:], " \t\r\n")
		if len(next) > 0 && (next[0] == '}' || next[0] == ']') {
			result[index] = ' '
		}
	}
	return result
}
