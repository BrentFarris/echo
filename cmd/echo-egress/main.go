// Command echo-egress is the sandbox's only externally routed network peer.
// It provides authenticated HTTP CONNECT/forward proxying, SOCKS5 with remote
// DNS, and filtered DNS answers. Every dial resolves again and rejects private,
// host, link-local, metadata, multicast, and reserved addresses unless an
// exact machine-local grant matches the requested host/IP and TCP port.
package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type grant struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	SandboxAlias string `json:"sandboxAlias,omitempty"`
}

type policy struct {
	grantsPath string
	mu         sync.Mutex
	grants     []grant
	lookup     func(context.Context, string) ([]netip.Addr, error)
}

type gateway struct {
	tokenFile   string
	policy      *policy
	requireAuth bool
}

func main() {
	tokenFile := environment("ECHO_PROXY_TOKEN_FILE", "/run/echo/proxy.token")
	grantsFile := environment("ECHO_NETWORK_GRANTS_FILE", "/run/echo/grants.json")
	sharedPolicy := &policy{grantsPath: grantsFile}
	internal := &gateway{tokenFile: tokenFile, policy: sharedPolicy}
	external := &gateway{tokenFile: tokenFile, policy: sharedPolicy, requireAuth: true}
	errorsChannel := make(chan error, 10)
	go func() {
		server := &http.Server{Addr: ":3128", Handler: internal, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 64 << 10}
		errorsChannel <- server.ListenAndServe()
	}()
	go func() {
		server := &http.Server{Addr: ":3129", Handler: external, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 64 << 10}
		errorsChannel <- server.ListenAndServe()
	}()
	go func() { errorsChannel <- internal.serveSOCKS(":1080", false) }()
	go func() { errorsChannel <- external.serveSOCKS(":1081", true) }()
	go func() { errorsChannel <- internal.serveDNS(":53") }()
	go func() { errorsChannel <- internal.serveDNSTCP(":53") }()
	for _, forward := range []struct{ listen, target string }{
		{":17777", environment("ECHO_WORKBENCH_AGENT_TARGET", "workbench:7777")},
		{":27777", environment("ECHO_DESKTOP_AGENT_TARGET", "desktop:7777")},
		{":25900", environment("ECHO_DESKTOP_VNC_TARGET", "desktop:5900")},
		{":23000", environment("ECHO_DESKTOP_BROWSER_TARGET", "desktop:3000")},
	} {
		forward := forward
		go func() { errorsChannel <- serveTCPForward(forward.listen, forward.target) }()
	}
	go watchHeartbeat(environment("ECHO_HEARTBEAT_FILE", "/run/echo/heartbeat"))
	if err := <-errorsChannel; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// serveTCPForward keeps workbench and desktop attached only to the internal
// Docker network. Echo publishes these gateway listeners on random loopback
// host ports; the management services themselves never receive an external
// route.
func serveTCPForward(listenAddress, targetAddress string) error {
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	var failureMu sync.Mutex
	var lastFailure time.Time
	for {
		connection, err := listener.Accept()
		if err != nil {
			return err
		}
		go func(client net.Conn) {
			target, err := net.DialTimeout("tcp", targetAddress, 5*time.Second)
			if err != nil {
				failureMu.Lock()
				if time.Since(lastFailure) >= 5*time.Second {
					log.Printf("management relay %s -> %s unavailable: %v", listenAddress, targetAddress, err)
					lastFailure = time.Now()
				}
				failureMu.Unlock()
				_ = client.Close()
				return
			}
			proxyConnections(client, target)
		}(connection)
	}
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.requireAuth && !g.authorizedHTTP(r) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="Echo Sandbox"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		g.connect(w, r)
		return
	}
	if r.URL == nil || r.URL.Hostname() == "" || (r.URL.Scheme != "http" && r.URL.Scheme != "https") {
		http.Error(w, "absolute http(s) URL required", http.StatusBadRequest)
		return
	}
	request := r.Clone(r.Context())
	request.RequestURI = ""
	request.Header = request.Header.Clone()
	removeHopHeaders(request.Header)
	request.Header.Del("Proxy-Authorization")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = g.dialContext
	defer transport.CloseIdleConnections()
	response, err := transport.RoundTrip(request)
	if err != nil {
		http.Error(w, "sandbox egress blocked or unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	removeHopHeaders(response.Header)
	copyHeader(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (g *gateway) connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := splitHostPort(r.Host, "443")
	if err != nil {
		http.Error(w, "invalid CONNECT target", http.StatusBadRequest)
		return
	}
	target, err := g.dial(r.Context(), host, port)
	if err != nil {
		http.Error(w, "sandbox egress target blocked", http.StatusForbidden)
		return
	}
	controller := http.NewResponseController(w)
	client, buffer, err := controller.Hijack()
	if err != nil {
		_ = target.Close()
		http.Error(w, "hijack unavailable", http.StatusInternalServerError)
		return
	}
	_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = buffer.Flush()
	proxyConnections(client, target)
}

func (g *gateway) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	return g.dial(ctx, host, port)
}

func (g *gateway) dial(ctx context.Context, host string, port int) (net.Conn, error) {
	addresses, err := g.policy.resolve(ctx, host, port)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	var joined error
	for _, address := range addresses {
		connection, dialErr := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.String(), strconv.Itoa(port)))
		if dialErr == nil {
			return connection, nil
		}
		joined = errors.Join(joined, dialErr)
	}
	return nil, joined
}

