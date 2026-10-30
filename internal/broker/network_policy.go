package broker

import (
	"errors"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// NetworkOrigin is shared by installation and every host network operation.
// Paths are checked separately because requests may address any path at an origin.
func NetworkOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.Hostname() == "" || strings.ContainsAny(raw, "\\\r\n\t ") {
		return "", errors.New("invalid network URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if !slices.Contains([]string{"http", "https", "ws", "wss"}, scheme) {
		return "", errors.New("network URLs require http, https, ws or wss")
	}
	host := strings.ToLower(u.Hostname())
	for _, c := range host {
		if c > 127 || !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune(".-:", c)) {
			return "", errors.New("network host must be an exact ASCII hostname or IP address (use punycode for international names)")
		}
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return "", errors.New("invalid network IP address")
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		if len(host) > 253 {
			return "", errors.New("network hostname is too long")
		}
		for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
			if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return "", errors.New("invalid network hostname")
			}
		}
	}
	port := u.Port()
	if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("empty network port")
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("invalid network port")
		}
		port = strconv.Itoa(n)
	}
	if (scheme == "http" || scheme == "ws") && port == "80" || (scheme == "https" || scheme == "wss") && port == "443" {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}

func NormalizeNetworkOrigins(origins []string) ([]string, error) {
	out := []string{}
	if len(origins) > 128 {
		return nil, errors.New("at most 128 network origins are allowed")
	}
	for _, raw := range origins {
		if len(raw) > 2048 {
			return nil, errors.New("network origin is too long")
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
			return nil, errors.New("network origins cannot contain paths, queries or fragments")
		}
		origin, err := NetworkOrigin(raw)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, origin) {
			out = append(out, origin)
		}
	}
	slices.Sort(out)
	return out, nil
}

func CheckNetworkURL(raw string, origins []string, socket bool) error {
	if len(origins) == 0 {
		return errors.New("plugin network access needs network.origins in its manifest; update the plugin and approve its destinations in Settings")
	}
	origin, err := NetworkOrigin(raw)
	if err != nil {
		return err
	}
	isSocket := strings.HasPrefix(origin, "ws:") || strings.HasPrefix(origin, "wss:")
	if isSocket != socket {
		return errors.New("wrong network protocol for this operation")
	}
	if !slices.Contains(origins, origin) {
		return errors.New("network destination is not approved: " + origin)
	}
	return nil
}
