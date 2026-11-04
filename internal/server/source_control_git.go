package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

type gitProcessScope struct {
	roots, readPaths            []string
	workTree, gitDir, commonDir string
}

// These host-generated commands create a new checkout or report the CLI
// version. Binding them to an existing parent repository would redirect init
// into that parent's .git directory.
func gitUsesRepository(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "-C":
			i++
		case "--no-pager", "--literal-pathspecs":
			continue
		case "init", "clone", "--version", "version":
			return false
		default:
			return true
		}
	}
	return false
}

// Parent search authorizes Git metadata for the nearest repository of each
// bot folder. It does not make the repository's other working-tree files
// accessible. Resolve the marker on the host so Git need not discover it by
// inspecting ancestors which the native sandbox has deliberately denied.
func (s *Server) gitSourceControlScope(bot domain.Bot, directory string) (gitProcessScope, error) {
	scope := gitProcessScope{roots: append([]string(nil), bot.Roots...)}
	if len(bot.Roots) == 0 {
		if !s.Files.Allowed(bot, directory) {
			return scope, errors.New("repository directory is outside the bot's folders")
		}
		return scope, nil
	}
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return scope, err
	}
	searchParents := s.scmSettings(bot.ID).SearchParentRepositories
	if s.Files.Allowed(bot, directory) {
		scope.workTree, err = s.findGitWorkTree(bot, directory, searchParents)
	} else if searchParents {
		for _, root := range bot.Roots {
			candidate, findErr := s.findGitWorkTree(bot, root, true)
			if findErr != nil {
				return scope, findErr
			}
			if sameGitPath(candidate, directory) {
				scope.workTree = candidate
				break
			}
		}
		if scope.workTree == "" {
			err = errors.New("repository directory is outside the bot's folders")
		}
	} else {
		err = errors.New("repository directory is outside the bot's folders")
	}
	if err != nil || scope.workTree == "" {
		return scope, err
	}
	marker := filepath.Join(scope.workTree, ".git")
	scope.gitDir, scope.commonDir, err = s.gitMetadataDirectories(bot, marker)
	if err != nil {
		return scope, err
	}
	for _, path := range []string{scope.commonDir, scope.gitDir} {
		covered := false
		for _, root := range scope.roots {
			covered = covered || broker.Within(root, path)
		}
		if !covered {
			scope.roots = append(scope.roots, path)
		}
	}
	readPaths := map[string]string{}
	addReadPath := func(path string) {
		for _, root := range scope.roots {
			if broker.Within(root, path) {
				return
			}
		}
		key := path
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		readPaths[key] = path
	}
	if info, err := os.Lstat(marker); err == nil && info.Mode().IsRegular() {
		addReadPath(marker)
	}
	// Git consults ignore/attribute files between a scoped folder and the
	// checkout root. Grant only those regular files and the directory entries;
	// no inheritable grant is placed on a parent directory.
	for _, start := range append(append([]string{}, bot.Roots...), directory) {
		if !broker.Within(scope.workTree, start) {
			continue
		}
		for path := start; ; path = filepath.Dir(path) {
			addReadPath(path)
			for _, name := range []string{".gitignore", ".gitattributes"} {
				file := filepath.Join(path, name)
				if s.Files.Allowed(bot, file) {
					continue
				}
				info, statErr := os.Lstat(file)
				if os.IsNotExist(statErr) {
					continue
				}
				if statErr != nil || !info.Mode().IsRegular() {
					return scope, fmt.Errorf("Git %s must be an accessible regular file", name)
				}
				addReadPath(file)
			}
			if sameGitPath(path, scope.workTree) {
				break
			}
		}
	}
	for _, path := range readPaths {
		scope.readPaths = append(scope.readPaths, path)
	}
	sort.Strings(scope.readPaths)
	return scope, nil
}

func (s *Server) findGitWorkTree(bot domain.Bot, directory string, searchParents bool) (string, error) {
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", err
	}
	for {
		if !s.Files.Allowed(domain.Bot{}, directory) || !searchParents && !s.Files.Allowed(bot, directory) {
			return "", nil
		}
		info, err := os.Lstat(filepath.Join(directory, ".git"))
		if err == nil {
			if !info.IsDir() && !info.Mode().IsRegular() {
				return "", errors.New("Git metadata must be a directory or regular gitdir file")
			}
			return directory, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", nil
		}
		directory = parent
	}
}

func (s *Server) gitMetadataDirectories(bot domain.Bot, marker string) (string, string, error) {
	info, err := os.Lstat(marker)
	if err != nil {
		return "", "", err
	}
	gitDir := marker
	if info.Mode().IsRegular() {
		value, err := gitReferenceFile(marker)
		if err != nil || !strings.HasPrefix(value, "gitdir: ") {
			return "", "", errors.New("invalid Git gitdir file")
		}
		gitDir = resolveGitReference(filepath.Dir(marker), strings.TrimPrefix(value, "gitdir: "))
	} else if !info.IsDir() {
		return "", "", errors.New("Git metadata must not be a link")
	}
	paths, err := broker.CanonicalRoots([]string{gitDir})
	if err != nil || !sameGitPath(paths[0], gitDir) {
		return "", "", errors.New("Git metadata must be an existing directory without links")
	}
	gitDir = paths[0]
	commonDir := gitDir
	if value, err := gitReferenceFile(filepath.Join(gitDir, "commondir")); err == nil {
		commonDir = resolveGitReference(gitDir, value)
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	paths, err = broker.CanonicalRoots([]string{commonDir})
	if err != nil || !sameGitPath(paths[0], commonDir) {
		return "", "", errors.New("Git common metadata must be an existing directory without links")
	}
	commonDir = paths[0]
	contained := s.Files.Allowed(bot, gitDir) && s.Files.Allowed(bot, commonDir)
	normal := sameGitPath(gitDir, marker) && sameGitPath(commonDir, gitDir)
	// Parent search grants the parent's own .git directory. A gitdir or
	// commondir file must not turn that into a grant to arbitrary other data;
	// linked metadata still requires an explicitly configured bot folder.
	if !contained && !normal {
		return "", "", errors.New("linked Git metadata is outside the bot's folders; add its containing folder to the bot")
	}
	for _, path := range []string{gitDir, commonDir} {
		if !s.Files.Allowed(domain.Bot{}, path) || broker.Within(path, s.Dir) {
			return "", "", errors.New("Git metadata cannot include Echo private data")
		}
	}
	return gitDir, commonDir, nil
}

func gitReferenceFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", errors.New("Git metadata reference must be a small regular file")
	}
	data, err := os.ReadFile(path)
	value := strings.TrimSpace(string(data))
	if err == nil && (value == "" || strings.ContainsAny(value, "\x00\r\n")) {
		err = errors.New("invalid Git metadata reference")
	}
	return value, err
}

func resolveGitReference(base, value string) string {
	value = filepath.FromSlash(value)
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	return filepath.Clean(value)
}

func sameGitPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || runtime.GOOS == "windows" && strings.EqualFold(a, b)
}
