//go:build darwin

package plugins

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

type darwinSecretStore struct{ path string }

func newPlatformSecretStore() SecretStore {
	path, _ := exec.LookPath("security")
	return &darwinSecretStore{path: path}
}

func (s *darwinSecretStore) Available(context.Context) bool { return s.path != "" }

func (s *darwinSecretStore) Get(ctx context.Context, key string) (string, error) {
	if s.path == "" {
		return "", ErrSecretNotFound
	}
	output, err := exec.CommandContext(ctx, s.path, "find-generic-password", "-a", "Echo", "-s", key, "-w").Output()
	if err != nil {
		return "", ErrSecretNotFound
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

func (s *darwinSecretStore) Set(ctx context.Context, key, value string) error {
	if s.path == "" {
		return errors.New("OS credential store is unavailable")
	}
	return exec.CommandContext(ctx, s.path, "add-generic-password", "-a", "Echo", "-s", key, "-w", value, "-U").Run()
}

func (s *darwinSecretStore) Delete(ctx context.Context, key string) error {
	if s.path == "" {
		return nil
	}
	err := exec.CommandContext(ctx, s.path, "delete-generic-password", "-a", "Echo", "-s", key).Run()
	if err != nil {
		return ErrSecretNotFound
	}
	return nil
}
