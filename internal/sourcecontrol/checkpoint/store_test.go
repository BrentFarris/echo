package checkpoint

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func manifestFor(entry FileState) Manifest {
	return Manifest{
		Version: Version, WorkspaceID: "workspace ü", ProviderID: "fossil", RepositoryID: "fossil:opaque",
		CheckoutFingerprint: "checkout-fingerprint", Baseline: "baseline", Generation: 1, Entries: []FileState{entry},
	}
}

func TestStoreAtomicallyReplacesOneManifestAndCollectsOldBlobs(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "checkpoints"))
	firstData := append([]byte("binary\x00contents\n"), 0xff)
	firstID := BlobID(firstData)
	first := FileState{Path: "資料/first file.bin", Exists: true, Mode: 0o755, Hash: firstID, Blob: firstID, Kind: "modified"}
	manifest := manifestFor(first)
	if err := store.ReplaceManifest(manifest, map[string][]byte{firstID: firstData}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadManifest(manifest.WorkspaceID, manifest.ProviderID, manifest.RepositoryID)
	if err != nil || loaded == nil || loaded.Generation != 1 || len(loaded.Entries) != 1 {
		t.Fatalf("loaded manifest = %#v, %v", loaded, err)
	}
	data, err := store.ReadBlob(manifest.WorkspaceID, manifest.ProviderID, manifest.RepositoryID, firstID)
	if err != nil || !bytes.Equal(data, firstData) {
		t.Fatalf("loaded blob = %x, %v", data, err)
	}

	secondData := []byte("replacement\n")
	secondID := BlobID(secondData)
	manifest.Generation = 2
	manifest.Entries = []FileState{{Path: "second.txt", Exists: true, Mode: 0o644, Hash: secondID, Blob: secondID, Kind: "added"}}
	if err := store.ReplaceManifest(manifest, map[string][]byte{secondID: secondData}); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.LoadManifest(manifest.WorkspaceID, manifest.ProviderID, manifest.RepositoryID)
	if err != nil || loaded == nil || loaded.Generation != 2 || loaded.Entries[0].Path != "second.txt" {
		t.Fatalf("replacement manifest = %#v, %v", loaded, err)
	}
	if _, err := store.ReadBlob(manifest.WorkspaceID, manifest.ProviderID, manifest.RepositoryID, firstID); !os.IsNotExist(unwrapPathError(err)) {
		t.Fatalf("obsolete blob remains or returned unexpected error: %v", err)
	}
}

func TestFailedReplacementLeavesPreviousManifestIntact(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "checkpoints"))
	data := []byte("safe\n")
	id := BlobID(data)
	manifest := manifestFor(FileState{Path: "safe.txt", Exists: true, Hash: id, Blob: id})
	if err := store.ReplaceManifest(manifest, map[string][]byte{id: data}); err != nil {
		t.Fatal(err)
	}
	missingData := []byte("missing")
	missingID := BlobID(missingData)
	broken := manifest
	broken.Generation = 2
	broken.Entries = []FileState{{Path: "missing.txt", Exists: true, Hash: missingID, Blob: missingID}}
	if err := store.ReplaceManifest(broken, nil); err == nil {
		t.Fatal("replacement without blob data unexpectedly succeeded")
	}
	loaded, err := store.LoadManifest(manifest.WorkspaceID, manifest.ProviderID, manifest.RepositoryID)
	if err != nil || loaded == nil || loaded.Generation != 1 || loaded.Entries[0].Path != "safe.txt" {
		t.Fatalf("previous manifest was not retained: %#v, %v", loaded, err)
	}
}

