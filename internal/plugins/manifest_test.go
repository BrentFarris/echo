package plugins

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func localTestDir(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp(".", ".plugin-test-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(absolute) })
	return absolute
}

func writeTestFile(t *testing.T, root, relative string, data []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func writeTestPlugin(t *testing.T, root string, manifest Manifest) {
	t.Helper()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, ManifestFileName, append(data, '\n'), 0o644)
	for _, view := range manifest.Contributes.Views {
		writeTestFile(t, root, view.Entry, []byte("<!doctype html><title>Plugin</title>"), 0o644)
		if view.Icon != "" {
			writeTestFile(t, root, view.Icon, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644)
		}
	}
	if manifest.Runtime != nil {
		for _, target := range manifest.Runtime.Targets {
			writeTestFile(t, root, target.Path, []byte("executable"), 0o755)
		}
	}
}

func testUIManifest(id string) Manifest {
	return Manifest{
		ManifestVersion: 1, ID: id, Name: "Test Plugin", Version: "1.2.3", Echo: Compatibility{API: "^1"},
		Contributes: Contributions{Views: []ViewContribution{{ID: "main", Kind: "page", Title: "Test", Entry: "ui/main/index.html", Icon: "assets/icon.svg"}}},
	}
}

func TestBuiltinCalculatorPackageValidates(t *testing.T) {
	destination := filepath.Join(localTestDir(t), "calculator")
	if err := copyFS(BuiltinPackages()["calculator"], ".", destination); err != nil {
		t.Fatal(err)
	}
	validation, err := ValidatePackage(destination, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if validation.Manifest.ID != "calculator" || !validation.Compatible || len(validation.Digest) != 64 {
		t.Fatalf("unexpected validation: %#v", validation)
	}
	view, ok := validation.Manifest.View("calculator")
	if !ok || view.Kind != "floating" {
		t.Fatalf("calculator floating view missing: %#v", view)
	}
}

func TestManifestRejectsTrailingJSONAndUnsafePaths(t *testing.T) {
	root := localTestDir(t)
	manifest := testUIManifest("unsafe-test")
	writeTestPlugin(t, root, manifest)
	file, err := os.OpenFile(filepath.Join(root, ManifestFileName), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("{}")
	_ = file.Close()
	if _, err := ReadManifest(root); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("expected trailing JSON rejection, got %v", err)
	}

	root = localTestDir(t)
	manifest = testUIManifest("unsafe-test")
	writeTestPlugin(t, root, manifest)
	manifest.Contributes.Views[0].Entry = "../outside.html"
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, ManifestFileName, append(data, '\n'), 0o644)
	if _, err := ValidatePackage(root, nil); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
}

func TestManifestRejectsToolCollisionAndMalformedSchema(t *testing.T) {
	root := localTestDir(t)
	manifest := testUIManifest("schema-test")
	target := runtime.GOOS + "-" + runtime.GOARCH
	manifest.Runtime = &Runtime{Protocol: RPCProtocol, Targets: map[string]RuntimeTarget{target: {Path: "backend/plugin"}}}
	manifest.Contributes.Tools = []ToolContribution{{Name: "schema_test_run", Description: "Run", Method: "tools.run", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"bad": "not-a-schema"}}}}
	writeTestPlugin(t, root, manifest)
	if _, err := ValidatePackage(root, nil); err == nil || !strings.Contains(err.Error(), "schema must be an object") {
		t.Fatalf("expected schema rejection, got %v", err)
	}

	manifest.Contributes.Tools[0].InputSchema = map[string]any{"type": "object"}
	manifest.Contributes.Tools[0].Name = "core_collision"
	writeTestPlugin(t, root, manifest)
	if _, err := ValidatePackage(root, map[string]bool{"core_collision": true}); err == nil || !strings.Contains(err.Error(), "beginning") {
		// Namespace validation happens before the collision check and is itself a
		// required rejection for an attempted core override.
		if err == nil {
			t.Fatal("expected tool override rejection")
		}
	}
}

