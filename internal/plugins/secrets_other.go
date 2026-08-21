//go:build !windows && !darwin && !linux

package plugins

import "context"

type unavailableSecretStore struct{}

func newPlatformSecretStore() SecretStore                     { return unavailableSecretStore{} }
func (unavailableSecretStore) Available(context.Context) bool { return false }
func (unavailableSecretStore) Get(context.Context, string) (string, error) {
	return "", ErrSecretNotFound
}
func (unavailableSecretStore) Set(context.Context, string, string) error { return ErrSecretNotFound }
func (unavailableSecretStore) Delete(context.Context, string) error      { return nil }