func TestRecoveryJournalSurvivesRestartAndCanBeClearedIndependently(t *testing.T) {
	root := filepath.Join(t.TempDir(), "checkpoints")
	store := New(root)
	data := []byte("frozen")
	id := BlobID(data)
	manifest := manifestFor(FileState{Path: "file.txt", Exists: true, Hash: id, Blob: id})
	if err := store.ReplaceManifest(manifest, map[string][]byte{id: data}); err != nil {
		t.Fatal(err)
	}
	journalData := []byte("later")
	journalID := BlobID(journalData)
	journal := Journal{
		Version: Version, WorkspaceID: manifest.WorkspaceID, ProviderID: manifest.ProviderID, RepositoryID: manifest.RepositoryID,
		CheckoutFingerprint: manifest.CheckoutFingerprint, Baseline: manifest.Baseline, Phase: "prepared",
		Current: []FileState{{Path: "file.txt", Exists: true, Hash: journalID, Blob: journalID}},
	}
	if err := store.WriteJournal(journal, map[string][]byte{journalID: journalData}); err != nil {
		t.Fatal(err)
	}
	restarted := New(root)
	loadedJournal, err := restarted.LoadJournal(manifest.WorkspaceID, manifest.ProviderID, manifest.RepositoryID)
	if err != nil || loadedJournal == nil || loadedJournal.Phase != "prepared" {
		t.Fatalf("journal after restart = %#v, %v", loadedJournal, err)
	}
	if err := restarted.ClearJournal(manifest.WorkspaceID, manifest.ProviderID, manifest.RepositoryID); err != nil {
		t.Fatal(err)
	}
	if loaded, err := restarted.LoadManifest(manifest.WorkspaceID, manifest.ProviderID, manifest.RepositoryID); err != nil || loaded == nil {
		t.Fatalf("clearing journal removed manifest: %#v, %v", loaded, err)
	}
	if loaded, err := restarted.LoadJournal(manifest.WorkspaceID, manifest.ProviderID, manifest.RepositoryID); err != nil || loaded != nil {
		t.Fatalf("journal remains after clear: %#v, %v", loaded, err)
	}
}

func TestRemoveWorkspaceDoesNotAffectOtherWorkspace(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "checkpoints"))
	data := []byte("content")
	id := BlobID(data)
	first := manifestFor(FileState{Path: "file.txt", Exists: true, Hash: id, Blob: id})
	second := first
	second.WorkspaceID = "another workspace"
	if err := store.ReplaceManifest(first, map[string][]byte{id: data}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceManifest(second, map[string][]byte{id: data}); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveWorkspace(first.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.LoadManifest(first.WorkspaceID, first.ProviderID, first.RepositoryID); err != nil || loaded != nil {
		t.Fatalf("removed workspace checkpoint = %#v, %v", loaded, err)
	}
	if loaded, err := store.LoadManifest(second.WorkspaceID, second.ProviderID, second.RepositoryID); err != nil || loaded == nil {
		t.Fatalf("other workspace checkpoint = %#v, %v", loaded, err)
	}
}

func TestStorePreservesSymlinkAndExecutableMetadata(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "checkpoints"))
	target := "../target with space"
	symlink := FileState{
		Path: "links/current", Exists: true, Mode: 0o777, Symlink: true, SymlinkTarget: target,
		Hash: BlobID([]byte("symlink\x00" + target)), Kind: "modified",
	}
	manifest := manifestFor(symlink)
	if err := store.ReplaceManifest(manifest, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadManifest(manifest.WorkspaceID, manifest.ProviderID, manifest.RepositoryID)
	if err != nil || loaded == nil || len(loaded.Entries) != 1 {
		t.Fatalf("loaded symlink manifest = %#v, %v", loaded, err)
	}
	entry := loaded.Entries[0]
	if !entry.Symlink || entry.SymlinkTarget != target || entry.Mode != 0o777 || entry.Hash != symlink.Hash {
		t.Fatalf("symlink metadata changed: %#v", entry)
	}
}

func TestStoreRejectsEscapingAndCorruptFileStates(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "checkpoints"))
	for _, pathValue := range []string{"../outside", "/absolute", "C:/absolute", "nested/../../outside", "unclean//path"} {
		manifest := manifestFor(FileState{Path: pathValue})
		if err := store.ReplaceManifest(manifest, nil); err == nil {
			t.Fatalf("unsafe checkpoint path %q was accepted", pathValue)
		}
	}
	badLink := manifestFor(FileState{Path: "link", Exists: true, Symlink: true, SymlinkTarget: "target", Hash: "wrong"})
	if err := store.ReplaceManifest(badLink, nil); err == nil {
		t.Fatal("corrupt symlink metadata was accepted")
	}
}

func unwrapPathError(err error) error {
	for err != nil {
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = unwrapper.Unwrap()
	}
	return nil
}