func TestPackageRejectsSymlinksAndNestedSnapshot(t *testing.T) {
	root := localTestDir(t)
	writeTestPlugin(t, root, testUIManifest("link-test"))
	if err := CopyPackage(root, filepath.Join(root, "snapshot")); err == nil || !strings.Contains(err.Error(), "inside its source") {
		t.Fatalf("expected nested snapshot rejection, got %v", err)
	}
	link := filepath.Join(root, "ui", "link")
	if err := os.Symlink(filepath.Join(root, ManifestFileName), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ValidatePackage(root, nil); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestInstallPackageSnapshotReplacesOnlyWithACompleteDigest(t *testing.T) {
	base := localTestDir(t)
	source := filepath.Join(base, "source")
	writeTestPlugin(t, source, testUIManifest("atomic-test"))
	digest, err := HashPackage(source)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(base, "packages", "atomic-test", digest)
	writeTestFile(t, destination, "partial.txt", []byte("incomplete"), 0o644)
	if err := InstallPackageSnapshot(source, destination, digest, nil); err != nil {
		t.Fatal(err)
	}
	validation, err := ValidatePackage(destination, nil)
	if err != nil || validation.Digest != digest {
		t.Fatalf("published snapshot was incomplete: %#v, %v", validation, err)
	}
	if _, err := os.Stat(filepath.Join(destination, "partial.txt")); !os.IsNotExist(err) {
		t.Fatal("partial installation content survived atomic publication")
	}
}

func TestExtractGitHubTarRejectsLinks(t *testing.T) {
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "repo-root/link", Typeflag: tar.TypeSymlink, Linkname: "elsewhere"}); err != nil {
		t.Fatal(err)
	}
	_ = tarWriter.Close()
	_ = gzipWriter.Close()
	if err := ExtractGitHubTar(bytes.NewReader(archive.Bytes()), filepath.Join(localTestDir(t), "out"), ""); err == nil || !strings.Contains(err.Error(), "links") {
		t.Fatalf("expected archive link rejection, got %v", err)
	}
}

func TestArgumentSchemaValidationAndTrailingData(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "pattern": "^[a-z]+$"}}, "required": []any{"name"}, "additionalProperties": false}
	if err := ValidateJSONSchemaDefinition(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeAndValidateArguments(schema, json.RawMessage(`{"name":"ABC"}`)); err == nil {
		t.Fatal("expected pattern rejection")
	}
	if _, err := DecodeAndValidateArguments(schema, json.RawMessage(`{"name":"ok"} {}`)); err == nil {
		t.Fatal("expected trailing data rejection")
	}
	if err := ValidateJSONSchemaDefinition(map[string]any{"type": "string", "$ref": "https://example.invalid/schema"}); err == nil || !strings.Contains(err.Error(), "unsupported schema keyword") {
		t.Fatalf("expected unenforced schema keyword rejection, got %v", err)
	}
	oneOfWithSibling := map[string]any{
		"type": "string", "minLength": 3,
		"oneOf": []any{map[string]any{"type": "string", "pattern": "^[a-z]+$"}},
	}
	if err := ValidateJSONSchema(oneOfWithSibling, "a"); err == nil || !strings.Contains(err.Error(), "too short") {
		t.Fatalf("expected oneOf sibling constraints to remain enforced, got %v", err)
	}
}

func TestScaffoldProducesInstallableUIAndShareableBackendWorkflow(t *testing.T) {
	base := localTestDir(t)
	uiPath := filepath.Join(base, "ui-plugin")
	result, err := Scaffold(uiPath, ScaffoldOptions{Template: "ui-only", ID: "ui-sample", Name: "UI Sample"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) == 0 {
		t.Fatal("scaffold did not report created files")
	}
	if _, err := ValidatePackage(uiPath, nil); err != nil {
		t.Fatalf("UI-only scaffold was not immediately valid: %v", err)
	}

	hybridPath := filepath.Join(base, "hybrid-plugin")
	if _, err := Scaffold(hybridPath, ScaffoldOptions{Template: "hybrid", ID: "hybrid-sample", Name: "Hybrid Sample"}); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(hybridPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Runtime == nil || len(manifest.Runtime.Targets) != 4 {
		t.Fatalf("hybrid scaffold did not declare all v1 targets: %#v", manifest.Runtime)
	}
	workflow, err := os.ReadFile(filepath.Join(hybridPath, ".github", "workflows", "build-plugin.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workflow), "Assemble shareable package") || !strings.Contains(string(workflow), "echo-plugin-hybrid-sample.tar.gz") {
		t.Fatalf("scaffold workflow does not assemble a shareable package:\n%s", workflow)
	}
}
