package editorfs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func newTestService(t *testing.T) (*Service, string, string, Root) {
	t.Helper()
	directory := t.TempDir()
	rootPath := filepath.Join(directory, "project")
	if err := os.Mkdir(rootPath, 0o755); err != nil {
		t.Fatal(err)
	}
	service := New(func(id string) (domain.Bot, error) {
		if id != "bot" {
			return domain.Bot{}, ErrNotFound
		}
		return domain.Bot{ID: id, Roots: []string{rootPath}}, nil
	}, filepath.Join(directory, "private"))
	t.Cleanup(service.Close)
	roots, err := service.Roots("bot")
	if err != nil || len(roots) != 1 {
		t.Fatalf("roots: %+v %v", roots, err)
	}
	return service, "bot", rootPath, roots[0]
}

func TestReadSaveRevisionAndMetadata(t *testing.T) {
	service, workspaceID, rootPath, root := newTestService(t)
	path := filepath.Join(rootPath, "main.go")
	if err := os.WriteFile(path, append(append([]byte(nil), utf8BOM...), []byte("package main\r\n")...), 0o640); err != nil {
		t.Fatal(err)
	}
	ref := FileRef{RootID: root.ID, Path: "main.go"}
	snapshot, err := service.Read(workspaceID, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.HasBOM || snapshot.EOL != "crlf" || snapshot.Content != "package main\r\n" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	saved, err := service.Save(workspaceID, SaveRequest{
		Ref: ref, Content: "package main\r\n\r\nfunc main() {}\r\n", ExpectedRevision: snapshot.Revision, HasBOM: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision == snapshot.Revision || !saved.HasBOM {
		t.Fatalf("save did not advance revision: %+v", saved)
	}
	if _, err := service.Save(workspaceID, SaveRequest{Ref: ref, Content: "stale", ExpectedRevision: snapshot.Revision}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("mode was not preserved: %v", info.Mode())
	}
}

func TestTraversalCreateRenameTrashRestore(t *testing.T) {
	service, workspaceID, rootPath, root := newTestService(t)
	if _, err := service.Read(workspaceID, FileRef{RootID: root.ID, Path: "../secret"}); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
	entry, snapshot, err := service.Create(workspaceID, CreateRequest{
		Parent: FileRef{RootID: root.ID}, Name: "hello.txt", Kind: "file", Content: "hello\n",
	})
	if err != nil || snapshot == nil || entry.Name != "hello.txt" {
		t.Fatalf("create: %+v %+v %v", entry, snapshot, err)
	}
	renamed, err := service.Rename(workspaceID, entry.Ref, "greeting.txt")
	if err != nil || renamed.Name != "greeting.txt" {
		t.Fatalf("rename: %+v %v", renamed, err)
	}
	item, err := service.Trash(workspaceID, renamed.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "greeting.txt")); !os.IsNotExist(err) {
		t.Fatalf("trashed file still exists: %v", err)
	}
	restored, err := service.Restore(workspaceID, item.ID)
	if err != nil || restored.Name != "greeting.txt" {
		t.Fatalf("restore: %+v %v", restored, err)
	}
}

func TestMoveEntryBetweenWorkspaceDirectories(t *testing.T) {
	service, workspaceID, rootPath, root := newTestService(t)
	for _, directory := range []string{"source", "target", filepath.Join("parent", "child")} {
		if err := os.MkdirAll(filepath.Join(rootPath, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(rootPath, "source", "open.txt"), []byte("open\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	moved, err := service.Move(workspaceID,
		FileRef{RootID: root.ID, Path: "source/open.txt"},
		FileRef{RootID: root.ID, Path: "target"},
	)
	if err != nil || moved.Ref.Path != "target/open.txt" {
		t.Fatalf("move: %+v %v", moved, err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "source", "open.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still exists after move: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(rootPath, "target", "open.txt")); err != nil || string(content) != "open\n" {
		t.Fatalf("moved content: %q %v", content, err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, "source", "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "source", "folder", "nested.txt"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	movedDirectory, err := service.Move(workspaceID,
		FileRef{RootID: root.ID, Path: "source/folder"},
		FileRef{RootID: root.ID, Path: "target"},
	)
	if err != nil || movedDirectory.Ref.Path != "target/folder" {
		t.Fatalf("move directory: %+v %v", movedDirectory, err)
	}
	if content, err := os.ReadFile(filepath.Join(rootPath, "target", "folder", "nested.txt")); err != nil || string(content) != "nested\n" {
		t.Fatalf("moved directory content: %q %v", content, err)
	}

	if _, err := service.Move(workspaceID,
		FileRef{RootID: root.ID, Path: "parent"},
		FileRef{RootID: root.ID, Path: "parent/child"},
	); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected descendant move rejection, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "source", "open.txt"), []byte("collision\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Move(workspaceID,
		FileRef{RootID: root.ID, Path: "source/open.txt"},
		FileRef{RootID: root.ID, Path: "target"},
	); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected move collision, got %v", err)
	}
}

func TestRejectsBinaryAndOversizedFiles(t *testing.T) {
	service, workspaceID, rootPath, root := newTestService(t)
	if err := os.WriteFile(filepath.Join(rootPath, "binary.dat"), []byte{1, 0, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Read(workspaceID, FileRef{RootID: root.ID, Path: "binary.dat"}); !errors.Is(err, ErrUnsupportedFile) {
		t.Fatalf("expected unsupported binary, got %v", err)
	}
	large, err := os.Create(filepath.Join(rootPath, "large.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(MaxEditableBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = large.Close()
	if _, err := service.Read(workspaceID, FileRef{RootID: root.ID, Path: "large.txt"}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected oversized rejection, got %v", err)
	}
}

func TestMediaMetaClassifiesPreviewableFiles(t *testing.T) {
	service, workspaceID, rootPath, root := newTestService(t)
	if err := os.WriteFile(filepath.Join(rootPath, "pixel.png"), []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "clip.mp4"), []byte{0, 0, 0, 24, 'f', 't', 'y', 'p'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "HERO.AVIF"), []byte("avif-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "notes.md"), []byte("# hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, size, mediaType, truncated, err := service.MediaMeta(workspaceID, FileRef{RootID: root.ID, Path: "pixel.png"})
	if err != nil || mediaType != "image/png" || truncated || size != int64(len([]byte{0x89, 'P', 'N', 'G'})) {
		t.Fatalf("png meta: path=%q size=%d type=%q truncated=%v err=%v", path, size, mediaType, truncated, err)
	}
	if info, statErr := os.Stat(path); statErr != nil || info.Size() == 0 {
		t.Fatalf("resolved media path is unusable: %v %v", info, statErr)
	}

	_, _, mediaType, _, err = service.MediaMeta(workspaceID, FileRef{RootID: root.ID, Path: "clip.mp4"})
	if err != nil || mediaType != "video/mp4" {
		t.Fatalf("mp4 meta: type=%q err=%v", mediaType, err)
	}
	_, _, mediaType, _, err = service.MediaMeta(workspaceID, FileRef{RootID: root.ID, Path: "HERO.AVIF"})
	if err != nil || mediaType != "image/avif" {
		t.Fatalf("uppercase-extension avif meta: type=%q err=%v", mediaType, err)
	}

	var fsError *Error
	_, _, _, _, err = service.MediaMeta(workspaceID, FileRef{RootID: root.ID, Path: "notes.md"})
	if !errors.As(err, &fsError) || fsError.Code != "unsupported_preview" || !errors.Is(fsError.Cause, ErrNotPreviewable) {
		t.Fatalf("expected unsupported preview error, got %T %v", err, err)
	}
	_, _, _, _, err = service.MediaMeta(workspaceID, FileRef{RootID: root.ID, Path: "../escape.png"})
	if !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
	_, _, _, _, err = service.MediaMeta(workspaceID, FileRef{RootID: root.ID, Path: "missing.png"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestMediaTypeForNameCoversBrowserFormats(t *testing.T) {
	cases := map[string]string{
		"a.PNG": "image/png", "b.jpg": "image/jpeg", "c.jpeg": "image/jpeg",
		"d.gif": "image/gif", "e.webp": "image/webp", "f.svg": "image/svg+xml",
		"g.bmp": "image/bmp", "h.ico": "image/x-icon", "i.avif": "image/avif",
		"j.mp4": "video/mp4", "k.m4v": "video/mp4", "l.webm": "video/webm",
		"m.ogv": "video/ogg",
		"n.mp3": "audio/mpeg", "o.wav": "audio/wav", "p.ogg": "audio/ogg",
		"q.oga": "audio/ogg", "r.opus": "audio/ogg", "s.flac": "audio/flac",
		"t.m4a": "audio/mp4", "u.aac": "audio/aac", "v.weba": "audio/webm",
		"z.txt": "", "noext": "",
	}
	for name, want := range cases {
		if got := MediaTypeForName(name); got != want {
			t.Errorf("MediaTypeForName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSymlinkConfinementAndEntryMutations(t *testing.T) {
	service, workspaceID, rootPath, root := newTestService(t)
	target := filepath.Join(rootPath, "target.txt")
	if err := os.WriteFile(target, []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(rootPath, "link.txt")
	if err := os.Symlink("target.txt", link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	ref := FileRef{RootID: root.ID, Path: "link.txt"}
	renamed, err := service.Rename(workspaceID, ref, "renamed-link.txt")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(rootPath, "renamed-link.txt")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("rename did not preserve the symlink: %v %v", info, err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "target\n" {
		t.Fatalf("renaming the link changed its target: %q %v", data, err)
	}
	item, err := service.Trash(workspaceID, renamed.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("trashing the link removed its target: %v", err)
	}
	if _, err := service.Restore(workspaceID, item.ID); err != nil {
		t.Fatal(err)
	}

	external := filepath.Join(filepath.Dir(rootPath), "external.txt")
	if err := os.WriteFile(external, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideLink := filepath.Join(rootPath, "outside.txt")
	if err := os.Symlink(external, outsideLink); err != nil {
		t.Fatal(err)
	}
	outsideRef := FileRef{RootID: root.ID, Path: "outside.txt"}
	if _, err := service.Read(workspaceID, outsideRef); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("expected outside-root symlink read rejection, got %v", err)
	}
	if _, err := service.Rename(workspaceID, outsideRef, "still-outside.txt"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("expected outside-root symlink mutation rejection, got %v", err)
	}
	if _, err := os.Lstat(outsideLink); err != nil {
		t.Fatalf("rejected mutation removed outside-root link: %v", err)
	}
}
