package gotest

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/brent/echo/internal/gotestconfig"
)

type commandPlan struct {
	Args           []string
	Display        string
	Selector       string
	Benchmark      bool
	BuildFlags     string
	DebugArguments []string
}

func buildCommand(target Target, info sourceInfo, config gotestconfig.GoConfig, coverageProfile ...string) (commandPlan, error) {
	config = config.Normalized()
	if err := config.Validate(); err != nil {
		return commandPlan{}, err
	}
	selector, benchmark, err := selectorFor(target, info)
	if err != nil {
		return commandPlan{}, err
	}
	goFlags, binaryArguments := splitArgs(config.Flags)
	goFlags = removeSelectorFlags(goFlags)
	profile := ""
	if len(coverageProfile) > 0 {
		profile = strings.TrimSpace(coverageProfile[0])
	}
	if profile != "" {
		goFlags = removeNamedFlags(goFlags, "coverprofile", "test.coverprofile")
	}
	args := []string{"test", "-timeout", config.Timeout}
	if config.Tags != "" && !hasTagsFlag(goFlags) {
		args = append(args, "-tags", config.Tags)
	}
	if benchmark {
		args = append(args, "-benchmem", "-run=^$", "-bench", selector)
	} else if selector != "" {
		args = append(args, "-run", selector)
	}
	if profile != "" {
		args = append(args, "-coverprofile", profile)
	}
	args = append(args, ".")
	args = append(args, goFlags...)
	if len(binaryArguments) > 0 {
		args = append(args, "-args")
		args = append(args, binaryArguments...)
	}

	debugBuildFlags := append([]string(nil), goFlags...)
	if config.Tags != "" && !hasTagsFlag(debugBuildFlags) {
		debugBuildFlags = append([]string{"-tags", config.Tags}, debugBuildFlags...)
	}
	debugArguments := []string{"-test.timeout=" + config.Timeout}
	if benchmark {
		debugArguments = append(debugArguments, "-test.run=^$", "-test.bench="+selector, "-test.benchmem=true")
	} else if selector != "" {
		debugArguments = append(debugArguments, "-test.run="+selector)
	}
	debugArguments = append(debugArguments, binaryArguments...)
	return commandPlan{
		Args: args, Display: displayCommand("go", args), Selector: selector, Benchmark: benchmark,
		BuildFlags: strings.Join(quoteArguments(debugBuildFlags), " "), DebugArguments: debugArguments,
	}, nil
}

func removeNamedFlags(flags []string, names ...string) []string {
	remove := make(map[string]bool, len(names))
	for _, name := range names {
		remove[name] = true
	}
	result := make([]string, 0, len(flags))
	for index := 0; index < len(flags); index++ {
		key := strings.TrimLeft(flags[index], "-")
		if equals := strings.IndexByte(key, '='); equals >= 0 {
			key = key[:equals]
		}
		if !remove[key] {
			result = append(result, flags[index])
			continue
		}
		if !strings.Contains(flags[index], "=") && index+1 < len(flags) {
			index++
		}
	}
	return result
}

func selectorFor(target Target, info sourceInfo) (string, bool, error) {
	switch target.Kind {
	case TargetPackageTests:
		return "", false, nil
	case TargetFileTests:
		return unionSelector(info.Tests), false, nil
	case TargetTest, TargetExample, TargetFuzz, TargetSubtest:
		return pathSelector(target.Path), false, nil
	case TargetPackageBenchmarks:
		return ".", true, nil
	case TargetFileBenchmarks:
		return unionSelector(info.Benchmarks), true, nil
	case TargetBenchmark, TargetSubbenchmark:
		return pathSelector(target.Path), true, nil
	default:
		return "", false, fmt.Errorf("unsupported Go test target %q", target.Kind)
	}
}

func unionSelector(names []string) string {
	if len(names) == 0 {
		return "^$"
	}
	values := make([]string, len(names))
	for index, name := range names {
		values[index] = regexp.QuoteMeta(name)
	}
	return "^(?:" + strings.Join(values, "|") + ")$"
}

func pathSelector(path []string) string {
	parts := make([]string, 0, len(path))
	for _, value := range path {
		for _, segment := range strings.Split(sanitizeTestName(value), "/") {
			parts = append(parts, "^"+regexp.QuoteMeta(segment)+"$")
		}
	}
	return strings.Join(parts, "/")
}

func sanitizeTestName(value string) string {
	var result strings.Builder
	for _, character := range value {
		switch {
		case isGoTestSpace(character):
			result.WriteByte('_')
		case !strconv.IsPrint(character):
			quoted := strconv.QuoteRune(character)
			result.WriteString(quoted[1 : len(quoted)-1])
		default:
			result.WriteRune(character)
		}
	}
	return result.String()
}

// This intentionally matches testing.isSpace rather than unicode.IsSpace.
func isGoTestSpace(character rune) bool {
	if character < 0x2000 {
		switch character {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0x85, 0xA0, 0x1680:
			return true
		}
		return false
	}
	if character <= 0x200a {
		return true
	}
	switch character {
	case 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	default:
		return false
	}
}

func splitArgs(flags []string) ([]string, []string) {
	for index, flag := range flags {
		if flag == "-args" || flag == "--args" {
			return append([]string(nil), flags[:index]...), append([]string(nil), flags[index+1:]...)
		}
	}
	return append([]string(nil), flags...), nil
}

func removeSelectorFlags(flags []string) []string {
	result := make([]string, 0, len(flags))
	for index := 0; index < len(flags); index++ {
		name := strings.TrimLeft(flags[index], "-")
		key := name
		if equals := strings.IndexByte(key, '='); equals >= 0 {
			key = key[:equals]
		}
		if key == "run" || key == "bench" {
			if !strings.Contains(flags[index], "=") && index+1 < len(flags) {
				index++
			}
			continue
		}
		result = append(result, flags[index])
	}
	return result
}

func hasTagsFlag(flags []string) bool {
	for _, flag := range flags {
		key := strings.TrimLeft(flag, "-")
		if equals := strings.IndexByte(key, '='); equals >= 0 {
			key = key[:equals]
		}
		if key == "tags" {
			return true
		}
	}
	return false
}

func quoteArguments(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = displayArgument(value)
	}
	return result
}

func displayCommand(command string, args []string) string {
	return strings.Join(append([]string{displayArgument(command)}, quoteArguments(args)...), " ")
}

func displayArgument(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n\"") {
		return value
	}
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}
