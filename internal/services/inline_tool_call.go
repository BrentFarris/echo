package services

import (
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/brent/echo/internal/llm"
)

const inlineToolCallIndexBase = 10000

var (
	inlineFunctionTagPattern  = regexp.MustCompile(`(?is)<function\s*=\s*["']?([A-Za-z0-9_-]+)["']?\s*>(.*?)</function\s*>`)
	inlineParameterTagPattern = regexp.MustCompile(`(?is)<parameter\s*=\s*["']?([A-Za-z0-9_-]+)["']?\s*>(.*?)</parameter\s*>`)
)

type inlineToolCallParseResult struct {
	Text      string
	ToolCalls []llm.ToolCall
}

type inlineToolCallStreamParser struct {
	buffer string
}

func (p *inlineToolCallStreamParser) Consume(text string) inlineToolCallParseResult {
	p.buffer += text
	return p.drain(false)
}

func (p *inlineToolCallStreamParser) Flush() inlineToolCallParseResult {
	return p.drain(true)
}

func (p *inlineToolCallStreamParser) drain(flush bool) inlineToolCallParseResult {
	var result inlineToolCallParseResult
	var visible strings.Builder

	for {
		start := indexInlineToolCallStart(p.buffer)
		if start < 0 {
			if flush {
				visible.WriteString(p.buffer)
				p.buffer = ""
			} else {
				ready, pending := splitPossibleInlineToolCallPrefix(p.buffer)
				visible.WriteString(ready)
				p.buffer = pending
			}
			result.Text = visible.String()
			return result
		}

		if start > 0 {
			visible.WriteString(p.buffer[:start])
			p.buffer = p.buffer[start:]
		}

		close := indexFold(p.buffer, "</tool_call>")
		if close < 0 {
			if flush {
				if call, ok := parseInlineToolCallBlock(p.buffer); ok {
					result.ToolCalls = append(result.ToolCalls, call)
				} else {
					visible.WriteString(p.buffer)
				}
				p.buffer = ""
			}
			result.Text = visible.String()
			return result
		}

		end := close + len("</tool_call>")
		block := p.buffer[:end]
		if call, ok := parseInlineToolCallBlock(block); ok {
			result.ToolCalls = append(result.ToolCalls, call)
		} else {
			visible.WriteString(block)
		}
		p.buffer = p.buffer[end:]
	}
}

func parseInlineToolCallBlock(block string) (llm.ToolCall, bool) {
	if call, ok := parseTaggedInlineToolCall(block); ok {
		return call, true
	}
	return parseJSONInlineToolCall(block)
}

func parseTaggedInlineToolCall(block string) (llm.ToolCall, bool) {
	functionMatch := inlineFunctionTagPattern.FindStringSubmatch(block)
	if functionMatch == nil {
		return llm.ToolCall{}, false
	}

	name := strings.TrimSpace(functionMatch[1])
	if name == "" {
		return llm.ToolCall{}, false
	}

	args := map[string]any{}
	for _, match := range inlineParameterTagPattern.FindAllStringSubmatch(functionMatch[2], -1) {
		if len(match) != 3 {
			continue
		}
		key := strings.TrimSpace(match[1])
		if key == "" {
			continue
		}
		args[key] = inlineToolParameterValue(key, match[2])
	}

	arguments, err := json.Marshal(args)
	if err != nil {
		return llm.ToolCall{}, false
	}
	return llm.ToolCall{
		Type: "function",
		Function: llm.FunctionCall{
			Name:      name,
			Arguments: string(arguments),
		},
	}, true
}

func parseJSONInlineToolCall(block string) (llm.ToolCall, bool) {
	body := strings.TrimSpace(stripInlineToolCallWrapper(block))
	if body == "" {
		return llm.ToolCall{}, false
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return llm.ToolCall{}, false
	}

	name := jsonString(raw["name"])
	args := firstJSONRaw(raw["arguments"], raw["parameters"])
	if functionRaw, ok := raw["function"]; ok {
		if functionName := jsonString(functionRaw); functionName != "" {
			name = functionName
		} else {
			var function map[string]json.RawMessage
			if err := json.Unmarshal(functionRaw, &function); err == nil {
				if functionName := jsonString(function["name"]); functionName != "" {
					name = functionName
				}
				args = firstJSONRaw(args, function["arguments"], function["parameters"])
			}
		}
	}
	if name == "" {
		return llm.ToolCall{}, false
	}

	arguments := normalizeInlineJSONArguments(args)
	return llm.ToolCall{
		Type: "function",
		Function: llm.FunctionCall{
			Name:      name,
			Arguments: string(arguments),
		},
	}, true
}

func stripInlineToolCallWrapper(block string) string {
	block = strings.TrimSpace(block)
	startEnd := strings.Index(block, ">")
	if indexInlineToolCallStart(block) == 0 && startEnd >= 0 {
		block = block[startEnd+1:]
	}
	if close := indexFold(block, "</tool_call>"); close >= 0 {
		block = block[:close]
	}
	return html.UnescapeString(strings.TrimSpace(block))
}

func inlineToolParameterValue(name string, raw string) any {
	value := strings.TrimSpace(html.UnescapeString(raw))
	switch name {
	case "includeHidden", "overwrite":
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	case "timeoutSeconds", "maxOutputBytes", "maxBytes":
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			return parsed
		}
	}

	if strings.HasPrefix(value, `"`) {
		var decoded string
		if err := json.Unmarshal([]byte(value), &decoded); err == nil {
			return decoded
		}
	}
	if strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err == nil {
			return decoded
		}
	}
	return value
}

func normalizeInlineJSONArguments(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{}`)
	}
	if strings.HasPrefix(trimmed, "{") {
		return json.RawMessage(trimmed)
	}

	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		encoded = strings.TrimSpace(encoded)
		if strings.HasPrefix(encoded, "{") && json.Valid([]byte(encoded)) {
			return json.RawMessage(encoded)
		}
	}
	return json.RawMessage(`{}`)
}

func jsonString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func firstJSONRaw(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		if len(strings.TrimSpace(string(value))) > 0 {
			return value
		}
	}
	return nil
}

func indexInlineToolCallStart(text string) int {
	lower := strings.ToLower(text)
	offset := 0
	for {
		index := strings.Index(lower[offset:], "<tool_call")
		if index < 0 {
			return -1
		}
		position := offset + index
		after := position + len("<tool_call")
		if after >= len(text) {
			return position
		}
		r, _ := utf8RuneAt(text, after)
		if r == '>' || unicode.IsSpace(r) {
			return position
		}
		offset = after
	}
}

func splitPossibleInlineToolCallPrefix(text string) (string, string) {
	lower := strings.ToLower(text)
	marker := "<tool_call"
	limit := len(marker)
	if len(lower) < limit {
		limit = len(lower)
	}
	for size := limit; size > 0; size-- {
		if strings.HasPrefix(marker, lower[len(lower)-size:]) {
			return text[:len(text)-size], text[len(text)-size:]
		}
	}
	return text, ""
}

func indexFold(text string, needle string) int {
	return strings.Index(strings.ToLower(text), strings.ToLower(needle))
}

func utf8RuneAt(text string, byteIndex int) (rune, int) {
	for index, r := range text[byteIndex:] {
		return r, byteIndex + index
	}
	return 0, byteIndex
}
