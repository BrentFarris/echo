//go:build windows

package plugins

import (
	"context"
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
)

type windowsCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type windowsSecretStore struct {
	write  *windows.LazyProc
	read   *windows.LazyProc
	delete *windows.LazyProc
	free   *windows.LazyProc
}

func newPlatformSecretStore() SecretStore {
	dll := windows.NewLazySystemDLL("advapi32.dll")
	return &windowsSecretStore{
		write: dll.NewProc("CredWriteW"), read: dll.NewProc("CredReadW"),
		delete: dll.NewProc("CredDeleteW"), free: dll.NewProc("CredFree"),
	}
}

func (s *windowsSecretStore) Available(context.Context) bool {
	return s.write.Find() == nil && s.read.Find() == nil && s.delete.Find() == nil && s.free.Find() == nil
}

func (s *windowsSecretStore) Get(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	target, err := windows.UTF16PtrFromString(key)
	if err != nil {
		return "", err
	}
	var credential *windowsCredential
	result, _, callErr := s.read.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&credential)))
	if result == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return "", ErrSecretNotFound
		}
		return "", callErr
	}
	defer s.free.Call(uintptr(unsafe.Pointer(credential)))
	if credential == nil || credential.CredentialBlobSize == 0 {
		return "", nil
	}
	bytes := append([]byte(nil), unsafe.Slice(credential.CredentialBlob, int(credential.CredentialBlobSize))...)
	return string(bytes), nil
}

func (s *windowsSecretStore) Set(ctx context.Context, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(key)
	if err != nil {
		return err
	}
	username, _ := windows.UTF16PtrFromString("Echo")
	blob := []byte(value)
	credential := windowsCredential{
		Type: credTypeGeneric, TargetName: target, Persist: credPersistLocalMachine, UserName: username,
		CredentialBlobSize: uint32(len(blob)),
	}
	if len(blob) > 0 {
		credential.CredentialBlob = &blob[0]
	}
	result, _, callErr := s.write.Call(uintptr(unsafe.Pointer(&credential)), 0)
	runtime.KeepAlive(blob)
	if result == 0 {
		return callErr
	}
	return nil
}

func (s *windowsSecretStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(key)
	if err != nil {
		return err
	}
	result, _, callErr := s.delete.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0)
	if result == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return ErrSecretNotFound
		}
		return callErr
	}
	return nil
}
