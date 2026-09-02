//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

const (
	maxRequestBytes = 8 << 20
	maxOutputBytes  = 4 << 20
	maxScreenshot   = 5 << 20
)

type agent struct {
	role, tokenFile, heartbeatFile string
	started                        time.Time
}

func main() {
	listen := flag.String("listen", "0.0.0.0:7777", "management listen address")
	role := flag.String("role", os.Getenv("ECHO_SANDBOX_ROLE"), "sandbox role")
	tokenFile := flag.String("token-file", "/run/echo/agent.token", "root-only bearer token file")
	heartbeatFile := flag.String("heartbeat-file", "/run/echo/heartbeat", "Echo heartbeat path")
	flag.Parse()
	service := &agent{role: *role, tokenFile: *tokenFile, heartbeatFile: *heartbeatFile, started: time.Now()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", service.auth(service.health))
	mux.HandleFunc("POST /v1/heartbeat", service.auth(service.heartbeat))
	mux.HandleFunc("POST /v1/exec", service.auth(service.execute))
	mux.HandleFunc("GET /v1/pty", service.auth(service.openPTY))
	mux.HandleFunc("GET /v1/process", service.auth(service.openProcess))
	mux.HandleFunc("GET /v1/dap", service.auth(service.openDAP))
	mux.HandleFunc("GET /v1/screenshot", service.auth(service.screenshot))
	mux.HandleFunc("POST /v1/desktop/action", service.auth(service.desktopAction))
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10}
	go service.watchHeartbeat(server)
	log.Printf("echo sandbox agent listening on %s (%s)", *listen, *role)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (a *agent) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := os.ReadFile(a.tokenFile)
		if err != nil {
			http.Error(w, "agent is initializing", http.StatusServiceUnavailable)
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		expected := strings.TrimSpace(string(token))
		if len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (a *agent) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "role": a.role, "protocolVersion": "2", "uptimeSeconds": int(time.Since(a.started).Seconds())})
}

