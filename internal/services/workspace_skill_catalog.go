package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/brent/echo/internal/tools"
)

const workspaceSkillCatalogFolderSize = 256

var workspaceSkillSearchStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "as": true, "at": true, "be": true,
	"by": true, "do": true, "for": true, "from": true, "how": true, "i": true,
	"in": true, "is": true, "it": true, "of": true, "on": true, "or": true,
	"that": true, "the": true, "this": true, "to": true, "we": true, "with": true,
}

type workspaceSkillCatalogEntry struct {
	skill tools.WorkspaceSkill
	score int
}

func searchWorkspaceSkills(ctx context.Context, workspace Workspace, request tools.WorkspaceSkillSearchRequest) (tools.WorkspaceSkillSearchResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	query := strings.TrimSpace(request.Query)
	limit := tools.NormalizeWorkspaceSkillSearchLimit(request.Limit)
	response := tools.WorkspaceSkillSearchResponse{
		Query:  query,
		Skills: []tools.WorkspaceSkillSummary{},
	}
	entries, warnings, err := workspaceSkillCatalog(ctx, workspace, strings.TrimSpace(request.Folder))
	if err != nil {
		return response, err
	}
	response.Warnings = warnings
	queryNormalized := normalizeWorkspaceSkillSearchText(query)
	queryTokens := workspaceSkillSearchTokens(queryNormalized)
	if queryNormalized == "" || len(queryTokens) == 0 {
		return response, nil
	}
	for index := range entries {
		entries[index].score = scoreWorkspaceSkill(entries[index].skill.WorkspaceSkillSummary, queryNormalized, queryTokens)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].score != entries[j].score {
			return entries[i].score > entries[j].score
		}
		return entries[i].skill.ID < entries[j].skill.ID
	})
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

func workspaceSkillCatalog(ctx context.Context, workspace Workspace, folderFilter string) ([]workspaceSkillCatalogEntry, []string, error) {
	entries := []workspaceSkillCatalogEntry{}
	warnings := []string{}
	if folderFilter != "" {
		if folder, ok := workspaceFolderByLabel(workspace, folderFilter); !ok || folder.Missing {
			return nil, nil, tools.SafeError{Code: "invalid_arguments", Message: "folder must name an available workspace folder"}
		}
	}
	for _, folder := range workspace.Folders {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if folder.Missing || (folderFilter != "" && folder.Label != folderFilter) {
			continue
		}
		root, err := workspaceSkillExistingRoot(folder)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", folder.Label, err))
			continue
		}
		children, err := os.ReadDir(root)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: read skills: %v", folder.Label, err))
			continue
		}
		if len(children) > workspaceSkillCatalogFolderSize {
			children = children[:workspaceSkillCatalogFolderSize]
			warnings = append(warnings, fmt.Sprintf("%s: skill catalog limited to %d entries", folder.Label, workspaceSkillCatalogFolderSize))
		}
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			name := child.Name()
			if !workspaceSkillNamePattern.MatchString(name) {
				continue
			}
			childPath := filepath.Join(root, name)
			info, err := os.Lstat(childPath)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				if err == nil {
					warnings = append(warnings, fmt.Sprintf("%s/%s: skill directory is invalid", folder.Label, name))
				}
				continue
			}
			path := filepath.Join(childPath, workspaceSkillFileName)
			info, err = os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				warnings = append(warnings, fmt.Sprintf("%s/%s: SKILL.md is invalid", folder.Label, name))
				continue
			}
			skill, err := loadWorkspaceSkillFile(folder, name, path)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s/%s: %v", folder.Label, name, err))
				continue
			}
			entries = append(entries, workspaceSkillCatalogEntry{skill: skill})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].skill.ID < entries[j].skill.ID
	})
	return entries, warnings, nil
}

func scoreWorkspaceSkill(skill tools.WorkspaceSkillSummary, query string, queryTokens map[string]bool) int {
	name := normalizeWorkspaceSkillSearchText(strings.ReplaceAll(skill.Name, "-", " "))
	description := normalizeWorkspaceSkillSearchText(skill.Description)
	triggers := make([]string, 0, len(skill.Triggers))
	for _, trigger := range skill.Triggers {
		triggers = append(triggers, normalizeWorkspaceSkillSearchText(trigger))
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
	for _, trigger := range triggers {
		if trigger == query {
			score += 60
		} else if trigger != "" && (strings.Contains(query, trigger) || strings.Contains(trigger, query)) {
			score += 30
		}
	}
	nameTokens := workspaceSkillSearchTokens(name)
	descriptionTokens := workspaceSkillSearchTokens(description)
	triggerTokens := map[string]bool{}
	for _, trigger := range triggers {
		for token := range workspaceSkillSearchTokens(trigger) {
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

func normalizeWorkspaceSkillSearchText(value string) string {
	var output strings.Builder
	spaced := false
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			output.WriteRune(r)
			spaced = false
			continue
		}
		if !spaced && output.Len() > 0 {
			output.WriteByte(' ')
			spaced = true
		}
	}
	return strings.TrimSpace(output.String())
}

func workspaceSkillSearchTokens(value string) map[string]bool {
	tokens := map[string]bool{}
	for _, token := range strings.Fields(value) {
		if len(token) < 2 || workspaceSkillSearchStopWords[token] {
			continue
		}
		tokens[token] = true
	}
	return tokens
}
