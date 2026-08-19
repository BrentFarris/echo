package workspacefs

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	ignore "github.com/sabhiram/go-gitignore"
)

const maximumIndexedFiles = 250000

type SearchResult struct {
	Ref           FileRef `json:"ref"`
	Name          string  `json:"name"`
	HostPath      string  `json:"hostPath"`
	ReferencePath string  `json:"referencePath"`
	Kind          string  `json:"kind"`
	Score         int     `json:"score"`
}

type SearchResponse struct {
	Items     []SearchResult `json:"items"`
	Indexing  bool           `json:"indexing"`
	Indexed   int            `json:"indexed"`
	Truncated bool           `json:"truncated"`
}

type indexState struct {
	generation uint64
	building   bool
	entries    []SearchResult
	truncated  bool
	cancel     context.CancelFunc
}

// Index is an asynchronous, in-memory file-name index. It publishes partial
// snapshots while walking so Quick Open can respond before a large workspace
// finishes scanning.
type Index struct {
	service *Service
	mu      sync.RWMutex
	states  map[string]*indexState
	closed  bool
	wg      sync.WaitGroup
}

func (s *Service) Search(workspaceID, query string, limit int) SearchResponse {
	return s.index.Search(workspaceID, query, limit, false)
}

func (s *Service) SearchEntries(workspaceID, query string, limit int, includeDirectories bool) SearchResponse {
	return s.index.Search(workspaceID, query, limit, includeDirectories)
}

func (s *Service) StartIndex(workspaceID string) {
	s.index.Start(workspaceID)
}

func newIndex(service *Service) *Index {
	return &Index{service: service, states: make(map[string]*indexState)}
}

func (i *Index) Close() {
	i.mu.Lock()
	i.closed = true
	for _, state := range i.states {
		if state.cancel != nil {
			state.cancel()
		}
		state.building = false
	}
	i.mu.Unlock()
	i.wg.Wait()
}

func (i *Index) Start(workspaceID string) {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return
	}
	state := i.states[workspaceID]
	if state != nil && state.building {
		i.mu.Unlock()
		return
	}
	if state == nil {
		state = &indexState{}
		i.states[workspaceID] = state
	}
	state.generation++
	generation := state.generation
	state.building = true
	state.entries = nil
	state.truncated = false
	ctx, cancel := context.WithCancel(context.Background())
	state.cancel = cancel
	i.wg.Add(1)
	i.mu.Unlock()
	go func() {
		defer i.wg.Done()
		i.build(ctx, workspaceID, generation)
	}()
}

func (i *Index) Invalidate(workspaceID string) {
	i.mu.Lock()
	if state := i.states[workspaceID]; state != nil && state.cancel != nil {
		state.cancel()
		state.building = false
	}
	i.mu.Unlock()
	i.Start(workspaceID)
}

// ApplyChanges updates the completed Quick Open index in place for ordinary
// file events. Directory and ignore-rule changes fall back to an asynchronous
// rebuild because they can affect an arbitrary subtree.
func (i *Index) ApplyChanges(workspaceID string, changes []Change) {
	if len(changes) == 0 {
		return
	}
	i.mu.RLock()
	state := i.states[workspaceID]
	if state == nil || state.building {
		i.mu.RUnlock()
		i.Invalidate(workspaceID)
		return
	}
	generation := state.generation
	entries := append([]SearchResult(nil), state.entries...)
	truncated := state.truncated
	i.mu.RUnlock()

	roots, err := i.service.resolvedRoots(workspaceID)
	if err != nil {
		i.Invalidate(workspaceID)
		return
	}
	roots = availableResolvedRoots(roots)
	rootByID := make(map[string]resolvedRoot, len(roots))
	matchers := make(map[string]*ignore.GitIgnore, len(roots))
	for _, root := range roots {
		rootByID[root.ID] = root
		if matcher, compileErr := ignore.CompileIgnoreFile(filepath.Join(root.realPath, ".gitignore")); compileErr == nil {
			matchers[root.ID] = matcher
		}
	}

	rebuild := false
	for _, change := range changes {
		cleanPath := strings.TrimPrefix(filepath.ToSlash(change.Ref.Path), "./")
		if cleanPath == ".gitignore" {
			rebuild = true
			break
		}
		prefix := cleanPath + "/"
		filtered := entries[:0]
		for _, entry := range entries {
			if entry.Ref.RootID == change.Ref.RootID && (entry.Ref.Path == cleanPath || strings.HasPrefix(entry.Ref.Path, prefix)) {
				continue
			}
			filtered = append(filtered, entry)
		}
		entries = filtered
		if change.Op == "delete" || change.Op == "rename" {
			continue
		}
		root, ok := rootByID[change.Ref.RootID]
		if !ok || cleanPath == "" || cleanPath == "." {
			rebuild = true
			break
		}
		_, resolved, visible, resolveErr := i.service.resolveEntry(workspaceID, change.Ref, false, false)
		if resolveErr != nil {
			continue
		}
		info, statErr := os.Lstat(resolved)
		if statErr != nil {
			continue
		}
		if info.IsDir() {
			rebuild = true
			break
		}
		if info.Mode()&os.ModeSymlink != 0 || strings.HasPrefix(cleanPath, ".git/") || strings.HasPrefix(cleanPath, ".echo/") {
			continue
		}
		if matcher := matchers[root.ID]; matcher != nil && matcher.MatchesPath(cleanPath) {
			continue
		}
		if len(entries) >= maximumIndexedFiles {
			truncated = true
			continue
		}
		entries = append(entries, SearchResult{
			Ref: change.Ref, Name: filepath.Base(cleanPath), HostPath: visible,
			ReferencePath: root.ReferenceLabel + "/" + cleanPath, Kind: "file",
		})
	}
	if rebuild {
		i.Invalidate(workspaceID)
		return
	}

	i.mu.Lock()
	state = i.states[workspaceID]
	if state != nil && !state.building && state.generation == generation {
		state.entries = entries
		state.truncated = truncated
	}
	i.mu.Unlock()
}

