// Package gotest implements Echo's built-in Go testing CodeLens support.
package gotest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxSourceBytes = 4 << 20

const (
	TargetPackageTests      = "package_tests"
	TargetFileTests         = "file_tests"
	TargetTest              = "test"
	TargetExample           = "example"
	TargetFuzz              = "fuzz"
	TargetSubtest           = "subtest"
	TargetPackageBenchmarks = "package_benchmarks"
	TargetFileBenchmarks    = "file_benchmarks"
	TargetBenchmark         = "benchmark"
	TargetSubbenchmark      = "subbenchmark"
)

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Target struct {
	Kind string   `json:"kind"`
	Name string   `json:"name,omitempty"`
	Path []string `json:"path,omitempty"`
}

type Lens struct {
	Range  Range  `json:"range"`
	Title  string `json:"title"`
	Action string `json:"action"`
	Target Target `json:"target"`
}

type sourceInfo struct {
	Lenses     []Lens
	Tests      []string
	Benchmarks []string
}

func DiscoverSource(filename, source string) []Lens {
	info, ok := parseSource(filename, source)
	if !ok {
		return nil
	}
	return info.Lenses
}

func addPackageBenchmarkLens(lenses []Lens) []Lens {
	if len(lenses) < 2 {
		return lenses
	}
	for _, item := range lenses {
		if item.Target.Kind == TargetPackageBenchmarks {
			return lenses
		}
	}
	result := make([]Lens, 0, len(lenses)+1)
	result = append(result, lenses[:2]...)
	result = append(result, lens(lenses[0].Range, "run package benchmarks", "run", Target{Kind: TargetPackageBenchmarks}))
	return append(result, lenses[2:]...)
}

func parseSource(filename, source string) (sourceInfo, bool) {
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, filename, source, parser.AllErrors)
	if err != nil || file == nil {
		return sourceInfo{}, false
	}
	aliases := testingAliases(file)
	packageRange := pointRange(set, file.Package)
	info := sourceInfo{Lenses: []Lens{
		lens(packageRange, "run package tests", "run", Target{Kind: TargetPackageTests}),
		lens(packageRange, "run file tests", "run", Target{Kind: TargetFileTests}),
	}}
	runNames := map[string]int32{}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Body == nil {
			continue
		}
		kind, parameter := functionKind(function, aliases)
		if kind == "" || function.Name.Name == "TestMain" {
			continue
		}
		name := function.Name.Name
		rangeValue := pointRange(set, function.Pos())
		target := Target{Kind: kind, Name: name, Path: []string{name}}
		if kind == TargetBenchmark {
			info.Benchmarks = append(info.Benchmarks, name)
			info.Lenses = append(info.Lenses,
				lens(rangeValue, "run benchmark", "run", target),
				lens(rangeValue, "debug benchmark", "debug", target),
			)
			walkRuns(set, function.Body, parameter, target.Path, name, true, aliases, runNames, &info.Lenses)
			continue
		}
		info.Tests = append(info.Tests, name)
		info.Lenses = append(info.Lenses,
			lens(rangeValue, "run test", "run", target),
			lens(rangeValue, "debug test", "debug", target),
		)
		if kind == TargetTest {
			walkRuns(set, function.Body, parameter, target.Path, name, false, aliases, runNames, &info.Lenses)
		}
	}
	if len(info.Benchmarks) > 0 {
		info.Lenses = append(info.Lenses[:2], append([]Lens{
			lens(packageRange, "run package benchmarks", "run", Target{Kind: TargetPackageBenchmarks}),
			lens(packageRange, "run file benchmarks", "run", Target{Kind: TargetFileBenchmarks}),
		}, info.Lenses[2:]...)...)
	}
	return info, true
}

func functionKind(function *ast.FuncDecl, aliases map[string]bool) (string, string) {
	name := function.Name.Name
	if function.Type.Results != nil && len(function.Type.Results.List) != 0 {
		return "", ""
	}
	parameters := function.Type.Params
	if strings.HasPrefix(name, "Example") && isTestName(name, "Example") {
		if parameters == nil || len(parameters.List) == 0 {
			return TargetExample, ""
		}
		return "", ""
	}
	if parameters == nil || len(parameters.List) != 1 || len(parameters.List[0].Names) != 1 {
		return "", ""
	}
	parameter := parameters.List[0]
	parameterName := parameter.Names[0].Name
	switch {
	case isTestName(name, "Test") && isTestingPointer(parameter.Type, aliases, "T"):
		return TargetTest, parameterName
	case isTestName(name, "Benchmark") && isTestingPointer(parameter.Type, aliases, "B"):
		return TargetBenchmark, parameterName
	case isTestName(name, "Fuzz") && isTestingPointer(parameter.Type, aliases, "F"):
		return TargetFuzz, parameterName
	default:
		return "", ""
	}
}

