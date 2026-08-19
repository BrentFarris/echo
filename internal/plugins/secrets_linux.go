//go:build linux

package plugins

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

type linuxSecretStore struct{ path string }

func newPlatformSecretStore() SecretStore {
	path, _ := exec.LookPath("secret-tool")
	return &linuxSecretStore{path: path}
}

func (s *linuxSecretStore) Available(context.Context) bool { return s.path != "" }

func (s *linuxSecretStore) Get(ctx context.Context, key string) (string, error) {
	if s.path == "" {
		return "", ErrSecretNotFound
	}
	output, err := exec.CommandContext(ctx, s.path, "lookup", "echo-plugin", key).Output()
	if err != nil || len(output) == 0 {
		return "", ErrSecretNotFound
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

func (s *linuxSecretStore) Set(ctx context.Context, key, value string) error {
	if s.path == "" {
		return errors.New("OS credential store is unavailable")
	}
	command := exec.CommandContext(ctx, s.path, "store", "--label=Echo plugin secret", "echo-plugin", key)
	command.Stdin = bytes.NewBufferString(value)
	return command.Run()
}

func (s *linuxSecretStore) Delete(ctx context.Context, key string) error {
	if s.path == "" {
		return nil
	}
	if err := exec.CommandContext(ctx, s.path, "clear", "echo-plugin", key).Run(); err != nil {
		return ErrSecretNotFound
	}
	return nil
}
