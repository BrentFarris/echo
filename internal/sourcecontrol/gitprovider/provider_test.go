package gitprovider

import (
	"encoding/json"
	"testing"

	"github.com/brent/echo/internal/gitservice"
	"github.com/brent/echo/internal/sourcecontrol"
)

func TestHistoryFromGitKeepsJSONCollectionsAsArrays(t *testing.T) {
	tests := []struct {
		name    string
		history gitservice.History
		check   func(*testing.T, sourcecontrol.History)
	}{
		{
			name: "empty history",
			check: func(t *testing.T, history sourcecontrol.History) {
				if history.Commits == nil || len(history.Commits) != 0 {
					t.Fatalf("commits = %#v, want an empty non-nil slice", history.Commits)
				}
			},
		},
		{
			name: "tagged and untagged commits",
			history: gitservice.History{
				Commits: []gitservice.Commit{
					{Hash: "tagged", Parents: []string{"parent"}, Refs: []string{"HEAD -> main"}, Subject: "tagged commit"},
					{Hash: "root", Subject: "untagged root commit"},
				},
				NextOffset: 2,
				HasMore:    true,
			},
			check: func(t *testing.T, history sourcecontrol.History) {
				if len(history.Commits) != 2 || history.Commits[0].Hash != "tagged" || history.Commits[1].Hash != "root" {
					t.Fatalf("commit ordering changed: %#v", history.Commits)
				}
				if history.Commits[1].Parents == nil || history.Commits[1].Refs == nil {
					t.Fatalf("empty commit collections must be non-nil: %#v", history.Commits[1])
				}
				if history.NextOffset != 2 || !history.HasMore {
					t.Fatalf("pagination changed: %#v", history)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			history := historyFromGit(test.history)
			test.check(t, history)
			encoded, err := json.Marshal(history)
			if err != nil {
				t.Fatal(err)
			}
			var wire sourcecontrol.History
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.Commits == nil {
				t.Fatalf("commits serialized as null: %s", encoded)
			}
			for _, commit := range wire.Commits {
				if commit.Parents == nil || commit.Refs == nil {
					t.Fatalf("commit collections serialized as null: %s", encoded)
				}
			}
		})
	}
}
