// Package workspaceskills manages reusable, workspace-local Echo skills.
package workspaceskills

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
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/brent/echo/internal/tools"
	"gopkg.in/yaml.v3"
)

const (
	FileName         = "SKILL.md"
	maxBytes         = 64 * 1024
	maxDescription   = 500
	maxTriggers      = 12
	maxTriggerLength = 120
	maxCatalogSize   = 256
)

var namePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type frontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Triggers    []string `yaml:"triggers,omitempty"`
}

type catalogEntry struct {
	skill tools.WorkspaceSkill
	score int
}

// Service is a concurrency-safe tools.WorkspaceSkillsProvider for one
// workspace's labeled roots.
type Service struct {
	mu    sync.Mutex
	roots []tools.WorkspaceRoot
}

func New(roots []tools.WorkspaceRoot) *Service {
	return &Service{roots: append([]tools.WorkspaceRoot(nil), roots...)}
}

func (s *Service) SearchWorkspaceSkills(ctx context.Context, request tools.WorkspaceSkillSearchRequest) (tools.WorkspaceSkillSearchResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	query := strings.TrimSpace(request.Query)
	response := tools.WorkspaceSkillSearchResponse{Query: query, Skills: []tools.WorkspaceSkillSummary{}}
	entries, warnings, err := s.catalog(ctx, strings.TrimSpace(request.Folder))
	if err != nil {
		return response, err
	}
	response.Warnings = warnings
	normalized := normalizeSearchText(query)
	tokens := searchTokens(normalized)
	if normalized == "" || len(tokens) == 0 {
		return response, nil
	}
	for i := range entries {
		entries[i].score = scoreSkill(entries[i].skill.WorkspaceSkillSummary, normalized, tokens)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].score != entries[j].score {
			return entries[i].score > entries[j].score
		}
		return entries[i].skill.ID < entries[j].skill.ID
	})
	limit := tools.NormalizeWorkspaceSkillSearchLimit(request.Limit)
	for _, entry := range entries {
		if entry.score <= 0 {
			continue
		}
		response.Skills = append(response.Skills, entry.skill.WorkspaceSkillSummary)
		if len(response.Skills) == limit {
			break
		}
	}
	return response, nil
}

func (s *Service) ReadWorkspaceSkill(ctx context.Context, request tools.WorkspaceSkillReadRequest) (tools.WorkspaceSkill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return tools.WorkspaceSkill{}, err
		}
	}
	root, name, err := s.identity(request.ID)
	if err != nil {
		return tools.WorkspaceSkill{}, err
	}
	path, err := existingPath(root, name)
	if errors.Is(err, os.ErrNotExist) {
		return tools.WorkspaceSkill{}, tools.SafeError{Code: "skill_not_found", Message: "workspace skill was not found"}
	}
	if err != nil {
		return tools.WorkspaceSkill{}, err
	}
	return load(root, name, path)
}

func (s *Service) RecordWorkspaceSkill(ctx context.Context, request tools.WorkspaceSkillRecordRequest) (tools.WorkspaceSkillRecordResponse, error) {
	if request.Action == "skip" {
		return tools.WorkspaceSkillRecordResponse{Action: "skip", Reason: strings.TrimSpace(request.Reason)}, nil
	}
	return s.Upsert(ctx, request)
}

func (s *Service) Upsert(ctx context.Context, request tools.WorkspaceSkillRecordRequest) (tools.WorkspaceSkillRecordResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upsertLocked(ctx, request)
}

// CreateUnique creates a new skill and suffixes name collisions through -100.
func (s *Service) CreateUnique(ctx context.Context, request tools.WorkspaceSkillRecordRequest) (tools.WorkspaceSkillRecordResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, ok := s.rootByLabel(request.Folder)
	if !ok {
		return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "folder must name an available workspace folder"}
	}
	requested := strings.TrimSpace(request.Name)
	if !validName(requested) {
		return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "name must be a lowercase kebab-case slug no longer than 64 characters"}
	}
	for index := 1; index <= 100; index++ {
		candidate := requested
		if index > 1 {
			suffix := fmt.Sprintf("-%d", index)
			base := strings.TrimRight(requested[:min(len(requested), 64-len(suffix))], "-")
			candidate = base + suffix
		}
		if _, err := existingPath(root, candidate); errors.Is(err, os.ErrNotExist) {
			request.Name = candidate
			request.ExpectedRevision = ""
			return s.upsertLocked(ctx, request)
		} else if err != nil {
			return tools.WorkspaceSkillRecordResponse{}, err
		}
	}
	return tools.WorkspaceSkillRecordResponse{}, fmt.Errorf("could not choose a unique workspace skill name")
}

