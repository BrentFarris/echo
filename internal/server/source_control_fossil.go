package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

// Fossil stores the repository filename in the checkout database, not in a
// predictable sibling filename. Read that reference without running an
// unrestricted CLI. Only the checkout's immediate parent can receive the
// additional Fossil-only grant needed for SQLite's journal/sidecar files.
func (s *Server) fossilRepositoryAccess(ctx context.Context, bot domain.Bot, checkout string) (repository, metadataRoot string, err error) {
	if !s.Files.Allowed(bot, checkout) {
		return "", "", errors.New("Fossil checkout is outside the bot's folders")
	}
	checkout, err = filepath.EvalSymlinks(checkout)
	if err != nil {
		return "", "", err
	}
	var marker string
	for _, name := range []string{".fslckout", "_FOSSIL_", ".fos"} {
		candidate := filepath.Join(checkout, name)
		info, e := os.Lstat(candidate)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil || !info.Mode().IsRegular() || !s.Files.Allowed(bot, candidate) {
			return "", "", errors.New("Fossil checkout metadata must be an accessible regular file")
		}
		marker = candidate
		break
	}
	if marker == "" {
		return "", "", nil
	}
	uriPath := filepath.ToSlash(marker)
	if runtime.GOOS == "windows" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath}
	query := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "trusted_schema(0)", "busy_timeout(1000)"}}
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return "", "", err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Do not evaluate an arbitrary view supplied by a malformed checkout.
	var kind string
	err = db.QueryRowContext(ctx, "SELECT type FROM sqlite_schema WHERE name = 'vvar'").Scan(&kind)
	if err != nil || kind != "table" {
		return "", "", errors.New("Fossil checkout metadata has no vvar table")
	}
	err = db.QueryRowContext(ctx, "SELECT value FROM vvar WHERE name = 'repository' AND length(value) BETWEEN 1 AND 32767 LIMIT 1").Scan(&repository)
	if err != nil {
		return "", "", fmt.Errorf("read Fossil checkout repository reference: %w", err)
	}
	if strings.ContainsRune(repository, 0) {
		return "", "", errors.New("invalid Fossil repository filename")
	}
	if !filepath.IsAbs(repository) {
		repository = filepath.Join(checkout, repository)
	}
	repository = filepath.Clean(repository)
	if s.Files.Allowed(bot, repository) {
		repository, err = filepath.EvalSymlinks(repository)
		return repository, "", err
	}
	parent := filepath.Dir(checkout)
	if !sameFossilPath(filepath.Dir(repository), parent) {
		return "", "", errors.New("Fossil repository database is outside the checkout's parent folder; add its folder to the bot's workspace")
	}
	private, err := filepath.EvalSymlinks(s.Dir)
	if err != nil {
		return "", "", err
	}
	if broker.Within(parent, private) || broker.Within(private, parent) {
		return "", "", errors.New("Fossil repository folder cannot include Echo private data")
	}
	info, err := os.Lstat(repository)
	if err != nil {
		return "", "", fmt.Errorf("Fossil repository database is unavailable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", "", errors.New("Fossil repository database must be a regular file, not a link or directory")
	}
	return repository, parent, nil
}

func sameFossilPath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || runtime.GOOS == "windows" && strings.EqualFold(a, b)
}

func (s *Server) validateFossilRepository(ctx context.Context, botID, checkout, path string) (string, error) {
	bot, err := s.Bot(botID)
	if err != nil {
		return "", err
	}
	repository, _, err := s.fossilRepositoryAccess(ctx, bot, checkout)
	if err != nil {
		return "", err
	}
	if !sameFossilPath(repository, path) {
		return "", errors.New("Fossil repository reference changed; refresh Source Control")
	}
	return repository, nil
}