func testingAliases(file *ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, item := range file.Imports {
		value, err := strconv.Unquote(item.Path.Value)
		if err != nil || value != "testing" {
			continue
		}
		name := "testing"
		if item.Name != nil {
			name = item.Name.Name
		}
		if name != "_" {
			aliases[name] = true
		}
	}
	return aliases
}

func isTestingPointer(expression ast.Expr, aliases map[string]bool, typeName string) bool {
	pointer, ok := expression.(*ast.StarExpr)
	if !ok {
		return false
	}
	if identifier, ok := pointer.X.(*ast.Ident); ok {
		return identifier.Name == typeName && aliases["."]
	}
	selector, ok := pointer.X.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != typeName {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && aliases[identifier.Name]
}

func isTestName(name, prefix string) bool {
	if name == prefix {
		return true
	}
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	next, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return next != utf8.RuneError && !unicode.IsLower(next)
}

func walkRuns(set *token.FileSet, node ast.Node, receiver string, parentPath []string, parentName string, benchmark bool, aliases map[string]bool, names map[string]int32, out *[]Lens) {
	if receiver == "" || node == nil {
		return
	}
	ast.Inspect(node, func(candidate ast.Node) bool {
		call, ok := candidate.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Run" {
			return true
		}
		owner, ok := selector.X.(*ast.Ident)
		if !ok || owner.Name != receiver || len(call.Args) != 2 {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		name, err := strconv.Unquote(literal.Value)
		if err != nil {
			return true
		}
		callback, ok := call.Args[1].(*ast.FuncLit)
		if !ok || !validRunCallback(callback, benchmark, aliases) {
			return true
		}
		fullName := uniqueRunName(names, parentName, sanitizeTestName(name))
		pathName := strings.TrimPrefix(fullName, parentName+"/")
		path := append(append([]string(nil), parentPath...), pathName)
		kind, runTitle, debugTitle := TargetSubtest, "run test", "debug test"
		if benchmark {
			kind, runTitle, debugTitle = TargetSubbenchmark, "run benchmark", "debug benchmark"
		}
		target := Target{Kind: kind, Name: name, Path: path}
		rangeValue := pointRange(set, call.Pos())
		*out = append(*out, lens(rangeValue, runTitle, "run", target), lens(rangeValue, debugTitle, "debug", target))
		walkRuns(set, callback.Body, callback.Type.Params.List[0].Names[0].Name, path, fullName, benchmark, aliases, names, out)
		return false
	})
}

func uniqueRunName(names map[string]int32, parent, subname string) string {
	base := parent + "/" + subname
	for {
		count := names[base]
		names[base] = count + 1
		if count == 0 && subname != "" {
			prefix, number := parseRunNumber(base)
			if len(prefix) < len(base) && number < names[prefix] {
				continue
			}
			return base
		}
		candidate := fmt.Sprintf("%s#%02d", base, count)
		if names[candidate] == 0 {
			return candidate
		}
	}
}

func parseRunNumber(value string) (string, int32) {
	index := strings.LastIndex(value, "#")
	if index < 0 {
		return value, 0
	}
	prefix, suffix := value[:index], value[index+1:]
	if len(suffix) < 2 || (len(suffix) > 2 && suffix[0] == '0') || (suffix == "00" && !strings.HasSuffix(prefix, "/")) {
		return value, 0
	}
	number, err := strconv.ParseInt(suffix, 10, 32)
	if err != nil || number < 0 {
		return value, 0
	}
	return prefix, int32(number)
}

func validRunCallback(callback *ast.FuncLit, benchmark bool, aliases map[string]bool) bool {
	if callback.Type.Results != nil && len(callback.Type.Results.List) != 0 {
		return false
	}
	parameters := callback.Type.Params
	if parameters == nil || len(parameters.List) != 1 || len(parameters.List[0].Names) != 1 {
		return false
	}
	typeName := "T"
	if benchmark {
		typeName = "B"
	}
	return isTestingPointer(parameters.List[0].Type, aliases, typeName)
}

func lens(rangeValue Range, title, action string, target Target) Lens {
	return Lens{Range: rangeValue, Title: title, Action: action, Target: target}
}

func pointRange(set *token.FileSet, position token.Pos) Range {
	value := set.Position(position)
	start := Position{Line: max(0, value.Line-1), Character: max(0, value.Column-1)}
	return Range{Start: start, End: start}
}

func sameTarget(left, right Target) bool {
	if left.Kind != right.Kind || left.Name != right.Name || len(left.Path) != len(right.Path) {
		return false
	}
	for index := range left.Path {
		if left.Path[index] != right.Path[index] {
			return false
		}
	}
	return true
}