func (s *Service) upsertLocked(ctx context.Context, request tools.WorkspaceSkillRecordRequest) (tools.WorkspaceSkillRecordResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return tools.WorkspaceSkillRecordResponse{}, err
	}
	root, ok := s.rootByLabel(strings.TrimSpace(request.Folder))
	if !ok {
		return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "folder must name an available workspace folder"}
	}
	name := strings.TrimSpace(request.Name)
	if !validName(name) {
		return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "name must be a lowercase kebab-case slug no longer than 64 characters"}
	}
	description := strings.TrimSpace(request.Description)
	if description == "" || utf8.RuneCountInString(description) > maxDescription {
		return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "description is required and must not exceed 500 characters"}
	}
	triggers, err := normalizeTriggers(request.Triggers)
	if err != nil {
		return tools.WorkspaceSkillRecordResponse{}, err
	}
	body := strings.TrimSpace(request.Body)
	if body == "" || !utf8.ValidString(body) {
		return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "invalid_arguments", Message: "body is required and must be valid UTF-8"}
	}
	data, err := marshal(frontmatter{Name: name, Description: description, Triggers: triggers}, body)
	if err != nil {
		return tools.WorkspaceSkillRecordResponse{}, err
	}

	existing, existingErr := existingPath(root, name)
	created := errors.Is(existingErr, os.ErrNotExist)
	if existingErr != nil && !created {
		return tools.WorkspaceSkillRecordResponse{}, existingErr
	}
	if !created {
		current, err := load(root, name, existing)
		if err != nil {
			return tools.WorkspaceSkillRecordResponse{}, err
		}
		expected := strings.TrimSpace(request.ExpectedRevision)
		if expected == "" {
			return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "skill_revision_required", Message: "read the existing skill and pass its revision before updating it"}
		}
		if expected != current.Revision {
			return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "skill_revision_conflict", Message: "the workspace skill changed; read it again before updating"}
		}
		currentData, err := readBytes(existing)
		if err != nil {
			return tools.WorkspaceSkillRecordResponse{}, err
		}
		if bytes.Equal(currentData, data) {
			return tools.WorkspaceSkillRecordResponse{Action: "upsert", Skill: &current, Unchanged: true}, nil
		}
	} else if strings.TrimSpace(request.ExpectedRevision) != "" {
		return tools.WorkspaceSkillRecordResponse{}, tools.SafeError{Code: "skill_revision_conflict", Message: "the workspace skill no longer exists; create it without an expected revision"}
	}

	target, err := writablePath(root, name)
	if err != nil {
		return tools.WorkspaceSkillRecordResponse{}, err
	}
	if err := writeAtomic(target, data); err != nil {
		return tools.WorkspaceSkillRecordResponse{}, err
	}
	skill, err := load(root, name, target)
	if err != nil {
		return tools.WorkspaceSkillRecordResponse{}, err
	}
	return tools.WorkspaceSkillRecordResponse{Action: "upsert", Skill: &skill, Created: created}, nil
}

func (s *Service) catalog(ctx context.Context, folderFilter string) ([]catalogEntry, []string, error) {
	if folderFilter != "" {
		if _, ok := s.rootByLabel(folderFilter); !ok {
			return nil, nil, tools.SafeError{Code: "invalid_arguments", Message: "folder must name an available workspace folder"}
		}
	}
	entries := []catalogEntry{}
	warnings := []string{}
	for _, root := range s.roots {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if folderFilter != "" && !strings.EqualFold(root.Label, folderFilter) {
			continue
		}
		skillsRoot, err := existingRoot(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", root.Label, err))
			continue
		}
		children, err := os.ReadDir(skillsRoot)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: read skills: %v", root.Label, err))
			continue
		}
		if len(children) > maxCatalogSize {
			children = children[:maxCatalogSize]
			warnings = append(warnings, fmt.Sprintf("%s: skill catalog limited to %d entries", root.Label, maxCatalogSize))
		}
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			name := child.Name()
			if !validName(name) {
				continue
			}
			path, err := existingPath(root, name)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s/%s: %v", root.Label, name, err))
				continue
			}
			skill, err := load(root, name, path)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s/%s: %v", root.Label, name, err))
				continue
			}
			entries = append(entries, catalogEntry{skill: skill})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].skill.ID < entries[j].skill.ID })
	return entries, warnings, nil
}

func (s *Service) Summaries(ctx context.Context, limit int) []tools.WorkspaceSkillSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _, err := s.catalog(ctx, "")
	if err != nil {
		return nil
	}
	if limit <= 0 || limit > len(entries) {
		limit = len(entries)
	}
	result := make([]tools.WorkspaceSkillSummary, 0, limit)
	for _, entry := range entries[:limit] {
		result = append(result, entry.skill.WorkspaceSkillSummary)
	}
	return result
}

