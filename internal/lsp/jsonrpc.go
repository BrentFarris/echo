package lsp

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
	"sync/atomic"
)

const maxRPCMessageBytes = 32 << 20

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("LSP error %d: %s", e.Code, e.Message)
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

type requestHandler func(context.Context, string, json.RawMessage) (any, *RPCError)
type notificationHandler func(string, json.RawMessage)

type rpcPeer struct {
	reader *bufio.Reader
	writer io.Writer
	closer io.Closer

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[int64]chan rpcResponse
	nextID  atomic.Int64
	done    chan struct{}
	once    sync.Once
	err     error

	onRequest      requestHandler
	onNotification notificationHandler
}

func newRPCPeer(reader io.Reader, writer io.Writer, closer io.Closer, onRequest requestHandler, onNotification notificationHandler) *rpcPeer {
	peer := &rpcPeer{
		reader: bufio.NewReader(reader), writer: writer, closer: closer,
		pending: make(map[int64]chan rpcResponse), done: make(chan struct{}),
		onRequest: onRequest, onNotification: onNotification,
	}
	go peer.readLoop()
	return peer
}

func (p *rpcPeer) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := p.nextID.Add(1)
	paramsRaw, err := marshalOptional(params)
	if err != nil {
		return nil, err
	}
	result := make(chan rpcResponse, 1)
	p.mu.Lock()
	select {
	case <-p.done:
		err := p.err
		p.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return nil, err
	default:
	}
	p.pending[id] = result
	p.mu.Unlock()
	if err := p.write(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: paramsRaw}); err != nil {
		p.removePending(id)
		return nil, err
	}
	select {
	case response := <-result:
		return response.result, response.err
	case <-ctx.Done():
		p.removePending(id)
		_ = p.Notify("$/cancelRequest", map[string]any{"id": id})
		return nil, ctx.Err()
	case <-p.done:
		p.removePending(id)
		p.mu.Lock()
		err := p.err
		p.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return nil, err
	}
}

func (p *rpcPeer) Notify(method string, params any) error {
	paramsRaw, err := marshalOptional(params)
	if err != nil {
		return err
	}
	return p.write(rpcMessage{JSONRPC: "2.0", Method: method, Params: paramsRaw})
}

func (p *rpcPeer) Close() error {
	p.fail(io.EOF)
	if p.closer != nil {
		return p.closer.Close()
	}
	return nil
}

func (p *rpcPeer) Done() <-chan struct{} { return p.done }

func (p *rpcPeer) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *rpcPeer) readLoop() {
	for {
		data, err := readRPCFrame(p.reader)
		if err != nil {
			p.fail(err)
			return
		}
		var message rpcMessage
		if err := json.Unmarshal(data, &message); err != nil {
			p.fail(fmt.Errorf("decode LSP message: %w", err))
			return
		}
		if message.Method != "" {
			if len(message.ID) > 0 && string(message.ID) != "null" {
				go p.handleRequest(message)
			} else if p.onNotification != nil {
				p.onNotification(message.Method, message.Params)
			}
			continue
		}
		p.handleResponse(message)
	}
}

func (p *rpcPeer) handleRequest(message rpcMessage) {
	var result any
	var rpcErr *RPCError
	if p.onRequest == nil {
		rpcErr = &RPCError{Code: -32601, Message: "method not found"}
	} else {
		result, rpcErr = p.onRequest(context.Background(), message.Method, message.Params)
	}
	response := rpcMessage{JSONRPC: "2.0", ID: message.ID}
	if rpcErr != nil {
		response.Error = rpcErr
	} else {
		resultRaw, err := marshalOptional(result)
		if err != nil {
			response.Error = &RPCError{Code: -32603, Message: err.Error()}
		} else {
			if len(resultRaw) == 0 {
				resultRaw = json.RawMessage("null")
			}
			response.Result = resultRaw
		}
	}
	_ = p.write(response)
}

func (p *rpcPeer) handleResponse(message rpcMessage) {
	var id int64
	if err := json.Unmarshal(message.ID, &id); err != nil {
		return
	}
	p.mu.Lock()
	result := p.pending[id]
	delete(p.pending, id)
	p.mu.Unlock()
	if result == nil {
		return
	}
	if message.Error != nil {
		result <- rpcResponse{err: message.Error}
	} else {
		result <- rpcResponse{result: message.Result}
	}
}

func (p *rpcPeer) write(message rpcMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal LSP message: %w", err)
	}
	if len(data) > maxRPCMessageBytes {
		return fmt.Errorf("LSP message exceeds %d bytes", maxRPCMessageBytes)
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	select {
	case <-p.done:
		return io.ErrClosedPipe
	default:
	}
	if _, err := fmt.Fprintf(p.writer, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
		p.fail(err)
		return err
	}
	if _, err := p.writer.Write(data); err != nil {
		p.fail(err)
		return err
	}
	return nil
}

func (p *rpcPeer) removePending(id int64) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}

func (p *rpcPeer) fail(err error) {
	p.once.Do(func() {
		p.mu.Lock()
		p.err = err
		pending := p.pending
		p.pending = make(map[int64]chan rpcResponse)
		close(p.done)
		p.mu.Unlock()
		for _, result := range pending {
			select {
			case result <- rpcResponse{err: err}:
			default:
			}
		}
	})
}

func readRPCFrame(reader *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, errors.New("malformed LSP header")
		}
		if strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			length, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || length < 0 || length > maxRPCMessageBytes {
				return nil, errors.New("invalid LSP Content-Length")
			}
			contentLength = length
		}
	}
	if contentLength < 0 {
		return nil, errors.New("LSP message is missing Content-Length")
	}
	data := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, data); err != nil {
		return nil, err
	}
	return data, nil
}

func marshalOptional(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	if raw, ok := value.(json.RawMessage); ok {
		if len(raw) == 0 {
			return nil, nil
		}
		return raw, nil
	}
	data, err := json.Marshal(value)
	return data, err
}
