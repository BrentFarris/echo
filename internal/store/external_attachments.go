package store

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (s *Store) externalAttachmentDirectory(chat string) string {
	// Hashing IDs also confines Swarm projection IDs and arbitrary imported IDs.
	return filepath.Join(s.dir, "external-attachments", fmt.Sprintf("%x", sha256.Sum256([]byte(chat))))
}

// ExternalAttachment materializes the original upload for native file tools.
// Its cache belongs to the chat, so resumed sessions can still refer to it.
func (s *Store) ExternalAttachment(chat, id string) (string, error) {
	a, data, err := s.Attachment(chat, id, false)
	if err != nil {
		return "", err
	}
	dir := s.externalAttachmentDirectory(chat)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	ext := filepath.Ext(strings.ReplaceAll(a.Name, "\\", "/"))
	if len(ext) > 20 || strings.ContainsAny(ext, "/\\:\x00") {
		ext = ""
	}
	path, err := filepath.Abs(filepath.Join(dir, fmt.Sprintf("%x", sha256.Sum256([]byte(id)))+ext))
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		return "", err
	}
	return path, nil
}
