package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBlockedAddressClassifications(t *testing.T) {
	for _, value := range []string{
		"127.0.0.1", "0.0.0.0", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "198.18.0.1", "224.0.0.1", "255.255.255.255", "::1", "fc00::1", "fe80::1", "ff02::1", "2001:db8::1",
	} {
		if !blockedAddress(netip.MustParseAddr(value)) {
			t.Errorf("%s should be blocked", value)
		}
	}
	for _, value := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if blockedAddress(netip.MustParseAddr(value)) {
			t.Errorf("%s should be public", value)
		}
	}
}

func TestPolicyExactGrantAliasAndImmediateRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	if err := os.WriteFile(path, []byte(`[{"host":"10.20.30.40","port":8443,"sandboxAlias":"service.echo.internal"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := &policy{grantsPath: path}
	addresses, err := policy.resolve(context.Background(), "service.echo.internal", 8443)
	if err != nil || len(addresses) != 1 || addresses[0].String() != "10.20.30.40" {
		t.Fatalf("alias grant failed: %v %v", addresses, err)
	}
	if _, err := policy.resolve(context.Background(), "service.echo.internal", 443); err == nil {
		t.Fatal("alias grant incorrectly covered another port")
	}
	if _, err := policy.resolve(context.Background(), "10.20.30.40", 8443); err != nil {
		t.Fatalf("exact IP grant failed: %v", err)
	}
	if _, err := policy.resolve(context.Background(), "10.20.30.41", 8443); err == nil {
		t.Fatal("grant incorrectly covered another IP")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := policy.resolve(context.Background(), "10.20.30.40", 8443); err == nil {
		t.Fatal("revoked grant remained effective")
	}
}

func TestPolicyRejectsDNSRebindingOnEveryResolution(t *testing.T) {
	var lookups atomic.Int32
	policy := &policy{lookup: func(context.Context, string) ([]netip.Addr, error) {
		if lookups.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}}
	if addresses, err := policy.resolve(context.Background(), "rebind.example", 443); err != nil || len(addresses) != 1 {
		t.Fatalf("public first resolution failed: %v %v", addresses, err)
	}
	if _, err := policy.resolve(context.Background(), "rebind.example", 443); err == nil {
		t.Fatal("private rebound address was accepted")
	}
}

func TestHTTPProxyRedirectCONNECTAuthenticationAndLiveRevocation(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "public-through-filter")
	}))
	defer target.Close()
	tlsTarget := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "connect-through-filter") }))
	defer tlsTarget.Close()

	grantsPath := filepath.Join(t.TempDir(), "grants.json")
	proxyTokenPath := filepath.Join(t.TempDir(), "proxy.token")
	if err := os.WriteFile(grantsPath, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proxyTokenPath, []byte("test-proxy-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	sharedPolicy := &policy{grantsPath: grantsPath}
	internal := httptest.NewServer(&gateway{policy: sharedPolicy, tokenFile: proxyTokenPath})
	defer internal.Close()
	proxyURL, _ := url.Parse(internal.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return http.ErrUseLastResponse
		}
		return nil
	}}

	if response, err := client.Get(target.URL + "/start"); err != nil {
		t.Fatal(err)
	} else {
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadGateway {
			t.Fatalf("ungranted private target returned %d", response.StatusCode)
		}
	}
	targetGrant := grantForServer(t, target, "")
	tlsGrant := grantForServer(t, tlsTarget, "")
	writeGatewayGrants(t, grantsPath, []grant{targetGrant, tlsGrant})
	response, err := client.Get(target.URL + "/start")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "public-through-filter" || !strings.HasSuffix(response.Request.URL.Path, "/final") {
		t.Fatalf("filtered redirect failed: status=%d body=%q final=%s", response.StatusCode, body, response.Request.URL)
	}

	connectClient := &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // controlled test server
	}}
	response, err = connectClient.Get(tlsTarget.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "connect-through-filter" {
		t.Fatalf("CONNECT proxy failed: status=%d body=%q", response.StatusCode, body)
	}

	external := httptest.NewServer(&gateway{policy: sharedPolicy, tokenFile: proxyTokenPath, requireAuth: true})
	defer external.Close()
	externalURL, _ := url.Parse(external.URL)
	unauthenticated := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(externalURL)}}
	response, err = unauthenticated.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("external proxy accepted missing credentials: %d", response.StatusCode)
	}
	externalURL.User = url.UserPassword("echo", "test-proxy-token")
	authenticated := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(externalURL)}}
	response, err = authenticated.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("external proxy rejected valid credentials: %d", response.StatusCode)
	}

	writeGatewayGrants(t, grantsPath, nil)
	response, err = client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("revoked grant remained live: %d", response.StatusCode)
	}
}

func TestSOCKS5RemoteTargetHonorsExactGrant(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "socks-through-filter") }))
	defer target.Close()
	grantsPath := filepath.Join(t.TempDir(), "grants.json")
	writeGatewayGrants(t, grantsPath, []grant{grantForServer(t, target, "")})
	gateway := &gateway{policy: &policy{grantsPath: grantsPath}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			gateway.handleSOCKS(connection, false)
		}
	}()
	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(connection, method); err != nil || method[0] != 5 || method[1] != 0 {
		t.Fatalf("SOCKS method negotiation failed: %v %x", err, method)
	}
	targetURL, _ := url.Parse(target.URL)
	host, portText, _ := net.SplitHostPort(targetURL.Host)
	address := net.ParseIP(host).To4()
	port, _ := strconv.Atoi(portText)
	request := []byte{5, 1, 0, 1, address[0], address[1], address[2], address[3], 0, 0}
	binary.BigEndian.PutUint16(request[8:10], uint16(port))
	if _, err := connection.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(connection, reply); err != nil || reply[1] != 0 {
		t.Fatalf("SOCKS connect failed: %v %x", err, reply)
	}
	_, _ = io.WriteString(connection, "GET / HTTP/1.1\r\nHost: "+targetURL.Host+"\r\nConnection: close\r\n\r\n")
	status, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil || !strings.Contains(status, "200 OK") {
		t.Fatalf("proxied SOCKS request failed: %q %v", status, err)
	}
}

func grantForServer(t *testing.T, server *httptest.Server, alias string) grant {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return grant{Host: host, Port: port, SandboxAlias: alias}
}

func writeGatewayGrants(t *testing.T, path string, grants []grant) {
	t.Helper()
	data, err := json.Marshal(grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
