// Package hunk applies editor line mappings to immutable text snapshots.
package hunk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// Range is one-based and end-exclusive, including Monaco's final empty line.
type Range struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type Target struct {
	Kind    string `json:"kind"`
	GroupID string `json:"groupId,omitempty"`
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	BaseRef string `json:"baseRef,omitempty"`
	Ref     string `json:"ref,omitempty"`
}

type Request struct {
	Target   Target `json:"target"`
	Token    string `json:"token"`
	Original Range  `json:"original"`
	Modified Range  `json:"modified"`
}

type Side struct {
	Content string
	Exists  bool
	EOL     string
	HasBOM  bool
}

func Token(values ...any) string {
	data, _ := json.Marshal(values)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func Lines(content string) []string {
	return strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
}

// Apply copies the source range into the destination. Neither snapshot is
// mutated. Empty ranges represent insertions/deletions, not whole-file actions.
func Apply(destination, source Side, destRange, sourceRange Range) (Side, error) {
	dest, src := Lines(destination.Content), Lines(source.Content)
	valid := func(r Range, n int) bool { return r.Start >= 1 && r.End >= r.Start && r.End <= n+1 }
	if !valid(destRange, len(dest)) || !valid(sourceRange, len(src)) ||
		(destRange.Start == destRange.End && sourceRange.Start == sourceRange.End) {
		return Side{}, errors.New("invalid change block range")
	}
	result := destination
	if !destination.Exists {
		result.EOL, result.HasBOM = source.EOL, source.HasBOM
	}
	separator := "\n"
	if result.EOL == "crlf" {
		separator = "\r\n"
	}
	// Carry unchanged separators with their lines, so mixed-EOL files are not
	// normalized by a block action. New boundaries use the destination EOL.
	type line struct{ text, eol string }
	parse := func(content string) []line {
		parts := strings.Split(content, "\n")
		lines := make([]line, len(parts))
		for i, part := range parts {
			lines[i].text = part
			if i < len(parts)-1 {
				lines[i].eol = "\n"
				if strings.HasSuffix(part, "\r") {
					lines[i].text = strings.TrimSuffix(part, "\r")
					lines[i].eol = "\r\n"
				}
			}
		}
		return lines
	}
	destLines := parse(destination.Content)
	lines := append([]line{}, destLines[:destRange.Start-1]...)
	for _, text := range src[sourceRange.Start-1 : sourceRange.End-1] {
		lines = append(lines, line{text, separator})
	}
	lines = append(lines, destLines[destRange.End-1:]...)
	var content strings.Builder
	for i, line := range lines {
		content.WriteString(line.text)
		if i < len(lines)-1 {
			if line.eol == "" {
				content.WriteString(separator)
			} else {
				content.WriteString(line.eol)
			}
		}
	}
	result.Content = content.String()
	result.Exists = true
	// An absent source means removal only when no destination text remains.
	if !source.Exists && result.Content == "" {
		result.Exists = false
		result.HasBOM = false
	}
	return result, nil
}

func Bytes(side Side) []byte {
	if !side.Exists {
		return nil
	}
	if side.HasBOM {
		return append([]byte{0xef, 0xbb, 0xbf}, []byte(side.Content)...)
	}
	return []byte(side.Content)
}
