package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/plugins"
)

func sourceControl(id string) bool { return id == "git" || id == "fossil" || id == "p4" }

var scmCommand = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Workspace operations always use the configured server. DVCS/bootstrap and
// credential commands can select other transports or print secrets and are not
// exposed through this credential-bearing client.
const p4WorkspaceCommands = " help info status diff diff2 opened files dirs fstat where have changes change changelist describe filelog annotate print clients client streams stream add edit reopen delete move reconcile revert resolve resolved sync submit shelve unshelve labels label labelsync branches branch integrate merge copy lock unlock protects groups users jobs job fixes fix review reviews sizes typemap depot depots monitor trust "

// Configuration from chat can change ordinary preferences but cannot grant a
// process network access or enlarge the executable/runtime grant.
func (s *Server) sourceControlConfig(m domain.PluginManifest, updates map[string]any, owner bool) (map[string]any, error) {
	config := map[string]any{}
	for k, v := range m.Config {
		if k != "credential" {
			config[k] = v
		}
	}
	for k, v := range updates {
		if k == "credential" {
			if v != m.Config[k] {
				return nil, errors.New("credentials must be changed through the protected settings field")
			}
			continue
		}
		if !owner {
			if k == "runOutsideSandbox" {
				return nil, errors.New("only the owner can set Run Git outside the sandbox in Git settings")
			}
			if (k == "internet" || k == "privateNetwork") && v == true && m.Config[k] != true {
				return nil, errors.New("grant network access directly in the plugin's settings")
			}
			if (k == "executable" || k == "toolchains") && !reflect.DeepEqual(v, m.Config[k]) {
				return nil, errors.New("change executable/runtime grants directly in the plugin's settings")
			}
		}
		config[k] = v
	}
	raw, _ := json.Marshal(config)
	if e := plugins.Validate(m.Settings, raw); e != nil {
		return nil, e
	}
	// Previously saved paths may have disappeared. A mode change can reuse
	// them without setup, and the sandbox checks them again before execution.
	modeChanged := m.ID == "git" && boolean(config, "runOutsideSandbox") != boolean(m.Config, "runOutsideSandbox")
	if path := text(config, "executable"); path != "" && (!modeChanged || path != text(m.Config, "executable")) {
		if !filepath.IsAbs(path) || runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(path), ".exe") {
			return nil, errors.New("executable must be an absolute executable path")
		}
		if !s.Files.Allowed(domain.Bot{ID: "echo"}, path) {
			return nil, errors.New("executable cannot be inside Echo private data")
		}
	}
	if paths := stringsValue(config["toolchains"]); len(paths) > 0 && (!modeChanged || !slices.Equal(paths, stringsValue(m.Config["toolchains"]))) {
		roots, e := broker.CanonicalRoots(paths)
		if e != nil {
			return nil, e
		}
		for _, root := range roots {
			if broker.Within(root, s.Dir) || broker.Within(s.Dir, root) {
				return nil, errors.New("runtime grants cannot include Echo private data")
			}
		}
		config["toolchains"] = roots
	}
	for key, value := range config {
		if text, ok := value.(string); ok && strings.ContainsAny(text, "\x00\r\n") {
			return nil, fmt.Errorf("%s must be a single line", key)
		}
	}
	if m.ID == "p4" && text(m.Config, "credential") != "" {
		if text(config, "port") == text(m.Config, "port") && text(config, "user") == text(m.Config, "user") {
			config["credential"] = m.Config["credential"]
		} else if !owner {
			return nil, errors.New("changing the authenticated P4 server/user requires owner settings and a new credential")
		}
	}
	return config, nil
}

