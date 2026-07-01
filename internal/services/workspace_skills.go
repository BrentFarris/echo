package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/brent/echo/internal/tools"
	"gopkg.in/yaml.v3"
)

const (
	workspaceSkillFileName         = "SKILL.md"
	workspaceSkillMaxBytes         = 64 * 1024
	workspaceSkillMaxDescription   = 500
	workspaceSkillMaxTriggers      = 12
	workspaceSkillMaxTriggerLength = 120
)

var workspaceSkillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type workspaceSkillsProvider struct {
	workspace Workspace
}

type workspaceSkillFrontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Triggers    []string `yaml:"triggers,omitempty"`
}

func (s *SystemService) workspaceSkillsProvider(workspace Workspace) tools.WorkspaceSkillsProvider {
	return workspaceSkillsProvider{workspace: workspace}
}

func (p workspaceSkillsProvider) SearchWorkspaceSkills(ctx context.Context, request tools.WorkspaceSkillSearchRequest) (tools.WorkspaceSkillSearchResponse, error) {
	return searchWorkspaceSkills(ctx, p.workspace, request)
}

func (p workspaceSkillsProvider) ReadWorkspaceSkill(ctx context.Context, request tools.WorkspaceSkillReadRequest) (tools.WorkspaceSkill, error) {
	return readWorkspaceSkill(ctx, p.workspace, request.ID)
}

func (p workspaceSkillsProvider) RecordWorkspaceSkill(ctx context.Context, request tools.WorkspaceSkillRecordRequest) (tools.WorkspaceSkillRecordResponse, error) {
	if request.Action == "skip" {
		return tools.WorkspaceSkillRecordResponse{
			Action: "skip",
			Reason: strings.TrimSpace(request.Reason),
		}, nil
	}
	return upsertWorkspaceSkill(ctx, p.workspace, request)
}

func readWorkspaceSkill(ctx context.Context, workspace Workspace, id string) (tools.WorkspaceSkill, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return tools.WorkspaceSkill{}, err
	}
	folder, name, err := workspaceSkillIdentity(workspace, id)
	if err != nil {
		return tools.WorkspaceSkill{}, err
	}
	path, err := workspaceSkillExistingPath(folder, name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tools.WorkspaceSkill{}, tools.SafeError{Code: "skill_not_found", Message: "workspace skill was not found"}
		}
		return tools.WorkspaceSkill{}, err
	}
	return loadWorkspaceSkillFile(folder, name, path)
}

func upsertWorkspaceSkill(ctx context.Context, workspace Workspace, request tools.WorkspaceSkillRecordRequest) (tools.WorkspaceSkillRecordResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return tools.WorkspaceSkillRecordResponse{}, err
	}
	folder, ok := workspaceFolderByLabel(workspace, strings.TrimSpace(request.Folder))
	if !ok || folder.Missing {
		return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "folder must name an available workspace folder"}
	}
	name := strings.TrimSpace(request.Name)
	if !workspaceSkillNamePattern.MatchString(name) || len(name) > 64 {
		return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "name must be a lowercase kebab-case slug no longer than 64 characters"}
	}
	description := strings.TrimSpace(request.Description)
	if description == "" || utf8.RuneCountInString(description) > workspaceSkillMaxDescription {
		return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "description is required and must not exceed 500 characters"}
	}
	triggers, err := normalizeWorkspaceSkillTriggers(request.Triggers)
	if err != nil {
		return tools.WorkspaceSkillRecordResponse{}, err
	}
	body := strings.TrimSpace(request.Body)
	if body == "" {
		return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "body is required"}
	}
	if !utf8.ValidString(body) {
		return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "body must be valid UTF-8"}
	}
	data, err := marshalWorkspaceSkill(workspaceSkillFrontmatter{
		Name:        name,
		Description: description,
		Triggers:    triggers,
	}, body)
	if err != nil {
		return tools.WorkspaceSkillRecordResponse{}, err
	}

	existingPath, existingErr := workspaceSkillExistingPath(folder, name)
	created := errors.Is(existingErr, os.ErrNotExist)
	if existingErr != nil && !created {
		return tools.WorkspaceSkillRecordResponse{}, existingErr
	}
	if !created {
		existing, err := loadWorkspaceSkillFile(folder, name, existingPath)
		if err != nil {
			return tools.WorkspaceSkillRecordResponse{}, err
		}
		expected := strings.TrimSpace(request.ExpectedRevision)
		if expected == "" {
			return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "skill_revision_required", Message: "read the existing skill and pass its revision before updating it"}
		}
		if expected != existing.Revision {
			return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "skill_revision_conflict", Message: "the workspace skill changed; read it again before updating"}
		}
		existingData, err := readWorkspaceSkillBytes(existingPath)
		if err != nil {
			return tools.WorkspaceSkillRecordResponse{}, err
		}
		if bytes.Equal(existingData, data) {
			return tools.WorkspaceSkillRecordResponse{
				Action:    "upsert",
				Skill:     &existing,
				Unchanged: true,
			}, nil
		}
	} else if strings.TrimSpace(request.ExpectedRevision) != "" {
		return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "skill_revision_conflict", Message: "the workspace skill no longer exists; create it without an expected revision"}
	}

	target, err := workspaceSkillCachePath(folder, filepath.ToSlash(filepath.Join(name, workspaceSkillFileName)))
	if err != nil {
		return tools.WorkspaceSkillRecordResponse{}, err
	}
	if err := writeWorkspaceSkillFile(target, data); err != nil {
		return tools.WorkspaceSkillRecordResponse{}, err
	}
	skill, err := loadWorkspaceSkillFile(folder, name, target)
	if err != nil {
		return tools.WorkspaceSkillRecordResponse{}, err
	}
	return tools.WorkspaceSkillRecordResponse{
		Action:  "upsert",
		Skill:   &skill,
		Created: created,
	}, nil
}