func (s *Service) rootByLabel(label string) (tools.WorkspaceRoot, bool) {
	for _, root := range s.roots {
		if strings.EqualFold(root.Label, strings.TrimSpace(label)) {
			return root, true
		}
	}
	return tools.WorkspaceRoot{}, false
}

func (s *Service) identity(id string) (tools.WorkspaceRoot, string, error) {
	parts := strings.Split(strings.TrimSpace(strings.ReplaceAll(id, "\\", "/")), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return tools.WorkspaceRoot{}, "", tools.SafeError{Code: "invalid_arguments", Message: "skill id must use <folder-label>/<skill-name>"}
	}
	root, ok := s.rootByLabel(parts[0])
	if !ok {
		return tools.WorkspaceRoot{}, "", tools.SafeError{Code: "skill_not_found", Message: "workspace skill was not found"}
	}
	if !validName(parts[1]) {
		return tools.WorkspaceRoot{}, "", tools.SafeError{Code: "invalid_arguments", Message: "skill id contains an invalid skill name"}
	}
	return root, parts[1], nil
}

func validName(name string) bool { return len(name) <= 64 && namePattern.MatchString(name) }

func existingRoot(root tools.WorkspaceRoot) (string, error) {
	base, err := cleanRoot(root.Path)
	if err != nil {
		return "", err
	}
	echo := filepath.Join(base, ".echo")
	if err := validateDir(echo, base); err != nil {
		return "", err
	}
	skills := filepath.Join(echo, "skills")
	if err := validateDir(skills, echo); err != nil {
		return "", err
	}
	return skills, nil
}

func existingPath(root tools.WorkspaceRoot, name string) (string, error) {
	if !validName(name) {
		return "", tools.SafeError{Code: "invalid_arguments", Message: "skill name is invalid"}
	}
	skills, err := existingRoot(root)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(skills, name)
	if err := validateDir(dir, skills); err != nil {
		return "", err
	}
	path := filepath.Join(dir, FileName)
	if err := ensureInside(skills, path); err != nil {
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

func writablePath(root tools.WorkspaceRoot, name string) (string, error) {
	base, err := cleanRoot(root.Path)
	if err != nil {
		return "", err
	}
	echo := filepath.Join(base, ".echo")
	if err := ensureDir(echo, base); err != nil {
		return "", err
	}
	skills := filepath.Join(echo, "skills")
	if err := ensureDir(skills, echo); err != nil {
		return "", err
	}
	dir := filepath.Join(skills, name)
	if err := ensureDir(dir, skills); err != nil {
		return "", err
	}
	path := filepath.Join(dir, FileName)
	if err := ensureInside(skills, path); err != nil {
		return "", err
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return "", fmt.Errorf("workspace skill file must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, nil
}

func cleanRoot(path string) (string, error) {
	path, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || path == "" {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace root is not a directory")
	}
	return filepath.Clean(path), nil
}

func validateDir(path, boundary string) error {
	if err := ensureInside(boundary, path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("workspace skill directory %s must be a regular directory", path)
	}
	return nil
}

func ensureDir(path, boundary string) error {
	if err := ensureInside(boundary, path); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return validateDir(path, boundary)
}

func ensureInside(boundary, path string) error {
	base, err := filepath.Abs(boundary)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("workspace skill path escapes its workspace folder")
	}
	return nil
}

func load(root tools.WorkspaceRoot, name, path string) (tools.WorkspaceSkill, error) {
	data, err := readBytes(path)
	if err != nil {
		return tools.WorkspaceSkill{}, err
	}
	header, body, err := parse(data)
	if err != nil {
		return tools.WorkspaceSkill{}, err
	}
	if header.Name != name {
		return tools.WorkspaceSkill{}, fmt.Errorf("frontmatter name must match skill directory %q", name)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return tools.WorkspaceSkill{}, err
	}
	sum := sha256.Sum256(data)
	return tools.WorkspaceSkill{
		WorkspaceSkillSummary: tools.WorkspaceSkillSummary{ID: root.Label + "/" + name, Folder: root.Label, Name: name, Description: header.Description, Triggers: append([]string(nil), header.Triggers...)},
		Body:                  body, Revision: hex.EncodeToString(sum[:]), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano),
	}, nil
}

func readBytes(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("workspace skill exceeds the %d byte limit", maxBytes)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("workspace skill must be valid UTF-8")
	}
	return data, nil
}

func parse(data []byte) (frontmatter, string, error) {
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return frontmatter{}, "", fmt.Errorf("workspace skill must start with YAML frontmatter")
	}
	end := strings.Index(normalized[4:], "\n---\n")
	if end < 0 {
		return frontmatter{}, "", fmt.Errorf("workspace skill frontmatter is not terminated")
	}
	end += 4
	var header frontmatter
	decoder := yaml.NewDecoder(strings.NewReader(normalized[4:end]))
	decoder.KnownFields(true)
	if err := decoder.Decode(&header); err != nil {
		return frontmatter{}, "", fmt.Errorf("parse workspace skill frontmatter: %w", err)
	}
	header.Name = strings.TrimSpace(header.Name)
	header.Description = strings.TrimSpace(header.Description)
	if !validName(header.Name) {
		return frontmatter{}, "", fmt.Errorf("workspace skill name must be a lowercase kebab-case slug")
	}
	if header.Description == "" || utf8.RuneCountInString(header.Description) > maxDescription {
		return frontmatter{}, "", fmt.Errorf("workspace skill description is required and must not exceed 500 characters")
	}
	triggers, err := normalizeTriggers(header.Triggers)
	if err != nil {
		return frontmatter{}, "", err
	}
	header.Triggers = triggers
	body := strings.TrimSpace(normalized[end+5:])
	if body == "" {
		return frontmatter{}, "", fmt.Errorf("workspace skill body is required")
	}
	return header, body, nil
}

