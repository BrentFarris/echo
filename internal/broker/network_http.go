package broker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const NetworkMessageLimit = 1 << 20
const NetworkWireLimit = 2 << 20

type NetworkRequest struct {
	URL      string            `json:"url"`
	Method   string            `json:"method,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     string            `json:"body,omitempty"`
	Encoding string            `json:"encoding,omitempty"`
}

func networkBytes(value, encoding string) ([]byte, error) {
	if len(value) > NetworkWireLimit {
		return nil, errors.New("network message exceeds 1 MiB")
	}
	var data []byte
	var err error
	switch encoding {
	case "", "text":
		data = []byte(value)
	case "base64":
		data, err = base64.StdEncoding.DecodeString(value)
	default:
		return nil, errors.New("encoding must be text or base64")
	}
	if err != nil {
		return nil, errors.New("invalid base64 data")
	}
	if len(data) > NetworkMessageLimit {
		return nil, errors.New("network message exceeds 1 MiB")
	}
	return data, nil
}

func networkContent(data []byte) (string, string) {
	if utf8.Valid(data) {
		text := string(data)
		encoded, _ := json.Marshal(text)
		if len(encoded) <= 3*NetworkMessageLimit/2 {
			return text, "text"
		}
	}
	return base64.StdEncoding.EncodeToString(data), "base64"
}

// Resolve once and dial the selected IP, without environment proxies. The exact
// approved origin grants private addresses too; no unrelated plugin supplies grants.
func networkDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("host has no addresses")
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

func networkHeaders(values map[string]string) (http.Header, error) {
	h := http.Header{}
	size := 0
	for key, value := range values {
		key = http.CanonicalHeaderKey(key)
		size += len(key) + len(value)
		if size > 32<<10 {
			return nil, errors.New("network headers exceed 32 KiB")
		}
		if slices.Contains([]string{"Cookie", "Cookie2", "Host", "Connection", "Upgrade", "Content-Length", "Transfer-Encoding", "Proxy-Authorization", "Proxy-Connection", "Origin", "Referer", "Trailer", "Te"}, key) || strings.HasPrefix(key, "Sec-") || strings.HasPrefix(key, "Proxy-") {
			return nil, errors.New("network header is reserved: " + key)
		}
		h.Set(key, value)
	}
	return h, nil
}

func NetworkFetch(ctx context.Context, args NetworkRequest, origins []string) (map[string]any, error) {
	if err := CheckNetworkURL(args.URL, origins, false); err != nil {
		return nil, err
	}
	method := strings.ToUpper(args.Method)
	if method == "" {
		method = http.MethodGet
	}
	if !slices.Contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}, method) {
		return nil, errors.New("unsupported HTTP method")
	}
	body, err := networkBytes(args.Body, args.Encoding)
	if err != nil {
		return nil, err
	}
	headers, err := networkHeaders(args.Headers)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DialContext: networkDial, MaxResponseHeaderBytes: 32 << 10}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		if err := CheckNetworkURL(req.URL.String(), origins, false); err != nil {
			return err
		}
		previous, _ := NetworkOrigin(via[len(via)-1].URL.String())
		current, _ := NetworkOrigin(req.URL.String())
		if previous != current {
			// Custom API key headers are not reliably identifiable. Drop all caller
			// headers on an origin change, including credentials on later redirects.
			headers = http.Header{}
		}
		req.Header = headers.Clone()
		req.Header.Set("User-Agent", "Echo/1.0")
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, method, args.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	req.Header.Set("User-Agent", "Echo/1.0")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, NetworkMessageLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > NetworkMessageLimit {
		return nil, errors.New("network response exceeds 1 MiB")
	}
	res.Header.Del("Set-Cookie")
	res.Header.Del("Set-Cookie2")
	content, encoding := networkContent(data)
	result := map[string]any{"url": res.Request.URL.String(), "status": res.StatusCode, "headers": res.Header, "contentType": res.Header.Get("Content-Type"), "content": content, "encoding": encoding}
	wire, _ := json.Marshal(result)
	if len(wire) > NetworkWireLimit-4096 {
		return nil, errors.New("network response exceeds bridge limit")
	}
	return result, nil
}
