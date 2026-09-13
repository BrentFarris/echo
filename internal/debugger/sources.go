package debugger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/google/uuid"
)

type adapterSource struct {
	Name      string `json:"name,omitempty"`
	Path      string `json:"path,omitempty"`
	Reference int    `json:"sourceReference,omitempty"`
}

func (s *Service) translateEventBody(workspaceID, sessionID string, body json.RawMessage) json.RawMessage {
	s.mu.Lock()
	current, err := s.sessionLocked(workspaceID, sessionID)
	s.mu.Unlock()
	if err != nil {
		return body
	}
	return s.translateSessionBody(current, body)
}

// Retain the adapter's original path (including guest paths). Browser requests
// can only read a source the adapter actually reported in this session.
func (s *Service) registerSource(current *session, source adapterSource) string {
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(current.id+"\x00"+source.Path+"\x00"+strconv.Itoa(source.Reference)+"\x00"+source.Name)).String()
	s.mu.Lock()
	defer s.mu.Unlock()
	if current.sources == nil {
		current.sources = map[string]adapterSource{}
	}
	if _, exists := current.sources[id]; !exists && len(current.sources) >= 16384 {
		return ""
	}
	current.sources[id] = source
	return id
}

func (s *Service) translateSessionBody(current *session, body json.RawMessage) json.RawMessage {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return body
	}
	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case []any:
			for _, child := range node {
				visit(child)
			}
		case map[string]any:
			_, hasPath := node["path"]
			_, hasReference := node["sourceReference"]
			if hasReference || hasPath {
				source := adapterSource{Name: stringValue(node["name"], ""), Path: stringValue(node["path"], ""), Reference: intValue(node["sourceReference"])}
				node["echoSourceId"] = s.registerSource(current, source)
			}
			for _, child := range node {
				visit(child)
			}
		}
	}
	visit(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return body
	}
	return s.translateDAPBody(current.workspaceID, encoded)
}

func (s *Service) sourceRequest(ctx context.Context, current *session, request ControlRequest) (RequestResponse, error) {
	raw, _ := request.Arguments["source"].(map[string]any)
	id, _ := raw["echoSourceId"].(string)
	s.mu.Lock()
	source, known := current.sources[id]
	connection := current.conn
	s.mu.Unlock()
	if !known {
		return RequestResponse{}, fmt.Errorf("source was not reported by this debug session")
	}
	if connection == nil || current.ctx.Err() != nil {
		return RequestResponse{}, fmt.Errorf("debug adapter is not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	stopCancel := context.AfterFunc(current.ctx, cancel)
	defer stopCancel()
	var body json.RawMessage
	var err error
	if source.Reference > 0 {
		var response dapEnvelope
		response, err = connection.request(ctx, "source", map[string]any{"source": source, "sourceReference": source.Reference})
		body = response.Body
	} else {
		var content string
		content, err = s.readAdapterSource(ctx, current.workspaceID, source.Path)
		if err == nil {
			body, err = json.Marshal(map[string]any{"content": content})
		}
	}
	if err != nil {
		return RequestResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return RequestResponse{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if request.StopGeneration != 0 && current.stopGeneration != request.StopGeneration {
		return RequestResponse{}, &RevisionError{Expected: request.StopGeneration, Actual: current.stopGeneration, Stop: true}
	}
	return RequestResponse{WorkspaceID: current.workspaceID, SessionID: current.id, Revision: current.revision, StopGeneration: current.stopGeneration, Body: body}, nil
}

func (s *Service) readAdapterSource(ctx context.Context, workspaceID, filename string) (string, error) {
	if filename == "" || strings.ContainsRune(filename, 0) {
		return "", fmt.Errorf("the debugger did not provide an available source path")
	}
	workspace, err := s.workspace(workspaceID)
	if err != nil {
		return "", err
	}
	var data []byte
	if workspace.Sandbox.Enabled {
		if !path.IsAbs(filename) {
			return "", fmt.Errorf("generated source %q is unavailable", filename)
		}
		manager := s.sandboxManager()
		if manager == nil {
			return "", fmt.Errorf("sandbox is unavailable")
		}
		// head bounds the read even when the file changes while being read. The
		// fixed shell program rejects devices/directories; the path is argv, never code.
		result, runErr := manager.Execute(ctx, workspaceID, sandbox.ExecRequest{
			Role:        "runtime",
			Command:     []string{"sh", "-c", `test -f "$1" && exec head -c 10485761 -- "$1"`, "echo-debug-source", filename},
			OutputLimit: int(workspacefs.MaxEditableBytes + 1),
		})
		if runErr != nil {
			return "", runErr
		}
		if result.ExitCode != 0 {
			return "", fmt.Errorf("source %q is unavailable in the sandbox", filename)
		}
		if result.StdoutTruncated {
			return "", workspacefs.ErrTooLarge
		}
		data = result.Stdout
	} else {
		if !filepath.IsAbs(filename) {
			return "", fmt.Errorf("generated source %q is unavailable", filename)
		}
		file, openErr := os.Open(filename)
		if openErr != nil {
			return "", openErr
		}
		defer file.Close()
		info, statErr := file.Stat()
		if statErr != nil {
			return "", statErr
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("debug source must be a regular file")
		}
		data, err = io.ReadAll(io.LimitReader(file, workspacefs.MaxEditableBytes+1))
		if err != nil {
			return "", err
		}
	}
	if int64(len(data)) > workspacefs.MaxEditableBytes {
		return "", fmt.Errorf("debug source exceeds the 10 MiB editor limit")
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return "", fmt.Errorf("debug source is not UTF-8 text")
	}
	return strings.TrimPrefix(string(data), "\ufeff"), nil
}

func intValue(value any) int {
	switch n := value.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case json.Number:
		v, _ := n.Int64()
		return int(v)
	}
	return 0
}

// Delve evaluates C values with its Go expression parser. Preserve strings and
// character literals; only adapt C's pointer-member operator.
func delveCExpression(expression string) string {
	var out strings.Builder
	var quote byte
	for i := 0; i < len(expression); i++ {
		c := expression[i]
		if quote != 0 {
			out.WriteByte(c)
			if c == '\\' && quote != '`' && i+1 < len(expression) {
				i++
				out.WriteByte(expression[i])
			} else if c == quote {
				quote = 0
			}
		} else if c == '"' || c == '\'' || c == '`' {
			quote = c
			out.WriteByte(c)
		} else if c == '-' && i+1 < len(expression) && expression[i+1] == '>' {
			out.WriteByte('.')
			i++
		} else {
			out.WriteByte(c)
		}
	}
	return out.String()
}
