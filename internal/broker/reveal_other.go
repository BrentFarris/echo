//go:build !windows && !linux && !darwin

package broker

import "errors"

func RevealInFileBrowser(RevealTarget) error {
	return errors.New("revealing files is unavailable on this platform")
}
