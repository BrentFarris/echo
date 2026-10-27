//go:build !windows

package broker

import "errors"

func RevealInFileBrowser(RevealTarget) error {
	return errors.New("revealing changed files in the native file browser currently requires Windows")
}
