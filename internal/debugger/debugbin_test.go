package debugger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/debugconfig"
)

func TestDelveBuildsBinary(t *testing.T) {
	profile := debugconfig.AdapterProfile{AdapterID: "go"}
	cases := []struct {
		name     string
		profile  debugconfig.AdapterProfile
		request  string
		mode     string
		expected bool
	}{
		{"launch debug", profile, "launch", "debug", true},
		{"launch test", profile, "launch", "test", true},
		{"launch exec", profile, "launch", "exec", false},
		{"launch unset mode", profile, "launch", "", true},
		{"attach", profile, "attach", "debug", false},
		{"other adapter", debugconfig.AdapterProfile{AdapterID: "lldb"}, "launch", "debug", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			arguments := map[string]any{}
			if testCase.mode != "" {
				arguments["mode"] = testCase.mode
			}
			if got := delveBuildsBinary(testCase.profile, testCase.request, arguments); got != testCase.expected {
				t.Fatalf("delveBuildsBinary() = %v, want %v", got, testCase.expected)
			}
		})
	}
}

func TestRemoveHostDelveBinaries(t *testing.T) {
	directory := t.TempDir()
	leftover := filepath.Join(directory, "__debug_bin.exe329938022")
	plain := filepath.Join(directory, "__debug_bin")
	unrelated := filepath.Join(directory, "main.go")
	for _, path := range []string{leftover, plain, unrelated} {
		if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if !removeHostDelveBinaries(directory) {
		t.Fatal("expected removal to succeed")
	}
	for _, path := range []string{leftover, plain} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, got %v", path, err)
		}
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated file was removed: %v", err)
	}

	// A missing directory must report nothing left to remove.
	if !removeHostDelveBinaries(filepath.Join(directory, "missing")) {
		t.Fatal("missing directory should report no remaining binaries")
	}
}
