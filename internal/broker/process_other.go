//go:build !windows

package broker

import (
	"context"
	"errors"
)

func (b *ProcessBroker) run(context.Context, ProcessRequest, func(string)) (ProcessResult, error) {
	return ProcessResult{}, errors.New("native shell isolation currently requires Windows")
}
func (b *ProcessBroker) Recover() error { return nil }
