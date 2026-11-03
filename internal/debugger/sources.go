package debugger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/brent/echo/internal/editorfs"
	"github.com/google/uuid"
)

type adapterSource = Source

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
	if s.options.AllowedSource != nil && !s.options.AllowedSource(filename) {
		return "", fmt.Errorf("debug source is outside permitted source access")
	}
	var data []byte
	var err error

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
	data, err = io.ReadAll(io.LimitReader(file, editorfs.MaxEditableBytes+1))
	if err != nil {
		return "", err
	}

	if int64(len(data)) > editorfs.MaxEditableBytes {
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