func scmArguments(id, command string, args []string, config map[string]any, client string) ([]string, map[string]string, error) {
	if !scmCommand.MatchString(command) {
		return nil, nil, errors.New("command must be one CLI subcommand; put options in the args array")
	}
	if len(args) > 512 {
		return nil, nil, errors.New("too many command arguments")
	}
	for _, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return nil, nil, errors.New("arguments cannot contain NUL")
		}
	}
	env := map[string]string{"EDITOR": "echo-editor-disabled", "VISUAL": "echo-editor-disabled"}
	switch id {
	case "git":
		env["GIT_TERMINAL_PROMPT"] = "0"
		env["GCM_INTERACTIVE"] = "never"
		env["GIT_EDITOR"] = "echo-editor-disabled"
		env["GIT_SEQUENCE_EDITOR"] = "echo-editor-disabled"
		if v := text(config, "userName"); v != "" {
			env["GIT_AUTHOR_NAME"] = v
			env["GIT_COMMITTER_NAME"] = v
		}
		if v := text(config, "userEmail"); v != "" {
			env["GIT_AUTHOR_EMAIL"] = v
			env["GIT_COMMITTER_EMAIL"] = v
		}
		if command == "version" {
			return []string{"--version"}, env, nil
		}
		return append([]string{"--no-pager", command}, args...), env, nil
	case "fossil":
		out := []string{command}
		if user := text(config, "user"); user != "" {
			out = append(out, "--user", user)
		}
		return append(out, args...), env, nil
	case "p4":
		if client == "" {
			client = text(config, "client")
		}
		if strings.ContainsAny(client, "\x00\r\n") {
			return nil, nil, errors.New("P4 client must be a single line without NUL")
		}
		if command == "version" {
			return []string{"-V"}, env, nil
		}
		// Login/ticket printing would put a new secret in the transcript. Credentials
		// instead arrive through the owner-only Windows Credential Manager field.
		if command == "set" {
			return nil, nil, errors.New("select the P4 workspace with the client tool parameter; Echo can save a default with plugin_configure (id p4, config.client). Manage credentials in the protected P4 settings field")
		}
		if command == "tickets" || command == "login" || command == "passwd" {
			return nil, nil, errors.New("manage P4 credentials in the protected P4 settings field; use an existing ticket")
		}
		if !strings.Contains(p4WorkspaceCommands, " "+command+" ") {
			return nil, nil, errors.New("P4 plugin supports workspace operations on the configured server; DVCS/bootstrap and arbitrary transports are unavailable")
		}
		if port := strings.ToLower(text(config, "port")); strings.HasPrefix(port, "rsh:") || strings.ContainsAny(port, " \t\r\n") {
			return nil, nil, errors.New("P4 server must be a network address, not a shell transport")
		}
		if command != "help" && text(config, "port") == "" {
			return nil, nil, errors.New("configure the P4 server in Settings → P4 (Perforce)")
		}
		out := []string{}
		for _, option := range []struct{ value, flag string }{{text(config, "port"), "-p"}, {text(config, "user"), "-u"}, {client, "-c"}, {text(config, "charset"), "-C"}} {
			if option.value != "" {
				out = append(out, option.flag, option.value)
			}
		}
		env["P4CONFIG"] = "__echo2_disabled_" + domain.ID()
		env["P4EDITOR"] = "echo-editor-disabled"
		env["P4PASSWD"] = "echo2-no-credential"
		if path := text(config, "trustFile"); path != "" {
			env["P4TRUST"] = path
		}
		return append(append(out, command), args...), env, nil
	}
	return nil, nil, errors.New("unknown source control plugin")
}

