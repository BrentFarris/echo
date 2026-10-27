package credentials

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

type fileStore struct {
	dir string
	mu  sync.Mutex
}

// NewFileStore stores authenticated ciphertext separately from the profile DB.
// The key and vault require the same owner-only filesystem protection. This
// does not claim protection against an administrator or the owning OS account.
func NewFileStore(dir string) SecretStore { return &fileStore{dir: dir} }
func (s *fileStore) Available(ctx context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.cipher(ctx)
	return err == nil
}
func (s *fileStore) cipher(ctx context.Context) (cipher.AEAD, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(s.dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("credential vault must be a real private directory")
	}
	if err = os.Chmod(s.dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, "master.key")
	key, err := readPrivate(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		var file *os.File
		file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			key, err = readPrivate(path)
		} else if err == nil {
			_, err = file.Write(key)
			if err == nil {
				err = file.Sync()
			}
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = syncVault(s.dir)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid credential vault key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&0077 != 0 {
		return nil, errors.New("credential file must be an owner-only regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil || !os.SameFile(info, current) {
		return nil, errors.New("credential file changed while opening")
	}
	return io.ReadAll(io.LimitReader(file, 1<<20))
}
func (s *fileStore) path(key string) string {
	hash := sha256.Sum256([]byte(key))
	return filepath.Join(s.dir, hex.EncodeToString(hash[:])+".secret")
}
func (s *fileStore) Get(ctx context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	aead, err := s.cipher(ctx)
	if err != nil {
		return "", err
	}
	data, err := readPrivate(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrSecretNotFound
	}
	if err != nil {
		return "", err
	}
	if len(data) < aead.NonceSize() {
		return "", errors.New("invalid encrypted credential")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(key))
	return string(plain), err
}
func (s *fileStore) Set(ctx context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	aead, err := s.cipher(ctx)
	if err != nil {
		return err
	}
	if len(value) > 512<<10 {
		return errors.New("credential is too large")
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	data := aead.Seal(nonce, nonce, []byte(value), []byte(key))
	file, err := os.CreateTemp(s.dir, ".credential-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, s.path(key)); err != nil {
		return err
	}
	return syncVault(s.dir)
}
func (s *fileStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.Remove(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return ErrSecretNotFound
	}
	if err != nil {
		return err
	}
	return syncVault(s.dir)
}

func syncVault(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
