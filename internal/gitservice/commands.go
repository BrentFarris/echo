package gitservice

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// RunScopedCommand coordinates the bot CLI with editor mutations without
// replacing the caller's streaming/tracked process runner. root and commonDir
// must have been freshly resolved and authorized by the host's Git resolver.
func (s *Service) RunScopedCommand(ctx context.Context, workspaceID, root, commonDir, directory, command string, args []string, run func(context.Context, string, []string) error) error {
	s.mu.Lock()
	lock := s.commandLock(commonDir)
	s.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	roots, err := s.fs.Roots(workspaceID)
	if err != nil {
		return err
	}
	infos := make([]rootInfo, 0, len(roots))
	for _, folder := range roots {
		if folder.BlockedReason != "" {
			continue
		}
		canonical, err := canonicalExisting(folder.HostPath)
		if err != nil {
			return err
		}
		infos = append(infos, rootInfo{Root: folder, canonical: canonical})
	}
	scopes, ref, parent := scopesForRepository(root, infos)
	if len(scopes) == 0 {
		return fmt.Errorf("repository is outside the bot's folders")
	}
	state := &repositoryState{workspaceID: workspaceID, root: root, commonDir: commonDir, scopes: scopes, rootRef: ref, parent: parent}
	plan := scopedCommand{args: append([]string{command}, args...)}
	if parent {
		plan, err = state.prepareScopedCommand(directory, command, args)
		if err != nil {
			return err
		}
		if command == "status" || command == "diff" || command == "add" || command == "commit" {
			directory = root
		}
	}
	defer s.InvalidateWorkspace(workspaceID)
	if parent && command == "commit" {
		if _, err := s.checkCommitScope(ctx, state); err != nil {
			return err
		}
		if len(plan.stage) > 0 {
			if _, err := runGit(ctx, root, nil, false, plan.stage...); err != nil {
				return err
			}
			if _, err := s.checkCommitScope(ctx, state); err != nil {
				return err
			}
		}
	}
	return run(ctx, directory, append([]string{"--no-pager"}, plan.args...))
}

type scopedCommand struct {
	args, stage []string
}

func scopedArgumentError(command, argument string) error {
	return fmt.Errorf("Git %s argument %q cannot be confined to the bot's folders in a parent repository; use literal folder/file paths after --, or configure the repository root as a bot folder for repository-wide operations", command, argument)
}

