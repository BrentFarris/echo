package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/workspaceskills"
)

func seedSkill(t *testing.T, s *Server, bot domain.Bot, workspace, name, description string) workspaceskills.Skill {
	t.Helper()
	result, err := (workspaceskills.Service{Files: s.Files}).Save(context.Background(), bot, workspaceskills.SaveRequest{Workspace: workspace, Name: name, Description: description, Body: "FULL BODY ONLY AFTER READING"})
	if err != nil {
		t.Fatal(err)
	}
	return result.Skill
}

func TestWorkspaceSkillToolsPersistChangesAndEditorAccess(t *testing.T) {
	s := testServer(t)
	bot, err := s.saveBot(domain.Bot{Handle: "builder", Roots: []string{t.TempDir()}, Plugins: map[string]bool{"monaco-editor": true}})
	if err != nil {
		t.Fatal(err)
	}
	root := bot.Roots[0]
	args, _ := json.Marshal(map[string]any{"workspace": root, "name": "build-project", "description": "Build the project", "body": "Run project checks."})
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []model.Message `json:"messages"`
			Tools    []domain.Tool   `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Messages[len(req.Messages)-1].Role == "tool" {
			stream(w, "Saved reusable guidance.", nil)
			return
		}
		stream(w, "", []domain.ToolCall{{ID: "save", Type: "function", Function: domain.FunctionCall{Name: "workspace_skill_save", Arguments: string(args)}}})
	})
	c := chat(t, s, g)
	d, err := s.send(c.ID, SendRequest{Content: "@builder save useful build guidance", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	messages, err := s.Store.Messages(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var changes []domain.FileChange
	for _, m := range messages {
		changes = append(changes, m.FileChanges...)
	}
	path := workspaceskills.Path(root, "build-project")
	if len(changes) != 1 || !strings.EqualFold(changes[0].Path, path) || changes[0].Operation != "created" {
		t.Fatalf("changes=%+v messages=%+v", changes, messages)
	}
	// Exercise the same filesystem service used by the existing editor routes.
	roots, err := s.EditorFS.Roots(bot.ID)
	if err != nil || len(roots) != 1 {
		t.Fatal(roots, err)
	}
	entries, err := s.EditorFS.List(bot.ID, editorfs.FileRef{RootID: roots[0].ID, Path: "skills/build-project"})
	if err != nil || len(entries) != 1 || entries[0].ReadOnly {
		t.Fatal(entries, err)
	}
	snapshot, err := s.EditorFS.Read(bot.ID, entries[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.ReplaceAll(snapshot.Content, "Run project checks.", "Run all project checks.")
	if _, err := s.EditorFS.Save(bot.ID, editorfs.SaveRequest{Ref: snapshot.Ref, Content: edited, ExpectedRevision: snapshot.Revision}); err != nil {
		t.Fatal(err)
	}
	value, err := s.skillTool(context.Background(), bot, "workspace_skill_read", map[string]any{"workspace": root, "name": "build-project"})
	if err != nil || value.(workspaceskills.Skill).Body != "Run all project checks." {
		t.Fatal(value, err)
	}
	skill := value.(workspaceskills.Skill)
	collector := &fileChangeCollector{}
	ctx := context.WithValue(context.Background(), fileChangesKey{}, collector)
	_, err = s.skillTool(ctx, bot, "workspace_skill_save", map[string]any{"workspace": root, "name": skill.Name, "description": skill.Description, "body": "New stable instructions.", "revision": skill.Revision})
	if err != nil || len(collector.changes) != 1 || collector.changes[0].Operation != "edited" {
		t.Fatal(collector.changes, err)
	}
	old := bot
	bot.Roots = []string{t.TempDir()}
	bot, err = s.saveBot(bot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.skillTool(context.Background(), old, "workspace_skill_read", map[string]any{"workspace": root, "name": skill.Name}); err == nil {
		t.Fatal("accepted stale execution scope")
	}
	if _, err := s.skillTool(context.Background(), bot, "workspace_skill_read", map[string]any{"workspace": root, "name": skill.Name}); err == nil {
		t.Fatal("accepted revoked workspace")
	}
}

func TestWorkspaceSkillAutomaticContext(t *testing.T) {
	s := testServer(t)
	bot, err := s.saveBot(domain.Bot{Handle: "builder", Roots: []string{t.TempDir(), t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"project-build", "project-checks", "project-release", "project-tests"} {
		seedSkill(t, s, bot, bot.Roots[1], name, "Project build instructions")
	}
	seedSkill(t, s, bot, bot.Roots[0], "gardening", "Water houseplants")
	history := []model.Message{{Role: "system", Content: "System"}, {Role: "user", Content: "gardening"}, {Role: "assistant", Content: "done"}, {Role: "user", Content: "project build"}}
	s.addSkillContext(context.Background(), bot, history)
	content := history[0].Content
	if strings.Contains(content, "FULL BODY") || strings.Contains(content, `"name":"gardening"`) || strings.Count(content, `"workspace":`) != 3 || !strings.Contains(content, `"name":"project-build"`) {
		t.Fatal(content)
	}
	unbound := []model.Message{{Role: "system"}, {Role: "user", Content: "project build"}}
	s.addSkillContext(context.Background(), domain.Bot{}, unbound)
	if strings.Contains(unbound[0].Content, "Workspace skill suggestions") || !strings.Contains(unbound[0].Content, "Unbound bots") {
		t.Fatal(unbound)
	}
	// The regular history filter excludes requests addressed to other bots.
	c := chat(t, s, domain.Generation{})
	for _, m := range []domain.Message{
		{ID: domain.ID(), ChatID: c.ID, Role: "user", Recipients: []string{bot.ID}, Content: "project build", CreatedAt: domain.Now()},
		{ID: domain.ID(), ChatID: c.ID, Role: "user", Recipients: []string{"echo"}, Content: "gardening", CreatedAt: domain.Now()},
	} {
		if err := s.Store.AddMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	filtered, err := s.history(bot, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.addSkillContext(context.Background(), bot, filtered)
	if strings.Contains(filtered[0].Content, `"name":"gardening"`) || !strings.Contains(filtered[0].Content, `"name":"project-build"`) {
		t.Fatal(filtered[0].Content)
	}
}

func TestWorkspaceSkillRemoteWorkerIsolation(t *testing.T) {
	home, _ := swarmFixture(t, "desktop", remotePassword)
	worker, host := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, home, host, remotePassword)
	local, err := home.saveBot(domain.Bot{Handle: "builder", Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := worker.saveBot(domain.Bot{Handle: "builder", Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	seedSkill(t, home, local, local.Roots[0], "home-build", "Build at home")
	seedSkill(t, worker, remote, remote.Roots[0], "worker-build", "Build at worker")
	var received string
	attachModel(t, worker, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []model.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Messages[len(req.Messages)-1].Role == "tool" {
			stream(w, "Saved on worker.", nil)
			return
		}
		received = req.Messages[0].Content
		args, _ := json.Marshal(map[string]any{"workspace": remote.Roots[0], "name": "worker-saved", "description": "Build checks", "body": "Check worker builds."})
		stream(w, "", []domain.ToolCall{{ID: "save", Type: "function", Function: domain.FunctionCall{Name: "workspace_skill_save", Arguments: string(args)}}})
	})
	c := chatForSwarm(t, home)
	d, err := home.send(c.ID, SendRequest{Content: "@linux.builder build and save useful guidance"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if !strings.Contains(received, `"name":"worker-build"`) || strings.Contains(received, `"name":"home-build"`) || strings.Contains(received, "FULL BODY") {
		t.Fatal(received)
	}
	if _, err := os.Stat(workspaceskills.Path(remote.Roots[0], "worker-saved")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(local.Roots[0], "skills", "worker-saved")); !os.IsNotExist(err) {
		t.Fatal("skill written at chat home", err)
	}
}
