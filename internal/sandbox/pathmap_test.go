package sandbox

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/workspaces"
)

func TestWorkspaceMountsAndPathMapperRoundTrip(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main folder Ω")
	extra := filepath.Join(base, "extra folder")
	for _, directory := range []string{main, extra} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mounts, err := WorkspaceMounts(workspaces.Workspace{MainPath: main, Folders: []string{main, extra}})
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 2 || !mounts[0].Main || mounts[0].GuestPath == mounts[1].GuestPath {
		t.Fatalf("unexpected mounts: %+v", mounts)
	}
	mapper := NewPathMapper(mounts)
	host := filepath.Join(extra, "src", "hello world.go")
	guest, err := mapper.HostToGuest(host)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(guest, mounts[1].GuestPath+"/") || !strings.Contains(guest, "hello world.go") {
		t.Fatalf("unexpected guest path %q", guest)
	}
	roundTrip, err := mapper.GuestToHost(guest)
	if err != nil || filepath.Clean(roundTrip) != filepath.Clean(host) {
		t.Fatalf("round trip %q: %v", roundTrip, err)
	}
	if _, err := mapper.HostToGuest(filepath.Join(base, "outside.txt")); err == nil {
		t.Fatal("outside host path was accepted")
	}
	if _, err := mapper.GuestToHost(mounts[0].GuestPath + "/../../etc/passwd"); err == nil {
		t.Fatal("escaping guest path was accepted")
	}
}

func TestPathMapperRecursivelyTranslatesLSPFileURIs(t *testing.T) {
	root := t.TempDir()
	mount := RootMount{ID: "root-a", HostPath: root, GuestPath: "/workspace/root-a", Main: true}
	mapper := NewPathMapper([]RootMount{mount})
	hostPath := filepath.Join(root, "space Ω.go")
	uriPath := filepath.ToSlash(hostPath)
	if filepath.VolumeName(hostPath) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	hostURI := (&url.URL{Scheme: "file", Path: uriPath}).String()
	input, _ := json.Marshal(map[string]any{"diagnostics": []any{map[string]any{"uri": hostURI, "message": "file:leave-this-text"}}, "plain": "https://example.com/file:x"})
	guestJSON, err := mapper.TranslateJSON(input, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(guestJSON), "workspace/root-a/space") || !strings.Contains(string(guestJSON), "https://example.com/file:x") {
		t.Fatalf("unexpected translated JSON: %s", guestJSON)
	}
	hostJSON, err := mapper.TranslateJSON(guestJSON, false)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if json.Unmarshal(hostJSON, &decoded) != nil {
		t.Fatal("translated JSON is invalid")
	}
	diagnostic := decoded["diagnostics"].([]any)[0].(map[string]any)
	parsed, _ := url.Parse(diagnostic["uri"].(string))
	if !strings.Contains(parsed.Path, "space Ω.go") {
		t.Fatalf("URI did not round trip: %s", parsed)
	}
}
