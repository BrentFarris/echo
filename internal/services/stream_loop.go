package services

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/brent/echo/internal/llm"
)

const (
	maxStreamLoopRetries         = 2
	streamLoopMaxTrackedBytes    = 12000
	streamLoopMaxPatternWords    = 32
	streamLoopMinRepeatedWords   = 16
	streamLoopMinRepeatedChars   = 80
	streamLoopRequiredRepetition = 4
)

type streamLoopKind string

const (
	streamLoopContent   streamLoopKind = "message"
	streamLoopReasoning streamLoopKind = "thinking"
)

type streamLoopDetection struct {
	Kind        streamLoopKind
	Pattern     string
	Repetitions int
}

type streamLoopDetector struct {
	content   streamLoopChannelDetector
	reasoning streamLoopChannelDetector
}

type streamLoopChannelDetector struct {
	text string
}

func (d *streamLoopDetector) observe(kind streamLoopKind, text string) (streamLoopDetection, bool) {
	switch kind {
	case streamLoopReasoning:
		return d.reasoning.observe(kind, text)
	default:
		return d.content.observe(streamLoopContent, text)
	}
}

func (d *streamLoopChannelDetector) observe(kind streamLoopKind, text string) (streamLoopDetection, bool) {
	if text == "" {
		return streamLoopDetection{}, false
	}
	d.text += text
	if len(d.text) > streamLoopMaxTrackedBytes {
		d.text = d.text[len(d.text)-streamLoopMaxTrackedBytes:]
	}

	words := normalizedLoopWords(d.text)
	pattern, repetitions, ok := repeatedWordSuffix(words)
	if !ok {
		return streamLoopDetection{}, false
	}
	return streamLoopDetection{
		Kind:        kind,
		Pattern:     strings.Join(pattern, " "),
		Repetitions: repetitions,
	}, true
}

func normalizedLoopWords(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	words := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != "" {
			words = append(words, field)
		}
	}
	return words
}

func repeatedWordSuffix(words []string) ([]string, int, bool) {
	if len(words) < streamLoopMinRepeatedWords {
		return nil, 0, false
	}

	maxPatternWords := len(words) / streamLoopRequiredRepetition
	if maxPatternWords > streamLoopMaxPatternWords {
		maxPatternWords = streamLoopMaxPatternWords
	}
	for size := 1; size <= maxPatternWords; size++ {
		patternStart := len(words) - size
		pattern := words[patternStart:]
		repetitions := 1
		for start := patternStart - size; start >= 0; start -= size {
			if !sameWords(words[start:start+size], pattern) {
				break
			}
			repetitions++
		}
		if repetitions < streamLoopRequiredRepetition {
			continue
		}
		if repetitions*size < streamLoopMinRepeatedWords {
			continue
		}
		if len(strings.Join(pattern, " "))*repetitions < streamLoopMinRepeatedChars {
			continue
		}
		return pattern, repetitions, true
	}
	return nil, 0, false
}

func sameWords(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func appendStreamLoopRetryMessages(messages []llm.Message, content string, detection streamLoopDetection) []llm.Message {
	if strings.TrimSpace(content) != "" {
		messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: content})
	}
	return append(messages, llm.Message{Role: llm.RoleUser, Content: streamLoopRetryPrompt(detection)})
}

func streamLoopRetryPrompt(detection streamLoopDetection) string {
	target := streamLoopTarget(detection)

	pattern := truncateLoopPattern(detection.Pattern)
	avoid := ""
	if pattern != "" {
		avoid = fmt.Sprintf(" Avoid repeating this looped fragment: %q.", pattern)
	}
	return fmt.Sprintf(
		"Your previous streamed response was stopped because the %s started repeating itself. Treat any assistant content immediately before this message as already sent to the user. Continue or retry from the next useful step without restating prior content.%s If the previous content is unusable, restart concisely.",
		target,
		avoid,
	)
}

func streamLoopExceededError(detection streamLoopDetection) error {
	return fmt.Errorf("The LLM response kept repeating while streaming %s. Echo stopped retrying after %d attempts.", streamLoopTarget(detection), maxStreamLoopRetries)
}

func streamLoopTarget(detection streamLoopDetection) string {
	if detection.Kind == streamLoopReasoning {
		return "thinking"
	}
	return "visible answer"
}

func truncateLoopPattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if len(pattern) <= 180 {
		return pattern
	}
	return pattern[:177] + "..."
}
