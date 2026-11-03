//go:build !windows && !linux && !darwin

package broker

import (
	"context"
	"errors"
)

func (b *ProcessBroker) run(context.Context, ProcessRequest, func(string)) (ProcessResult, error) {
	return ProcessResult{}, errors.New("native process containment is unavailable on this platform")
}
func (b *ProcessBroker) Recover() error { return nil }