func (i *Index) Search(workspaceID, query string, limit int, includeDirectories bool) SearchResponse {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	i.mu.RLock()
	state := i.states[workspaceID]
	if state == nil {
		i.mu.RUnlock()
		i.Start(workspaceID)
		return SearchResponse{Items: []SearchResult{}, Indexing: true}
	}
	entries := append([]SearchResult(nil), state.entries...)
	building, truncated := state.building, state.truncated
	i.mu.RUnlock()

	query = strings.ToLower(strings.TrimSpace(query))
	matches := make([]SearchResult, 0, min(limit*2, len(entries)))
	indexed := 0
	for _, entry := range entries {
		if entry.Kind == "directory" && !includeDirectories {
			continue
		}
		indexed++
		score := rankSearch(query, entry.Name, entry.ReferencePath)
		if score < 0 {
			continue
		}
		entry.Score = score
		matches = append(matches, entry)
	}
	sort.SliceStable(matches, func(left, right int) bool {
		if matches[left].Score != matches[right].Score {
			return matches[left].Score > matches[right].Score
		}
		return strings.ToLower(matches[left].Ref.Path) < strings.ToLower(matches[right].Ref.Path)
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return SearchResponse{Items: matches, Indexing: building, Indexed: indexed, Truncated: truncated}
}

func (i *Index) build(ctx context.Context, workspaceID string, generation uint64) {
	roots, err := i.service.resolvedRoots(workspaceID)
	if err != nil {
		i.finish(workspaceID, generation, nil, false)
		return
	}
	roots = availableResolvedRoots(roots)
	entries := make([]SearchResult, 0, 4096)
	fileCount := 0
	truncated := false
	for _, root := range roots {
		entries = append(entries, SearchResult{
			Ref: FileRef{RootID: root.ID, Path: ""}, Name: root.Label, HostPath: root.HostPath,
			ReferencePath: root.ReferenceLabel, Kind: "directory",
		})
		var matcher *ignore.GitIgnore
		if compiled, compileErr := ignore.CompileIgnoreFile(filepath.Join(root.realPath, ".gitignore")); compileErr == nil {
			matcher = compiled
		}
		walkErr := filepath.WalkDir(root.realPath, func(current string, item os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return context.Canceled
			default:
			}
			relative, relErr := filepath.Rel(root.realPath, current)
			if relErr != nil {
				return nil
			}
			relative = filepath.ToSlash(relative)
			if item.IsDir() {
				if relative == ".git" || strings.HasPrefix(relative, ".git/") || relative == ".echo" || strings.HasPrefix(relative, ".echo/") {
					return filepath.SkipDir
				}
				if relative != "." && matcher != nil && matcher.MatchesPath(relative+"/") {
					return filepath.SkipDir
				}
				if relative != "." {
					entries = append(entries, SearchResult{
						Ref: FileRef{RootID: root.ID, Path: relative}, Name: item.Name(),
						HostPath:      filepath.Join(root.HostPath, filepath.FromSlash(relative)),
						ReferencePath: root.ReferenceLabel + "/" + relative, Kind: "directory",
					})
				}
				return nil
			}
			if item.Type()&os.ModeSymlink != 0 || (matcher != nil && matcher.MatchesPath(relative)) {
				return nil
			}
			entries = append(entries, SearchResult{
				Ref: FileRef{RootID: root.ID, Path: relative}, Name: item.Name(),
				HostPath:      filepath.Join(root.HostPath, filepath.FromSlash(relative)),
				ReferencePath: root.ReferenceLabel + "/" + relative, Kind: "file",
			})
			fileCount++
			if len(entries)%512 == 0 {
				i.publish(workspaceID, generation, entries, false, true)
			}
			if fileCount >= maximumIndexedFiles {
				truncated = true
				return context.Canceled
			}
			return nil
		})
		if walkErr == context.Canceled {
			if ctx.Err() != nil {
				return
			}
			break
		}
	}
	i.finish(workspaceID, generation, entries, truncated)
}

func (i *Index) publish(workspaceID string, generation uint64, entries []SearchResult, truncated, building bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	state := i.states[workspaceID]
	if state == nil || state.generation != generation {
		return
	}
	state.entries = append([]SearchResult(nil), entries...)
	state.truncated = truncated
	state.building = building
}

func (i *Index) finish(workspaceID string, generation uint64, entries []SearchResult, truncated bool) {
	i.publish(workspaceID, generation, entries, truncated, false)
}

func rankSearch(query, name, filePath string) int {
	if query == "" {
		return 1
	}
	name = strings.ToLower(name)
	filePath = strings.ToLower(filePath)
	switch {
	case name == query:
		return 1000
	case strings.HasPrefix(name, query):
		return 800 - len(name)
	case strings.Contains(name, query):
		return 600 - strings.Index(name, query)
	case strings.HasPrefix(filePath, query):
		return 500 - len(filePath)/10
	case strings.Contains(filePath, query):
		return 350 - strings.Index(filePath, query)/10
	case fuzzyContains(filePath, query):
		return 100 - len(filePath)/20
	default:
		return -1
	}
}

func fuzzyContains(value, query string) bool {
	queryRunes := []rune(query)
	position := 0
	for _, character := range value {
		if position < len(queryRunes) && character == queryRunes[position] {
			position++
		}
	}
	return position == len(queryRunes)
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