func (g *gateway) authorizedHTTP(r *http.Request) bool {
	header := strings.TrimSpace(r.Header.Get("Proxy-Authorization"))
	scheme, value, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return false
	}
	user, password, ok := strings.Cut(string(decoded), ":")
	return ok && user == "echo" && g.validToken(password)
}

func (g *gateway) validToken(provided string) bool {
	data, err := os.ReadFile(g.tokenFile)
	if err != nil {
		return false
	}
	expected := strings.TrimSpace(string(data))
	return len(provided) == len(expected) && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func (g *gateway) serveSOCKS(address string, requireAuth bool) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return acceptErr
		}
		go g.handleSOCKS(connection, requireAuth)
	}
}

func (g *gateway) handleSOCKS(connection net.Conn, requireAuth bool) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(connection)
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != 5 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return
	}
	supportsAuth, supportsNoAuth := false, false
	for _, method := range methods {
		if method == 2 {
			supportsAuth = true
		}
		if method == 0 {
			supportsNoAuth = true
		}
	}
	if requireAuth && !supportsAuth || !requireAuth && !supportsNoAuth {
		_, _ = connection.Write([]byte{5, 0xff})
		return
	}
	if requireAuth {
		_, _ = connection.Write([]byte{5, 2})
		if !g.socksAuthenticate(reader, connection) {
			return
		}
	} else {
		_, _ = connection.Write([]byte{5, 0})
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(reader, request); err != nil || request[0] != 5 || request[1] != 1 {
		return
	}
	host, err := readSOCKSHost(reader, request[3])
	if err != nil {
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return
	}
	port := int(binary.BigEndian.Uint16(portBytes))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	target, err := g.dial(ctx, host, port)
	cancel()
	if err != nil {
		_, _ = connection.Write([]byte{5, 2, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer target.Close()
	_, _ = connection.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	_ = connection.SetDeadline(time.Time{})
	proxyConnections(connection, target)
}

func (g *gateway) socksAuthenticate(reader *bufio.Reader, connection net.Conn) bool {
	version, err := reader.ReadByte()
	if err != nil || version != 1 {
		return false
	}
	userLength, err := reader.ReadByte()
	if err != nil {
		return false
	}
	user := make([]byte, int(userLength))
	if _, err := io.ReadFull(reader, user); err != nil {
		return false
	}
	passwordLength, err := reader.ReadByte()
	if err != nil {
		return false
	}
	password := make([]byte, int(passwordLength))
	if _, err := io.ReadFull(reader, password); err != nil {
		return false
	}
	valid := string(user) == "echo" && g.validToken(string(password))
	status := byte(1)
	if valid {
		status = 0
	}
	_, _ = connection.Write([]byte{1, status})
	return valid
}

func readSOCKSHost(reader *bufio.Reader, addressType byte) (string, error) {
	switch addressType {
	case 1:
		data := make([]byte, 4)
		_, err := io.ReadFull(reader, data)
		return net.IP(data).String(), err
	case 4:
		data := make([]byte, 16)
		_, err := io.ReadFull(reader, data)
		return net.IP(data).String(), err
	case 3:
		length, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		data := make([]byte, int(length))
		_, err = io.ReadFull(reader, data)
		return string(data), err
	default:
		return "", fmt.Errorf("unsupported address type")
	}
}

func (g *gateway) serveDNS(address string) error {
	connection, err := net.ListenPacket("udp", address)
	if err != nil {
		return err
	}
	buffer := make([]byte, 4096)
	for {
		count, peer, readErr := connection.ReadFrom(buffer)
		if readErr != nil {
			return readErr
		}
		request := append([]byte(nil), buffer[:count]...)
		go func() {
			response := g.answerDNS(request)
			if len(response) > 0 {
				_, _ = connection.WriteTo(response, peer)
			}
		}()
	}
}

func (g *gateway) serveDNSTCP(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return acceptErr
		}
		go func() {
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
			var size [2]byte
			if _, err := io.ReadFull(connection, size[:]); err != nil {
				return
			}
			length := int(binary.BigEndian.Uint16(size[:]))
			if length <= 0 || length > 4096 {
				return
			}
			request := make([]byte, length)
			if _, err := io.ReadFull(connection, request); err != nil {
				return
			}
			response := g.answerDNS(request)
			if len(response) == 0 || len(response) > 65535 {
				return
			}
			binary.BigEndian.PutUint16(size[:], uint16(len(response)))
			payload := append(size[:], response...)
			for len(payload) > 0 {
				written, writeErr := connection.Write(payload)
				if writeErr != nil || written == 0 {
					return
				}
				payload = payload[written:]
			}
		}()
	}
}

func (g *gateway) answerDNS(request []byte) []byte {
	var parser dnsmessage.Parser
	header, err := parser.Start(request)
	if err != nil {
		return nil
	}
	question, err := parser.Question()
	if err != nil {
		return nil
	}
	host := strings.TrimSuffix(question.Name.String(), ".")
	lookupHost := g.policy.aliasTarget(host)
	if lookupHost == "" {
		lookupHost = host
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addresses, lookupErr := g.policy.lookupAddresses(ctx, lookupHost)
	responseHeader := dnsmessage.Header{ID: header.ID, Response: true, RecursionAvailable: true, RecursionDesired: header.RecursionDesired}
	if lookupErr != nil {
		responseHeader.RCode = dnsmessage.RCodeNameError
	}
	builder := dnsmessage.NewBuilder(nil, responseHeader)
	builder.EnableCompression()
	if builder.StartQuestions() != nil {
		return nil
	}
	if builder.Question(question) != nil {
		return nil
	}
	if builder.StartAnswers() != nil {
		return nil
	}
	for _, address := range addresses {
		normalized := address.Unmap()
		internalService := host == "gateway" || host == "workbench" || host == "desktop"
		if blockedAddress(normalized) && !internalService && !g.policy.hasHostGrant(lookupHost) && lookupHost == host {
			continue
		}
		ttl := uint32(30)
		if normalized.Is4() && question.Type == dnsmessage.TypeA {
			value := normalized.As4()
			_ = builder.AResource(dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: ttl}, dnsmessage.AResource{A: value})
		} else if normalized.Is6() && question.Type == dnsmessage.TypeAAAA {
			value := normalized.As16()
			_ = builder.AAAAResource(dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET, TTL: ttl}, dnsmessage.AAAAResource{AAAA: value})
		}
	}
	response, _ := builder.Finish()
	return response
}

