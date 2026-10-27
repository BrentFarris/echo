package broker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/domain"
)

const ReferenceLimit = 200
const referenceIndexLimit = 250000
const referenceRefreshInterval = 5 * time.Second

type ReferenceStatus struct{ Indexing, Incomplete bool }
type referenceTree struct {
	entries                           []referenceCandidate
	building, incomplete, invalidated bool
	finished, used                    time.Time
}

// ReferenceIndex caches names only, on demand for explicit workspace roots.
// It never indexes an unrestricted bot's whole disk. Published entries are
// immutable; a refresh retains the previous snapshot until the new one is ready.
type ReferenceIndex struct {
	files  *Files
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	wg     sync.WaitGroup
	roots  map[string]*referenceTree
}

func NewReferenceIndex(ctx context.Context, files *Files) *ReferenceIndex {
	ctx, cancel := context.WithCancel(ctx)
	return &ReferenceIndex{files: files, ctx: ctx, cancel: cancel, roots: map[string]*referenceTree{}}
}

func (i *ReferenceIndex) Close() { i.mu.Lock(); i.cancel(); i.mu.Unlock(); i.wg.Wait() }

func (i *ReferenceIndex) Invalidate(paths []string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for root, tree := range i.roots {
		for _, path := range paths {
			if Within(root, path) || Within(path, root) {
				tree.invalidated = true
				break
			}
		}
	}
}

func normalizeReference(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimPrefix(value, "./")
	return strings.TrimRight(strings.ToLower(value), "/")
}

type referenceCandidate struct {
	entry                         Entry
	name, path, relative, labeled string
}

func referenceName(entry Entry, root string) referenceCandidate {
	relative, _ := filepath.Rel(root, entry.Path)
	return referenceCandidate{entry: entry, name: strings.ToLower(entry.Name), path: normalizeReference(entry.Path), relative: normalizeReference(relative), labeled: normalizeReference(filepath.Join(filepath.Base(root), relative))}
}

type referenceQuery struct {
	text     string
	absolute bool
	terms    []string
	letters  []rune
}

func referencePattern(query string) referenceQuery {
	normalized := normalizeReference(query)
	return referenceQuery{text: normalized, absolute: filepath.IsAbs(strings.ReplaceAll(query, "\\", string(filepath.Separator))), terms: strings.Fields(normalized), letters: []rune(normalized)}
}

func referenceScore(query string, entry Entry, root string) int {
	return referencePattern(query).score(referenceName(entry, root))
}

func (q referenceQuery) score(candidate referenceCandidate) int {
	query, name, path, relative, labeled := q.text, candidate.name, candidate.path, candidate.relative, candidate.labeled
	depth := strings.Count(relative, "/")
	if query == "" {
		score := 1000 - depth*10
		if candidate.entry.Directory {
			score += 5
		}
		return score
	}
	if query == path || query == relative || query == labeled {
		return 11000
	}
	if query == name {
		return 10000
	}
	if strings.HasPrefix(name, query) {
		return 8000 - min(len(name), 1000)
	}
	if at := strings.Index(name, query); at >= 0 {
		return 6000 - min(at, 1000)
	}
	if strings.HasPrefix(relative, query) || strings.HasPrefix(labeled, query) || (q.absolute && strings.HasPrefix(path, query)) {
		return 5000 - min(len(relative), 1000)
	}
	if strings.Contains(relative, query) || strings.Contains(labeled, query) || (q.absolute && strings.Contains(path, query)) {
		return 3500 - min(len(relative), 1000)
	}
	terms := q.terms
	if len(terms) > 1 {
		all := true
		for _, term := range terms {
			if !strings.Contains(labeled, term) && !strings.Contains(name, term) {
				all = false
				break
			}
		}
		if all {
			return 2000 - min(len(relative), 500)
		}
	}
	letters := q.letters
	position := 0
	for _, char := range labeled {
		if position < len(letters) && char == letters[position] {
			position++
		}
	}
	if position == len(letters) {
		return 1000 - min(len(relative), 500)
	}
	return -1
}

func SortReferences(entries []Entry) {
	sort.Slice(entries, func(a, b int) bool {
		if entries[a].Score != entries[b].Score {
			return entries[a].Score > entries[b].Score
		}
		if entries[a].Directory != entries[b].Directory {
			return entries[a].Directory
		}
		return strings.ToLower(entries[a].Path) < strings.ToLower(entries[b].Path)
	})
}

