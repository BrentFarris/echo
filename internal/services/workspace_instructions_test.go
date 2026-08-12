package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceSecretsNoticePresent(t *testing.T) {
	root := t.TempDir()
	secretsPath := filepath.Join(root, workspaceCacheDirName, workspaceSecretsDirName)
	if err := os.MkdirAll(secretsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsPath, "token.txt"), []byte("api_key=secret-value"), 0o600); err != nil {
		t.Fatal(err)
	}

	folder := workspaceFolderFromPath(root, nil)
	workspace := Workspace{Folders: []WorkspaceFolder{folder}}

	notice := workspaceSecretsNotice(workspace)
	if notice == "" {
		t.Fatal("expected a secrets notice when .echo/secrets exists")
	}
	if !strings.Contains(notice, secretsPath) {
		t.Fatalf("expected notice to mention the secrets path %q, got %q", secretsPath, notice)
	}
	if strings.Contains(notice, "api_key") || strings.Contains(notice, "secret-value") {
		t.Fatalf("notice must not include secrets contents, got %q", notice)
	}

	prompt := workspaceSystemPrompt("Base prompt.", workspace)
	if !strings.Contains(prompt, "A project secrets directory exists at") {
		t.Fatalf("expected system prompt to include the secrets notice, got %q", prompt)
	}
}

func TestWorkspaceSecretsNoticeAbsent(t *testing.T) {
	root := t.TempDir()
	folder := workspaceFolderFromPath(root, nil)
	workspace := Workspace{Folders: []WorkspaceFolder{folder}}

	if notice := workspaceSecretsNotice(workspace); notice != "" {
		t.Fatalf("expected no secrets notice when directory is absent, got %q", notice)
	}

	prompt := workspaceSystemPrompt("Base prompt.", workspace)
	if strings.Contains(prompt, "secrets directory exists") {
		t.Fatalf("expected system prompt not to mention secrets when absent, got %q", prompt)
	}
}

func TestWorkspaceSecretsNoticeSkipsDisabledOrMissingFolder(t *testing.T) {
	root := t.TempDir()
	secretsPath := filepath.Join(root, workspaceCacheDirName, workspaceSecretsDirName)
	if err := os.MkdirAll(secretsPath, 0o755); err != nil {
		t.Fatal(err)
	}

	disabled := workspaceFolderFromPath(root, nil)
	disabled.UseAgents = false
	if notice := workspaceSecretsNotice(Workspace{Folders: []WorkspaceFolder{disabled}}); notice != "" {
		t.Fatalf("expected no notice for disabled folder, got %q", notice)
	}

	missing := workspaceFolderFromPath(root, nil)
	missing.Missing = true
	if notice := workspaceSecretsNotice(Workspace{Folders: []WorkspaceFolder{missing}}); notice != "" {
		t.Fatalf("expected no notice for missing folder, got %q", notice)
	}
}

func TestWorkspaceSecretsNoticeRequiresDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, workspaceCacheDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	// A regular file named "secrets" should not count as a secrets directory.
	if err := os.WriteFile(filepath.Join(root, workspaceCacheDirName, workspaceSecretsDirName), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}

	folder := workspaceFolderFromPath(root, nil)
	workspace := Workspace{Folders: []WorkspaceFolder{folder}}
	if notice := workspaceSecretsNotice(workspace); notice != "" {
		t.Fatalf("expected no notice for a non-directory named secrets, got %q", notice)
	}
}