func workspaceSkillExistingRoot(folder WorkspaceFolder) (string, error) {
	root, err := workspaceFolderAbsolutePath(folder)
	if err != nil {
		return "", err
	}
	cacheRoot := filepath.Join(root, workspaceCacheDirName)
	if err := validateExistingWorkspaceSkillDirectory(cacheRoot, root); err != nil {
		return "", err
	}
	skillsRoot := filepath.Join(cacheRoot, workspaceSkillCacheDirName)
	if err := validateExistingWorkspaceSkillDirectory(skillsRoot, cacheRoot); err != nil {
		return "", err
	}
	return skillsRoot, nil
}

func workspaceSkillExistingPath(folder WorkspaceFolder, name string) (string, error) {
	if !workspaceSkillNamePattern.MatchString(name) || len(name) > 64 {
		return "", tools.SafeError{Code: "invalid_arguments", Message: "skill name is invalid"}
	}
	root, err := workspaceSkillExistingRoot(folder)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, name)
	if err := validateExistingWorkspaceSkillDirectory(dir, root); err != nil {
		return "", err
	}
	path := filepath.Join(dir, workspaceSkillFileName)
	if err := ensureWorkspaceCachePathInside(root, path); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("workspace skill file must be a regular file")
	}
	return path, nil
}

func validateExistingWorkspaceSkillDirectory(dir string, boundary string) error {
	if err := ensureWorkspaceCachePathInside(boundary, dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace skill directory %s must not be a symlink", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace skill path %s is not a directory", dir)
	}
	return nil
}

func workspaceSkillIdentity(workspace Workspace, id string) (WorkspaceFolder, string, error) {
	id = strings.TrimSpace(strings.ReplaceAll(id, "\\", "/"))
	parts := strings.Split(id, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return WorkspaceFolder{}, "", tools.SafeError{Code: "invalid_arguments", Message: "skill id must use <folder-label>/<skill-name>"}
	}
	folder, ok := workspaceFolderByLabel(workspace, parts[0])
	if !ok || folder.Missing {
		return WorkspaceFolder{}, "", tools.SafeError{Code: "skill_not_found", Message: "workspace skill was not found"}
	}
	if !workspaceSkillNamePattern.MatchString(parts[1]) || len(parts[1]) > 64 {
		return WorkspaceFolder{}, "", tools.SafeError{Code: "invalid_arguments", Message: "skill id contains an invalid skill name"}
	}
	return folder, parts[1], nil
}

func loadWorkspaceSkillFile(folder WorkspaceFolder, name string, path string) (tools.WorkspaceSkill, error) {
	data, err := readWorkspaceSkillBytes(path)
	if err != nil {
		return tools.WorkspaceSkill{}, err
	}
	frontmatter, body, err := parseWorkspaceSkill(data)
	if err != nil {
		return tools.WorkspaceSkill{}, err
	}
	if frontmatter.Name != name {
		return tools.WorkspaceSkill{}, fmt.Errorf("frontmatter name must match skill directory %q", name)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return tools.WorkspaceSkill{}, fmt.Errorf("stat workspace skill: %w", err)
	}
	sum := sha256.Sum256(data)
	return tools.WorkspaceSkill{
		WorkspaceSkillSummary: tools.WorkspaceSkillSummary{
			ID:          folder.Label + "/" + name,
			Folder:      folder.Label,
			Name:        name,
			Description: frontmatter.Description,
			Triggers:    append([]string(nil), frontmatter.Triggers...),
		},
		Body:       body,
		Revision:   hex.EncodeToString(sum[:]),
		ModifiedAt: info.ModTime().UTC().Format(timeRFC3339Nano),
	}, nil
}

const timeRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"

func readWorkspaceSkillBytes(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read workspace skill: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, workspaceSkillMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read workspace skill: %w", err)
	}
	if len(data) > workspaceSkillMaxBytes {
		return nil, fmt.Errorf("workspace skill exceeds the %d byte limit", workspaceSkillMaxBytes)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("workspace skill must be valid UTF-8")
	}
	return data, nil
}

func parseWorkspaceSkill(data []byte) (workspaceSkillFrontmatter, string, error) {
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return workspaceSkillFrontmatter{}, "", fmt.Errorf("workspace skill must start with YAML frontmatter")
	}
	end := strings.Index(normalized[4:], "\n---\n")
	if end < 0 {
		return workspaceSkillFrontmatter{}, "", fmt.Errorf("workspace skill frontmatter is not terminated")
	}
	end += 4
	header := normalized[4:end]
	body := strings.TrimSpace(normalized[end+5:])
	var frontmatter workspaceSkillFrontmatter
	decoder := yaml.NewDecoder(strings.NewReader(header))
	decoder.KnownFields(true)
	if err := decoder.Decode(&frontmatter); err != nil {
		return workspaceSkillFrontmatter{}, "", fmt.Errorf("parse workspace skill frontmatter: %w", err)
	}
	frontmatter.Name = strings.TrimSpace(frontmatter.Name)
	frontmatter.Description = strings.TrimSpace(frontmatter.Description)
	if !workspaceSkillNamePattern.MatchString(frontmatter.Name) || len(frontmatter.Name) > 64 {
		return workspaceSkillFrontmatter{}, "", fmt.Errorf("workspace skill name must be a lowercase kebab-case slug")
	}
	if frontmatter.Description == "" || utf8.RuneCountInString(frontmatter.Description) > workspaceSkillMaxDescription {
		return workspaceSkillFrontmatter{}, "", fmt.Errorf("workspace skill description is required and must not exceed 500 characters")
	}
	triggers, err := normalizeWorkspaceSkillTriggers(frontmatter.Triggers)
	if err != nil {
		return workspaceSkillFrontmatter{}, "", err
	}
	frontmatter.Triggers = triggers
	if body == "" {
		return workspaceSkillFrontmatter{}, "", fmt.Errorf("workspace skill body is required")
	}
	return frontmatter, body, nil
}

func normalizeWorkspaceSkillTriggers(values []string) ([]string, error) {
	if len(values) > workspaceSkillMaxTriggers {
		return nil, tools.SafeError{Code: "invalid_arguments", Message: "triggers must not contain more than 12 entries"}
	}
	seen := map[string]bool{}
	triggers := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, tools.SafeError{Code: "invalid_arguments", Message: "triggers must not contain empty entries"}
		}
		if utf8.RuneCountInString(value) > workspaceSkillMaxTriggerLength {
			return nil, tools.SafeError{Code: "invalid_arguments", Message: "each trigger must not exceed 120 characters"}
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		triggers = append(triggers, value)
	}
	return triggers, nil
}

func marshalWorkspaceSkill(frontmatter workspaceSkillFrontmatter, body string) ([]byte, error) {
	header, err := yaml.Marshal(frontmatter)
	if err != nil {
		return nil, fmt.Errorf("marshal workspace skill frontmatter: %w", err)
	}
	data := []byte("---\n" + string(header) + "---\n\n" + strings.TrimSpace(body) + "\n")
	if len(data) > workspaceSkillMaxBytes {
		return nil, tools.SafeError{Code: "skill_too_large", Message: fmt.Sprintf("workspace skill exceeds the %d byte limit", workspaceSkillMaxBytes)}
	}
	return data, nil
}

func writeWorkspaceSkillFile(path string, data []byte) error {
	parent := filepath.Dir(path)
	temp, err := os.CreateTemp(parent, ".skill-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary workspace skill: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(workspaceCacheFilePermission); err != nil {
		temp.Close()
		return fmt.Errorf("set workspace skill permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write temporary workspace skill: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync temporary workspace skill: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary workspace skill: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace workspace skill: %w", err)
	}
	return nil
}
