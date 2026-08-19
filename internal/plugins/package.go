package plugins

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	MaxPackageBytes = int64(128 << 20)
	MaxPackageFiles = 4096
	MaxFileBytes    = int64(32 << 20)
)

func HashPackage(root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	paths := []string{}
	var total int64
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin package contains a symlink")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if len(paths) >= MaxPackageFiles || total > MaxPackageBytes || info.Size() > MaxFileBytes {
			return fmt.Errorf("plugin package exceeds size or file-count limits")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		return "", fmt.Errorf("walk plugin package: %w", err)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, relative := range paths {
		path, err := packagePath(root, relative)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.Size() > MaxFileBytes {
			return "", fmt.Errorf("plugin file %q exceeds the per-file limit", relative)
		}
		_, _ = io.WriteString(hash, relative)
		_, _ = hash.Write([]byte{0})
		_, _ = io.WriteString(hash, fmt.Sprintf("%04o", info.Mode().Perm()))
		_, _ = hash.Write([]byte{0})
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(hash, io.LimitReader(file, MaxFileBytes+1))
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// CopyPackage creates a safe immutable snapshot. It never follows symlinks and
// enforces the same bounded package shape accepted by the GitHub extractor.
func CopyPackage(source, destination string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	if err := rejectUnsafeTree(source); err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	if relative, relErr := filepath.Rel(source, destination); relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("plugin snapshot destination may not be inside its source")
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	files := 0
	var total int64
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target, err := packagePath(destination, filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		files++
		total += info.Size()
		if files > MaxPackageFiles || total > MaxPackageBytes || info.Size() > MaxFileBytes {
			return fmt.Errorf("plugin package exceeds size or file-count limits")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyRegularFile(path, target, info.Mode())
	})
}

// InstallPackageSnapshot copies through a sibling temporary directory and
// publishes only a fully validated package. A failed copy can therefore never
// be mistaken for an installed digest on a later approval retry.
func InstallPackageSnapshot(source, destination, expectedDigest string, coreToolNames map[string]bool) error {
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if validation, validateErr := ValidatePackage(destination, coreToolNames); validateErr == nil && validation.Digest == expectedDigest {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".install-*")
	if err != nil {
		return err
	}
	// CopyPackage requires the destination not to exist.
	if err := os.Remove(temporary); err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err := CopyPackage(source, temporary); err != nil {
		return err
	}
	validation, err := ValidatePackage(temporary, coreToolNames)
	if err != nil {
		return err
	}
	if validation.Digest != expectedDigest {
		return fmt.Errorf("plugin snapshot digest changed while it was copied")
	}
	if _, err := os.Stat(destination); err == nil {
		if err := os.RemoveAll(destination); err != nil {
			return fmt.Errorf("replace corrupt plugin snapshot: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := replaceAtomic(temporary, destination); err != nil {
		// Some Windows filesystem and endpoint-security combinations deny
		// directory renames even on the same volume. The registry is the commit
		// point for an installation, so a validated copy is still transactional:
		// no registry entry can reference the destination until this completes.
		if published, validationErr := ValidatePackage(destination, coreToolNames); validationErr == nil && published.Digest == expectedDigest {
			return nil
		}
		if removeErr := os.RemoveAll(destination); removeErr != nil {
			return fmt.Errorf("publish plugin snapshot: %w (cleanup failed: %v)", err, removeErr)
		}
		if copyErr := CopyPackage(temporary, destination); copyErr != nil {
			_ = os.RemoveAll(destination)
			return fmt.Errorf("publish plugin snapshot: %w (copy fallback failed: %v)", err, copyErr)
		}
		published, validationErr := ValidatePackage(destination, coreToolNames)
		if validationErr != nil || published.Digest != expectedDigest {
			_ = os.RemoveAll(destination)
			if validationErr != nil {
				return fmt.Errorf("validate published plugin snapshot: %w", validationErr)
			}
			return fmt.Errorf("published plugin snapshot digest changed")
		}
	}
	return nil
}

func copyRegularFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	permissions := mode.Perm()
	if permissions == 0 {
		permissions = 0o644
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, permissions)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, io.LimitReader(input, MaxFileBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// ExtractGitHubTar extracts a GitHub tarball into destination. GitHub archives
// contain a generated top-level directory; subdirectory optionally selects a
// package below it. No link or special entry is accepted.
func ExtractGitHubTar(reader io.Reader, destination, subdirectory string) error {
	gzipReader, err := gzip.NewReader(io.LimitReader(reader, MaxPackageBytes+1))
	if err != nil {
		return fmt.Errorf("open GitHub archive: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	temporary, err := os.MkdirTemp(filepath.Dir(destination), "github-extract-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	entries := 0
	files := 0
	var total int64
	var archiveRoot string
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read GitHub archive: %w", err)
		}
		name := filepath.ToSlash(strings.TrimSpace(header.Name))
		parts := strings.SplitN(name, "/", 2)
		if len(parts) == 0 || parts[0] == "" {
			return fmt.Errorf("GitHub archive contains an invalid path")
		}
		if archiveRoot == "" {
			archiveRoot = parts[0]
		}
		if parts[0] != archiveRoot || len(parts) == 1 || parts[1] == "" {
			continue
		}
		entries++
		if entries > MaxPackageFiles {
			return fmt.Errorf("GitHub plugin package exceeds entry-count limits")
		}
		relative := parts[1]
		path, err := packagePath(temporary, relative)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			files++
			total += header.Size
			if files > MaxPackageFiles || total > MaxPackageBytes || header.Size < 0 || header.Size > MaxFileBytes {
				return fmt.Errorf("GitHub plugin package exceeds size or file-count limits")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(output, tarReader, header.Size)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("GitHub plugin packages may not contain links or special files")
		}
	}
	selected := temporary
	if strings.TrimSpace(subdirectory) != "" {
		selected, err = packagePath(temporary, subdirectory)
		if err != nil {
			return fmt.Errorf("invalid GitHub package subdirectory: %w", err)
		}
	}
	if _, err := os.Stat(filepath.Join(selected, ManifestFileName)); err != nil {
		return fmt.Errorf("GitHub package does not contain %s at the selected path", ManifestFileName)
	}
	return CopyPackage(selected, destination)
}