func (p *policy) resolve(ctx context.Context, host string, port int) ([]netip.Addr, error) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid target")
	}
	grants := p.load()
	for _, item := range grants {
		if item.Port == port && item.SandboxAlias != "" && strings.EqualFold(strings.TrimSpace(item.SandboxAlias), host) {
			host = strings.TrimSpace(item.Host)
			break
		}
	}
	hostGranted := grantMatches(grants, host, port)
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if blockedAddress(address) && !hostGranted {
			return nil, fmt.Errorf("address is blocked")
		}
		return []netip.Addr{address}, nil
	}
	if strings.ContainsAny(host, "*/\\") || strings.Contains(host, "..") {
		return nil, fmt.Errorf("invalid hostname")
	}
	resolved, err := p.lookupAddresses(ctx, host)
	if err != nil {
		return nil, err
	}
	result := make([]netip.Addr, 0, len(resolved))
	for _, raw := range resolved {
		address := raw.Unmap()
		if blockedAddress(address) && !hostGranted && !grantMatches(grants, address.String(), port) {
			return nil, fmt.Errorf("resolved address is blocked")
		}
		result = append(result, address)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("hostname did not resolve")
	}
	return result, nil
}

func (p *policy) aliasTarget(alias string) string {
	for _, item := range p.load() {
		if item.SandboxAlias != "" && strings.EqualFold(strings.TrimSpace(item.SandboxAlias), strings.TrimSpace(alias)) {
			return strings.TrimSpace(item.Host)
		}
	}
	return ""
}

