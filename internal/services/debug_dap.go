package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/google/go-dap"
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

type dapCallResult struct {
	message dapEnvelope
	err     error
}

type dapClientResponse struct {
	Seq        int    `json:"seq"`
	Type       string `json:"type"`
	RequestSeq int    `json:"request_seq"`
	Success    bool   `json:"success"`
	Command    string `json:"command"`
	Message    string `json:"message,omitempty"`
}

// dapConnection is transport-neutral: any full-duplex io.ReadWriteCloser can
// host a DAP adapter (TCP today, stdio or a remote tunnel later).
type dapConnection struct {
	transport io.ReadWriteCloser
	reader    *bufio.Reader

	writeMu  sync.Mutex
	mu       sync.Mutex
	nextSeq  int
	pending  map[int]chan dapCallResult
	closed   bool
	closeErr error

	onEvent   func(dapEnvelope)
	onClose   func(error)
	done      chan struct{}
	closeOnce sync.Once
}

func newDAPConnection(transport io.ReadWriteCloser, onEvent func(dapEnvelope), onClose ...func(error)) *dapConnection {
	c := &dapConnection{
		transport: transport,
		reader:    bufio.NewReader(transport),
		pending:   make(map[int]chan dapCallResult),
		onEvent:   onEvent,
		done:      make(chan struct{}),
	}
	if len(onClose) > 0 {
		c.onClose = onClose[0]
	}
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
	response := make(chan dapCallResult, 1)
	c.pending[seq] = response
	c.mu.Unlock()

	message := dapEnvelope{Seq: seq, Type: "request", Command: command, Arguments: args}
	if err := c.write(message); err != nil {
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
			message := result.message.Message
			if message == "" {
				message = "debug adapter rejected the request"
			}
			return dapEnvelope{}, fmt.Errorf("DAP %s failed: %s", command, message)
		}
		return result.message, nil
	case <-ctx.Done():
		c.removePending(seq)
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

func (c *dapConnection) notify(command string, arguments any) error {
	args, err := json.Marshal(arguments)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return io.ErrClosedPipe
	}
	c.nextSeq++
	seq := c.nextSeq
	c.mu.Unlock()
	return c.write(dapEnvelope{Seq: seq, Type: "request", Command: command, Arguments: args})
}

func (c *dapConnection) write(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return dap.WriteBaseMessage(c.transport, data)
}

func (c *dapConnection) readLoop() {
	for {
		data, err := dap.ReadBaseMessage(c.reader)
		if err != nil {
			c.fail(err)
			return
		}
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
				pending <- dapCallResult{message: message}
			}
		case "event":
			if c.onEvent != nil {
				c.onEvent(message)
			}
		case "request":
			// Echo does not currently allow adapters to launch terminals or run
			// arbitrary commands. Reply explicitly so the adapter cannot hang.
			_ = c.write(dapClientResponse{
				Seq:        c.nextSequence(),
				Type:       "response",
				RequestSeq: message.Seq,
				Success:    false,
				Command:    message.Command,
				Message:    "client request is not supported by Echo",
			})
		default:
			c.fail(fmt.Errorf("invalid DAP message type %q", message.Type))
			return
		}
	}
}

func (c *dapConnection) nextSequence() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextSeq++
	return c.nextSeq
}

func (c *dapConnection) removePending(seq int) {
	c.mu.Lock()
	delete(c.pending, seq)
	c.mu.Unlock()
}

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
		c.pending = make(map[int]chan dapCallResult)
		c.mu.Unlock()
		for _, response := range pending {
			response <- dapCallResult{err: err}
		}
		close(c.done)
		if c.onClose != nil {
			c.onClose(err)
		}
	})
}

func (c *dapConnection) Close() error {
	c.fail(io.ErrClosedPipe)
	return nil
}

func isDAPConnectionClosed(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe)
}
