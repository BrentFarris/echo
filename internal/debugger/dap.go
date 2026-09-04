package debugger

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

const (
	maxDAPMessageBytes = 32 << 20
	maxDAPHeaderBytes  = 64 << 10
)

type dapEnvelope struct {
	Seq        int             `json:"seq"`
	Type       string          `json:"type"`
	RequestSeq int             `json:"request_seq,omitempty"`
	Success    bool            `json:"success,omitempty"`
	Command    string          `json:"command,omitempty"`
	Event      string          `json:"event,omitempty"`
	Message    string          `json:"message,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
}

type dapResult struct {
	message dapEnvelope
	err     error
}
type reverseHandler func(context.Context, dapEnvelope) (any, error)

type dapConnection struct {
	transport      io.ReadWriteCloser
	reader         *bufio.Reader
	writeMu        sync.Mutex
	mu             sync.Mutex
	nextSeq        int
	pending        map[int]chan dapResult
	closed         bool
	closeErr       error
	supportsCancel bool
	onEvent        func(dapEnvelope)
	onReverse      reverseHandler
	onClose        func(error)
	trace          func(string, []byte)
	done           chan struct{}
	closeOnce      sync.Once
}

func newDAPConnection(transport io.ReadWriteCloser, onEvent func(dapEnvelope), onReverse reverseHandler, onClose func(error)) *dapConnection {
	c := &dapConnection{transport: transport, reader: bufio.NewReader(transport), pending: map[int]chan dapResult{}, onEvent: onEvent, onReverse: onReverse, onClose: onClose, done: make(chan struct{})}
	go c.readLoop()
	return c
}

func (c *dapConnection) request(ctx context.Context, command string, arguments any) (dapEnvelope, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	args, err := json.Marshal(arguments)
	if err != nil {
		return dapEnvelope{}, fmt.Errorf("encode DAP %s arguments: %w", command, err)
	}
	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		if err == nil {
			err = io.ErrClosedPipe
		}
		c.mu.Unlock()
		return dapEnvelope{}, err
	}
	c.nextSeq++
	seq := c.nextSeq
	response := make(chan dapResult, 1)
	c.pending[seq] = response
	c.mu.Unlock()
	if err := c.write(dapEnvelope{Seq: seq, Type: "request", Command: command, Arguments: args}); err != nil {
		c.removePending(seq)
		c.fail(err)
		return dapEnvelope{}, fmt.Errorf("send DAP %s request: %w", command, err)
	}
	select {
	case result := <-response:
		if result.err != nil {
			return dapEnvelope{}, result.err
		}
		if !result.message.Success {
			return dapEnvelope{}, fmt.Errorf("DAP %s failed: %s", command, dapFailureMessage(result.message))
		}
		return result.message, nil
	case <-ctx.Done():
		c.removePending(seq)
		c.sendCancel(seq)
		return dapEnvelope{}, ctx.Err()
	case <-c.done:
		c.mu.Lock()
		err := c.closeErr
		c.mu.Unlock()
		if err == nil {
			err = io.ErrClosedPipe
		}
		return dapEnvelope{}, err
	}
}

func dapFailureMessage(message dapEnvelope) string {
	base := strings.TrimSpace(message.Message)
	var body struct {
		Error struct {
			Format    string            `json:"format"`
			Variables map[string]string `json:"variables"`
		} `json:"error"`
	}
	if len(message.Body) > 0 && json.Unmarshal(message.Body, &body) == nil {
		detail := strings.TrimSpace(body.Error.Format)
		for name, value := range body.Error.Variables {
			detail = strings.ReplaceAll(detail, "{"+name+"}", value)
		}
		if detail != "" {
			if base == "" || strings.HasPrefix(strings.ToLower(detail), strings.ToLower(base)) {
				return detail
			}
			return base + ": " + detail
		}
	}
	if base != "" {
		return base
	}
	return "debug adapter rejected the request"
}

func (c *dapConnection) write(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > maxDAPMessageBytes {
		return fmt.Errorf("DAP message exceeds %d bytes", maxDAPMessageBytes)
	}
	c.emitTrace("client → adapter", data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err = io.WriteString(c.transport, header); err != nil {
		return err
	}
	_, err = c.transport.Write(data)
	return err
}

func (c *dapConnection) readLoop() {
	for {
		data, err := readDAPMessage(c.reader)
		if err != nil {
			c.fail(err)
			return
		}
		c.emitTrace("adapter → client", data)
		var message dapEnvelope
		if err := json.Unmarshal(data, &message); err != nil {
			c.fail(fmt.Errorf("decode DAP message: %w", err))
			return
		}
		switch message.Type {
		case "response":
			c.mu.Lock()
			pending := c.pending[message.RequestSeq]
			delete(c.pending, message.RequestSeq)
			c.mu.Unlock()
			if pending != nil {
				pending <- dapResult{message: message}
			}
		case "event":
			if c.onEvent != nil {
				c.onEvent(message)
			}
		case "request":
			go c.handleReverse(message)
		default:
			c.fail(fmt.Errorf("invalid DAP message type %q", message.Type))
			return
		}
	}
}

func (c *dapConnection) handleReverse(message dapEnvelope) {
	var body any
	var err error
	if c.onReverse == nil {
		err = ErrUnsupported
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		body, err = c.onReverse(ctx, message)
		cancel()
	}
	response := map[string]any{"seq": c.nextSequence(), "type": "response", "request_seq": message.Seq, "success": err == nil, "command": message.Command}
	if err != nil {
		response["message"] = err.Error()
	} else if body != nil {
		response["body"] = body
	}
	_ = c.write(response)
}

func readDAPMessage(reader *bufio.Reader) ([]byte, error) {
	contentLength := -1
	headerBytes := 0
	for {
		lineData, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return nil, fmt.Errorf("DAP header line exceeds buffer limit")
		}
		if err != nil {
			return nil, err
		}
		headerBytes += len(lineData)
		if headerBytes > maxDAPHeaderBytes {
			return nil, fmt.Errorf("DAP headers exceed %d bytes", maxDAPHeaderBytes)
		}
		line := string(lineData)
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("invalid DAP content length: %w", err)
			}
			contentLength = parsed
		}
	}
	if contentLength < 0 || contentLength > maxDAPMessageBytes {
		return nil, fmt.Errorf("invalid DAP content length %d", contentLength)
	}
	data := make([]byte, contentLength)
	_, err := io.ReadFull(reader, data)
	return data, err
}

func (c *dapConnection) nextSequence() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextSeq++
	return c.nextSeq
}
func (c *dapConnection) setSupportsCancel(value bool) {
	c.mu.Lock()
	c.supportsCancel = value
	c.mu.Unlock()
}
func (c *dapConnection) setTrace(trace func(string, []byte)) {
	c.mu.Lock()
	c.trace = trace
	c.mu.Unlock()
}
func (c *dapConnection) emitTrace(direction string, data []byte) {
	c.mu.Lock()
	trace := c.trace
	c.mu.Unlock()
	if trace != nil {
		trace(direction, data)
	}
}
func (c *dapConnection) sendCancel(requestSeq int) {
	c.mu.Lock()
	if c.closed || !c.supportsCancel {
		c.mu.Unlock()
		return
	}
	c.nextSeq++
	sequence := c.nextSeq
	c.mu.Unlock()
	arguments, _ := json.Marshal(map[string]any{"requestId": requestSeq})
	_ = c.write(dapEnvelope{Seq: sequence, Type: "request", Command: "cancel", Arguments: arguments})
}
func (c *dapConnection) removePending(seq int) { c.mu.Lock(); delete(c.pending, seq); c.mu.Unlock() }
func (c *dapConnection) fail(err error) {
	if err == nil {
		err = io.EOF
	}
	c.closeOnce.Do(func() {
		_ = c.transport.Close()
		c.mu.Lock()
		c.closed = true
		c.closeErr = err
		pending := c.pending
		c.pending = map[int]chan dapResult{}
		c.mu.Unlock()
		for _, response := range pending {
			response <- dapResult{err: err}
		}
		close(c.done)
		if c.onClose != nil {
			c.onClose(err)
		}
	})
}
func (c *dapConnection) Close() error { c.fail(io.ErrClosedPipe); return nil }
func isDAPClosed(err error) bool      { return errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) }
