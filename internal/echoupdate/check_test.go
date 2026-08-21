package echoupdate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	localHash  = "1111111111111111111111111111111111111111"
	remoteHash = "2222222222222222222222222222222222222222"
)

func TestCheckComparesLocalAndRemoteMaster(t *testing.T) {
	checkedAt := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.FixedZone("test", -5*60*60))
	var calls [][]string
	checker := &Checker{
		now: func() time.Time { return checkedAt },
		run: func(_ context.Context, dir, name string, arguments ...string) (string, error) {
			calls = append(calls, append([]string{dir, name}, arguments...))
			if arguments[0] == "rev-parse" {
				return localHash + "\n", nil
			}
			return remoteHash + "\t" + MasterRef + "\n", nil
		},
	}

	status, err := checker.Check(context.Background(), `C:\Echo`)
	if err != nil {
		t.Fatal(err)
	}
	if !status.UpdateAvailable || status.LocalMasterCommit != localHash || status.RemoteMasterCommit != remoteHash {
		t.Fatalf("status = %#v", status)
	}
	if !status.CheckedAt.Equal(checkedAt.UTC()) {
		t.Fatalf("checked at = %v", status.CheckedAt)
	}
	want := [][]string{
		{`C:\Echo`, "git", "rev-parse", "--verify", MasterRef + "^{commit}"},
		{`C:\Echo`, "git", "ls-remote", "--exit-code", RepositoryURL, MasterRef},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestCheckTreatsMatchingHashesAsCurrent(t *testing.T) {
	checker := &Checker{
		now: time.Now,
		run: func(_ context.Context, _ string, _ string, arguments ...string) (string, error) {
			if arguments[0] == "rev-parse" {
				return strings.ToUpper(localHash), nil
			}
			return localHash + "\t" + MasterRef, nil
		},
	}
	status, err := checker.Check(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if status.UpdateAvailable {
		t.Fatalf("matching hashes reported an update: %#v", status)
	}
}

func TestCheckReportsCommandAndMalformedOutputErrors(t *testing.T) {
	t.Run("command failure", func(t *testing.T) {
		checker := &Checker{now: time.Now, run: func(context.Context, string, string, ...string) (string, error) {
			return "", errors.New("offline")
		}}
		if _, err := checker.Check(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "local master") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("malformed remote", func(t *testing.T) {
		checker := &Checker{now: time.Now, run: func(_ context.Context, _ string, _ string, arguments ...string) (string, error) {
			if arguments[0] == "rev-parse" {
				return localHash, nil
			}
			return "not-a-ref", nil
		}}
		if _, err := checker.Check(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), MasterRef) {
			t.Fatalf("error = %v", err)
		}
	})
}
