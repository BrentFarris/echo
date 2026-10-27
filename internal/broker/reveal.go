package broker

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/brent/echo/internal/domain"
)

type RevealTarget struct {
	Path   string `json:"path"`
	Select bool   `json:"select"`
}

// RevealTarget revalidates the current workspace grant. For removed entries,
// walk up only as far as an existing directory still inside that grant.
func (f *Files) RevealTarget(bot domain.Bot, path string) (RevealTarget, error) {
	if !filepath.IsAbs(path) {
		return RevealTarget{}, errors.New("an absolute changed-file path is required")
	}
	requested := filepath.Clean(path)
	for candidate := requested; ; candidate = filepath.Dir(candidate) {
		root, relative, err := f.resolve(bot, candidate)
		if err != nil {
			return RevealTarget{}, err
		}
		info, err := root.Stat(relative)
		root.Close()
		if err == nil {
			if !info.IsDir() && !info.Mode().IsRegular() {
				return RevealTarget{}, errors.New("not a file or folder")
			}
			real, err := filepath.EvalSymlinks(candidate)
			if err != nil || !f.Allowed(bot, real) {
				return RevealTarget{}, errors.New("changed-file location is no longer within the bot's folders")
			}
			return RevealTarget{Path: real, Select: candidate == requested && !info.IsDir()}, nil
		}
		if !os.IsNotExist(err) {
			return RevealTarget{}, err
		}
		if filepath.Dir(candidate) == candidate {
			return RevealTarget{}, errors.New("the containing folder no longer exists")
		}
	}
}