// Search ranks the entire available index before limiting results. The display
// limit therefore cannot bury an exact match behind the first traversal matches.
func (i *ReferenceIndex) Search(ctx context.Context, bot domain.Bot, root, query string) ([]Entry, ReferenceStatus, error) {
	root = filepath.Clean(root)
	if !i.files.Allowed(bot, root) {
		return nil, ReferenceStatus{}, errors.New("workspace is unavailable or outside this bot's folders")
	}
	i.mu.Lock()
	if i.ctx.Err() != nil {
		i.mu.Unlock()
		return nil, ReferenceStatus{}, i.ctx.Err()
	}
	now := time.Now()
	for key, tree := range i.roots {
		if !tree.building && now.Sub(tree.used) > time.Minute {
			delete(i.roots, key)
		}
	}
	tree := i.roots[root]
	if tree == nil {
		tree = &referenceTree{}
		i.roots[root] = tree
	}
	tree.used = now
	if !tree.building && (tree.finished.IsZero() || tree.invalidated || now.Sub(tree.finished) >= referenceRefreshInterval) {
		tree.building = true
		tree.invalidated = false
		i.wg.Add(1)
		go i.build(root, tree, len(tree.entries) == 0)
	}
	entries := tree.entries
	status := ReferenceStatus{Indexing: tree.building, Incomplete: tree.incomplete}
	i.mu.Unlock()
	// A typed path can find a just-created entry or an intentionally unindexed
	// dependency folder immediately, even while a large workspace is scanning.
	direct, err := i.files.DirectReferences(ctx, bot, root, query)
	if err != nil {
		return nil, status, err
	}
	found := map[string]Entry{}
	pattern := referencePattern(query)
	for n, candidate := range entries {
		if n%256 == 0 && ctx.Err() != nil {
			return nil, status, ctx.Err()
		}
		entry := candidate.entry
		entry.Score = pattern.score(candidate)
		if entry.Score >= 0 {
			found[changePath(entry.Path)] = entry
		}
	}
	for _, entry := range direct {
		found[changePath(entry.Path)] = entry
	}
	out := make([]Entry, 0, len(found))
	for _, entry := range found {
		out = append(out, entry)
	}
	SortReferences(out)
	// Recheck live permissions and existence before emitting cached names. Keep
	// checking beyond stale candidates so removals cannot crowd out valid hits.
	return i.files.FilterReferences(ctx, bot, out), status, ctx.Err()
}

// Keep one root handle per grant during a search. Reopening every root and
// resolving every ancestor of every result dominated picker latency on Windows.
// Root.Stat still checks each live path and rejects junction/symlink escapes.
type referenceAccess struct {
	files         *Files
	bot           domain.Bot
	roots         map[string]*os.Root
	privateInside map[string]bool
}

func newReferenceAccess(files *Files, bot domain.Bot) *referenceAccess {
	return &referenceAccess{files: files, bot: bot, roots: map[string]*os.Root{}, privateInside: map[string]bool{}}
}

func (a *referenceAccess) close() {
	for _, root := range a.roots {
		if root != nil {
			root.Close()
		}
	}
}

func (a *referenceAccess) allowed(path string) bool {
	if a.files.Private != "" && Within(a.files.Private, path) {
		return false
	}
	roots := a.bot.Roots
	if len(roots) == 0 {
		roots = []string{filepath.VolumeName(path) + string(os.PathSeparator)}
	}
	for _, scope := range roots {
		if !Within(scope, path) {
			continue
		}
		root, known := a.roots[scope]
		if !known {
			var err error
			root, _, err = a.files.resolve(a.bot, scope)
			if err == nil {
				real, err := filepath.EvalSymlinks(root.Name())
				if err != nil || (a.files.Private != "" && Within(a.files.Private, real)) {
					root.Close()
					root = nil
				} else {
					a.privateInside[scope] = a.files.Private != "" && Within(real, a.files.Private)
				}
			}
			a.roots[scope] = root
		}
		if root == nil {
			return false
		}
		if a.privateInside[scope] {
			// An unrestricted/root-wide grant can contain Echo's private state.
			// Resolve aliases as well as lexical paths before exposing a name.
			real, err := filepath.EvalSymlinks(path)
			if err != nil || Within(a.files.Private, real) {
				return false
			}
		}
		relative, err := filepath.Rel(root.Name(), path)
		if err != nil {
			return false
		}
		_, err = root.Stat(relative)
		return err == nil
	}
	return false
}

func (f *Files) FilterReferences(ctx context.Context, bot domain.Bot, entries []Entry) []Entry {
	access := newReferenceAccess(f, bot)
	defer access.close()
	valid := []Entry{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			break
		}
		if access.allowed(entry.Path) {
			valid = append(valid, entry)
		}
		if len(valid) == ReferenceLimit {
			break
		}
	}
	return valid
}

