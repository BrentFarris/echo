package store

import "testing"

func TestSingleInstanceLockReleasesAfterClose(t *testing.T) {
	dir := t.TempDir()
	first, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if second, e := Open(dir); e == nil {
		second.Close()
		t.Fatal("two servers opened the same data directory")
	}
	first.Close()
	third, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	third.Close()
}
