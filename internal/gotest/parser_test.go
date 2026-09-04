package gotest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/brent/echo/internal/gotestconfig"
)

func TestDiscoverSourceFindsGoTestTargetsAndNestedRuns(t *testing.T) {
	source := `package sample

import check "testing"

func TestMain(m *check.M) {}
func TestAlpha(t *check.T) {
	t.Run("space name", func(inner *check.T) {
		inner.Run("nested.+", func(deep *check.T) {})
	})
}
func Testlower(t *check.T) {}
func BenchmarkFast(b *check.B) {
	b.Run("size/one", func(child *check.B) {})
}
func FuzzBytes(f *check.F) {}
func ExampleThing() {}
`
	lenses := DiscoverSource("sample_test.go", source)
	got := make([]string, 0, len(lenses))
	for _, item := range lenses {
		got = append(got, item.Action+":"+item.Title+":"+item.Target.Kind+":"+pathSelector(item.Target.Path))
	}
	want := []string{
		"run:run package tests:package_tests:",
		"run:run file tests:file_tests:",
		"run:run package benchmarks:package_benchmarks:",
		"run:run file benchmarks:file_benchmarks:",
		"run:run test:test:^TestAlpha$", "debug:debug test:test:^TestAlpha$",
		"run:run test:subtest:^TestAlpha$/^space_name$", "debug:debug test:subtest:^TestAlpha$/^space_name$",
		`run:run test:subtest:^TestAlpha$/^space_name$/^nested\.\+$`, `debug:debug test:subtest:^TestAlpha$/^space_name$/^nested\.\+$`,
		"run:run benchmark:benchmark:^BenchmarkFast$", "debug:debug benchmark:benchmark:^BenchmarkFast$",
		"run:run benchmark:subbenchmark:^BenchmarkFast$/^size$/^one$", "debug:debug benchmark:subbenchmark:^BenchmarkFast$/^size$/^one$",
		"run:run test:fuzz:^FuzzBytes$", "debug:debug test:fuzz:^FuzzBytes$",
		"run:run test:example:^ExampleThing$", "debug:debug test:example:^ExampleThing$",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lenses:\n got %#v\nwant %#v", got, want)
	}
}

func TestDiscoverSourceSupportsDotImportAndRejectsIncompleteSource(t *testing.T) {
	source := "package p\nimport . \"testing\"\nfunc TestDot(t *T) {}\n"
	if got := DiscoverSource("dot_test.go", source); len(got) != 4 {
		t.Fatalf("dot-import lenses = %#v", got)
	}
	if got := DiscoverSource("broken_test.go", "package p\nfunc TestBroken("); len(got) != 0 {
		t.Fatalf("incomplete source returned lenses: %#v", got)
	}
}

func TestDiscoverSourceRejectsInvalidCallbacksAndFuzzRun(t *testing.T) {
	source := `package p
import "testing"
func TestValid(t *testing.T) {
	t.Run("wrong", func(value int) {})
	t.Run("valid", func(child *testing.T) {})
}
func FuzzInput(f *testing.F) {
	f.Run("not-a-fuzz-operation", func(child *testing.T) {})
}
`
	lenses := DiscoverSource("callbacks_test.go", source)
	paths := make([]string, 0, len(lenses))
	for _, item := range lenses {
		if item.Target.Kind == TargetSubtest || item.Target.Kind == TargetSubbenchmark {
			paths = append(paths, pathSelector(item.Target.Path))
		}
	}
	want := []string{"^TestValid$/^valid$", "^TestValid$/^valid$"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("nested targets = %#v, want %#v", paths, want)
	}
}

