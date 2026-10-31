package server

import (
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
)

func hostURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
func (g *goLanguageSession) processURI(path string) string {
	if g.process != nil {
		path = g.process.ProcessPath(path)
	}
	return hostURI(path)
}
func (g *goLanguageSession) hostPath(uri string) (string, error) {
	path, err := uriPath(uri)
	if err != nil {
		return "", err
	}
	if g.process != nil {
		path = g.process.HostPath(path)
	}
	return path, nil
}
func uriPath(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "file" || u.Host != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid host file URI")
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.FromSlash(p), nil
}
func (g *goLanguageSession) dependency(path string) bool {
	roots := []string{g.request.Environment["GOMODCACHE"]}
	if len(g.request.PathPrepend) > 1 {
		roots = append(roots, filepath.Join(filepath.Dir(g.request.PathPrepend[1]), "src"))
	}
	for _, root := range roots {
		if root != "" && broker.Within(root, path) {
			return true
		}
	}
	return false
}
func (g *goLanguageSession) mapURI(raw string, incoming bool) (string, error) {
	if incoming {
		u, err := url.Parse(raw)
		if err != nil {
			return "", err
		}
		if u.Scheme == "echo-go" {
			g.meta.Lock()
			path := g.sources[u.Path]
			g.meta.Unlock()
			if u.Host != g.id || path == "" {
				return "", errors.New("unknown Go source handle")
			}
			real, e := filepath.EvalSymlinks(path)
			if e != nil || !g.dependency(real) {
				return "", errors.New("Go source is no longer accessible")
			}
			return g.processURI(real), nil
		}
		if u.Scheme != "echo-workspace" || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("expected scoped workspace URI")
		}
		path, err := g.fs.ResolveExistingHostPath(g.bot.ID, editorfs.FileRef{RootID: u.Host, Path: strings.TrimPrefix(u.Path, "/")}, false)
		if err != nil {
			return "", err
		}
		return g.processURI(path), nil
	}
	path, err := g.hostPath(raw)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if g.dependency(real) {
		g.meta.Lock()
		defer g.meta.Unlock()
		for key, value := range g.sources {
			if value == real {
				return (&url.URL{Scheme: "echo-go", Host: g.id, Path: key}).String(), nil
			}
		}
		if len(g.sources) >= 4096 {
			return "", errors.New("Go source handle limit reached")
		}
		key := "/" + domain.ID() + "/" + filepath.Base(real)
		g.sources[key] = real
		return (&url.URL{Scheme: "echo-go", Host: g.id, Path: key}).String(), nil
	}
	ref, err := g.fs.ReferenceForHostPath(g.bot.ID, real)
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "echo-workspace", Host: ref.RootID, Path: "/" + ref.Path}).String(), nil
}

// Translate every URI in known LSP containers, including WorkspaceEdit's URI
// map keys. No host path is accepted from the browser as a file reference.
func (g *goLanguageSession) translate(raw json.RawMessage, incoming bool) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	var walk func(any) (any, error)
	walk = func(v any) (any, error) {
		switch x := v.(type) {
		case []any:
			for i, item := range x {
				mapped, err := walk(item)
				if err != nil {
					return nil, err
				}
				x[i] = mapped
			}
		case map[string]any:
			if _, ok := x["kind"]; ok {
				if _, a := x["oldUri"]; a {
					return nil, errors.New("file resource operations are unsupported")
				}
				if _, a := x["uri"]; a {
					kind, _ := x["kind"].(string)
					if kind == "create" || kind == "delete" {
						return nil, errors.New("file resource operations are unsupported")
					}
				}
			}
			for k, item := range x {
				if k == "uri" || k == "targetUri" {
					if str, ok := item.(string); ok {
						mapped, err := g.mapURI(str, incoming)
						if err != nil {
							return nil, err
						}
						x[k] = mapped
						continue
					}
				}
				if k == "changes" {
					if changes, ok := item.(map[string]any); ok {
						next := map[string]any{}
						for uri, edits := range changes {
							mapped, err := g.mapURI(uri, incoming)
							if err != nil {
								return nil, err
							}
							next[mapped] = edits
						}
						x[k] = next
						continue
					}
				}
				mapped, err := walk(item)
				if err != nil {
					return nil, err
				}
				x[k] = mapped
			}
		}
		return v, nil
	}
	value, err := walk(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
