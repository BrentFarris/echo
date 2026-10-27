package swarm

import (
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// stream is a flow-controlled virtual TCP connection. A slow media download
// cannot block the peer reader or consume unbounded memory on a relay.
type stream struct {
	m                              *Manager
	id                             string
	peer                           Node
	route                          []string
	data                           chan []byte
	credits                        chan struct{}
	opened                         chan error
	done                           chan struct{}
	once                           sync.Once
	readMu, writeMu, receiveMu, mu sync.Mutex
	buffer                         []byte
	err                            error
	readDeadline, writeDeadline    time.Time
	deadlines                      chan struct{}
	sent, received                 uint64
	challenge                      []byte
}

func newStream(m *Manager, id string, peer Node, route []string) *stream {
	s := &stream{m: m, id: id, peer: peer, route: route, data: make(chan []byte, windowSize), credits: make(chan struct{}, windowSize), opened: make(chan error, 1), done: make(chan struct{}), deadlines: make(chan struct{})}
	for i := 0; i < windowSize; i++ {
		s.credits <- struct{}{}
	}
	return s
}
func (s *stream) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	return io.EOF
}
func (s *stream) finish(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.done)
		s.m.mu.Lock()
		delete(s.m.streams, s.id)
		s.m.mu.Unlock()
	})
}
func (s *stream) receive(kind string, data []byte) {
	s.receiveMu.Lock()
	defer s.receiveMu.Unlock()
	if kind != "opened" && kind != "rejected" {
		s.mu.Lock()
		valid := len(s.challenge) == 32 && len(data) >= 32 && subtle.ConstantTimeCompare(s.challenge, data[:32]) == 1
		s.mu.Unlock()
		if !valid {
			return
		}
		data = data[32:]
	}
	switch kind {
	case "opened":
		s.mu.Lock()
		if len(s.challenge) != 0 || len(data) != 32 {
			s.mu.Unlock()
			return
		}
		s.challenge = append([]byte(nil), data...)
		s.mu.Unlock()
		select {
		case s.opened <- nil:
		default:
		}
	case "rejected":
		select {
		case s.opened <- errors.New(string(data)):
		default:
		}
	case "close":
		s.finish(io.EOF)
	case "credit":
		select {
		case s.credits <- struct{}{}:
		default:
			s.finish(errors.New("invalid stream flow control"))
		}
	case "data":
		if len(data) < 8 || binary.BigEndian.Uint64(data[:8]) != s.received {
			s.finish(errors.New("stream sequence gap; reconnect to recover"))
			return
		}
		s.received++
		select {
		case s.data <- data[8:]:
		default:
			s.finish(errors.New("stream receive window exceeded"))
		}
	}
}

func (s *stream) send(kind string, data []byte) error {
	s.mu.Lock()
	frame := append(append([]byte(nil), s.challenge...), data...)
	s.mu.Unlock()
	return s.m.send(s.peer, s.id, kind, s.route, frame)
}
func (s *stream) timer(write bool) (<-chan time.Time, <-chan struct{}, func()) {
	s.mu.Lock()
	deadline := s.readDeadline
	if write {
		deadline = s.writeDeadline
	}
	changed := s.deadlines
	s.mu.Unlock()
	if deadline.IsZero() {
		return nil, changed, func() {}
	}
	timer := time.NewTimer(time.Until(deadline))
	return timer.C, changed, func() { timer.Stop() }
}
func (s *stream) Read(out []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if len(out) == 0 {
		return 0, nil
	}
	for len(s.buffer) == 0 {
		select {
		case value := <-s.data:
			s.buffer = value
		default:
		}
		if len(s.buffer) > 0 {
			break
		}
		timeout, changed, stop := s.timer(false)
		select {
		case value := <-s.data:
			s.buffer = value
		case <-s.done:
			stop()
			select {
			case value := <-s.data:
				s.buffer = value
			default:
				return 0, s.failure()
			}
		case <-timeout:
			stop()
			return 0, os.ErrDeadlineExceeded
		case <-changed:
		}
		stop()
	}
	n := copy(out, s.buffer)
	s.buffer = s.buffer[n:]
	if len(s.buffer) == 0 {
		_ = s.send("credit", nil)
	}
	return n, nil
}
func (s *stream) Write(data []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	total := 0
	for len(data) > 0 {
		for {
			timeout, changed, stop := s.timer(true)
			ready := false
			select {
			case <-s.credits:
				ready = true
			case <-s.done:
				stop()
				return total, s.failure()
			case <-timeout:
				stop()
				return total, os.ErrDeadlineExceeded
			case <-changed:
			}
			stop()
			if ready {
				break
			}
		}
		n := min(len(data), frameSize-40)
		frame := make([]byte, n+8)
		binary.BigEndian.PutUint64(frame, s.sent)
		copy(frame[8:], data[:n])
		if err := s.send("data", frame); err != nil {
			s.finish(err)
			return total, err
		}
		s.sent++
		total += n
		data = data[n:]
	}
	return total, nil
}
func (s *stream) Close() error {
	select {
	case <-s.done:
		return nil
	default:
	}
	_ = s.send("close", nil)
	s.finish(net.ErrClosed)
	return nil
}
func (s *stream) LocalAddr() net.Addr  { return address("swarm:" + s.m.Node().ID) }
func (s *stream) RemoteAddr() net.Addr { return address("swarm:" + s.peer.ID) }
func (s *stream) setDeadline(read, write bool, value time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if read {
		s.readDeadline = value
	}
	if write {
		s.writeDeadline = value
	}
	close(s.deadlines)
	s.deadlines = make(chan struct{})
	return nil
}
func (s *stream) SetDeadline(value time.Time) error      { return s.setDeadline(true, true, value) }
func (s *stream) SetReadDeadline(value time.Time) error  { return s.setDeadline(true, false, value) }
func (s *stream) SetWriteDeadline(value time.Time) error { return s.setDeadline(false, true, value) }
