// Package godebug is the Go plugin's debugger contribution. Delve-specific
// configuration and CGO behavior are intentionally absent from Monaco and DAP.
package godebug

import (
	"bufio"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/debugger"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DelveVersion = "v1.27.2"

var ErrMissing = errors.New("Go debugger installation required; use Go: Install / Update Debugger")

type Provider struct {
	Project    func(context.Context, string) (broker.ProcessRequest, error)
	Executable func(broker.ProcessRequest, debugconfig.Configuration) (string, error)
}

func (*Provider) Metadata() debugconfig.AdapterProfile {
	return debugconfig.AdapterProfile{PluginID: "go", ID: "go", Name: "Go", AdapterID: "go", Selectors: []debugconfig.Selector{{LanguageID: "go", Extensions: []string{".go"}}}}
}
func (*Provider) Configure(c debugconfig.Configuration) (debugconfig.Configuration, []debugconfig.Input) {
	var inputs []debugconfig.Input
	merged := map[string]any{}
	if defaults, ok := c.Settings["go.delveConfig"].(map[string]any); ok {
		for k, v := range defaults {
			merged[k] = debugconfig.QualifyInputs(v, c.RootID)
		}
	}
	for k, v := range c.Arguments {
		merged[k] = v
	}
	c.Arguments = merged
	if c.Request == "attach" {
		pid := fmt.Sprint(c.Arguments["processId"])
		goOnly := pid == "${command:pickGoProcess}"
		pick := c.Arguments["processId"] == nil || pid == "0" || pid == "" || goOnly || pid == "${command:pickProcess}"
		filter := ""
		if !pick && intValue(c.Arguments["processId"]) == 0 && !strings.Contains(pid, "${") {
			pick = true
			filter = pid
		}
		if pick {
			key := c.ID + ":process"
			c.Arguments["processId"] = "${input:" + key + "}"
			inputs = append(inputs, debugconfig.Input{ID: key, Type: "pickProcess", PluginID: "go", GoOnly: goOnly, Filter: filter, Description: "Select a process to debug"})
		}
	}
	return c, inputs
}
func ManagedPath(r broker.ProcessRequest) string {
	name := "dlv"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(r.Environment["GOBIN"], name)
}
func ResolveExecutable(r broker.ProcessRequest, c debugconfig.Configuration) (string, error) {
	configured := ""
	if tools, ok := c.Settings["go.alternateTools"].(map[string]any); ok {
		configured = text(tools, "dlv")
	}
	if configured != "" {
		expanded, err := debugconfig.ExpandString(configured, debugconfig.ExpandOptions{WorkspaceFolder: c.Folder, Settings: c.Settings})
		if err != nil {
			return "", err
		}
		configured = expanded
		if strings.HasPrefix(configured, "~/") || strings.HasPrefix(configured, "~\\") {
			home, e := os.UserHomeDir()
			if e != nil {
				return "", e
			}
			configured = filepath.Join(home, configured[2:])
		}
	}
	path := configured
	if path != "" {
		if !filepath.IsAbs(path) {
			path = filepath.Join(c.Folder, path)
		}
	} else {
		path = ManagedPath(r)
		if _, err := os.Stat(path); err != nil {
			var e error
			path, e = exec.LookPath("dlv")
			if e != nil {
				return "", ErrMissing
			}
		}
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("Delve: %w", err)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return "", err
	}
	info, err := buildinfo.ReadFile(real)
	if err != nil || info.Path != "github.com/go-delve/delve/cmd/dlv" {
		return "", fmt.Errorf("selected debugger is not a Go-built Delve executable")
	}
	return real, nil
}
func (p *Provider) Prepare(ctx context.Context, scope debugger.LaunchContext, c debugconfig.Configuration) (*debugger.Launch, error) {
	if err := debugconfig.ValidateConfiguration(c); err != nil {
		return nil, err
	}
	args := map[string]any{}
	if defaults, ok := c.Settings["go.delveConfig"].(map[string]any); ok {
		for k, v := range defaults {
			args[k] = v
		}
	}
	for k, v := range c.Arguments {
		args[k] = v
	}
	options := debugconfig.ExpandOptions{WorkspaceFolder: c.Folder, CurrentFile: scope.CurrentFile, SelectedText: scope.SelectedText, Inputs: scope.Inputs, Settings: c.Settings, WorkspaceFolders: map[string]string{}}
	for _, root := range scope.Roots {
		if _, exists := options.WorkspaceFolders[root.Label]; exists {
			options.WorkspaceFolders[root.Label] = ""
		} else {
			options.WorkspaceFolders[root.Label] = root.HostPath
		}
		options.WorkspaceFolders[root.ReferenceLabel] = root.HostPath
	}
	expanded, err := debugconfig.ExpandValue(args, options)
	if err != nil {
		return nil, err
	}
	args = expanded.(map[string]any)
	effective := c
	effective.Arguments = args
	if err = debugconfig.ValidateConfiguration(effective); err != nil {
		return nil, err
	}
	console := text(args, "console")
	if console != "" && console != "internalConsole" && console != "integratedTerminal" {
		return nil, fmt.Errorf("unsupported debug console %q", console)
	}
	if console == "integratedTerminal" && scope.RunInTerminal == nil {
		return nil, fmt.Errorf("enable Terminal to use integratedTerminal debugging")
	}
	if console == "integratedTerminal" {
		args["outputMode"] = "local"
	} else if text(args, "stdoutTo") == "" && text(args, "stderrTo") == "" {
		args["outputMode"] = "remote"
	}
	if adapter := text(args, "debugAdapter"); adapter != "" && adapter != "dlv-dap" {
		return nil, fmt.Errorf("legacy Go adapters are unsupported; use dlv-dap")
	}
	for _, key := range []string{"host", "port", "debugServer"} {
		if value, ok := args[key]; ok && value != nil && value != "" && value != float64(0) {
			return nil, fmt.Errorf("external Delve servers (%s) are unsupported", key)
		}
	}
	if args["asRoot"] == true {
		return nil, fmt.Errorf("debugging with elevated permissions is unsupported")
	}
	mode := text(args, "mode")
	program := text(args, "program")
	if c.Request == "attach" {
		if mode == "" {
			mode = "local"
		}
		if mode != "local" {
			return nil, fmt.Errorf("only local Go attach is supported")
		}
		pid := intValue(args["processId"])
		if pid < 1 {
			return nil, fmt.Errorf("select a valid process ID")
		}
		args["processId"] = pid
	} else {
		if mode == "" || mode == "auto" {
			mode = "debug"
			file := scope.CurrentFile
			if file == "" {
				file = program
			}
			if strings.HasSuffix(file, "_test.go") {
				mode = "test"
			}
		}
		if mode != "debug" && mode != "test" && mode != "exec" {
			return nil, fmt.Errorf("unsupported Go launch mode %q", mode)
		}
		if program == "" {
			return nil, fmt.Errorf("Go launch requires program")
		}
		if !filepath.IsAbs(program) {
			program = filepath.Join(c.Folder, program)
		}
		if mode == "test" && strings.HasSuffix(program, "_test.go") {
			program = filepath.Dir(program)
		}
		args["program"] = program
	}
	args["mode"] = mode
	args["name"] = c.Name
	args["type"] = "go"
	args["request"] = c.Request
	args["noDebug"] = scope.NoDebug
	if cwd := text(args, "cwd"); cwd != "" && !filepath.IsAbs(cwd) {
		args["cwd"] = filepath.Join(c.Folder, cwd)
	}
	directory := c.Folder
	if c.Request == "launch" && (mode == "debug" || mode == "test") {
		directory = program
		if strings.HasSuffix(program, ".go") {
			directory = filepath.Dir(program)
		}
	}
	project, err := p.Project(ctx, directory)
	if err != nil {
		return nil, err
	}
	resolver := p.Executable
	if resolver == nil {
		resolver = ResolveExecutable
	}
	executable, err := resolver(project, c)
	if err != nil {
		return nil, err
	}
	environment := map[string]string{}
	for k, v := range project.Environment {
		environment[k] = v
	}
	if len(project.PathPrepend) > 0 {
		environment["PATH"] = strings.Join(project.PathPrepend, string(os.PathListSeparator)) + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	if vars, ok := c.Settings["go.toolsEnvVars"].(map[string]any); ok {
		for k, v := range vars {
			value, e := debugconfig.ExpandValue(v, options)
			if e != nil {
				return nil, e
			}
			environment[k] = fmt.Sprint(value)
		}
	}
	files := []string{}
	switch value := args["envFile"].(type) {
	case string:
		if value != "" {
			files = append(files, value)
		}
	case []any:
		for _, v := range value {
			file, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("envFile must contain filenames")
			}
			files = append(files, file)
		}
	}
	for _, file := range files {
		if !filepath.IsAbs(file) {
			file = filepath.Join(c.Folder, file)
		}
		values, e := readEnvFile(file)
		if e != nil {
			return nil, e
		}
		for k, v := range values {
			environment[k] = v
		}
	}
	if vars, ok := args["env"].(map[string]any); ok {
		for k, v := range vars {
			value, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("environment value %s must be a string", k)
			}
			environment[k] = value
		}
	}
	args["env"] = environment
	delete(args, "envFile")
	if value, ok := args["args"].(string); ok {
		parsed, e := splitArguments(value)
		if e != nil {
			return nil, e
		}
		args["args"] = parsed
	}
	if values, ok := args["buildFlags"].([]any); ok {
		flags := []string{}
		for _, v := range values {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("buildFlags must contain strings")
			}
			if strings.ContainsAny(s, " \t") {
				s = strconv.Quote(s)
			}
			flags = append(flags, s)
		}
		args["buildFlags"] = strings.Join(flags, " ")
	}
	extra := []string{}
	if values, ok := args["dlvFlags"].([]any); ok {
		for _, v := range values {
			flag, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("dlvFlags must contain strings")
			}
			for _, reserved := range []string{"--listen", "-l", "--client-addr", "--headless", "--accept-multiclient", "--only-same-user", "--log-dest"} {
				if flag == reserved || strings.HasPrefix(flag, reserved+"=") {
					return nil, fmt.Errorf("%s is managed by Echo", reserved)
				}
			}
			extra = append(extra, flag)
		}
	}
	if args["showLog"] == true {
		extra = append(extra, "--log")
	}
	if value := text(args, "logOutput"); value != "" {
		extra = append(extra, "--log-output="+value)
	}
	// Only Echo's generated output belongs to the session. Explicit output paths
	// are respected but are never swept or removed by cleanup.
	owned := ""
	if c.Request == "launch" && (mode == "debug" || mode == "test") {
		if output := text(args, "output"); output != "" {
			if !filepath.IsAbs(output) {
				args["output"] = filepath.Join(c.Folder, output)
			}
		} else {
			owned, err = os.MkdirTemp("", "echo-debug-")
			if err != nil {
				return nil, err
			}
			name := "program"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			args["output"] = filepath.Join(owned, name)
		}
	}
	var cleanup sync.Once
	launch := &debugger.Launch{Arguments: args, Step: sourceStep, Expression: func(frame debugger.Frame, expression string) string {
		if strings.HasPrefix(frame.Name, "C.") {
			return delveCExpression(expression)
		}
		return expression
	}, Cleanup: func() {
		cleanup.Do(func() {
			if owned != "" {
				for attempt := 0; attempt < 5; attempt++ {
					if os.RemoveAll(owned) == nil {
						return
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
		})
	}}
	launch.Start = func(ctx context.Context, output func(string, string)) (*debugger.Adapter, error) {
		arguments := func(address string) []string { return append([]string{"dap", "--client-addr=" + address}, extra...) }
		if console == "integratedTerminal" {
			return debugger.StartTerminalAdapter(ctx, scope, executable, directory, environment, arguments)
		}
		return debugger.StartClientAdapter(ctx, executable, directory, debugger.Environment(environment), arguments, output)
	}
	return launch, nil
}
func text(values map[string]any, key string) string { v, _ := values[key].(string); return v }
func intValue(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}
func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > 1<<20 {
		return nil, fmt.Errorf("envFile exceeds 1 MiB")
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	values := map[string]string{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid environment line in %s", path)
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, "\x00 =\t") {
			return nil, fmt.Errorf("invalid environment key in %s", path)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	return values, scanner.Err()
}
func splitArguments(input string) ([]string, error) {
	var args []string
	var value strings.Builder
	var quote rune
	started := false
	for _, c := range input {
		if c == quote {
			quote = 0
			continue
		}
		if quote != 0 {
			value.WriteRune(c)
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			started = true
			continue
		}
		if c == ' ' || c == '\t' {
			if started {
				args = append(args, value.String())
				value.Reset()
				started = false
			}
			continue
		}
		started = true
		value.WriteRune(c)
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in arguments")
	}
	if started {
		args = append(args, value.String())
	}
	return args, nil
}
