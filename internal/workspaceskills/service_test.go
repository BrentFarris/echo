package workspaceskills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

func fixture(t *testing.T) (Service, domain.Bot, string) {
	t.Helper()
	roots, err := broker.CanonicalRoots([]string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return Service{Files: &broker.Files{}}, domain.Bot{ID: "one", Roots: roots}, roots[0]
}

func request(root, name string) SaveRequest {
	return SaveRequest{Workspace: root, Name: name, Description: "Build and test the project", Triggers: []string{"build project"}, Body: "Run project checks before release."}
}

func save(t *testing.T, s Service, bot domain.Bot, req SaveRequest) SaveResult {
	t.Helper()
	result, err := s.Save(context.Background(), bot, req)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSkillLifecycleAndFreshSearch(t *testing.T) {
	s, bot, root := fixture(t)
	ctx := context.Background()
	listed, err := s.Search(ctx, bot, "", "", 0)
	if err != nil || len(listed.Skills) != 0 {
		t.Fatal(listed, err)
	}
	if _, err := os.Stat(filepath.Join(root, "skills")); !os.IsNotExist(err) {
		t.Fatal("read created skills directory", err)
	}
	req := request(root, "build-project")
	created := save(t, s, bot, req)
	if !created.Created || created.Unchanged || created.Skill.Path != Path(root, req.Name) {
		t.Fatal(created)
	}
	asset := filepath.Join(root, "skills", req.Name, "reference.txt")
	if err := os.WriteFile(asset, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := s.Read(ctx, bot, root, req.Name)
	if err != nil || !reflect.DeepEqual(read, created.Skill) {
		t.Fatal(read, err)
	}
	if _, err := s.Save(ctx, bot, req); err == nil {
		t.Fatal("overwrote existing skill without revision")
	}
	req.Revision = read.Revision
	if result := save(t, s, bot, req); !result.Unchanged {
		t.Fatal(result)
	}
	req.Body = "Updated reusable instructions."
	updated := save(t, s, bot, req)
	if updated.Created || updated.Unchanged || updated.Skill.Revision == read.Revision {
		t.Fatal(updated)
	}
	if _, err := s.Save(ctx, bot, req); err == nil {
		t.Fatal("accepted stale revision")
	}
	if data, err := os.ReadFile(asset); err != nil || string(data) != "keep" {
		t.Fatal("lost supporting file", err)
	}
	data, err := os.ReadFile(read.Path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), "Build and test the project", "Investigate meteorites"))
	if err := os.WriteFile(read.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	listed, err = s.Search(ctx, bot, "", "meteorites", 10)
	if err != nil || len(listed.Skills) != 1 {
		t.Fatal(listed, err)
	}
	encoded, _ := json.Marshal(listed)
	if strings.Contains(string(encoded), "Updated reusable") || strings.Contains(string(encoded), "revision") {
		t.Fatal("search exposed body", string(encoded))
	}
	if err := os.Remove(read.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, bot, req); err == nil {
		t.Fatal("accepted revision for deleted skill")
	}
}

func TestSkillWorkspaceOwnershipAndOrder(t *testing.T) {
	s, bot, first := fixture(t)
	roots, err := broker.CanonicalRoots([]string{first, t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	bot.Roots = roots
	for _, root := range roots {
		save(t, s, bot, request(root, "build-project"))
	}
	ctx := context.Background()
	all, err := s.Search(ctx, bot, "", "build project", 10)
	if err != nil || len(all.Skills) != 2 {
		t.Fatal(all, err)
	}
	bot.Roots = []string{roots[1], roots[0]}
	reordered, err := s.Search(ctx, bot, "", "build project", 10)
	if err != nil || !reflect.DeepEqual(all, reordered) {
		t.Fatal(all, reordered, err)
	}
	other := domain.Bot{ID: "two", Roots: []string{roots[1]}}
	shared, err := s.Search(ctx, other, "", "", 0)
	if err != nil || len(shared.Skills) != 1 || shared.Skills[0].Workspace != roots[1] {
		t.Fatal(shared, err)
	}
	if _, err := s.Read(ctx, other, roots[0], "build-project"); err == nil {
		t.Fatal("cross-workspace read")
	}
	if _, err := s.Search(ctx, other, roots[0], "", 0); err == nil {
		t.Fatal("cross-workspace search")
	}
	// Legacy and nested catalogs are not included.
	for _, dir := range []string{filepath.Join(first, ".echo", "skills", "legacy"), filepath.Join(first, "nested", "skills", "nested")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte("legacy"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := s.Search(ctx, bot, "", "", 0)
	if err != nil || len(listed.Skills) != 2 || len(listed.Warnings) != 0 {
		t.Fatal(listed, err)
	}
}

func TestSkillExplicitWorkspaceAndValidation(t *testing.T) {
	s, bot, root := fixture(t)
	ctx := context.Background()
	unbound := domain.Bot{}
	if _, err := s.Search(ctx, unbound, "", "", 0); err == nil {
		t.Fatal("unbound scan accepted")
	}
	save(t, s, unbound, request(root, "explicit"))
	if got, err := s.Search(ctx, unbound, root, "", 0); err != nil || len(got.Skills) != 1 {
		t.Fatal(got, err)
	}
	for _, path := range []string{"", ".", filepath.Join(root, "missing"), root + string(os.PathSeparator) + ".." + string(os.PathSeparator) + filepath.Base(root), t.TempDir()} {
		if _, err := s.Read(ctx, bot, path, "explicit"); err == nil {
			t.Fatalf("accepted workspace %q", path)
		}
		if _, err := s.Save(ctx, bot, request(path, "invalid")); err == nil {
			t.Fatalf("accepted save workspace %q", path)
		}
	}
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	s.Files.Private = private
	if _, err := s.Search(ctx, unbound, root, "", 0); err == nil {
		t.Fatal("accepted workspace containing private state")
	}
	s.Files.Private = ""
	for _, change := range []func(*SaveRequest){
		func(r *SaveRequest) { r.Name = "../escape" },
		func(r *SaveRequest) { r.Name = "UpperCase" },
		func(r *SaveRequest) { r.Name = strings.Repeat("a", 65) },
		func(r *SaveRequest) { r.Description = "" },
		func(r *SaveRequest) { r.Description = strings.Repeat("é", 501) },
		func(r *SaveRequest) { r.Body = " " },
		func(r *SaveRequest) { r.Body = strings.Repeat("a", maxBytes) },
		func(r *SaveRequest) { r.Body = string([]byte{255}) },
		func(r *SaveRequest) { r.Triggers = make([]string, 13) },
		func(r *SaveRequest) { r.Triggers = []string{strings.Repeat("a", 121)} },
		func(r *SaveRequest) { r.Triggers = []string{" "} },
	} {
		req := request(root, "invalid")
		change(&req)
		if _, err := s.Save(ctx, bot, req); err == nil {
			t.Fatalf("accepted invalid request %+v", req)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "invalid")); !os.IsNotExist(err) {
		t.Fatal("validation created directory", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Save(ctx, bot, request(root, "cancelled")); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestSkillCatalogWarningsLimitsAndRanking(t *testing.T) {
	s, bot, root := fixture(t)
	ctx := context.Background()
	for i := 0; i < 60; i++ {
		save(t, s, bot, request(root, fmt.Sprintf("skill-%03d", i)))
	}
	for _, limit := range []int{0, 3, 100} {
		want := limit
		if limit == 0 {
			want = 10
		}
		if limit > 50 {
			want = 50
		}
		got, err := s.Search(ctx, bot, "", "", limit)
		if err != nil || len(got.Skills) != want {
			t.Fatal(got, err)
		}
	}
	got, err := s.Search(ctx, bot, "", "skill 042", 3)
	if err != nil || len(got.Skills) == 0 || got.Skills[0].Name != "skill-042" {
		t.Fatal(got, err)
	}
	for i, content := range []string{"no frontmatter", "---\nname: mismatch\ndescription: Valid\n---\nBody", strings.Repeat("x", maxBytes+1)} {
		path := Path(root, fmt.Sprintf("bad-%d", i))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	missing := filepath.Join(root, "unavailable")
	bot.Roots = append(bot.Roots, missing)
	got, err = s.Search(ctx, bot, "", "", 10)
	if err != nil || len(got.Skills) != 10 || len(got.Warnings) != 4 {
		t.Fatal(got, err)
	}
	for i := 60; i < 260; i++ {
		if err := os.MkdirAll(filepath.Join(root, "skills", fmt.Sprintf("skill-%03d", i)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	got, err = s.Search(ctx, bot, "", "", 10)
	if err != nil || !strings.Contains(strings.Join(got.Warnings, " "), "limited to 256") {
		t.Fatal(got, err)
	}
}

func TestSkillConcurrentSavesAcrossBots(t *testing.T) {
	s, bot, root := fixture(t)
	ctx := context.Background()
	run := func(req SaveRequest) {
		t.Helper()
		var successes atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				other := bot
				other.ID = fmt.Sprint(i)
				candidate := req
				candidate.Body = fmt.Sprintf("Distinct instructions %d", i)
				if _, err := s.Save(ctx, other, candidate); err == nil {
					successes.Add(1)
				}
			}(i)
		}
		wg.Wait()
		if successes.Load() != 1 {
			t.Fatalf("successful writes: %d", successes.Load())
		}
	}
	req := request(root, "concurrent")
	run(req)
	skill, err := s.Read(ctx, bot, root, req.Name)
	if err != nil {
		t.Fatal(err)
	}
	req.Revision = skill.Revision
	// Ensure no contender is an unchanged save of the winning creation.
	req.Body = "updated"
	if err := os.WriteFile(skill.Path, []byte(strings.ReplaceAll(mustRead(t, skill.Path), "Distinct instructions", "Original instructions")), 0600); err != nil {
		t.Fatal(err)
	}
	skill, err = s.Read(ctx, bot, root, req.Name)
	if err != nil {
		t.Fatal(err)
	}
	req.Revision = skill.Revision
	run(req)
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSkillRejectsEscapingLinksWithOverlappingRoots(t *testing.T) {
	s, bot, parent := fixture(t)
	selected := filepath.Join(parent, "selected")
	out := filepath.Join(parent, "outside")
	for _, dir := range []string{selected, out} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	bot.Roots = append(bot.Roots, selected, out)
	save(t, s, bot, request(out, "secret"))
	if err := os.Symlink(filepath.Join(out, "skills"), filepath.Join(selected, "skills")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := s.Read(context.Background(), bot, selected, "secret"); err == nil {
		t.Fatal("link escaped selected workspace into another allowed root")
	}
	if _, err := s.Save(context.Background(), bot, request(selected, "new")); err == nil {
		t.Fatal("saved outside selected workspace")
	}
	got, err := s.Search(context.Background(), bot, selected, "", 10)
	if err != nil || len(got.Skills) != 0 || len(got.Warnings) == 0 {
		t.Fatal(got, err)
	}
}
