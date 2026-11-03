package broker

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/brent/echo/internal/domain"
)

// ReadBytes keeps workflow and media reads inside the same rooted scope as file tools.
func (f *Files) ReadBytes(bot domain.Bot, path string, limit int64) ([]byte, error) {
	r, p, err := f.resolve(bot, path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	file, err := r.Open(p)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("path is not a regular file")
	}
	if info.Size() > limit {
		return nil, errors.New("file exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("file exceeds size limit")
	}
	return data, err
}

func (f *Files) WriteBytes(bot domain.Bot, path string, data []byte, overwrite bool) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, p, err := f.resolve(bot, path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	overwritten := false
	if info, e := r.Stat(p); e == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("destination is not a regular file")
		}
		if !overwrite {
			return nil, errors.New("file already exists; set overwrite to replace it")
		}
		overwritten = true
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if err = r.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return nil, err
	}
	tmp := p + ".echo2-" + domain.ID()
	file, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	defer r.Remove(tmp)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if overwrite {
		err = r.Rename(tmp, p)
	} else {
		// Link fails atomically if another writer created the destination.
		err = r.Link(tmp, p)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "bytesWritten": len(data), "overwritten": overwritten}, nil
}
