package plugins

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func githubArchive(t *testing.T, manifest Manifest, marker string) []byte {
	t.Helper()
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"repository-root/echo-plugin.json":   manifestData,
		"repository-root/ui/main/index.html": []byte("<!doctype html><title>GitHub test</title>"),
		"repository-root/install.sh":         []byte("touch " + marker),
	}
	for _, view := range manifest.Contributes.Views {
		if view.Icon != "" {
			files["repository-root/"+view.Icon] = []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
		}
	}
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, data := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestGitHubStagePinsCommitAndNeverRunsRepositoryScripts(t *testing.T) {
	base := localTestDir(t)
	marker := filepath.Join(base, "script-ran")
	manifest := testUIManifest("github-test")
	archive := githubArchive(t, manifest, marker)
	commit := strings.Repeat("b", 40)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := []byte{}
		contentType := "application/json"
		switch {
		case strings.Contains(request.URL.Path, "/commits/"):
			body = []byte(`{"sha":"` + commit + `"}`)
		case strings.Contains(request.URL.Path, "/tarball/"):
			body = archive
			contentType = "application/gzip"
		default:
			t.Fatalf("unexpected GitHub request: %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
	})}
	manager, err := NewManager(Options{RootDir: filepath.Join(base, "plugins"), HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	stage, err := manager.StageGitHub(context.Background(), Source{Repository: "https://github.com/owner/repository.git", Ref: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if stage.Source.Commit != commit || stage.Source.Repository != "owner/repository" {
		t.Fatalf("GitHub source was not normalized and pinned: %#v", stage.Source)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("repository lifecycle script ran during staging")
	}
	installed, err := manager.Approve(context.Background(), stage.ID, ApprovalRequest{Scope: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if installed.Source.Commit != commit || installed.Digest != stage.Validation.Digest {
		t.Fatalf("approved GitHub snapshot lost its immutable identity: %#v", installed)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("repository lifecycle script ran during installation")
	}
}