func (p *policy) lookupAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []netip.Addr{address.Unmap()}, nil
	}
	if p.lookup != nil {
		return p.lookup(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func (p *policy) hasHostGrant(host string) bool {
	for _, item := range p.load() {
		if strings.EqualFold(strings.TrimSpace(item.Host), strings.TrimSpace(host)) {
			return true
		}
	}
	return false
}
func (p *policy) load() []grant {
	p.mu.Lock()
	defer p.mu.Unlock()
	data, err := os.ReadFile(p.grantsPath)
	if err != nil {
		p.grants = nil
		return nil
	}
	var grants []grant
	if json.Unmarshal(data, &grants) != nil {
		p.grants = nil
		return nil
	}
	p.grants = grants
	return append([]grant(nil), p.grants...)
}
func grantMatches(grants []grant, host string, port int) bool {
	for _, item := range grants {
		if item.Port == port && strings.EqualFold(strings.TrimSpace(item.Host), strings.TrimSpace(host)) {
			return true
		}
	}
	return false
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("168.63.129.16/32"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"),
}

func blockedAddress(address netip.Addr) bool {
	if !address.IsValid() || address.IsUnspecified() || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() {
		return true
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func splitHostPort(value, defaultPort string) (string, int, error) {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		host = value
		portText = defaultPort
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, err
	}
	return strings.Trim(host, "[]"), port, nil
}
func proxyConnections(left, right net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(left, right)
		if closer, ok := left.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(right, left)
		if closer, ok := right.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	_ = left.Close()
	_ = right.Close()
	<-done
}
func removeHopHeaders(header http.Header) {
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}
func copyHeader(destination, source http.Header) {
	for key, values := range source {
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}
func environment(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
func watchHeartbeat(path string) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	grace := time.Now().Add(2 * time.Minute)
	for range ticker.C {
		info, err := os.Stat(path)
		if err == nil && time.Since(info.ModTime()) <= 2*time.Minute {
			continue
		}
		if time.Now().After(grace) {
			os.Exit(0)
		}
	}
}
