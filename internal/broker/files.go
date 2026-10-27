package broker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/brent/echo/internal/domain"
)

type Files struct {
	Private string
	mu      sync.Mutex
}
type Entry struct {
	Path        string   `json:"path"`
	Name        string   `json:"name"`
	Directory   bool     `json:"directory"`
	AllowedBots []string `json:"allowedBots,omitempty"`
	Score       int      `json:"-"`
}

func Within(root, path string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(os.PathSeparator)) && !filepath.IsAbs(r)
}
func CanonicalRoots(roots []string) ([]string, error) {
	out := []string{}
	for _, r := range roots {
		a, e := filepath.Abs(r)
		if e != nil {
			return nil, e
		}
		a, e = filepath.EvalSymlinks(a)
		if e != nil {
			return nil, e
		}
		i, e := os.Stat(a)
		if e != nil || !i.IsDir() {
			return nil, fmt.Errorf("workspace must be an existing directory: %s", r)
		}
		duplicate := false
		for _, v := range out {
			if v == a || runtime.GOOS == "windows" && strings.EqualFold(v, a) {
				duplicate = true
			}
		}
		if !duplicate {
			out = append(out, a)
		}
	}
	return out, nil
}
func (f *Files) resolve(bot domain.Bot, path string) (*os.Root, string, error) {
	if !filepath.IsAbs(path) {
		if len(bot.Roots) == 0 {
			return nil, "", errors.New("unbound bots must use absolute paths")
		}
		path = filepath.Join(bot.Roots[0], path)
	}
	a, e := filepath.Abs(path)
	if e != nil {
		return nil, "", e
	}
	if f.Private != "" && Within(f.Private, a) {
		return nil, "", errors.New("Echo private state is not workspace data")
	}
	if f.Private != "" {
		probe := a
		for {
			real, err := filepath.EvalSymlinks(probe)
			if err == nil {
				if Within(f.Private, real) {
					return nil, "", errors.New("Echo private state is not workspace data")
				}
				break
			}
			if !os.IsNotExist(err) {
				break
			}
			parent := filepath.Dir(probe)
			if parent == probe {
				break
			}
			probe = parent
		}
	}
	roots := bot.Roots
	if len(roots) == 0 {
		roots = []string{filepath.VolumeName(a) + string(os.PathSeparator)}
	}
	for _, r := range roots {
		if !Within(r, a) {
			continue
		}
		root, e := os.OpenRoot(r)
		if e != nil {
			return nil, "", e
		}
		rel, e := filepath.Rel(r, a)
		if e != nil {
			root.Close()
			return nil, "", e
		}
		return root, rel, nil
	}
	return nil, "", errors.New("path is outside this bot's folders")
}
func (f *Files) Allowed(bot domain.Bot, path string) bool {
	r, p, e := f.resolve(bot, path)
	if e != nil {
		return false
	}
	defer r.Close()
	_, e = r.Stat(p)
	return e == nil
}

// ReferenceKind validates through the same rooted filesystem as file tools.
func (f *Files) ReferenceKind(bot domain.Bot, path string) (string, error) {
	r, p, err := f.resolve(bot, path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	info, err := r.Stat(p)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "directory", nil
	}
	if info.Mode().IsRegular() {
		return "file", nil
	}
	return "", errors.New("not a file or folder")
}
func (f *Files) Read(bot domain.Bot, path string) (map[string]any, error) {
	r, p, e := f.resolve(bot, path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	file, e := r.Open(p)
	if e != nil {
		return nil, e
	}
	defer file.Close()
	info, e := file.Stat()
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	b, e := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(b) > 1<<20 {
		return nil, errors.New("file exceeds 1 MiB; use search to narrow the request")
	}
	h := sha256.Sum256(b)
	return map[string]any{"path": path, "content": string(b), "revision": hex.EncodeToString(h[:])}, nil
}
func (f *Files) Write(bot domain.Bot, path, content, expected string) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(content) > 1<<20 {
		return nil, errors.New("file exceeds 1 MiB")
	}
	r, p, e := f.resolve(bot, path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	if expected != "" {
		b, e := r.ReadFile(p)
		if e != nil {
			return nil, e
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != expected {
			return nil, errors.New("file changed since it was read")
		}
	}
	tmp := p + ".echo2-" + domain.ID()
	file, e := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, e
	}
	defer r.Remove(tmp)
	_, e = file.WriteString(content)
	if e == nil {
		e = file.Sync()
	}
	ce := file.Close()
	if e != nil {
		return nil, e
	}
	if ce != nil {
		return nil, ce
	}
	if e = r.Rename(tmp, p); e != nil {
		return nil, e
	}
	return map[string]any{"path": path, "written": len(content)}, nil
}
func (f *Files) Delete(bot domain.Bot, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, p, e := f.resolve(bot, path)
	if e != nil {
		return e
	}
	defer r.Close()
	if p == "." {
		return errors.New("cannot delete a workspace root")
	}
	return r.Remove(p)
}
func (f *Files) Move(bot domain.Bot, from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, p, e := f.resolve(bot, from)
	if e != nil {
		return e
	}
	defer r.Close()
	dest, q, e := f.resolve(bot, to)
	if e != nil {
		return e
	}
	defer dest.Close()
	if r.Name() != dest.Name() {
		return errors.New("move must stay within one root")
	}
	if p == "." || q == "." {
		return errors.New("cannot move a root")
	}
	return r.Rename(p, q)
}
func (f *Files) Mkdir(bot domain.Bot, path string) error {
	r, p, e := f.resolve(bot, path)
	if e != nil {
		return e
	}
	defer r.Close()
	return r.MkdirAll(p, 0700)
}
func (f *Files) List(bot domain.Bot, path string) ([]Entry, error) {
	r, p, e := f.resolve(bot, path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	dir, e := r.Open(p)
	if e != nil {
		return nil, e
	}
	defer dir.Close()
	entries, e := dir.ReadDir(1000)
	if e != nil && e != io.EOF {
		return nil, e
	}
	out := []Entry{}
	for _, v := range entries {
		candidate := filepath.Join(path, v.Name())
		if !f.Allowed(bot, candidate) {
			continue
		}
		out = append(out, Entry{Path: candidate, Name: v.Name(), Directory: v.IsDir()})
	}
	return out, nil
}
func (f *Files) Search(bot domain.Bot, base, query string, contents bool) ([]Entry, error) {
	r, p, e := f.resolve(bot, base)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []Entry{}
	visited := 0
	query = strings.ToLower(query)
	e = fs.WalkDir(r.FS(), filepath.ToSlash(p), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		visited++
		if visited > 10000 || len(out) >= 200 {
			return fs.SkipAll
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".echo" || d.Name() == ".cache") {
			return fs.SkipDir
		}
		abs := filepath.Join(r.Name(), filepath.FromSlash(path))
		if f.Private != "" && Within(f.Private, abs) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		match := strings.Contains(strings.ToLower(path), query)
		if contents && !d.IsDir() {
			file, e := r.Open(filepath.FromSlash(path))
			if e == nil {
				b, _ := io.ReadAll(io.LimitReader(file, 256<<10))
				file.Close()
				match = strings.Contains(strings.ToLower(string(b)), query)
			}
		}
		if match {
			out = append(out, Entry{Path: abs, Name: d.Name(), Directory: d.IsDir()})
		}
		return nil
	})
	return out, e
}
