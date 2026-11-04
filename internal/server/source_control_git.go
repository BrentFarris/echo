package server

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// Editor commands bind Git to the host-resolved checkout. Bot commands have
// whole-checkout access and let Git perform its normal repository discovery.
// Git changes directory to the worktree during setup but does not adjust a
// relative GIT_COMMON_DIR, so probes from subfolders need an absolute common
// directory. Editor mutations run at the checkout root with relative metadata paths
// to keep Git's loose-ref lock directory walk within the Windows grant.
func (scope gitProcessScope) environment(directory string, env map[string]string) {
	if scope.workTree == "" {
		return
	}
	for key, path := range map[string]string{"GIT_DIR": scope.gitDir, "GIT_COMMON_DIR": scope.commonDir, "GIT_WORK_TREE": scope.workTree} {
		if relative, err := filepath.Rel(directory, path); err == nil && (key != "GIT_COMMON_DIR" || sameGitPath(directory, scope.workTree)) {
			path = relative
		}
		env[key] = filepath.ToSlash(path)
	}
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

// Bot Git tools operate on the complete checkout. The configured folders
// identify which parent repository is authorized; they do not restrict Git's
// pathspecs or index operations within that checkout. This grant is private to
// the native Git process and never changes the bot's file or shell scope.
func (s *Server) gitCommandScope(bot domain.Bot, directory string) (gitProcessScope, error) {
	return s.gitRepositoryScope(bot, directory, true)
}

// Parent search authorizes Git metadata for the nearest repository of each
// bot folder. It does not make the repository's other working-tree files
// accessible. Resolve the marker on the host so Git need not discover it by
// inspecting ancestors which the native sandbox has deliberately denied.
func (s *Server) gitSourceControlScope(bot domain.Bot, directory string) (gitProcessScope, error) {
	return s.gitRepositoryScope(bot, directory, false)
}

func (s *Server) gitRepositoryScope(bot domain.Bot, directory string, wholeCheckout bool) (gitProcessScope, error) {
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
			if wholeCheckout && candidate != "" && broker.Within(candidate, directory) {
				// A sibling directory may itself be a nested checkout. Resolve
				// its actual metadata so the command uses that repository's lock.
				checkout := bot
				checkout.Roots = []string{candidate}
				scope.workTree, err = s.findGitWorkTree(checkout, directory, false)
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
	if wholeCheckout {
		if broker.Within(scope.workTree, s.Dir) {
			return scope, errors.New("Git repository access cannot include Echo private data")
		}
		// Replace configured subfolders with their checkout, preserving any
		// other explicit folders. Avoid redundant recursive Windows ACL grants.
		covered := false
		for _, root := range bot.Roots {
			covered = covered || broker.Within(root, scope.workTree)
		}
		if !covered {
			scope.roots = []string{scope.workTree}
			for _, root := range bot.Roots {
				if !broker.Within(scope.workTree, root) {
					scope.roots = append(scope.roots, root)
				}
			}
		}
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
	if wholeCheckout {
		return scope, nil
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
		marker := filepath.Join(directory, ".git")
		info, err := os.Lstat(marker)
		if err == nil {
			if !info.IsDir() && !info.Mode().IsRegular() {
				return "", errors.New("Git metadata must be a directory or regular gitdir file")
			}
			// Git skips incomplete .git directories during discovery. Do not
			// force GIT_DIR to one and hide a valid enclosing repository.
			// Gitdir files retain their explicit validation/error behavior.
			if info.Mode().IsRegular() {
				return directory, nil
			}
			gitDir, commonDir, err := s.gitMetadataDirectories(bot, marker)
			if err != nil {
				return "", err
			}
			valid, err := gitDirectorySignatures(gitDir, commonDir)
			if err != nil {
				return "", err
			}
			if valid {
				return directory, nil
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", nil
		}
		directory = parent
	}
}

// Match the repository signatures Git checks before selecting a .git directory:
// HEAD must name a ref/object, and the common metadata needs objects and refs.
// Check only metadata already authorized by gitMetadataDirectories; an invalid
// directory must not redirect this host-side probe to arbitrary paths.
func gitDirectorySignatures(gitDir, commonDir string) (bool, error) {
	headPath := filepath.Join(gitDir, "HEAD")
	info, err := os.Lstat(headPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	validHead := false
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(headPath)
		if err != nil {
			return false, err
		}
		validHead = strings.HasPrefix(filepath.ToSlash(target), "refs/")
	} else if info.Mode().IsRegular() {
		file, err := os.Open(headPath)
		if err != nil {
			return false, err
		}
		// Git's discovery probe reads at most 255 bytes from HEAD.
		data, err := io.ReadAll(io.LimitReader(file, 255))
		file.Close()
		if err != nil {
			return false, err
		}
		head := string(data)
		if ref, ok := strings.CutPrefix(head, "ref:"); ok {
			validHead = strings.HasPrefix(strings.TrimSpace(ref), "refs/")
		} else {
			for _, size := range []int{40, 64} {
				if len(head) >= size {
					if _, err := hex.DecodeString(head[:size]); err == nil {
						validHead = true
						break
					}
				}
			}
		}
	}
	if !validHead {
		return false, nil
	}
	for _, name := range []string{"objects", "refs"} {
		info, err := os.Stat(filepath.Join(commonDir, name))
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() {
			return false, nil
		}
	}
	return true, nil
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
