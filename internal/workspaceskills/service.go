package workspaceskills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

type Summary struct {
	Workspace   string   `json:"workspace"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Triggers    []string `json:"triggers,omitempty"`
	Path        string   `json:"path"`
}

type Skill struct {
	Summary
	Body     string `json:"body"`
	Revision string `json:"revision"`
}

type SearchResult struct {
	Skills   []Summary `json:"skills"`
	Warnings []string  `json:"warnings,omitempty"`
}

type SaveRequest struct {
	Workspace   string   `json:"workspace"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Triggers    []string `json:"triggers,omitempty"`
	Body        string   `json:"body"`
	Revision    string   `json:"revision,omitempty"`
}

type SaveResult struct {
	Skill     Skill `json:"skill"`
	Created   bool  `json:"created"`
	Unchanged bool  `json:"unchanged"`
}

type Service struct{ Files *broker.Files }

func Path(workspace, name string) string {
	return filepath.Join(workspace, "skills", name, FileName)
}

func load(root *os.Root, name string) (Skill, error) {
	if !validName(name) {
		return Skill{}, errors.New("skill name must be a lowercase kebab-case slug of at most 64 characters")
	}
	relative := filepath.Join("skills", name, FileName)
	info, err := root.Stat(relative)
	if err != nil {
		return Skill{}, err
	}
	if !info.Mode().IsRegular() {
		return Skill{}, errors.New("skill must be a regular file")
	}
	file, err := root.Open(relative)
	if err != nil {
		return Skill{}, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return Skill{}, err
	}
	if !info.Mode().IsRegular() {
		return Skill{}, errors.New("skill must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return Skill{}, err
	}
	if len(data) > maxBytes || !utf8.Valid(data) {
		return Skill{}, errors.New("skill must be UTF-8 and at most 64 KiB")
	}
	return decode(filepath.Clean(root.Name()), name, data)
}

func decode(workspace, name string, data []byte) (Skill, error) {
	header, body, err := parse(data)
	if err != nil {
		return Skill{}, err
	}
	if header.Name != name {
		return Skill{}, errors.New("frontmatter name must match the skill directory")
	}
	sum := sha256.Sum256(data)
	return Skill{Summary: Summary{Workspace: workspace, Name: name, Description: header.Description, Triggers: header.Triggers, Path: Path(workspace, name)}, Body: body, Revision: hex.EncodeToString(sum[:])}, nil
}

func (s Service) Read(ctx context.Context, bot domain.Bot, workspace, name string) (Skill, error) {
	if err := ctx.Err(); err != nil {
		return Skill{}, err
	}
	root, err := s.Files.OpenWorkspace(bot, workspace)
	if err != nil {
		return Skill{}, err
	}
	defer root.Close()
	return load(root, name)
}

func (s Service) Save(ctx context.Context, bot domain.Bot, request SaveRequest) (SaveResult, error) {
	if err := ctx.Err(); err != nil {
		return SaveResult{}, err
	}
	if !validName(request.Name) || !utf8.ValidString(request.Body) || !utf8.ValidString(request.Description) {
		return SaveResult{}, errors.New("invalid skill name or UTF-8 content")
	}
	data, err := marshal(frontmatter{Name: request.Name, Description: strings.TrimSpace(request.Description), Triggers: request.Triggers}, request.Body)
	if err != nil {
		return SaveResult{}, err
	}
	// Validate before creating any folders or changing an existing file.
	if _, _, err = parse(data); err != nil {
		return SaveResult{}, err
	}
	root, err := s.Files.OpenWorkspace(bot, request.Workspace)
	if err != nil {
		return SaveResult{}, err
	}
	workspace := filepath.Clean(root.Name())
	root.Close()
	created, unchanged, err := s.Files.WriteWorkspaceFile(bot, workspace, filepath.Join("skills", request.Name, FileName), string(data), request.Revision)
	if err != nil {
		return SaveResult{}, err
	}
	skill, err := decode(workspace, request.Name, data)
	return SaveResult{Skill: skill, Created: created, Unchanged: unchanged}, err
}

func (s Service) Search(ctx context.Context, bot domain.Bot, workspace, query string, limit int) (SearchResult, error) {
	result := SearchResult{Skills: []Summary{}}
	explicit := workspace != ""
	roots := append([]string(nil), bot.Roots...)
	if workspace != "" {
		roots = []string{workspace}
	}
	if len(roots) == 0 {
		return result, errors.New("unbound bots must supply an explicit absolute workspace")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	sort.Strings(roots)
	query = normalizeSearchText(strings.TrimSpace(query))
	tokens := searchTokens(query)
	type candidate struct {
		summary Summary
		score   int
	}
	entries := []candidate{}
	seen := map[string]bool{}
	warn := func(message string) {
		if runes := []rune(message); len(runes) > 512 {
			message = string(runes[:512]) + "…"
		}
		if len(result.Warnings) < 32 {
			result.Warnings = append(result.Warnings, message)
		} else if len(result.Warnings) == 32 {
			result.Warnings = append(result.Warnings, "Additional skill warnings omitted")
		}
	}
	for _, workspace := range roots {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		root, err := s.Files.OpenWorkspace(bot, workspace)
		if err != nil {
			if explicit {
				return result, err
			}
			warn(fmt.Sprintf("%s: %v", workspace, err))
			continue
		}
		if seen[root.Name()] {
			root.Close()
			continue
		}
		seen[root.Name()] = true
		err = func() error {
			defer root.Close()
			dir, err := root.Open("skills")
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			defer dir.Close()
			// Bound enumeration as well as file loading. A truncated catalog is
			// explicitly reported; direct reads remain available for omitted skills.
			children, err := dir.ReadDir(maxCatalogSize + 1)
			if err != nil && err != io.EOF {
				return err
			}
			sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
			if len(children) > maxCatalogSize {
				children = children[:maxCatalogSize]
				warn(fmt.Sprintf("%s: skill catalog limited to %d entries", workspace, maxCatalogSize))
			}
			for _, child := range children {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !child.IsDir() && child.Type()&os.ModeSymlink == 0 {
					continue
				}
				skill, err := load(root, child.Name())
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					warn(fmt.Sprintf("%s/%s: %v", workspace, child.Name(), err))
					continue
				}
				score := 0
				if query != "" {
					score = scoreSkill(skill.Summary, query, tokens)
					if score <= 0 {
						continue
					}
				}
				entries = append(entries, candidate{skill.Summary, score})
			}
			return nil
		}()
		if err != nil {
			warn(fmt.Sprintf("%s: %v", workspace, err))
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].score != entries[j].score {
			return entries[i].score > entries[j].score
		}
		if entries[i].summary.Workspace != entries[j].summary.Workspace {
			return entries[i].summary.Workspace < entries[j].summary.Workspace
		}
		return entries[i].summary.Name < entries[j].summary.Name
	})
	for _, entry := range entries[:min(limit, len(entries))] {
		result.Skills = append(result.Skills, entry.summary)
	}
	return result, nil
}
