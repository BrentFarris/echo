package p4

import (
	"context"
	"strings"
	"testing"
)

func TestTaggedRecordsAndPartialErrors(t *testing.T) {
	rows, err := parseRecords([]byte("{\"depotFile\":\"//depot/ü%23.txt\",\"rev\":2}\n{\"severity\":3,\"generic\":6,\"data\":\"permission denied\"}\n"))
	if len(rows) != 1 || rows[0]["rev"] != "2" || err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if _, err := parseRecords([]byte(`{"clientFile":"C:/bad�name"}`)); err == nil {
		t.Fatal("lossy filename accepted")
	}
	if rows, err := parseRecords(nil); len(rows) != 0 || err != nil {
		t.Fatalf("empty rows=%+v err=%v", rows, err)
	}
}
func TestExactArgumentsAndEmptyFiles(t *testing.T) {
	if got := fileArg("a%ü@#*.txt"); got != "a%25ü%40%23%2A.txt" {
		t.Fatal(got)
	}
	p := &Provider{commandHook: func([]string) error { t.Fatal("empty paths ran a command"); return nil }}
	if _, err := p.files(context.Background(), connection{}, []string{"reconcile"}, nil); err != nil {
		t.Fatal(err)
	}
}
func TestBoundedOutput(t *testing.T) {
	buffer := boundedBuffer{max: 4}
	if _, err := buffer.Write([]byte("12345")); err == nil || !buffer.exceeded {
		t.Fatal("output limit not enforced")
	}
}
func TestChangeFormPreservesUnrelatedFields(t *testing.T) {
	form := "Change: 12\nClient: test\nDescription:\n\told\nFiles:\n\t//depot/keep.txt\nJobs:\n\tjob1\n"
	updated := replaceField(form, "Description", "new\nsecond line")
	if !strings.Contains(updated, "\tnew\n\tsecond line") || !strings.Contains(updated, "Files:\n\t//depot/keep.txt") || !strings.Contains(updated, "Jobs:\n\tjob1") {
		t.Fatal(updated)
	}
	empty := replaceField(replaceField(form, "Files", ""), "Jobs", "")
	if strings.Contains(empty, "//depot/") || strings.Contains(empty, "job1") {
		t.Fatal("creation would sweep default files/jobs")
	}
}
