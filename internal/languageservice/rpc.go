// Package languageservice implements bounded LSP stdio connections. It does not
// launch tools, decide filesystem permissions, or know about browser clients.
package languageservice

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

const MaxMessage = 16 << 20

type Message struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

type Client struct {
	ctx     context.Context
	cancel  context.CancelFunc
	stream  io.Closer
	queue   chan Message
	Events  chan Message
	mu      sync.Mutex
	pending map[string]chan Message
	seq     atomic.Int64
	err     error
}

func New(ctx context.Context, input io.Writer, output io.Reader, stream io.Closer) *Client {
	ctx, cancel := context.WithCancel(ctx)
	c := &Client{ctx: ctx, cancel: cancel, stream: stream, queue: make(chan Message, 64), Events: make(chan Message, 64), pending: map[string]chan Message{}}
	go func() { <-ctx.Done(); stream.Close() }()
	go c.write(input)
	go c.read(output)
	return c
}
func (c *Client) Close() error          { c.fail(errors.New("language server closed")); return nil }
func (c *Client) Done() <-chan struct{} { return c.ctx.Done() }
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return c.ctx.Err()
}
func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
	c.cancel()
}
func (c *Client) send(ctx context.Context, m Message) error {
	m.JSONRPC = "2.0"
	select {
	case c.queue <- m:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return c.Err()
	}
}
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.send(ctx, Message{Method: method, Params: p})
}
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	p, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	id := strconv.FormatInt(c.seq.Add(1), 10)
	ch := make(chan Message, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err = c.send(ctx, Message{ID: json.RawMessage(id), Method: method, Params: p}); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	case <-ctx.Done():
		data, _ := json.Marshal(map[string]any{"id": json.RawMessage(id)})
		select {
		case c.queue <- Message{JSONRPC: "2.0", Method: "$/cancelRequest", Params: data}:
		default:
		}
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.Err()
	}
}
func (c *Client) write(w io.Writer) {
	for {
		select {
		case <-c.ctx.Done():
			return
		case m := <-c.queue:
			data, err := json.Marshal(m)
			if err == nil && len(data) > MaxMessage {
				err = errors.New("LSP message exceeds 16 MiB")
			}
			if err == nil {
				_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(data))
			}
			if err == nil {
				_, err = w.Write(data)
			}
			if err != nil {
				c.fail(err)
				return
			}
		}
	}
}
func ReadMessage(r *bufio.Reader) (Message, error) {
	length := -1
	total := 0
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			return Message{}, err
		}
		total += len(line)
		if total > 8192 {
			return Message{}, errors.New("LSP header exceeds limit")
		}
		s := strings.TrimSpace(string(line))
		if s == "" {
			break
		}
		key, value, ok := strings.Cut(s, ":")
		if ok && strings.EqualFold(key, "Content-Length") {
			if length != -1 {
				return Message{}, errors.New("duplicate LSP content length")
			}
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil || length < 0 || length > MaxMessage {
				return Message{}, errors.New("invalid LSP content length")
			}
		}
	}
	if length < 0 {
		return Message{}, errors.New("missing LSP content length")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return Message{}, err
	}
	var m Message
	err := json.Unmarshal(data, &m)
	return m, err
}
func (c *Client) read(output io.Reader) {
	defer close(c.Events)
	r := bufio.NewReader(output)
	for {
		m, err := ReadMessage(r)
		if err != nil {
			c.fail(err)
			return
		}
		if m.Method == "" {
			c.mu.Lock()
			ch := c.pending[string(m.ID)]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				default:
				}
			}
			continue
		}
		if len(m.ID) > 0 {
			reply := Message{ID: m.ID, Result: json.RawMessage("null")}
			switch m.Method {
			case "window/workDoneProgress/create", "client/registerCapability", "client/unregisterCapability":
			case "workspace/configuration":
				var p struct {
					Items []json.RawMessage `json:"items"`
				}
				json.Unmarshal(m.Params, &p)
				values := make([]any, len(p.Items))
				reply.Result, _ = json.Marshal(values)
			case "workspace/applyEdit":
				reply.Result = json.RawMessage(`{"applied":false,"failureReason":"Edits require an explicit editor action"}`)
			default:
				reply.Result = nil
				reply.Error = &RPCError{Code: -32601, Message: "unsupported server request"}
			}
			if err = c.send(c.ctx, reply); err != nil {
				return
			}
			continue
		}
		select {
		case c.Events <- m:
		case <-c.ctx.Done():
			return
		default:
			c.fail(errors.New("language server event queue overflow; reconnect required"))
			return
		}
	}
}
