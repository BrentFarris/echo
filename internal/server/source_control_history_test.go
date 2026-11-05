package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/sourcecontrol"
)

func TestNativeSourceControlHistory(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1 for contained CLI fixtures")
	}
	for _, provider := range []string{"git", "fossil"} {
		t.Run(provider, func(t *testing.T) {
			if _, err := exec.LookPath(provider); err != nil {
				t.Skip(err)
			}
			parent := t.TempDir()
			root := filepath.Join(parent, "checkout")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			run := func(args ...string) string { return fixtureSCM(t, root, provider, args...) }
			write := func(name, contents string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if provider == "git" {
				run("init", "-b", "main")
				run("config", "core.autocrlf", "false")
			} else {
				fixtureSCM(t, parent, "fossil", "init", "--admin-user", "echo-test", "repo.fossil")
				run("open", filepath.Join(parent, "repo.fossil"))
				run("user", "default", "echo-test")
			}
			commit := func(message string) string {
				if provider == "git" {
					run("commit", "-m", message)
					return strings.TrimSpace(run("rev-parse", "HEAD"))
				}
				run("commit", "--nosync", "--no-warnings", "-m", message)
				return strings.Fields(run("timeline", "ancestors", "current", "-n", "1", "--format", "%H"))[0]
			}
			if err := os.MkdirAll(filepath.Join(root, "visible"), 0700); err != nil {
				t.Fatal(err)
			}
			write("visible/old name.txt", "original\n")
			write("visible/deleted.txt", "deleted content\n")
			write("visible/modified.txt", "before\n")
			write("outside.txt", "private\n")
			write("module", "ordinary file\n")
			run("add", ".")
			first := commit("initial files")
			if provider == "git" {
				run("mv", "visible/old name.txt", "visible/新 name.txt")
				run("rm", "visible/deleted.txt")
			} else {
				run("mv", "--hard", "visible/old name.txt", "visible/新 name.txt")
				run("rm", "--hard", "visible/deleted.txt")
			}
			write("visible/modified.txt", "after\n")
			write("visible/added.txt", "added content\n")
			write("visible/binary.bin", "\x00\x01")
			write("visible/large.txt", strings.Repeat("x", (10<<20)+1))
			run("add", ".")
			second := commit("changed files")
			// Dirty working content must never enter a historical diff.
			write("visible/modified.txt", "uncommitted\n")
			if provider == "git" {
				run("stash", "push", "-m", "separate stash review")
				write("visible/modified.txt", "uncommitted\n")
			}
			s := testServer(t)
			bot, err := s.saveBot(domain.Bot{Handle: "history", Roots: []string{root}})
			if err != nil {
				t.Fatal(err)
			}
			repos, err := s.SourceControl.Repositories(t.Context(), bot.ID)
			if err != nil || len(repos) != 1 {
				t.Fatalf("repositories: %+v %v", repos, err)
			}
			repo := repos[0]
			if !slices.Contains(repo.Capabilities, sourcecontrol.CapabilityHistory) {
				t.Fatal("missing history capability")
			}
			base := "/plugins/monaco-editor/bots/" + bot.ID + "/source-control/repositories/" + repo.ID
			response := scmAPI(t, s, "GET", base+"/history?offset=0&limit=1", nil)
			var page sourcecontrol.History
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Commits) != 1 || !page.HasMore || page.NextOffset != 1 {
				t.Fatalf("history page: %d %s", response.Code, response.Body.String())
			}
			if provider == "git" && page.Commits[0].Hash != second {
				t.Fatal("stash commits leaked into history", page)
			}
			if provider == "git" {
				stash, err := s.SourceControl.RevisionDetail(t.Context(), bot.ID, repo.ID, "stash@{0}", "stash")
				if err != nil || len(stash.Files) != 1 {
					t.Fatalf("stash review regression: %+v %v", stash, err)
				}
			}
			next, err := s.SourceControl.History(t.Context(), bot.ID, repo.ID, 1, 1)
			if err != nil || len(next.Commits) != 1 || next.Commits[0].Hash == page.Commits[0].Hash {
				t.Fatalf("next page: %+v %v", next, err)
			}
			for _, query := range []string{"offset=-1", "offset=abc", "limit=0", "limit=101"} {
				if result := scmAPI(t, s, "GET", base+"/history?"+query, nil); result.Code != 400 {
					t.Fatalf("accepted %s: %d", query, result.Code)
				}
			}
			response = scmAPI(t, s, "GET", base+"/detail?kind=commit&ref="+second, nil)
			var detail sourcecontrol.RevisionDetail
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &detail) != nil {
				t.Fatalf("detail: %d %s", response.Code, response.Body.String())
			}
			if detail.Ref != second {
				t.Fatalf("detail lost full hash: %s != %s", detail.Ref, second)
			}
			found := map[string]sourcecontrol.RevisionFile{}
			for _, file := range detail.Files {
				found[file.Path] = file
			}
			for path, status := range map[string]string{"visible/新 name.txt": "R", "visible/deleted.txt": "D", "visible/added.txt": "A", "visible/modified.txt": "M"} {
				if !strings.HasPrefix(found[path].Status, status) {
					t.Fatalf("%s missing %s: %+v", path, status, detail)
				}
			}
			if found["visible/新 name.txt"].OldPath != "visible/old name.txt" {
				t.Fatal("lost rename origin", detail)
			}
			for _, file := range detail.Files {
				diff, err := s.SourceControl.Diff(t.Context(), bot.ID, repo.ID, sourcecontrol.DiffTarget{Kind: "revision", Ref: second, Path: file.Path, OldPath: file.OldPath})
				if err != nil || diff.Editable || len(diff.HunkActions) != 0 {
					t.Fatalf("read-only diff %s: %+v %v", file.Path, diff, err)
				}
				switch file.Path {
				case "visible/modified.txt":
					if diff.Original.Content != "before\n" || diff.Modified.Content != "after\n" {
						t.Fatalf("wrong revision content: %+v", diff)
					}
				case "visible/added.txt":
					if diff.Original.Exists || diff.Modified.Content != "added content\n" {
						t.Fatalf("addition: %+v", diff)
					}
				case "visible/deleted.txt":
					if diff.Modified.Exists || diff.Original.Content != "deleted content\n" {
						t.Fatalf("deletion: %+v", diff)
					}
				case "visible/新 name.txt":
					if diff.Original.Content != "original\n" || diff.Modified.Content != "original\n" {
						t.Fatalf("rename: %+v", diff)
					}
				case "visible/binary.bin":
					if diff.Kind != "binary" {
						t.Fatalf("binary: %+v", diff)
					}
				case "visible/large.txt":
					if diff.Kind != "too-large" {
						t.Fatalf("oversized historical diff: %s", diff.Kind)
					}
				}
			}
			initial, err := s.SourceControl.RevisionDetail(t.Context(), bot.ID, repo.ID, first, "commit")
			if err != nil || len(initial.Files) != 5 {
				t.Fatalf("initial files: %+v %v", initial, err)
			}
			if _, err := s.SourceControl.RevisionDetail(t.Context(), bot.ID, repo.ID, "missing-revision", "commit"); err == nil {
				t.Fatal("accepted invalid revision")
			}
			if _, err := s.SourceControl.Diff(t.Context(), bot.ID, repo.ID, sourcecontrol.DiffTarget{Kind: "revision", Ref: second, Path: "../outside.txt"}); err == nil {
				t.Fatal("accepted traversal")
			}
			if provider == "git" {
				// Merge review uses the first parent, even with an unrelated dirty file.
				run("checkout", "-b", "feature")
				write("feature.txt", "feature\n")
				run("add", "feature.txt")
				feature := commit("feature")
				run("checkout", "main")
				allBranches, err := s.SourceControl.History(t.Context(), bot.ID, repo.ID, 0, 100)
				if err != nil || !slices.ContainsFunc(allBranches.Commits, func(c sourcecontrol.Commit) bool { return c.Hash == feature }) {
					t.Fatalf("unmerged branch omitted: %+v %v", allBranches, err)
				}
				run("merge", "--no-ff", "feature", "-m", "merge feature")
				merge := strings.TrimSpace(run("rev-parse", "HEAD"))
				merged, err := s.SourceControl.RevisionDetail(t.Context(), bot.ID, repo.ID, merge, "commit")
				if err != nil || len(merged.Files) != 1 || merged.Files[0].Path != "feature.txt" {
					t.Fatalf("merge: %+v %v", merged, err)
				}
				run("rm", "module")
				run("update-index", "--add", "--cacheinfo", "160000,"+first+",module")
				sub := commit("submodule")
				diff, err := s.SourceControl.Diff(t.Context(), bot.ID, repo.ID, sourcecontrol.DiffTarget{Kind: "revision", Ref: first, Path: "module"})
				if err != nil || diff.Kind != "text" || diff.Original.Exists || diff.Modified.Content != "ordinary file\n" {
					t.Fatalf("root diff influenced by index: %+v %v", diff, err)
				}
				run("rm", "--cached", "module")
				diff, err = s.SourceControl.Diff(t.Context(), bot.ID, repo.ID, sourcecontrol.DiffTarget{Kind: "revision", Ref: sub, Path: "module"})
				if err != nil || diff.Kind != "submodule" {
					t.Fatalf("historical submodule: %+v %v", diff, err)
				}
			}
			// Parent repository browsing may show commit metadata, but neither
			// changed-file lists nor diffs can expose an out-of-scope rename end.
			if provider == "git" {
				run("mv", "outside.txt", "visible/from-outside.txt")
			} else {
				run("mv", "--hard", "outside.txt", "visible/from-outside.txt")
			}
			crossScope := commit("cross scope rename")
			scopedBot, err := s.saveBot(domain.Bot{Handle: "history-scoped", Roots: []string{filepath.Join(root, "visible")}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Store.Put("source-control-settings", scopedBot.ID, sourceControlSettings{SearchParentRepositories: true}); err != nil {
				t.Fatal(err)
			}
			scopedRepos, err := s.SourceControl.Repositories(t.Context(), scopedBot.ID)
			if err != nil || len(scopedRepos) != 1 {
				t.Fatalf("scoped repos: %+v %v", scopedRepos, err)
			}
			scopedID := scopedRepos[0].ID
			scopedDetail, err := s.SourceControl.RevisionDetail(t.Context(), scopedBot.ID, scopedID, first, "commit")
			if provider == "fossil" {
				// Fossil's native execution boundary does not grant a parent
				// checkout outside configured roots. History must preserve it.
				if err == nil || len(scopedDetail.Files) != 0 {
					t.Fatalf("Fossil parent scope escaped: %+v %v", scopedDetail, err)
				}
				return
			}
			if err != nil || len(scopedDetail.Files) != 3 {
				t.Fatalf("scoped initial: %+v %v", scopedDetail, err)
			}
			scopedDetail, err = s.SourceControl.RevisionDetail(t.Context(), scopedBot.ID, scopedID, crossScope, "commit")
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range scopedDetail.Files {
				if file.OldPath == "outside.txt" || file.Path == "visible/from-outside.txt" {
					t.Fatal("out-of-scope rename exposed", file)
				}
			}
			if _, err := s.SourceControl.Diff(t.Context(), scopedBot.ID, scopedID, sourcecontrol.DiffTarget{Kind: "revision", Ref: crossScope, Path: "visible/from-outside.txt", OldPath: "outside.txt"}); err == nil {
				t.Fatal("out-of-scope original diff accepted")
			}
		})
	}
}

func TestNativeGitEmptyHistory(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	root := t.TempDir()
	fixtureSCM(t, root, "git", "init")
	s := testServer(t)
	bot, err := s.saveBot(domain.Bot{Handle: "empty-history", Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	repos, err := s.SourceControl.Repositories(t.Context(), bot.ID)
	if err != nil || len(repos) != 1 {
		t.Fatalf("empty repo: %+v %v", repos, err)
	}
	history, err := s.SourceControl.History(t.Context(), bot.ID, repos[0].ID, 0, 100)
	if err != nil || len(history.Commits) != 0 || history.HasMore {
		t.Fatalf("empty history: %+v %v", history, err)
	}
}
