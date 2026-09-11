package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestProtocolThreePublicationRequiresLinuxAndMakesWindowsOptional(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "nightly-builds.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On struct {
			WorkflowDispatch struct {
				Inputs map[string]struct {
					Type    string `yaml:"type"`
					Default bool   `yaml:"default"`
				} `yaml:"inputs"`
			} `yaml:"workflow_dispatch"`
		} `yaml:"on"`
		Jobs map[string]struct {
			If      string            `yaml:"if"`
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
	for name := range jobs {
		if name != "publish-sandbox-images" && name != "sandbox-images" && name != "windows-sandbox-acceptance" && name != "build" && name != "release" {
			delete(jobs, name)
		}
	}
	selected, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(selected, &workflow); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(workflow.Jobs["publish-sandbox-images"].Needs, "sandbox-images") {
		t.Fatal("publication bypasses Linux acceptance")
	}
	// Keeping the optional job out of the release dependency graph prevents a
	// skipped or queued Windows check from blocking scheduled nightlies.
	for name, job := range workflow.Jobs {
		if slices.Contains(job.Needs, "windows-sandbox-acceptance") {
			t.Fatalf("%s depends on optional Windows acceptance", name)
		}
	}
	input, ok := workflow.On.WorkflowDispatch.Inputs["windows_sandbox_acceptance"]
	if !ok || input.Type != "boolean" || input.Default {
		t.Fatal("Windows acceptance must be a manual boolean input disabled by default")
	}
	if condition := workflow.Jobs["windows-sandbox-acceptance"].If; condition != "github.event_name == 'workflow_dispatch' && inputs.windows_sandbox_acceptance && needs.check-changes.outputs.should_build == 'true'" {
		t.Fatalf("Windows acceptance is not restricted to manual opt-in: %s", condition)
	}
	if !slices.Contains(workflow.Jobs["build"].Needs, "publish-sandbox-images") {
		t.Fatal("release binaries can bypass accepted image digests")
	}
	if outputs := workflow.Jobs["publish-sandbox-images"].Outputs; len(outputs) != 2 || outputs["runtime"] == "" || outputs["gateway"] == "" {
		t.Fatal("release image outputs are not runtime and gateway")
	}
	linuxAcceptance := false
	for _, step := range workflow.Jobs["sandbox-images"].Steps {
		if strings.Contains(step.Run, "go test ./internal/sandbox -run TestDockerIntegration") {
			linuxAcceptance = true
		}
		if step.With["push"] == true || strings.Contains(step.Run, "docker push") {
			t.Fatal("unaccepted candidate images are published")
		}
	}
	if !linuxAcceptance {
		t.Fatal("candidate images bypass Linux Docker acceptance")
	}
	if !strings.Contains(string(data), "-X github.com/brent/echo/internal/sandbox.RuntimeImage=${{ needs.publish-sandbox-images.outputs.runtime }}") {
		t.Fatal("binary does not embed accepted runtime digest")
	}
}