func TestPackageBenchmarkDiscoveryAcrossFiles(t *testing.T) {
	directory := t.TempDir()
	current := filepath.Join(directory, "current_test.go")
	other := filepath.Join(directory, "bench_test.go")
	if err := os.WriteFile(current, []byte("package p\nfunc TestOnly(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("package p\nimport \"testing\"\nfunc BenchmarkOther(b *testing.B) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !packageBenchmarkExists(directory, current) {
		t.Fatal("benchmark in a sibling test file was not found")
	}
	lenses := addPackageBenchmarkLens(DiscoverSource(current, "package p\nimport \"testing\"\nfunc TestOnly(t *testing.T) {}\n"))
	if !hasTargetKind(lenses, TargetPackageBenchmarks) || hasTargetKind(lenses, TargetFileBenchmarks) {
		t.Fatalf("package benchmark lenses = %#v", lenses)
	}
}

func TestDiscoverSourceUsesGoSubtestUniqueNames(t *testing.T) {
	source := `package p
import "testing"
func TestNames(t *testing.T) {
	t.Run("", func(child *testing.T) {})
	t.Run("same", func(child *testing.T) {})
	t.Run("same", func(child *testing.T) {})
	t.Run("same#01", func(child *testing.T) {})
}
`
	lenses := DiscoverSource("names_test.go", source)
	paths := make([]string, 0, 8)
	for _, item := range lenses {
		if item.Action == "run" && item.Target.Kind == TargetSubtest {
			paths = append(paths, pathSelector(item.Target.Path))
		}
	}
	want := []string{
		`^TestNames$/^#00$`,
		`^TestNames$/^same$`,
		`^TestNames$/^same#01$`,
		`^TestNames$/^same#01#01$`,
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("subtest selectors = %#v, want %#v", paths, want)
	}
}

func TestBuildCommandAppliesSelectorsSettingsAndArgs(t *testing.T) {
	info := sourceInfo{Tests: []string{"TestAlpha", "ExampleThing"}, Benchmarks: []string{"BenchmarkFast"}}
	config := gotestconfig.GoConfig{
		CodeLens: true, Timeout: "45s", Tags: "integration",
		Flags: []string{"-count=1", "-run", "user-choice", "-args", "-custom", "value"},
	}
	plan, err := buildCommand(Target{Kind: TargetSubtest, Path: []string{"TestAlpha", "nested.+"}}, info, config)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"test", "-timeout", "45s", "-tags", "integration", "-run", `^TestAlpha$/^nested\.\+$`, ".", "-count=1", "-args", "-custom", "value"}
	if !reflect.DeepEqual(plan.Args, want) {
		t.Fatalf("args = %#v, want %#v", plan.Args, want)
	}
	if plan.BuildFlags != "-tags integration -count=1" {
		t.Fatalf("build flags = %q", plan.BuildFlags)
	}
	wantDebug := []string{"-test.timeout=45s", `-test.run=^TestAlpha$/^nested\.\+$`, "-custom", "value"}
	if !reflect.DeepEqual(plan.DebugArguments, wantDebug) {
		t.Fatalf("debug args = %#v, want %#v", plan.DebugArguments, wantDebug)
	}

	benchmark, err := buildCommand(Target{Kind: TargetFileBenchmarks}, info, gotestconfig.DefaultGoConfig())
	if err != nil {
		t.Fatal(err)
	}
	benchmarkWant := []string{"test", "-timeout", "30s", "-benchmem", "-run=^$", "-bench", `^(?:BenchmarkFast)$`, "."}
	if !reflect.DeepEqual(benchmark.Args, benchmarkWant) {
		t.Fatalf("benchmark args = %#v, want %#v", benchmark.Args, benchmarkWant)
	}
}

func TestBuildCommandTargetMatrix(t *testing.T) {
	info := sourceInfo{Tests: []string{"TestAlpha", "ExampleThing", "FuzzInput"}, Benchmarks: []string{"BenchmarkFast", "BenchmarkSlow"}}
	cases := []struct {
		name      string
		target    Target
		selector  string
		benchmark bool
	}{
		{name: "package tests", target: Target{Kind: TargetPackageTests}},
		{name: "file tests", target: Target{Kind: TargetFileTests}, selector: `^(?:TestAlpha|ExampleThing|FuzzInput)$`},
		{name: "test", target: Target{Kind: TargetTest, Path: []string{"TestAlpha"}}, selector: `^TestAlpha$`},
		{name: "subtest", target: Target{Kind: TargetSubtest, Path: []string{"TestAlpha", "spaces\tand/control\x01"}}, selector: `^TestAlpha$/^spaces_and$/^control\\x01$`},
		{name: "package benchmarks", target: Target{Kind: TargetPackageBenchmarks}, selector: `.`, benchmark: true},
		{name: "file benchmarks", target: Target{Kind: TargetFileBenchmarks}, selector: `^(?:BenchmarkFast|BenchmarkSlow)$`, benchmark: true},
		{name: "benchmark", target: Target{Kind: TargetBenchmark, Path: []string{"BenchmarkFast"}}, selector: `^BenchmarkFast$`, benchmark: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			plan, err := buildCommand(testCase.target, info, gotestconfig.DefaultGoConfig())
			if err != nil {
				t.Fatal(err)
			}
			if plan.Selector != testCase.selector || plan.Benchmark != testCase.benchmark {
				t.Fatalf("plan selector=%q benchmark=%v", plan.Selector, plan.Benchmark)
			}
		})
	}
}

func TestBuildCommandRespectsUserTagsAndRemovesSelectors(t *testing.T) {
	info := sourceInfo{Tests: []string{"TestAlpha"}}
	config := gotestconfig.GoConfig{
		CodeLens: true, Timeout: "0s", Tags: "automatic",
		Flags: []string{"-tags=user", "--bench=ignored", "-run=ignored", "-count", "2"},
	}
	plan, err := buildCommand(Target{Kind: TargetTest, Path: []string{"TestAlpha"}}, info, config)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"test", "-timeout", "0s", "-run", "^TestAlpha$", ".", "-tags=user", "-count", "2"}
	if !reflect.DeepEqual(plan.Args, want) {
		t.Fatalf("args = %#v, want %#v", plan.Args, want)
	}
}
