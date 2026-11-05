// Package mockcli implements a native CLI fixture inside the test executable.
package mockcli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

func Run() {
	if os.Getenv("ECHO_EXTERNAL_TEST_CLI") != "1" {
		return
	}
	args := os.Args[1:]
	if len(args) == 0 {
		os.Exit(2)
	}
	if args[0] == "child" {
		time.Sleep(2 * time.Second)
		_ = os.WriteFile(os.Getenv("ECHO_EXTERNAL_CHILD_MARKER"), []byte("child survived"), 0600)
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if args[0] == "--version" {
		fmt.Println("mock-cli 1.0")
		os.Exit(0)
	}
	if args[0] == "login" {
		fmt.Fprintln(os.Stderr, "Logged in using ChatGPT")
		os.Exit(0)
	}
	if args[0] == "auth" {
		method := "claude.ai"
		if os.Getenv("ECHO_EXTERNAL_TEST_MODE") == "api-auth" {
			method = "api_key"
		}
		fmt.Printf(`{"loggedIn":true,"authMethod":%q,"apiProvider":"firstParty","subscriptionType":"pro"}`, method)
		os.Exit(0)
	}
	input, _ := io.ReadAll(os.Stdin)
	dir, _ := os.Getwd()
	if log := os.Getenv("ECHO_EXTERNAL_TEST_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		_ = json.NewEncoder(f).Encode(map[string]any{"args": args, "input": string(input), "directory": dir, "profile": os.Getenv("USERPROFILE")})
		_ = f.Close()
	}
	mode := os.Getenv("ECHO_EXTERNAL_TEST_MODE")
	if mode == "cancel" {
		child := exec.Command(os.Args[0], "child")
		if err := child.Start(); err != nil {
			panic(err)
		}
		fmt.Println(`{"type":"thread.started","thread_id":"cancel-session"}`)
		_ = os.WriteFile(os.Getenv("ECHO_EXTERNAL_STARTED"), []byte("ready"), 0600)
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if mode == "malformed" {
		fmt.Println("this is not JSON")
		os.Exit(0)
	}
	if mode == "missing-session" {
		fmt.Println(`{"type":"error","message":"Session not found"}`)
		os.Exit(1)
	}
	if mode == "crash" {
		fmt.Fprintln(os.Stderr, "deliberate crash")
		os.Exit(23)
	}
	session := fmt.Sprintf("session-%d", time.Now().UnixNano())
	for i, a := range args {
		if (a == "resume" || a == "--resume") && i+1 < len(args) {
			session = args[i+1]
		}
	}
	answer := "Mock response"
	if strings.Contains(string(input), "remember") {
		answer = "Remembered"
	}
	// Diagnostic output must not be parsed as JSON or mixed with response text.
	fmt.Fprintln(os.Stderr, "mock diagnostic")
	emit := func(v any) {
		data, _ := json.Marshal(v)
		midpoint := len(data) / 2
		_, _ = os.Stdout.Write(data[:midpoint])
		_, _ = os.Stdout.Write(append(data[midpoint:], '\n'))
	}
	if args[0] == "exec" {
		emit(map[string]any{"type": "thread.started", "thread_id": session})
		if strings.HasPrefix(mode, "reconnect-") {
			for attempt := 1; attempt <= 2; attempt++ {
				emit(map[string]any{"type": "error", "message": fmt.Sprintf("Reconnecting... %d/5 (stream disconnected before completion: error sending request)", attempt)})
			}
			switch mode {
			case "reconnect-failed":
				emit(map[string]any{"type": "turn.failed", "error": map[string]string{"message": "stream disconnected before completion: error sending request"}})
				os.Exit(1)
			case "reconnect-incomplete":
				os.Exit(0)
			}
		}
		emit(map[string]any{"type": "item.started", "item": map[string]any{"id": "tool-1", "type": "command_execution", "command": "echo mock", "status": "in_progress"}})
		emit(map[string]any{"type": "item.completed", "item": map[string]any{"id": "tool-1", "type": "command_execution", "command": "echo mock", "aggregated_output": "mock", "status": "completed"}})
		emit(map[string]any{"type": "item.completed", "item": map[string]any{"id": "answer", "type": "agent_message", "text": answer}})
		emit(map[string]any{"type": "turn.completed", "usage": map[string]int{"input_tokens": 20, "output_tokens": 5, "cached_input_tokens": 3}})
		if mode == "reconnect-crash" {
			fmt.Fprintln(os.Stderr, "deliberate crash after completion")
			os.Exit(23)
		}
	} else {
		emit(map[string]any{"type": "system", "subtype": "init", "session_id": session})
		emit(map[string]any{"type": "assistant", "message": map[string]any{"id": "message-1", "content": []any{map[string]any{"type": "tool_use", "id": "tool-1", "name": "Bash", "input": map[string]string{"command": "echo mock"}}}}})
		emit(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool-1", "content": "mock"}}}})
		emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_start", "message": map[string]any{"id": "answer"}}})
		emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "delta": map[string]string{"type": "text_delta", "text": answer}}})
		emit(map[string]any{"type": "assistant", "message": map[string]any{"id": "answer", "content": []any{map[string]any{"type": "text", "text": answer}}}})
		emit(map[string]any{"type": "result", "subtype": "success", "session_id": session, "result": answer, "usage": map[string]int{"input_tokens": 17, "cache_read_input_tokens": 3, "output_tokens": 5}})
	}
	os.Exit(0)
}
