package broker

import (
	"os"
	"path/filepath"
)

// Builds can prepare dependencies inside a checkout for offline scoped tests.
// Never select an ancestor module or a cache junction outside the bot's roots.
func workspaceGoCaches(r ProcessRequest) map[string]string {
	roots, err := CanonicalRoots(r.Roots)
	if err != nil || len(roots) == 0 {
		return nil
	}
	directory, err := filepath.EvalSymlinks(r.Directory)
	if err != nil {
		return nil
	}
	for ; ; directory = filepath.Dir(directory) {
		allowed := false
		for _, root := range roots {
			if Within(root, directory) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil
		}
		if info, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil && info.Mode().IsRegular() {
			env := map[string]string{}
			for key, name := range map[string]string{"GOMODCACHE": "go-mod", "GOCACHE": "go-build"} {
				// Junctions are ModeIrregular on modern Windows Go, so
				// EvalSymlinks alone does not reject an escaping junction.
				cacheDirectory := filepath.Join(directory, ".cache")
				unsafeLink := false
				for _, path := range []string{cacheDirectory, filepath.Join(cacheDirectory, name)} {
					info, err := os.Lstat(path)
					if err != nil || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
						unsafeLink = true
						break
					}
				}
				if unsafeLink {
					continue
				}
				cache, err := filepath.EvalSymlinks(filepath.Join(directory, ".cache", name))
				if err != nil {
					continue
				}
				info, err := os.Stat(cache)
				if err != nil || !info.IsDir() {
					continue
				}
				for _, root := range roots {
					if Within(root, cache) {
						env[key] = cache
						break
					}
				}
			}
			return env
		}
		if filepath.Dir(directory) == directory {
			return nil
		}
	}
}
