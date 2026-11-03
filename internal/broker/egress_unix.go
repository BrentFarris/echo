//go:build linux || darwin

package broker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// The proxy resolves and pins destination IPs itself. Every address returned by
// DNS must satisfy the grant, preventing DNS rebinding and proxy redirects from
// expanding Internet access into the local network.
type egressProxy struct {
	listener          net.Listener
	ctx               context.Context
	cancel            context.CancelFunc
	internet, private bool
	mu                sync.Mutex
	connections       map[net.Conn]bool
	wg                sync.WaitGroup
	socket            string
}

func startEgress(parent context.Context, internet, private bool, scratch string) (*egressProxy, error) {
	network, address := "tcp", "127.0.0.1:0"
	socket := ""
	if runtime.GOOS == "linux" {
		network = "unix"
		socket = filepath.Join(scratch, "egress.sock")
		address = socket
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	p := &egressProxy{listener: listener, ctx: ctx, cancel: cancel, internet: internet, private: private, connections: map[net.Conn]bool{}, socket: socket}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.connections[conn] = true
			p.mu.Unlock()
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer conn.Close()
				defer func() { p.mu.Lock(); delete(p.connections, conn); p.mu.Unlock() }()
				_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
				reader := bufio.NewReader(conn)
				first, err := reader.Peek(1)
				if err != nil {
					return
				}
				_ = conn.SetReadDeadline(time.Time{})
				if first[0] == 5 {
					p.socks(conn, reader)
				} else {
					p.http(conn, reader)
				}
			}()
		}
	}()
	return p, nil
}
func (p *egressProxy) Close() {
	p.cancel()
	p.listener.Close()
	p.mu.Lock()
	for c := range p.connections {
		c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}
func (p *egressProxy) dial(ctx context.Context, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return nil, errors.New("invalid port")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("destination has no addresses")
	}
	for _, ip := range ips {
		private := ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || !ip.IP.IsGlobalUnicast()
		if private && !p.private || !private && !p.internet {
			return nil, errors.New("network destination has not been granted")
		}
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(ips[0].IP.String(), port))
}
func (p *egressProxy) tunnel(client net.Conn, reader io.Reader, upstream net.Conn) {
	defer upstream.Close()
	done := make(chan struct{})
	go func() { io.Copy(client, upstream); client.Close(); close(done) }()
	io.Copy(upstream, reader)
	upstream.Close()
	<-done
}
func (p *egressProxy) http(conn net.Conn, reader *bufio.Reader) {
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	defer request.Body.Close()
	reject := func(err error) {
		text := "Network request denied: " + err.Error()
		fmt.Fprintf(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(text), text)
	}
	if request.Method == "CONNECT" {
		upstream, err := p.dial(p.ctx, request.Host)
		if err != nil {
			reject(err)
			return
		}
		io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		p.tunnel(conn, reader, upstream)
		return
	}
	if request.URL.Scheme != "http" || request.URL.Host == "" || request.URL.User != nil {
		reject(errors.New("use an absolute HTTP URL or CONNECT"))
		return
	}
	request.RequestURI = ""
	request.Header.Del("Proxy-Authorization")
	request.Header.Del("Proxy-Connection")
	request.Close = true
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, address string) (net.Conn, error) { return p.dial(ctx, address) }, DisableKeepAlives: true, ResponseHeaderTimeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	response, err := transport.RoundTrip(request.WithContext(p.ctx))
	if err != nil {
		reject(err)
		return
	}
	defer response.Body.Close()
	response.Close = true
	_ = response.Write(conn)
}
func (p *egressProxy) socks(conn net.Conn, reader *bufio.Reader) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return
	}
	allowed := false
	for _, m := range methods {
		if m == 0 {
			allowed = true
		}
	}
	if !allowed {
		conn.Write([]byte{5, 255})
		return
	}
	conn.Write([]byte{5, 0})
	header = make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != 5 || header[1] != 1 {
		return
	}
	host := ""
	switch header[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(reader, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(reader, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		n, err := reader.ReadByte()
		if err != nil {
			return
		}
		b := make([]byte, int(n))
		if _, err = io.ReadFull(reader, b); err != nil {
			return
		}
		host = string(b)
	default:
		return
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(reader, port); err != nil {
		return
	}
	upstream, err := p.dial(p.ctx, net.JoinHostPort(host, strconv.Itoa(int(port[0])*256+int(port[1]))))
	if err != nil {
		conn.Write([]byte{5, 2, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	p.tunnel(conn, reader, upstream)
}