func normalizeTriggers(values []string) ([]string, error) {
	if len(values) > maxTriggers {
		return nil, tools.SafeError{Code: "invalid_arguments", Message: "triggers must not contain more than 12 entries"}
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, tools.SafeError{Code: "invalid_arguments", Message: "triggers must not contain empty entries"}
		}
		if utf8.RuneCountInString(value) > maxTriggerLength {
			return nil, tools.SafeError{Code: "invalid_arguments", Message: "each trigger must not exceed 120 characters"}
		}
		key := strings.ToLower(value)
		if !seen[key] {
			seen[key] = true
			result = append(result, value)
		}
	}
	return result, nil
}

func marshal(header frontmatter, body string) ([]byte, error) {
	yamlData, err := yaml.Marshal(header)
	if err != nil {
		return nil, err
	}
	data := []byte("---\n" + string(yamlData) + "---\n\n" + strings.TrimSpace(body) + "\n")
	if len(data) > maxBytes {
		return nil, tools.SafeError{Code: "skill_too_large", Message: fmt.Sprintf("workspace skill exceeds the %d byte limit", maxBytes)}
	}
	return data, nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".skill-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace workspace skill: %w", err)
	}
	return nil
}

var stopWords = map[string]bool{"a": true, "an": true, "and": true, "as": true, "at": true, "be": true, "by": true, "do": true, "for": true, "from": true, "how": true, "i": true, "in": true, "is": true, "it": true, "of": true, "on": true, "or": true, "that": true, "the": true, "this": true, "to": true, "we": true, "with": true}

func normalizeSearchText(value string) string {
	var output strings.Builder
	spaced := false
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			output.WriteRune(r)
			spaced = false
		} else if !spaced && output.Len() > 0 {
			output.WriteByte(' ')
			spaced = true
		}
	}
	return strings.TrimSpace(output.String())
}

func searchTokens(value string) map[string]bool {
	result := map[string]bool{}
	for _, token := range strings.Fields(value) {
		if len(token) >= 2 && !stopWords[token] {
			result[token] = true
		}
	}
	return result
}

func scoreSkill(skill tools.WorkspaceSkillSummary, query string, queryTokens map[string]bool) int {
	name := normalizeSearchText(strings.ReplaceAll(skill.Name, "-", " "))
	description := normalizeSearchText(skill.Description)
	triggers := make([]string, 0, len(skill.Triggers))
	for _, trigger := range skill.Triggers {
		triggers = append(triggers, normalizeSearchText(trigger))
	}
	score := 0
	if name == query {
		score += 80
	} else if name != "" && strings.Contains(query, name) {
		score += 35
	}
	if description == query {
		score += 45
	} else if strings.Contains(description, query) {
		score += 20
	}
	nameTokens, descriptionTokens, triggerTokens := searchTokens(name), searchTokens(description), map[string]bool{}
	for _, trigger := range triggers {
		if trigger == query {
			score += 60
		} else if trigger != "" && (strings.Contains(query, trigger) || strings.Contains(trigger, query)) {
			score += 30
		}
		for token := range searchTokens(trigger) {
			triggerTokens[token] = true
		}
	}
	for token := range queryTokens {
		if nameTokens[token] {
			score += 12
		}
		if triggerTokens[token] {
			score += 8
		}
		if descriptionTokens[token] {
			score += 4
		}
	}
	return score
}
