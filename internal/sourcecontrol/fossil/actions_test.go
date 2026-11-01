package fossil

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/brent/echo/internal/scmexec"
	"github.com/brent/echo/internal/sourcecontrol"
)

func TestCommitScopesWorkspaceMetadataCheck(t *testing.T) {
	for _, metadata := range []string{".echo/workspace.json", ".git/config", "nested/_FOSSIL_", ".fslckout", ".fos", "repository.data"} {
		for _, action := range []string{"commit_selected", "commit_all"} {
			t.Run(action+"/"+metadata, func(t *testing.T) {
				root := t.TempDir()
				state := &repositoryState{
					workspaceID: "workspace", root: root, repository: filepath.Join(root, "repository.data"),
					scopes: []repositoryScope{{rootPath: root}},
				}
				var commits [][]string
				provider := New(nil, nil, &scmexec.Runtime{
					Execute: func(_ context.Context, _ string, request scmexec.Request) (scmexec.Result, error) {
						switch request.Command[1] {
						case "changes":
							return scmexec.Result{Stdout: []byte("EDITED src/main.go\nEDITED other.txt\nEDITED " + metadata + "\n")}, nil
						case "commit":
							commits = append(commits, request.Command)
							return scmexec.Result{}, nil
						default:
							t.Fatalf("unexpected command: %v", request.Command)
							return scmexec.Result{}, nil
						}
					},
				}, t.TempDir())
				t.Cleanup(provider.Close)
				paths, _, err := provider.commit(context.Background(), state, sourcecontrol.ActionRequest{
					Action: action, Message: "selected change", Paths: []string{"src/main.go"},
				})
				if action == "commit_all" {
					var sourceErr *sourcecontrol.Error
					if !errors.As(err, &sourceErr) || sourceErr.Code != "protected_workspace_metadata" {
						t.Fatalf("Commit All error = %v", err)
					}
					if !strings.Contains(sourceErr.Message, "Commit Selected") {
						t.Fatalf("Commit All error has no actionable alternative: %s", sourceErr.Message)
					}
					if len(commits) != 0 {
						t.Fatalf("Commit All ran despite internal files: %v", commits)
					}
					return
				}
				if err != nil {
					t.Fatalf("unselected internal file blocked selected commit: %v", err)
				}
				want := [][]string{{"fossil", "commit", "--nosync", "--no-prompt", "--no-warnings", "-m", "selected change", "./src/main.go"}}
				if !reflect.DeepEqual(commits, want) || !reflect.DeepEqual(paths, []string{"src/main.go"}) {
					t.Fatalf("selected commit paths = %v; commands = %v", paths, commits)
				}
				commits = nil
				if _, _, err := provider.commit(context.Background(), state, sourcecontrol.ActionRequest{
					Action: action, Message: "internal file", Paths: []string{metadata},
				}); !errors.Is(err, sourcecontrol.ErrInvalidPath) || len(commits) != 0 {
					t.Fatalf("explicit internal-file selection was not rejected: commits=%v err=%v", commits, err)
				}
			})
		}
	}
}
