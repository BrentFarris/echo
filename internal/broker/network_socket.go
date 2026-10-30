package broker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/brent/echo/internal/domain"
	"github.com/gorilla/websocket"
)

type networkSessionKey struct{}

type NetworkSession struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	sockets map[string]*networkSocket
	opening int
}

type socketMessage struct {
	kind int
	data []byte
}
type networkSocket struct {
	conn      *websocket.Conn
	mu        sync.Mutex
	write     sync.Mutex
	queue     []socketMessage
	bytes     int
	err       error
	wake      chan struct{}
	receiving bool
}

// WithNetworkSession binds opaque socket handles to one invocation or app relay.
func WithNetworkSession(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	s := &NetworkSession{ctx: ctx, cancel: cancel, sockets: map[string]*networkSocket{}}
	stop := context.AfterFunc(ctx, s.close)
	return context.WithValue(ctx, networkSessionKey{}, s), func() { cancel(); stop(); s.close() }
}

func (s *NetworkSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, socket := range s.sockets {
		socket.fail(errors.New("network session closed"))
	}
	s.sockets = map[string]*networkSocket{}
}

func (s *networkSocket) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.conn.Close()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *networkSocket) read() {
	for {
		kind, data, err := s.conn.ReadMessage()
		if err != nil {
			s.fail(err)
			return
		}
		s.mu.Lock()
		if s.bytes+len(data) > 4<<20 || len(s.queue) >= 256 {
			s.mu.Unlock()
			s.fail(errors.New("WebSocket receive queue exceeded 4 MiB or 256 messages"))
			return
		}
		s.queue = append(s.queue, socketMessage{kind, data})
		s.bytes += len(data)
		s.mu.Unlock()
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

func (s *networkSocket) receive(ctx context.Context) (any, error) {
	s.mu.Lock()
	if s.receiving {
		s.mu.Unlock()
		return nil, errors.New("only one WebSocket receive may be pending")
	}
	s.receiving = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.receiving = false; s.mu.Unlock() }()
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			message := s.queue[0]
			s.queue[0] = socketMessage{}
			s.queue = s.queue[1:]
			s.bytes -= len(message.data)
			s.mu.Unlock()
			content, encoding := networkContent(message.data)
			if message.kind == websocket.BinaryMessage {
				content, encoding = base64.StdEncoding.EncodeToString(message.data), "base64"
			}
			return map[string]any{"data": content, "encoding": encoding, "binary": message.kind == websocket.BinaryMessage}, nil
		}
		err := s.err
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.wake:
		}
	}
}

type socketArgs struct {
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Protocols []string          `json:"protocols"`
	Handle    string            `json:"handle"`
	Data      string            `json:"data"`
	Encoding  string            `json:"encoding"`
	Binary    bool              `json:"binary"`
}

func NetworkSocketCall(ctx context.Context, method string, raw json.RawMessage, origins []string) (any, error) {
	s, ok := ctx.Value(networkSessionKey{}).(*NetworkSession)
	if !ok {
		return nil, errors.New("WebSockets require an active invocation or app network session")
	}
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	var a socketArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if method == "websocket.open" {
		if err := CheckNetworkURL(a.URL, origins, true); err != nil {
			return nil, err
		}
		headers, err := networkHeaders(a.Headers)
		if err != nil {
			return nil, err
		}
		if len(a.Protocols) > 16 {
			return nil, errors.New("too many WebSocket subprotocols")
		}
		for _, protocol := range a.Protocols {
			if len(protocol) == 0 || len(protocol) > 128 {
				return nil, errors.New("invalid WebSocket subprotocol")
			}
			for _, c := range protocol {
				if c < 33 || c > 126 || c == ',' {
					return nil, errors.New("invalid WebSocket subprotocol")
				}
			}
		}
		s.mu.Lock()
		if len(s.sockets)+s.opening >= 16 {
			s.mu.Unlock()
			return nil, errors.New("at most 16 WebSockets per session")
		}
		s.opening++
		s.mu.Unlock()
		defer func() { s.mu.Lock(); s.opening--; s.mu.Unlock() }()
		dial := websocket.Dialer{NetDialContext: networkDial, Proxy: nil, HandshakeTimeout: 30 * time.Second, Subprotocols: a.Protocols, ReadBufferSize: 4096, WriteBufferSize: 4096}
		// Gorilla requires a 101 response and never follows handshake redirects.
		conn, response, err := dial.DialContext(ctx, a.URL, headers)
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if err != nil {
			return nil, err
		}
		conn.SetReadLimit(NetworkMessageLimit)
		socket := &networkSocket{conn: conn, wake: make(chan struct{}, 1)}
		handle := domain.ID()
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			conn.Close()
			return nil, s.ctx.Err()
		}
		s.sockets[handle] = socket
		s.mu.Unlock()
		go socket.read()
		return map[string]any{"handle": handle, "protocol": conn.Subprotocol()}, nil
	}
	s.mu.Lock()
	socket := s.sockets[a.Handle]
	s.mu.Unlock()
	if socket == nil {
		return nil, errors.New("WebSocket handle does not belong to this session")
	}
	switch method {
	case "websocket.send":
		data, err := networkBytes(a.Data, a.Encoding)
		if err != nil {
			return nil, err
		}
		kind := websocket.TextMessage
		if a.Binary {
			kind = websocket.BinaryMessage
		} else if !utf8.Valid(data) {
			return nil, errors.New("WebSocket text messages must be valid UTF-8; use binary for arbitrary bytes")
		}
		// A waiting writer retains payload memory. Reject concurrent sends so
		// outbound queues cannot grow without bound.
		if !socket.write.TryLock() {
			socket.fail(errors.New("concurrent WebSocket send"))
			return nil, errors.New("WebSocket send already pending")
		}
		defer socket.write.Unlock()
		socket.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err = socket.conn.WriteMessage(kind, data); err != nil {
			socket.fail(err)
			return nil, err
		}
		return map[string]any{"sent": true}, nil
	case "websocket.receive":
		return socket.receive(ctx)
	case "websocket.close":
		s.mu.Lock()
		delete(s.sockets, a.Handle)
		s.mu.Unlock()
		socket.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		socket.fail(errors.New("WebSocket closed"))
		return map[string]any{"closed": true}, nil
	}
	return nil, errors.New("unknown WebSocket operation")
}
