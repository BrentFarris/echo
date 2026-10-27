package broker

import (
	"github.com/brent/echo/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestFileScopeAndRevision(t *testing.T) {
	root := t.TempDir()
	f := &Files{}
	b := domain.Bot{Roots: []string{root}}
	path := filepath.Join(root, "test.txt")
	if _, e := f.Write(b, path, "first", ""); e != nil {
		t.Fatal(e)
	}
	v, e := f.Read(b, path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write(b, path, "second", v["revision"].(string)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write(b, path, "bad", v["revision"].(string)); e == nil {
		t.Fatal("stale write accepted")
	}
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("secret"), 0600)
	if _, e = f.Read(b, outside); e == nil {
		t.Fatal("outside read accepted")
	}
	link := filepath.Join(root, "escape")
	if e = os.Symlink(outside, link); e == nil {
		if _, e = f.Read(b, link); e == nil {
			t.Fatal("symlink escape accepted")
		}
	}
}
