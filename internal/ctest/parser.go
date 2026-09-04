// Package ctest implements framework-free C test targets and coverage.
package ctest

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const MaxSourceBytes = 4 << 20

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Lens struct {
	Range    Range  `json:"range"`
	Title    string `json:"title"`
	Action   string `json:"action"`
	TargetID string `json:"targetId"`
}

type cToken struct {
	text   string
	offset int
	depth  int
}

func discoverFunction(source, name string) (Range, bool) {
	tokens := lexC(source)
	for index, token := range tokens {
		if token.depth != 0 || token.text != name || index+1 >= len(tokens) || tokens[index+1].text != "(" {
			continue
		}
		depth := 0
		end := index + 1
		for ; end < len(tokens); end++ {
			switch tokens[end].text {
			case "(":
				depth++
			case ")":
				depth--
				if depth == 0 {
					end++
					goto signatureEnd
				}
			}
		}
		continue
	signatureEnd:
		postfixDepth := 0
		for ; end < len(tokens) && tokens[end].depth == 0; end++ {
			switch tokens[end].text {
			case "(":
				postfixDepth++
			case ")":
				if postfixDepth > 0 {
					postfixDepth--
				}
			case "{":
				if postfixDepth == 0 {
					position := sourcePosition(source, token.offset)
					return Range{Start: position, End: position}, true
				}
			case ";", "=", ",":
				if postfixDepth == 0 {
					end = len(tokens)
				}
			default:
				if postfixDepth == 0 && isDeclarationStarter(tokens[end].text) {
					end = len(tokens)
				}
			}
		}
	}
	return Range{}, false
}

func isDeclarationStarter(value string) bool {
	switch value {
	case "struct", "union", "enum", "typedef", "extern", "static", "inline", "int", "void", "char", "short", "long", "float", "double", "signed", "unsigned", "const", "volatile", "_Atomic":
		return true
	default:
		return false
	}
}

func lexC(source string) []cToken {
	tokens := make([]cToken, 0, len(source)/6)
	depth := 0
	lineStart := true
	for index := 0; index < len(source); {
		character := source[index]
		if character == '\n' {
			index++
			lineStart = true
			continue
		}
		if character == ' ' || character == '\t' || character == '\r' || character == '\f' || character == '\v' {
			index++
			continue
		}
		if lineStart && character == '#' {
			for index < len(source) {
				if source[index] == '\n' {
					previous := index - 1
					if previous >= 0 && source[previous] == '\r' {
						previous--
					}
					if previous >= 0 && source[previous] == '\\' {
						index++
						continue
					}
					break
				}
				index++
			}
			continue
		}
		lineStart = false
		if character == '/' && index+1 < len(source) && source[index+1] == '/' {
			index += 2
			for index < len(source) && source[index] != '\n' {
				index++
			}
			continue
		}
		if character == '/' && index+1 < len(source) && source[index+1] == '*' {
			index += 2
			for index+1 < len(source) && (source[index] != '*' || source[index+1] != '/') {
				if source[index] == '\n' {
					lineStart = true
				}
				index++
			}
			if index+1 < len(source) {
				index += 2
			}
			continue
		}
		if character == '"' || character == '\'' {
			quote := character
			index++
			for index < len(source) {
				if source[index] == '\\' && index+1 < len(source) {
					index += 2
					continue
				}
				if source[index] == quote {
					index++
					break
				}
				if source[index] == '\n' {
					lineStart = true
				}
				index++
			}
			continue
		}
		start := index
		if isIdentifierStart(character) {
			index++
			for index < len(source) && isIdentifierContinue(source[index]) {
				index++
			}
			tokens = append(tokens, cToken{text: source[start:index], offset: start, depth: depth})
			continue
		}
		text := source[index : index+1]
		tokens = append(tokens, cToken{text: text, offset: index, depth: depth})
		if character == '{' {
			depth++
		} else if character == '}' && depth > 0 {
			depth--
		}
		index++
	}
	return tokens
}

func sourcePosition(source string, offset int) Position {
	line := strings.Count(source[:offset], "\n")
	lineStart := strings.LastIndex(source[:offset], "\n") + 1
	column := 0
	for len(source[lineStart:offset]) > 0 {
		r, size := utf8.DecodeRuneInString(source[lineStart:offset])
		if size == 0 {
			break
		}
		column += utf16.RuneLen(r)
		lineStart += size
	}
	return Position{Line: line, Character: column}
}

func isIdentifierStart(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func isIdentifierContinue(value byte) bool {
	return isIdentifierStart(value) || value >= '0' && value <= '9'
}