func (a *agent) heartbeat(w http.ResponseWriter, _ *http.Request) {
	if err := touch(a.heartbeatFile); err != nil {
		http.Error(w, "heartbeat failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type execRequest struct {
	Command        []string `json:"command"`
	Dir            string   `json:"dir,omitempty"`
	Env            []string `json:"env,omitempty"`
	Input          []byte   `json:"input,omitempty"`
	OutputLimit    int      `json:"outputLimit,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
	Root           bool     `json:"root,omitempty"`
}

func (a *agent) execute(w http.ResponseWriter, r *http.Request) {
	var request execRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Command) == 0 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	if request.TimeoutSeconds > 0 {
		timeout := request.TimeoutSeconds
		if timeout > 3600 {
			timeout = 3600
		}
		ctx, cancel = context.WithTimeout(r.Context(), time.Duration(timeout)*time.Second)
	}
	defer cancel()
	limit := request.OutputLimit
	if limit <= 0 {
		limit = 256 << 10
	}
	if limit > maxOutputBytes {
		limit = maxOutputBytes
	}
	command := exec.Command(request.Command[0], request.Command[1:]...)
	command.Dir, command.Env = request.Dir, append(baseEnvironment(), request.Env...)
	if request.Root {
		command.Env = append(command.Env, "HOME=/root")
	}
	command.Stdin = bytes.NewReader(request.Input)
	command.SysProcAttr = processAttributes(request.Root)
	stdout, stderr := &limitedWriter{limit: limit}, &limitedWriter{limit: limit}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		http.Error(w, "could not start command", http.StatusBadRequest)
		return
	}
	err := waitForCommand(ctx, command)
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"exitCode": exitCode, "stdout": stdout.Bytes(), "stderr": stderr.Bytes(), "stdoutTruncated": stdout.truncated, "stderrTruncated": stderr.truncated, "timedOut": ctx.Err() == context.DeadlineExceeded})
}

var ptyUpgrader = websocket.Upgrader{ReadBufferSize: 32 << 10, WriteBufferSize: 32 << 10, CheckOrigin: func(*http.Request) bool { return true }}

func (a *agent) openPTY(w http.ResponseWriter, r *http.Request) {
	connection, err := ptyUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	var start struct {
		Command    []string `json:"command"`
		Dir        string   `json:"dir"`
		Env        []string `json:"env"`
		Cols, Rows uint16
		Root       bool `json:"root,omitempty"`
	}
	if err := connection.ReadJSON(&start); err != nil || len(start.Command) == 0 {
		return
	}
	command := exec.Command(start.Command[0], start.Command[1:]...)
	command.Dir = start.Dir
	command.Env = append(baseEnvironment(), start.Env...)
	if start.Root {
		command.Env = append(command.Env, "HOME=/root")
	}
	command.SysProcAttr = processAttributes(start.Root)
	// pty.StartWithSize adds Setsid/Setctty. Setsid already creates a process
	// group whose ID is the child PID, so an additional Setpgid is both
	// unnecessary and rejected by some kernels.
	command.SysProcAttr.Setpgid = false
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: start.Cols, Rows: start.Rows})
	if err != nil {
		_ = connection.WriteJSON(map[string]any{"error": err.Error()})
		return
	}
	defer terminal.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 32<<10)
		for {
			count, readErr := terminal.Read(buffer)
			if count > 0 {
				if connection.WriteMessage(websocket.BinaryMessage, buffer[:count]) != nil {
					return
				}
			}
			if readErr != nil {
				break
			}
		}
		waitErr := command.Wait()
		_ = connection.WriteJSON(map[string]any{"type": "exit", "exitCode": commandExitCode(waitErr)})
		_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "process exited"), time.Now().Add(time.Second))
	}()
	for {
		messageType, data, readErr := connection.ReadMessage()
		if readErr != nil {
			break
		}
		if messageType == websocket.BinaryMessage {
			if _, err := terminal.Write(data); err != nil {
				break
			}
			continue
		}
		var control struct {
			Type       string `json:"type"`
			Cols, Rows uint16
		}
		if json.Unmarshal(data, &control) != nil {
			continue
		}
		switch control.Type {
		case "resize":
			_ = pty.Setsize(terminal, &pty.Winsize{Cols: control.Cols, Rows: control.Rows})
		case "kill":
			terminateProcessGroup(command, syscall.SIGTERM)
		case "close_stdin":
			_ = terminal.Close()
		}
	}
	terminateProcessGroup(command, syscall.SIGTERM)
	_ = terminal.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		terminateProcessGroup(command, syscall.SIGKILL)
		<-done
	}
}

type processStart struct {
	Command []string `json:"command"`
	Dir     string   `json:"dir"`
	Env     []string `json:"env"`
	Root    bool     `json:"root,omitempty"`
}

type processWrite struct {
	messageType int
	data        []byte
}

// openProcess provides authenticated, full-duplex stdio for language servers
// and other long-lived guest processes. Binary messages use a one-byte stream
// prefix: 0 for client stdin, 1 for stdout, and 2 for stderr.
func (a *agent) openProcess(w http.ResponseWriter, r *http.Request) {
	connection, err := ptyUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	var start processStart
	if err := connection.ReadJSON(&start); err != nil || len(start.Command) == 0 {
		return
	}
	command := exec.Command(start.Command[0], start.Command[1:]...)
	command.Dir = start.Dir
	command.Env = append(baseEnvironment(), start.Env...)
	if start.Root {
		command.Env = append(command.Env, "HOME=/root")
	}
	command.SysProcAttr = processAttributes(start.Root)
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = connection.WriteJSON(map[string]any{"type": "error", "error": "could not open stdin"})
		return
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		_ = connection.WriteJSON(map[string]any{"type": "error", "error": "could not open stdout"})
		return
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = connection.WriteJSON(map[string]any{"type": "error", "error": "could not open stderr"})
		return
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		_ = connection.WriteJSON(map[string]any{"type": "error", "error": "could not start process"})
		return
	}
	processContext, cancelProcess := context.WithCancel(r.Context())
	defer cancelProcess()

	writes := make(chan processWrite, 64)
	writerFinished := make(chan struct{})
	go func() {
		defer close(writerFinished)
		for message := range writes {
			if connection.WriteMessage(message.messageType, message.data) != nil {
				return
			}
		}
	}()
	send := func(messageType int, data []byte) bool {
		copyData := append([]byte(nil), data...)
		select {
		case writes <- processWrite{messageType: messageType, data: copyData}:
			return true
		case <-writerFinished:
			return false
		}
	}
	started, _ := json.Marshal(map[string]any{"type": "started"})
	if !send(websocket.TextMessage, started) {
		cancelProcess()
	}

	var streams sync.WaitGroup
	pump := func(stream byte, reader io.ReadCloser) {
		defer streams.Done()
		defer reader.Close()
		buffer := make([]byte, 32<<10)
		for {
			count, readErr := reader.Read(buffer)
			if count > 0 {
				frame := make([]byte, count+1)
				frame[0] = stream
				copy(frame[1:], buffer[:count])
				if !send(websocket.BinaryMessage, frame) {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}
	streams.Add(2)
	go pump(1, stdout)
	go pump(2, stderr)

	waitDone := make(chan error, 1)
	go func() { waitDone <- waitForCommand(processContext, command) }()
	readDone := make(chan struct{})
	var closeStdin sync.Once
	go func() {
		defer close(readDone)
		for {
			messageType, data, readErr := connection.ReadMessage()
			if readErr != nil {
				return
			}
			if messageType == websocket.BinaryMessage {
				if len(data) == 0 || data[0] != 0 {
					continue
				}
				if _, err := stdin.Write(data[1:]); err != nil {
					return
				}
				continue
			}
			var control struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &control) != nil {
				continue
			}
			switch control.Type {
			case "close_stdin":
				closeStdin.Do(func() { _ = stdin.Close() })
			case "kill":
				cancelProcess()
			}
		}
	}()

	var waitErr error
	select {
	case waitErr = <-waitDone:
	case <-readDone:
		cancelProcess()
		waitErr = <-waitDone
	case <-writerFinished:
		cancelProcess()
		waitErr = <-waitDone
	}
	closeStdin.Do(func() { _ = stdin.Close() })
	streams.Wait()
	exit, _ := json.Marshal(map[string]any{"type": "exit", "exitCode": commandExitCode(waitErr)})
	_ = send(websocket.TextMessage, exit)
	close(writes)
	<-writerFinished
}

type dapStart struct {
	Mode             string   `json:"mode"`
	Command          []string `json:"command"`
	Dir              string   `json:"dir"`
	Env              []string `json:"env"`
	Host             string   `json:"host"`
	Port             int      `json:"port"`
	StartupTimeoutMS int      `json:"startupTimeoutMs"`
}

// openDAP keeps server-adapter ports on guest loopback. Binary frames use the
// process protocol: 0 is DAP input, 1 is DAP output, and 2 is adapter logging.
func (a *agent) openDAP(w http.ResponseWriter, r *http.Request) {
	connection, err := ptyUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	var start dapStart
	if err := connection.ReadJSON(&start); err != nil {
		return
	}
	start.Mode = strings.ToLower(strings.TrimSpace(start.Mode))
	if start.Mode != "server" && start.Mode != "connect" {
		_ = connection.WriteJSON(map[string]any{"type": "error", "error": "invalid DAP bridge mode"})
		return
	}
	if start.Mode == "server" && len(start.Command) == 0 {
		_ = connection.WriteJSON(map[string]any{"type": "error", "error": "spawned DAP bridge requires a command"})
		return
	}
	if start.StartupTimeoutMS < 1000 {
		start.StartupTimeoutMS = 15000
	}
	if start.StartupTimeoutMS > 120000 {
		start.StartupTimeoutMS = 120000
	}

	processContext, cancelProcess := context.WithCancel(context.Background())
	defer cancelProcess()
	writes := make(chan processWrite, 64)
	writerFinished := make(chan struct{})
	go func() {
		defer close(writerFinished)
		for message := range writes {
			if connection.WriteMessage(message.messageType, message.data) != nil {
				return
			}
		}
	}()
	send := func(messageType int, data []byte) bool {
		copyData := append([]byte(nil), data...)
		select {
		case writes <- processWrite{messageType: messageType, data: copyData}:
			return true
		case <-writerFinished:
			return false
		}
	}
	sendError := func(message string) {
		payload, _ := json.Marshal(map[string]any{"type": "error", "error": message})
		_ = send(websocket.TextMessage, payload)
	}

	var command *exec.Cmd
	var waitDone chan error
	var streams sync.WaitGroup
	var logReaders []io.ReadCloser
	if start.Mode == "server" {
		listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
		if listenErr != nil {
			sendError("could not allocate guest DAP port")
			close(writes)
			<-writerFinished
			return
		}
		start.Port = listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		port := strconv.Itoa(start.Port)
		for index := range start.Command {
			start.Command[index] = strings.ReplaceAll(start.Command[index], "${debugAdapterPort}", port)
		}
		for index := range start.Env {
			start.Env[index] = strings.ReplaceAll(start.Env[index], "${debugAdapterPort}", port)
		}
		command = exec.Command(start.Command[0], start.Command[1:]...)
		command.Dir = start.Dir
		command.Env = append(baseEnvironment(), start.Env...)
		command.SysProcAttr = processAttributes(false)
		stdout, stdoutErr := command.StdoutPipe()
		stderr, stderrErr := command.StderrPipe()
		if stdoutErr != nil || stderrErr != nil || command.Start() != nil {
			sendError("could not start DAP adapter")
			close(writes)
			<-writerFinished
			return
		}
		logReaders = []io.ReadCloser{stdout, stderr}
		waitDone = make(chan error, 1)
		go func() { waitDone <- waitForCommand(processContext, command) }()
	} else {
		if start.Port < 1 || start.Port > 65535 {
			sendError("connect DAP bridge requires a valid port")
			close(writes)
			<-writerFinished
			return
		}
	}

	host := strings.TrimSpace(start.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	address := net.JoinHostPort(host, strconv.Itoa(start.Port))
	deadline := time.Now().Add(time.Duration(start.StartupTimeoutMS) * time.Millisecond)
	var adapter net.Conn
	var dialErr error
dialLoop:
	for time.Now().Before(deadline) {
		dialer := net.Dialer{Timeout: 250 * time.Millisecond}
		adapter, dialErr = dialer.DialContext(processContext, "tcp", address)
		if dialErr == nil {
			break
		}
		if waitDone != nil {
			select {
			case waitErr := <-waitDone:
				sendError("DAP adapter exited before accepting a connection (exit " + strconv.Itoa(commandExitCode(waitErr)) + ")")
				cancelProcess()
				streams.Wait()
				close(writes)
				<-writerFinished
				return
			default:
			}
		}
		select {
		case <-processContext.Done():
			break dialLoop
		case <-time.After(40 * time.Millisecond):
		}
	}
	if adapter == nil {
		sendError("could not connect to guest DAP server at " + address)
		cancelProcess()
		if waitDone != nil {
			<-waitDone
		}
		streams.Wait()
		close(writes)
		<-writerFinished
		return
	}
	started, _ := json.Marshal(map[string]any{"type": "started", "port": start.Port})
	if !send(websocket.TextMessage, started) {
		_ = adapter.Close()
		cancelProcess()
		return
	}
	for _, reader := range logReaders {
		streams.Add(1)
		go func(reader io.ReadCloser) {
			defer streams.Done()
			defer reader.Close()
			buffer := make([]byte, 32<<10)
			for {
				count, readErr := reader.Read(buffer)
				if count > 0 {
					frame := make([]byte, count+1)
					frame[0] = 2
					copy(frame[1:], buffer[:count])
					if !send(websocket.BinaryMessage, frame) {
						return
					}
				}
				if readErr != nil {
					return
				}
			}
		}(reader)
	}

	adapterDone := make(chan struct{})
	go func() {
		defer close(adapterDone)
		buffer := make([]byte, 32<<10)
		for {
			count, readErr := adapter.Read(buffer)
			if count > 0 {
				frame := make([]byte, count+1)
				frame[0] = 1
				copy(frame[1:], buffer[:count])
				if !send(websocket.BinaryMessage, frame) {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			messageType, data, readErr := connection.ReadMessage()
			if readErr != nil {
				return
			}
			if messageType == websocket.BinaryMessage {
				if len(data) > 0 && data[0] == 0 {
					if _, err := adapter.Write(data[1:]); err != nil {
						return
					}
				}
				continue
			}
			var control struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &control) != nil {
				continue
			}
			switch control.Type {
			case "kill":
				cancelProcess()
				return
			case "close_stdin":
				if tcp, ok := adapter.(*net.TCPConn); ok {
					_ = tcp.CloseWrite()
				}
			}
		}
	}()

	select {
	case <-adapterDone:
	case <-readDone:
	case <-writerFinished:
	case <-processContext.Done():
	}
	_ = adapter.Close()
	cancelProcess()
	<-adapterDone
	exitCode := 0
	if waitDone != nil {
		exitCode = commandExitCode(<-waitDone)
	}
	streams.Wait()
	exit, _ := json.Marshal(map[string]any{"type": "exit", "exitCode": exitCode})
	_ = send(websocket.TextMessage, exit)
	close(writes)
	<-writerFinished
}

func (a *agent) screenshot(w http.ResponseWriter, r *http.Request) {
	if a.role != "desktop" {
		http.Error(w, "desktop only", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "import", "-window", "root", "png:-")
	command.Env = append(baseEnvironment(), "DISPLAY=:1")
	command.SysProcAttr = processAttributes(false)
	output, err := command.Output()
	if err != nil || len(output) > maxScreenshot {
		http.Error(w, "screenshot unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(output)
}

type desktopAction struct {
	Action     string `json:"action"`
	X          int    `json:"x,omitempty"`
	Y          int    `json:"y,omitempty"`
	X2         int    `json:"x2,omitempty"`
	Y2         int    `json:"y2,omitempty"`
	Button     int    `json:"button,omitempty"`
	Clicks     int    `json:"clicks,omitempty"`
	DeltaX     int    `json:"deltaX,omitempty"`
	DeltaY     int    `json:"deltaY,omitempty"`
	DurationMS int    `json:"durationMs,omitempty"`
	Text       string `json:"text,omitempty"`
	Key        string `json:"key,omitempty"`
}

func (a *agent) desktopAction(w http.ResponseWriter, r *http.Request) {
	if a.role != "desktop" {
		http.Error(w, "desktop only", http.StatusNotFound)
		return
	}
	var action desktopAction
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if json.NewDecoder(r.Body).Decode(&action) != nil {
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}
	args := []string{}
	switch action.Action {
	case "move":
		args = []string{"mousemove", strconv.Itoa(action.X), strconv.Itoa(action.Y)}
	case "click":
		button := action.Button
		if button == 0 {
			button = 1
		}
		clicks := action.Clicks
		if clicks <= 0 {
			clicks = 1
		}
		if clicks > 100 {
			clicks = 100
		}
		args = []string{"mousemove", strconv.Itoa(action.X), strconv.Itoa(action.Y), "click", "--repeat", strconv.Itoa(clicks), "--delay", "50", strconv.Itoa(button)}
	case "double_click":
		args = []string{"mousemove", strconv.Itoa(action.X), strconv.Itoa(action.Y), "click", "--repeat", "2", "--delay", "100", "1"}
	case "drag":
		button := action.Button
		if button == 0 {
			button = 1
		}
		args = []string{
			"mousemove", "--sync", strconv.Itoa(action.X), strconv.Itoa(action.Y),
			"mousedown", strconv.Itoa(button),
			"mousemove", "--sync", strconv.Itoa(action.X2), strconv.Itoa(action.Y2),
			"mouseup", strconv.Itoa(button),
		}
	case "type":
		if len(action.Text) > 32<<10 {
			http.Error(w, "text too large", http.StatusBadRequest)
			return
		}
		args = []string{"type", "--clearmodifiers", "--delay", "1", "--", action.Text}
	case "key":
		args = []string{"key", "--clearmodifiers", action.Key}
	case "scroll":
		button := "5"
		if abs(action.DeltaX) > abs(action.DeltaY) {
			button = "7"
			if action.DeltaX < 0 {
				button = "6"
			}
		} else if action.DeltaY < 0 {
			button = "4"
		}
		clicks := action.Clicks
		if clicks <= 0 {
			clicks = 1
		}
		if clicks > 100 {
			clicks = 100
		}
		args = []string{"mousemove", strconv.Itoa(action.X), strconv.Itoa(action.Y), "click", "--repeat", strconv.Itoa(clicks), button}
	case "wait":
		duration := action.DurationMS
		if duration <= 0 {
			duration = 1000
		}
		if duration > 30000 {
			duration = 30000
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Duration(duration) * time.Millisecond):
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
	default:
		http.Error(w, "unsupported action", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "xdotool", args...)
	command.Env = append(baseEnvironment(), "DISPLAY=:1")
	command.SysProcAttr = processAttributes(false)
	if output, err := command.CombinedOutput(); err != nil {
		http.Error(w, strings.TrimSpace(string(output)), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *agent) watchHeartbeat(server *http.Server) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	grace := time.Now().Add(2 * time.Minute)
	for range ticker.C {
		info, err := os.Stat(a.heartbeatFile)
		if err == nil && time.Since(info.ModTime()) <= 2*time.Minute {
			continue
		}
		if time.Now().Before(grace) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = server.Shutdown(ctx)
		cancel()
		return
	}
}

func touch(path string) error {
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}
func baseEnvironment() []string {
	values := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/home/echo/go/bin",
		"HOME=/home/echo", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TERM=xterm-256color", "ECHO_SANDBOX=1",
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			values = append(values, key+"="+value)
		}
	}
	return values
}

func processAttributes(root bool) *syscall.SysProcAttr {
	attributes := &syscall.SysProcAttr{Setpgid: true}
	if root {
		return attributes
	}
	uid, gid := uint32(1000), uint32(1000)
	if account, err := user.Lookup("echo"); err == nil {
		if parsed, parseErr := strconv.ParseUint(account.Uid, 10, 32); parseErr == nil {
			uid = uint32(parsed)
		}
		if parsed, parseErr := strconv.ParseUint(account.Gid, 10, 32); parseErr == nil {
			gid = uint32(parsed)
		}
	}
	attributes.Credential = &syscall.Credential{Uid: uid, Gid: gid}
	return attributes
}

func waitForCommand(ctx context.Context, command *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		terminateProcessGroup(command, syscall.SIGTERM)
		select {
		case <-done:
			return ctx.Err()
		case <-time.After(2 * time.Second):
			terminateProcessGroup(command, syscall.SIGKILL)
			<-done
			return ctx.Err()
		}
	}
}

func terminateProcessGroup(command *exec.Cmd, signal syscall.Signal) {
	if command != nil && command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, signal)
	}
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

type limitedWriter struct {
	data      strings.Builder
	limit     int
	truncated bool
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	original := len(data)
	remaining := w.limit - w.data.Len()
	if remaining <= 0 {
		w.truncated = w.truncated || original > 0
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		w.truncated = true
	}
	_, _ = w.data.Write(data)
	return original, nil
}
func (w *limitedWriter) String() string { return w.data.String() }
func (w *limitedWriter) Bytes() []byte  { return []byte(w.data.String()) }

var _ io.Writer = (*limitedWriter)(nil)
