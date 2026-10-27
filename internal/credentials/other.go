//go:build !windows

package credentials

import (
	"context"
	"errors"
)

type unavailable struct{}

func New() SecretStore                                          { return unavailable{} }
func (unavailable) Available(context.Context) bool              { return false }
func (unavailable) Get(context.Context, string) (string, error) { return "", ErrSecretNotFound }
func (unavailable) Set(context.Context, string, string) error {
	return errors.New("credential storage currently requires Windows")
}
func (unavailable) Delete(context.Context, string) error { return ErrSecretNotFound }