func (s *Server) runSourceControl(ctx context.Context, scope domain.ExecutionScope, m domain.PluginManifest, method string, a map[string]any) (any, error) {
	id := strings.TrimSuffix(method, ".run")
	if !sourceControl(id) || scope.PluginID != id || !m.Builtin {
		return nil, errors.New("source control capability belongs to its built-in plugin")
	}
	hostGit := false
	if id == "git" {
		var cancel context.CancelFunc
		ctx, cancel = s.Git.CommandContext(ctx, scope.Bot.ID)
		defer cancel()
		var err error
		ctx, m, cancel, err = s.beginGit(ctx, scope.Bot, m)
		if err != nil {
			return nil, err
		}
		defer cancel()
		hostGit = boolean(m.Config, "runOutsideSandbox")
	}
	command := text(a, "command")
	args, env, e := scmArguments(id, command, stringsValue(a["args"]), m.Config, text(a, "client"))
	if e != nil {
		return nil, e
	}
	if id == "p4" && command != "help" && command != "version" && len(scope.Bot.Roots) > 0 && !boolean(m.Config, "internet") && !boolean(m.Config, "privateNetwork") {
		return nil, errors.New("P4 needs server access: grant the appropriate network access in P4 settings; workspace restrictions stay enforced")
	}
	binary, e := scmExecutable(id, m)
	if e != nil {
		return nil, e
	}
	directory := text(a, "directory")
	if (id == "git" || id == "fossil") && len(scope.Bot.Roots) > 0 {
		if directory == "" {
			directory = scope.Bot.Roots[0]
		} else if !filepath.IsAbs(directory) {
			directory = filepath.Join(scope.Bot.Roots[0], directory)
		}
	}
	if id == "git" && directory == "" {
		directory, e = os.UserHomeDir()
		if e != nil {
			return nil, e
		}
	}
	secret := ""
	if id == "p4" && command != "help" && command != "version" {
		if ref := text(m.Config, "credential"); ref != "" {
			secret, e = s.Secrets.Get(ctx, ref)
			if e != nil {
				return nil, e
			}
			env["P4PASSWD"] = secret
		}
	}
	timeout := integer(a, "timeoutSeconds")
	if timeout < 0 || int64(timeout) > int64((1<<63-1)/time.Second) {
		return nil, errors.New("timeoutSeconds must be nonnegative and fit in a duration; 0 disables the timeout")
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
	}
	var output strings.Builder
	filter := &secretFilter{secret: secret}
	emit := func(chunk string) {
		if chunk == "" {
			return
		}
		output.WriteString(chunk)
		s.nativeOutput(ctx, scope, chunk)
	}
	request := broker.ProcessRequest{BotID: scope.Bot.ID, Executable: binary, Args: args, Input: text(a, "input"), Environment: env, Directory: directory, TimeoutSeconds: timeout}
	runner := s.Process.Run
	var commonDir string
	if id == "git" {
		var stopProgress func()
		request.Progress, stopProgress = nativeCommandProgress(ctx, "Git "+command, 10*time.Second, func(message string) { s.nativeOutput(ctx, scope, message) })
		defer stopProgress()
	}
	if hostGit {
		runner = s.Process.RunHost
		hostGitEnvironment(env)
		if gitUsesRepository(args) || command == "init" {
			commonDir, e = s.hostGitCommonDir(ctx, request)
		}
	} else {
		commonDir, e = s.prepareSandboxedSCM(ctx, scope.Bot, id, m, &request)
	}
	if e != nil {
		return nil, e
	}
	var result broker.ProcessResult
	run := func(ctx context.Context) error {
		var err error
		stream := func(chunk string) { emit(filter.push(chunk, false)) }
		if id == "git" && (command == "status" || command == "version") {
			result, err = runner(ctx, request, stream)
		} else {
			result, err = s.trackProcess(ctx, scope.Bot, request, stream, runner)
		}
		return err
	}
	var err error
	if commonDir != "" {
		err = s.Git.RunCommand(ctx, scope.Bot.ID, commonDir, run)
	} else {
		err = run(ctx)
	}
	emit(filter.push("", true))
	result.Output = output.String()
	if err != nil {
		return nil, errors.New(filter.redact(err.Error()))
	}
	return result, nil
}

func scmExecutable(id string, m domain.PluginManifest) (string, error) {
	var e error
	binary := text(m.Config, "executable")
	if binary == "" {
		name := id
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		binary, e = exec.LookPath(name)
		if e != nil {
			return "", fmt.Errorf("%s CLI is not installed or on PATH; install it or set its executable in %s settings", id, m.Name)
		}
	}
	return filepath.Abs(binary)
}

