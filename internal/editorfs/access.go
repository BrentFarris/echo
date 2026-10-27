package editorfs

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// access revalidates the current grants and pins the containing directory. All
// data I/O uses os.Root so a concurrent symlink change cannot escape a grant.
func (s *Service) access(botID, absolute string) (*os.Root, string, error) {
	roots, err := s.resolvedRoots(botID)
	if err != nil {
		return nil, "", err
	}
	if err := s.checkPrivate(absolute); err != nil {
		return nil, "", err
	}
	for _, root := range availableResolvedRoots(roots) {
		if ensureWithin(root.realPath, absolute) != nil {
			continue
		}
		handle, err := os.OpenRoot(root.realPath)
		if err != nil {
			return nil, "", err
		}
		relative, err := filepath.Rel(root.realPath, absolute)
		if err != nil {
			handle.Close()
			return nil, "", err
		}
		if relative == "." {
			return handle, ".", nil
		}
		parent := filepath.Dir(relative)
		directory, err := handle.OpenRoot(parent)
		handle.Close()
		if err != nil {
			return nil, "", err
		}
		parentPath := filepath.Dir(absolute)
		info, statErr := directory.Stat(".")
		current, currentErr := os.Stat(parentPath)
		if statErr != nil || currentErr != nil || !os.SameFile(info, current) || s.checkPrivate(parentPath) != nil {
			directory.Close()
			return nil, "", ErrOutsideRoot
		}
		return directory, filepath.Base(relative), nil
	}
	return nil, "", ErrOutsideRoot
}

func (s *Service) open(botID, absolute string) (*os.File, error) {
	root, name, err := s.access(botID, absolute)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	current, currentErr := root.Lstat(name)
	// resolve has already followed legitimate in-root aliases. Do not accept a
	// new final-component link installed between resolution and opening.
	if err != nil || currentErr != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, current) {
		file.Close()
		return nil, ErrOutsideRoot
	}
	return file, nil
}
func (s *Service) stat(botID, absolute string) (os.FileInfo, error) {
	root, name, err := s.access(botID, absolute)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Stat(name)
}
func (s *Service) lstat(botID, absolute string) (os.FileInfo, error) {
	root, name, err := s.access(botID, absolute)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Lstat(name)
}
func (s *Service) readFile(botID, absolute string) ([]byte, error) {
	file, err := s.open(botID, absolute)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxEditableBytes+1))
	if int64(len(data)) > MaxEditableBytes {
		return nil, ErrTooLarge
	}
	return data, err
}
func (s *Service) readDir(botID, absolute string) ([]os.DirEntry, error) {
	file, err := s.open(botID, absolute)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.ReadDir(-1)
}
func (s *Service) mkdir(botID, absolute string, mode os.FileMode) error {
	root, name, err := s.access(botID, absolute)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Mkdir(name, mode)
}
func (s *Service) write(botID, absolute string, data []byte, mode os.FileMode, create bool) error {
	root, name, err := s.access(botID, absolute)
	if err != nil {
		return err
	}
	defer root.Close()
	temporary := fmt.Sprintf(".echo-save-%x.tmp", randomBytes())
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if create {
		return root.Link(temporary, name)
	}
	return root.Rename(temporary, name)
}
func randomBytes() []byte {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		panic(err)
	}
	return data
}

func (s *Service) rename(botID, source, destination string) error {
	roots, err := s.resolvedRoots(botID)
	if err != nil {
		return err
	}
	for _, root := range availableResolvedRoots(roots) {
		if ensureWithin(root.realPath, source) != nil || ensureWithin(root.realPath, destination) != nil {
			continue
		}
		if err := s.checkPrivate(source); err != nil {
			return err
		}
		if err := s.checkPrivate(destination); err != nil {
			return err
		}
		handle, err := os.OpenRoot(root.realPath)
		if err != nil {
			return err
		}
		defer handle.Close()
		from, _ := filepath.Rel(root.realPath, source)
		to, _ := filepath.Rel(root.realPath, destination)
		return handle.Rename(from, to)
	}
	return ErrOutsideRoot
}

func (s *Service) OpenMedia(botID string, ref FileRef) (*os.File, error) {
	_, absolute, _, err := s.resolve(botID, ref, false, false)
	if err != nil {
		return nil, err
	}
	return s.open(botID, absolute)
}
