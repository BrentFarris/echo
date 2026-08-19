package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

func TestFilesystemSearchDirectoryOptionAndRootReferenceLabel(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Shutdown(t.Context())
	rootPath := filepath.Join(t.TempDir(), "My Project")
	if err := os.MkdirAll(filepath.Join(rootPath, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspace, err := server.workspaces.Create(workspaces.CreateRequest{Name: "Search", MainPath: rootPath})
	if err != nil {
		t.Fatal(err)
	}

	rootsResponse := doRequest(t, server, http.MethodGet, "/api/workspaces/"+workspace.ID+"/fs/roots")
	if rootsResponse.Code != http.StatusOK || !strings.Contains(rootsResponse.Body.String(), `"referenceLabel":"my-project"`) {
		t.Fatalf("roots response omitted the agent reference label: %d %s", rootsResponse.Code, rootsResponse.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		response := doRequest(t, server, http.MethodGet, "/api/workspaces/"+workspace.ID+"/fs/search?q=docs&limit=10&includeDirectories=true")
		if response.Code != http.StatusOK {
			t.Fatalf("search response: %d %s", response.Code, response.Body.String())
		}
		var envelope struct {
			Data workspacefs.SearchResponse `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if !envelope.Data.Indexing {
			if len(envelope.Data.Items) != 1 || envelope.Data.Items[0].Kind != "directory" || envelope.Data.Items[0].Ref.Path != "docs" {
				t.Fatalf("directory search returned unexpected results: %#v", envelope.Data.Items)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("filesystem search index did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPermanentTrashDeletionRequiresExplicitConfirmation(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Shutdown(t.Context())
	rootPath := t.TempDir()
	workspace, err := server.workspaces.Create(workspaces.CreateRequest{Name: "Trash", MainPath: rootPath})
	if err != nil {
		t.Fatal(err)
	}
	roots, err := server.fs.Roots(workspace.ID)
	if err != nil || len(roots) != 1 {
		t.Fatalf("roots: %#v %v", roots, err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "retained.txt"), []byte("safe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	item, err := server.fs.Trash(workspace.ID, workspacefs.FileRef{RootID: roots[0].ID, Path: "retained.txt"})
	if err != nil {
		t.Fatal(err)
	}

	base := "/api/workspaces/" + workspace.ID + "/fs/trash/" + item.ID
	response := doRequest(t, server, http.MethodDelete, base)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed permanent delete returned %d: %s", response.Code, response.Body.String())
	}
	items, err := server.fs.ListTrash(workspace.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("unconfirmed delete did not preserve the item: %#v %v", items, err)
	}

	response = doRequest(t, server, http.MethodDelete, base+"?confirmed=true")
	if response.Code != http.StatusOK {
		t.Fatalf("confirmed permanent delete returned %d: %s", response.Code, response.Body.String())
	}
}
