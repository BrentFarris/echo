package main

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNightlyWorkflowYAML(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/nightly-builds.yml")
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("nightly workflow is invalid YAML: %v", err)
	}
	for _, job := range []string{"sandbox-images:", "windows-sandbox-acceptance:", "build:", "release:"} {
		if !containsYAMLLine(data, job) {
			t.Fatalf("nightly workflow is missing required job %q", job)
		}
	}
}

func containsYAMLLine(data []byte, expected string) bool {
	for _, line := range bytesLines(data) {
		if string(line) == "  "+expected {
			return true
		}
	}
	return false
}

func bytesLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for index, value := range data {
		if value == '\n' {
			line := data[start:index]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			start = index + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
