package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

const protocol = "echo-jsonrpc-1"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type server struct {
	encoder *json.Encoder
	stderr  io.Writer
	mu      sync.Mutex
}

func (s *server) write(value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.encoder.Encode(value); err != nil {
		fmt.Fprintln(s.stderr, "encode protocol response:", err)
	}
}

func (s *server) respond(id json.RawMessage, result any, callErr error) {
	message := map[string]any{"jsonrpc": "2.0", "id": id}
	if callErr != nil {
		message["error"] = map[string]any{"code": -32000, "message": callErr.Error()}
	} else {
		message["result"] = result
	}
	s.write(message)
}

func (s *server) notify(method string, params any) {
	s.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func serve(input io.Reader, output, stderr io.Writer) error {
	s := &server{encoder: json.NewEncoder(output), stderr: stderr}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var call request
		if err := json.Unmarshal(scanner.Bytes(), &call); err != nil || call.JSONRPC != "2.0" || call.Method == "" {
			return fmt.Errorf("malformed JSON-RPC request")
		}
		if len(call.ID) == 0 {
			// Cancellation and configuration-change notifications are safe to
			// ignore when no operation is currently in flight.
			continue
		}
		switch call.Method {
		case "echo.initialize":
			var params struct {
				Protocol string `json:"protocol"`
			}
			if err := json.Unmarshal(call.Params, &params); err != nil || params.Protocol != protocol {
				s.respond(call.ID, nil, fmt.Errorf("unsupported protocol"))
				continue
			}
			s.respond(call.ID, map[string]any{"protocol": protocol, "features": []string{"ui-events"}}, nil)
		case "echo.shutdown":
			s.respond(call.ID, map[string]any{"ok": true}, nil)
			return nil
		case "tools.echo":
			var params struct {
				Arguments map[string]any `json:"arguments"`
				Scope     struct {
					Kind string `json:"kind"`
				} `json:"scope"`
				Config map[string]any `json:"config"`
			}
			if err := json.Unmarshal(call.Params, &params); err != nil {
				s.respond(call.ID, nil, fmt.Errorf("invalid tool parameters"))
				continue
			}
			message, _ := params.Arguments["message"].(string)
			displayName, _ := params.Config["display-name"].(string)
			_, hasSecret := params.Config["api-token"]
			s.respond(call.ID, map[string]any{
				"message": message, "scope": params.Scope.Kind,
				"displayName": displayName, "secretConfigured": hasSecret,
			}, nil)
		case "showcase.ping":
			var params struct {
				SessionID string         `json:"sessionId"`
				Params    map[string]any `json:"params"`
				Config    map[string]any `json:"config"`
			}
			if err := json.Unmarshal(call.Params, &params); err != nil {
				s.respond(call.ID, nil, fmt.Errorf("invalid UI parameters"))
				continue
			}
			s.respond(call.ID, map[string]any{
				"ok": true, "sessionId": params.SessionID, "received": params.Params,
				"serviceUrl": params.Config["service-url"], "secretConfigured": params.Config["api-token"] != nil,
			}, nil)
		case "showcase.emit":
			var params struct {
				SessionID string `json:"sessionId"`
				Params    struct {
					Message string `json:"message"`
				} `json:"params"`
			}
			if err := json.Unmarshal(call.Params, &params); err != nil || strings.TrimSpace(params.SessionID) == "" {
				s.respond(call.ID, nil, fmt.Errorf("invalid event request"))
				continue
			}
			s.notify("echo.uiEvent", map[string]any{
				"sessionId": params.SessionID, "topic": "showcase.message",
				"data": map[string]any{"message": params.Params.Message},
			})
			s.respond(call.ID, map[string]any{"emitted": true}, nil)
		default:
			s.respond(call.ID, nil, fmt.Errorf("method not found"))
		}
	}
	return scanner.Err()
}

func main() {
	if err := serve(os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
