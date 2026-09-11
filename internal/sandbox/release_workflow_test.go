package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestProtocolThreePublicationRequiresBothAcceptanceHosts(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "nightly-builds.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs   []string          `yaml:"needs"`
			Outputs map[string]string `yaml:"outputs"`
			Steps   []struct {
				With map[string]any `yaml:"with"`
				Run  string         `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	// Some independent jobs use a scalar needs field. Decode only the jobs
	// involved in publishing, preserving the original YAML for syntax checks.
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	jobs := document["jobs"].(map[string]any)
	for name, value := range jobs {
		if name != "publish-sandbox-images" && name != "sandbox-images" && name != "windows-sandbox-acceptance" && name != "build" {
			delete(jobs, name)
		} else {
			_ = value
		}
	}
	selected, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(selected, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, dependency := range []string{"sandbox-images", "windows-sandbox-acceptance"} {
		if !slices.Contains(workflow.Jobs["publish-sandbox-images"].Needs, dependency) {
			t.Fatalf("publication bypasses %s", dependency)
		}
	}
	if !slices.Contains(workflow.Jobs["build"].Needs, "publish-sandbox-images") {
		t.Fatal("release binaries can bypass accepted image digests")
	}
	if outputs := workflow.Jobs["publish-sandbox-images"].Outputs; len(outputs) != 2 || outputs["runtime"] == "" || outputs["gateway"] == "" {
		t.Fatal("release image outputs are not runtime and gateway")
	}
	for _, step := range workflow.Jobs["sandbox-images"].Steps {
		if step.With["push"] == true || strings.Contains(step.Run, "docker push") {
			t.Fatal("unaccepted candidate images are published")
		}
	}
	if !strings.Contains(string(data), "-X github.com/brent/echo/internal/sandbox.RuntimeImage=${{ needs.publish-sandbox-images.outputs.runtime }}") {
		t.Fatal("binary does not embed accepted runtime digest")
	}
}
