// Package workspaceskills manages ordinary, versionable workspace skills.
package workspaceskills

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	FileName         = "SKILL.md"
	maxBytes         = 64 * 1024
	maxDescription   = 500
	maxTriggers      = 12
	maxTriggerLength = 120
	maxCatalogSize   = 256
)

var namePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type frontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Triggers    []string `yaml:"triggers,omitempty"`
}

func validName(name string) bool { return len(name) <= 64 && namePattern.MatchString(name) }

func parse(data []byte) (frontmatter, string, error) {
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return frontmatter{}, "", fmt.Errorf("workspace skill must start with YAML frontmatter")
	}
	end := strings.Index(normalized[4:], "\n---\n")
	if end < 0 {
		return frontmatter{}, "", fmt.Errorf("workspace skill frontmatter is not terminated")
	}
	end += 4
	var header frontmatter
	decoder := yaml.NewDecoder(strings.NewReader(normalized[4:end]))
	decoder.KnownFields(true)
	if err := decoder.Decode(&header); err != nil {
		return frontmatter{}, "", fmt.Errorf("parse workspace skill frontmatter: %w", err)
	}
	header.Name = strings.TrimSpace(header.Name)
	header.Description = strings.TrimSpace(header.Description)
	if !validName(header.Name) {
		return frontmatter{}, "", fmt.Errorf("workspace skill name must be a lowercase kebab-case slug")
	}
	if header.Description == "" || utf8.RuneCountInString(header.Description) > maxDescription {
		return frontmatter{}, "", fmt.Errorf("workspace skill description is required and must not exceed 500 characters")
	}
	triggers, err := normalizeTriggers(header.Triggers)
	if err != nil {
		return frontmatter{}, "", err
	}
	header.Triggers = triggers
	body := strings.TrimSpace(normalized[end+5:])
	if body == "" {
		return frontmatter{}, "", fmt.Errorf("workspace skill body is required")
	}
	return header, body, nil
}

func normalizeTriggers(values []string) ([]string, error) {
	if len(values) > maxTriggers {
		return nil, fmt.Errorf("triggers must not contain more than 12 entries")
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("triggers must not contain empty entries")
		}
		if utf8.RuneCountInString(value) > maxTriggerLength {
			return nil, fmt.Errorf("each trigger must not exceed 120 characters")
		}
		key := strings.ToLower(value)
		if !seen[key] {
			seen[key] = true
			result = append(result, value)
		}
	}
	return result, nil
}

func marshal(header frontmatter, body string) ([]byte, error) {
	yamlData, err := yaml.Marshal(header)
	if err != nil {
		return nil, err
	}
	data := []byte("---\n" + string(yamlData) + "---\n\n" + strings.TrimSpace(body) + "\n")
	if len(data) > maxBytes {
		return nil, fmt.Errorf("workspace skill exceeds the %d byte limit", maxBytes)
	}
	return data, nil
}

var stopWords = map[string]bool{"a": true, "an": true, "and": true, "as": true, "at": true, "be": true, "by": true, "do": true, "for": true, "from": true, "how": true, "i": true, "in": true, "is": true, "it": true, "of": true, "on": true, "or": true, "that": true, "the": true, "this": true, "to": true, "we": true, "with": true}

func normalizeSearchText(value string) string {
	var output strings.Builder
	spaced := false
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			output.WriteRune(r)
			spaced = false
		} else if !spaced && output.Len() > 0 {
			output.WriteByte(' ')
			spaced = true
		}
	}
	return strings.TrimSpace(output.String())
}

func searchTokens(value string) map[string]bool {
	result := map[string]bool{}
	for _, token := range strings.Fields(value) {
		if len(token) >= 2 && !stopWords[token] {
			result[token] = true
		}
	}
	return result
}

func scoreSkill(skill Summary, query string, queryTokens map[string]bool) int {
	name := normalizeSearchText(strings.ReplaceAll(skill.Name, "-", " "))
	description := normalizeSearchText(skill.Description)
	triggers := make([]string, 0, len(skill.Triggers))
	for _, trigger := range skill.Triggers {
		triggers = append(triggers, normalizeSearchText(trigger))
	}
	score := 0
	if name == query {
		score += 80
	} else if name != "" && strings.Contains(query, name) {
		score += 35
	}
	if description == query {
		score += 45
	} else if strings.Contains(description, query) {
		score += 20
	}
	nameTokens, descriptionTokens, triggerTokens := searchTokens(name), searchTokens(description), map[string]bool{}
	for _, trigger := range triggers {
		if trigger == query {
			score += 60
		} else if trigger != "" && (strings.Contains(query, trigger) || strings.Contains(trigger, query)) {
			score += 30
		}
		for token := range searchTokens(trigger) {
			triggerTokens[token] = true
		}
	}
	for token := range queryTokens {
		if nameTokens[token] {
			score += 12
		}
		if triggerTokens[token] {
			score += 8
		}
		if descriptionTokens[token] {
			score += 4
		}
	}
	return score
}
