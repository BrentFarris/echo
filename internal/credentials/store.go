package credentials

import (
	"context"
	"errors"
)

var ErrSecretNotFound = errors.New("credential not found")

type SecretStore interface {
	Available(context.Context) bool
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string) error
	Delete(context.Context, string) error
}