func (s *Server) prepareSandboxedSCM(ctx context.Context, bot domain.Bot, id string, m domain.PluginManifest, request *broker.ProcessRequest) (string, error) {
	var e error
	directory := request.Directory
	if !s.Files.Allowed(domain.Bot{ID: "echo"}, request.Executable) {
		return "", errors.New("executable cannot be inside Echo private data")
	}
	var gitScope gitProcessScope
	if id == "git" {
		gitScope, e = s.gitCommandScope(bot, directory)
		if e != nil {
			// Version/init/clone do not require an existing valid repository.
			// They can still run in an explicitly configured folder when a
			// surrounding checkout has unavailable metadata.
			if gitUsesRepository(request.Args) || !s.Files.Allowed(bot, directory) {
				return "", e
			}
			gitScope = gitProcessScope{}
		}
		// The whole checkout is accessible, so Git can discover it normally.
		// Preserve its native cwd/prefix handling instead of binding GIT_DIR.
	} else if directory != "" && !s.Files.Allowed(bot, directory) {
		return "", errors.New("working directory is outside the bot's scope")
	}
	roots := bot.Roots
	if gitScope.roots != nil {
		roots = gitScope.roots
	}
	if id == "fossil" {
		roots, e = s.fossilProcessRoots(ctx, bot, directory)
		if e != nil {
			return "", e
		}
	}
	if path := text(m.Config, "trustFile"); path != "" && (!filepath.IsAbs(path) || !s.Files.Allowed(bot, path)) {
		return "", errors.New("P4 trust file must be an absolute path inside the bot's scope")
	}
	runtime := filepath.Dir(request.Executable)
	if id == "git" && (strings.EqualFold(filepath.Base(runtime), "cmd") || strings.EqualFold(filepath.Base(runtime), "bin")) {
		runtime = filepath.Dir(runtime)
	}
	runtimePaths := append([]string{runtime}, stringsValue(m.Config["toolchains"])...)
	toolchains := []string{}
	for _, path := range runtimePaths {
		if broker.Within(path, s.Dir) || broker.Within(s.Dir, path) {
			return "", errors.New("install the CLI in a dedicated runtime folder that does not contain Echo private data")
		}
		within := false
		for _, root := range bot.Roots {
			if broker.Within(root, path) {
				within = true
			}
		}
		if !within {
			toolchains = append(toolchains, path)
		}
	}
	request.Roots, request.ReadPaths, request.Toolchains = roots, gitScope.readPaths, toolchains
	request.Internet, request.PrivateNetwork = boolean(m.Config, "internet"), boolean(m.Config, "privateNetwork")
	if id == "git" {
		request.SandboxSession = "source-control:git"
	}
	return gitScope.commonDir, nil
}

func (s *Server) nativeOutput(ctx context.Context, scope domain.ExecutionScope, value string) {
	if scope.Standalone {
		return // The invoking browser receives the result; no orphan chat events.
	}
	if buffer, ok := ctx.Value(toolOutputKey{}).(*streamBuffer); ok {
		buffer.append(model.Delta{Content: value})
		return
	}
	s.emit("tool-output", scope.ChatID, scope.RunID, scope.Bot.ID, map[string]any{"output": value, "id": scope.ToolMessageID})
}

type secretFilter struct{ secret, pending string }

func (f *secretFilter) redact(v string) string {
	if f.secret != "" {
		return strings.ReplaceAll(v, f.secret, "[redacted]")
	}
	return v
}
func (f *secretFilter) push(chunk string, final bool) string {
	f.pending = f.redact(f.pending + chunk)
	keep := 0
	if !final {
		for n := 1; n < len(f.secret) && n <= len(f.pending); n++ {
			if strings.HasSuffix(f.pending, f.secret[:n]) {
				keep = n
			}
		}
	}
	// Withhold a partial secret even when cancellation/output truncation happens.
	if final && f.secret != "" {
		for n := 1; n < len(f.secret) && n <= len(f.pending); n++ {
			if strings.HasSuffix(f.pending, f.secret[:n]) {
				keep = n
			}
		}
	}
	value := f.pending[:len(f.pending)-keep]
	f.pending = f.pending[len(f.pending)-keep:]
	if final && keep > 0 {
		value += "[redacted]"
		f.pending = ""
	}
	return value
}