func (i *ReferenceIndex) build(path string, tree *referenceTree, publishPartial bool) {
	defer i.wg.Done()
	entries := []referenceCandidate{}
	incomplete := false
	defer func() {
		i.mu.Lock()
		tree.entries = entries
		tree.building = false
		tree.incomplete = incomplete
		tree.finished = time.Now()
		i.mu.Unlock()
	}()
	root, relative, err := i.files.resolve(domain.Bot{Roots: []string{path}}, path)
	if err != nil {
		incomplete = true
		return
	}
	defer root.Close()
	queue := []string{relative}
	entries = append(entries, referenceName(Entry{Path: path, Name: filepath.Base(path), Directory: true}, path))
	for next := 0; next < len(queue); next++ {
		if i.ctx.Err() != nil {
			incomplete = true
			return
		}
		directory := queue[next]
		dir, err := root.Open(directory)
		if err != nil {
			incomplete = true
			continue
		}
		for {
			children, readErr := dir.ReadDir(256)
			for _, child := range children {
				if i.ctx.Err() != nil || len(entries) >= referenceIndexLimit {
					dir.Close()
					incomplete = true
					return
				}
				rel := filepath.Join(directory, child.Name())
				absolute := filepath.Join(root.Name(), rel)
				if i.files.Private != "" && Within(i.files.Private, absolute) {
					continue
				}
				isDir := child.IsDir()
				if child.Type()&os.ModeSymlink != 0 {
					info, err := root.Stat(rel)
					if err != nil || !i.files.Allowed(domain.Bot{Roots: []string{path}}, absolute) {
						continue
					}
					isDir = info.IsDir()
				}
				entries = append(entries, referenceName(Entry{Path: absolute, Name: child.Name(), Directory: isDir}, path))
				if isDir && child.Type()&os.ModeSymlink == 0 && !ignoredReferenceDirectory(child.Name()) {
					queue = append(queue, rel)
				}
			}
			if publishPartial {
				i.mu.Lock()
				tree.entries = entries
				i.mu.Unlock()
			}
			if readErr != nil {
				if readErr != io.EOF {
					incomplete = true
				}
				break
			}
		}
		dir.Close()
	}
}

func ignoredReferenceDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".hg", ".svn", "node_modules", ".cache", ".echo":
		return true
	}
	return false
}

// DirectReferences only enumerates the parent of a typed path. It is also the
// lazy path-completion mode for unrestricted searches outside known workspaces.
func (f *Files) DirectReferences(ctx context.Context, bot domain.Bot, root, query string) ([]Entry, error) {
	query = strings.TrimSpace(strings.ReplaceAll(query, "\\", string(filepath.Separator)))
	candidate := filepath.FromSlash(query)
	if !filepath.IsAbs(candidate) {
		prefix := filepath.Base(root) + string(filepath.Separator)
		if strings.EqualFold(candidate, filepath.Base(root)) {
			candidate = "."
		} else if strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(prefix)) {
			candidate = candidate[len(prefix):]
		}
		candidate = filepath.Join(root, candidate)
	}
	out := []Entry{}
	add := func(path string, info os.FileInfo) {
		entry := Entry{Path: path, Name: filepath.Base(path), Directory: info.IsDir()}
		entry.Score = referenceScore(query, entry, root)
		if entry.Score >= 0 {
			out = append(out, entry)
		}
	}
	r, relative, err := f.resolve(bot, candidate)
	if err != nil {
		return out, nil
	}
	defer r.Close()
	info, statErr := r.Stat(relative)
	if statErr == nil {
		add(candidate, info)
	}
	parent := filepath.Dir(relative)
	if statErr == nil && info.IsDir() && (query == "" || strings.HasSuffix(query, string(filepath.Separator)) || strings.HasSuffix(query, "/")) {
		parent = relative
	}
	if parent == ".." {
		return out, nil
	}
	dir, err := r.Open(parent)
	if err != nil {
		return out, nil
	}
	defer dir.Close()
	for {
		children, readErr := dir.ReadDir(256)
		for _, child := range children {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			path := filepath.Join(r.Name(), parent, child.Name())
			entry := Entry{Path: path, Name: child.Name(), Directory: child.IsDir()}
			entry.Score = referenceScore(query, entry, root)
			if entry.Score < 0 {
				continue
			}
			if child.Type()&os.ModeSymlink != 0 {
				info, err := r.Stat(filepath.Join(parent, child.Name()))
				if err != nil {
					continue
				}
				entry.Directory = info.IsDir()
			}
			out = append(out, entry)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	SortReferences(out)
	return f.FilterReferences(ctx, bot, out), ctx.Err()
}