// Parse only unambiguous common CLI forms. In particular, abbreviations,
// pathspec files, interactive staging and external diff programs must not
// bypass the scoped path list. Other forms remain available in full checkouts.
func (r *repositoryState) prepareScopedCommand(directory, command string, args []string) (scopedCommand, error) {
	plan := scopedCommand{args: []string{command}}
	var flags, values, optional, shortFlags, shortValues string
	switch command {
	case "status":
		flags = "short branch show-stash verbose long null ignored no-renames ahead-behind no-ahead-behind no-column"
		optional = "porcelain untracked-files ignored find-renames column"
		values = "ignore-submodules"
		shortFlags, shortValues = "sbvz", ""
	case "diff":
		flags = "cached staged patch no-patch raw patch-with-raw patch-with-stat numstat shortstat dirstat cumulative dirstat-by-file check summary name-only name-status stat compact-summary binary full-index color no-color color-moved no-color-moved relative no-relative text reverse exit-code quiet no-ext-diff no-textconv no-renames ignore-all-space ignore-space-change ignore-space-at-eol ignore-cr-at-eol ignore-blank-lines minimal patience histogram"
		optional = "stat dirstat dirstat-by-file color color-moved relative word-diff find-renames find-copies"
		values = "unified inter-hunk-context src-prefix dst-prefix line-prefix diff-algorithm word-diff-regex ignore-matching-lines diff-filter order-file rotate-to skip-to"
		shortFlags, shortValues = "pursbzwaRMBC", "USG"
	case "add":
		flags = "all update verbose dry-run force intent-to-add refresh ignore-errors ignore-missing renormalize no-all no-ignore-removal ignore-removal"
		shortFlags, shortValues = "AuvnfN", ""
	case "commit":
		flags = "all no-all amend no-edit signoff no-signoff no-verify allow-empty allow-empty-message reset-author only include dry-run short branch porcelain long null verbose quiet no-gpg-sign"
		values = "message file reuse-message reedit-message author date cleanup fixup squash trailer"
		optional = "gpg-sign untracked-files"
		shortFlags, shortValues = "asnvoqibz", "mFCc"
	default:
		// Metadata operations keep their native semantics. Commands which can
		// rewrite a whole worktree/index (including aliases) need full scope.
		if !wordIn("log show rev-parse rev-list ls-files ls-tree cat-file hash-object describe branch tag remote fetch push config for-each-ref show-ref merge-base reflog count-objects fsck help", command) {
			return plan, scopedArgumentError(command, command)
		}
		plan.args = append(plan.args, args...)
		return plan, nil
	}
	var paths []string
	all, dryRun, pathMode, broadAdd, partialCommit := false, false, false, false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if pathMode {
			paths = append(paths, arg)
			continue
		}
		if arg == "--" {
			pathMode = true
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if command == "diff" {
				// Git resolves these as revisions because we always supply --.
				// Paths for diff must be explicitly separated from revisions.
				plan.args = append(plan.args, arg)
			} else {
				paths = append(paths, arg)
			}
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, attached := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
			broadAdd = broadAdd || command == "add" && wordIn("all update renormalize", name)
			partialCommit = partialCommit || command == "commit" && wordIn("only include", name)
			if command == "commit" && (name == "all" || name == "no-all") && !attached {
				all = name == "all"
				continue
			}
			if wordIn(values, name) {
				if !attached {
					i++
					if i == len(args) {
						return plan, scopedArgumentError(command, arg)
					}
					value = args[i]
				}
				if command == "commit" && name == "file" {
					value = commandInputPath(directory, value)
				}
				plan.args = append(plan.args, "--"+name+"="+value)
			} else if wordIn(optional, name) || wordIn(flags, name) && !attached {
				plan.args = append(plan.args, arg)
			} else {
				return plan, scopedArgumentError(command, arg)
			}
			dryRun = dryRun || name == "dry-run" || command == "commit" && wordIn("short porcelain long", name)
			continue
		}
		for j := 1; j < len(arg); j++ {
			flag := arg[j]
			broadAdd = broadAdd || command == "add" && (flag == 'A' || flag == 'u')
			partialCommit = partialCommit || command == "commit" && (flag == 'o' || flag == 'i')
			if (command == "status" || command == "commit") && flag == 'u' || command == "diff" && strings.ContainsRune("MBC", rune(flag)) || command == "commit" && flag == 'S' {
				plan.args = append(plan.args, "-"+arg[j:])
				break // These short options take an optional attached value.
			}
			if command == "commit" && flag == 'a' {
				all = true
				continue
			}
			if strings.ContainsRune(shortValues, rune(flag)) {
				plan.args = append(plan.args, "-"+string(flag))
				value := ""
				if j+1 < len(arg) {
					value = arg[j+1:]
				} else {
					i++
					if i == len(args) {
						return plan, scopedArgumentError(command, arg)
					}
					value = args[i]
				}
				if command == "commit" && flag == 'F' {
					value = commandInputPath(directory, value)
				}
				plan.args = append(plan.args, value)
				break
			}
			if !strings.ContainsRune(shortFlags, rune(flag)) {
				return plan, scopedArgumentError(command, arg)
			}
			plan.args = append(plan.args, "-"+string(flag))
		}
	}
	if all && (dryRun || partialCommit || len(paths) > 0) {
		return plan, scopedArgumentError(command, "--all with paths, --only/--include, or dry-run output; stage the intended files explicitly first")
	}
	if command == "add" && len(paths) == 0 && !broadAdd {
		return plan, nil // Preserve Git's no-path add behavior; do not silently stage everything.
	}
	if command == "commit" && len(paths) == 0 {
		if all {
			pathspecs, err := r.commandPathspecs(r.root, nil)
			if err != nil {
				return plan, err
			}
			plan.stage = append([]string{"add", "-u", "--"}, pathspecs...)
		}
		return plan, nil // A plain commit must consume the existing index.
	}
	pathspecs, err := r.commandPathspecs(directory, paths)
	if err != nil {
		return plan, err
	}
	if command == "diff" {
		plan.args = append(plan.args, "--no-ext-diff", "--no-textconv")
	}
	plan.args = append(append(plan.args, "--"), pathspecs...)
	return plan, nil
}

func commandInputPath(directory, path string) string {
	if path != "-" && !filepath.IsAbs(path) {
		return filepath.Join(directory, path)
	}
	return path
}

func wordIn(words, word string) bool {
	return word != "" && strings.Contains(" "+words+" ", " "+word+" ")
}

// Intersect literal selections with the configured folders. This handles a
// broad '.' at the checkout root without turning positive Git pathspecs into
// a union that could accidentally widen an explicit selection.
func (r *repositoryState) commandPathspecs(directory string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		paths = []string{":(literal)" + r.root}
	}
	var out []string
	seen := map[string]bool{}
	for _, path := range paths {
		var literal bool
		path, literal = strings.CutPrefix(path, ":(literal)")
		if path == "" || !literal && strings.ContainsAny(path, "*?[") || strings.HasPrefix(path, ":") {
			return nil, scopedArgumentError("path selection", path)
		}
		absolute := filepath.Clean(path)
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(directory, absolute)
		}
		if _, ok := relativeWithin(r.root, absolute); !ok {
			return nil, scopedArgumentError("path selection", path)
		}
		matched := false
		for _, scope := range r.scopes {
			folder := filepath.Join(r.root, filepath.FromSlash(scope.RepoPrefix))
			selected := absolute
			if _, ok := relativeWithin(folder, absolute); !ok {
				if _, ok := relativeWithin(absolute, folder); !ok {
					continue
				}
				selected = folder
			}
			relative, _ := relativeWithin(r.root, selected)
			if r.protectedMetadataPath(relative) {
				return nil, scopedArgumentError("path selection", path)
			}
			matched = true
			key := pathIdentity(selected)
			if !seen[key] {
				seen[key] = true
				out = append(out, ":(top,literal)"+relative)
			}
		}
		if !matched {
			return nil, scopedArgumentError("path selection", path)
		}
	}
	for _, exclusion := range r.scopePathspecs() {
		if rest, ok := strings.CutPrefix(exclusion, ":(exclude)"); ok {
			out = append(out, ":(top,exclude)"+rest)
		}
	}
	return out, nil
}
