//go:build !windows

package calendar

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

func LocalTimezone() (string, error) {
	if zone := os.Getenv("TZ"); zone != "" {
		return strings.TrimPrefix(zone, ":"), nil
	}
	if path, e := filepath.EvalSymlinks("/etc/localtime"); e == nil {
		if _, zone, ok := strings.Cut(path, "/zoneinfo/"); ok {
			return zone, nil
		}
	}
	return time.Local.String(), nil
}
