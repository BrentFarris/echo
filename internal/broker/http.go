package broker

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func Fetch(ctx context.Context, raw string, allowedOrigins []string) (map[string]any, error) {
	allowed := func(u *url.URL) bool {
		for _, v := range allowedOrigins {
			if strings.EqualFold(strings.TrimRight(v, "/"), u.Scheme+"://"+u.Host) {
				return true
			}
		}
		return false
	}
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid HTTP(S) URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, e
		}
		if len(ips) == 0 {
			return nil, errors.New("host has no addresses")
		}
		for _, ip := range ips {
			if !ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() {
				if !allowed(u) {
					return nil, errors.New("private network destination has not been granted")
				}
			}
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		if req.URL.User != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
			return errors.New("unsafe redirect")
		}
		u = req.URL
		return nil
	}}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("User-Agent", "Echo/1.0")
	res, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	body, e := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(body) > 2<<20 {
		return nil, errors.New("response exceeds 2 MiB")
	}
	return map[string]any{"url": res.Request.URL.String(), "status": res.StatusCode, "contentType": res.Header.Get("Content-Type"), "content": string(body)}, nil
}
