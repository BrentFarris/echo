package broker

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
)

type remoteKey struct{}

func RemoteExecution(ctx context.Context) context.Context {
	return context.WithValue(ctx, remoteKey{}, true)
}

type restrictedKey struct{}

func RestrictedExecution(ctx context.Context) context.Context {
	return context.WithValue(ctx, restrictedKey{}, true)
}
func IsRestricted(ctx context.Context) bool {
	value, _ := ctx.Value(restrictedKey{}).(bool)
	return value
}

// Echo rejects requests marked by another instance's plugin broker. This
// leaves unrelated services (including those mounted at /api/v1) usable.
const RemotePluginHeader = "X-Echo-Remote-Plugin"

func markRemotePlugin(ctx context.Context, headers http.Header) {
	if ctx.Value(remoteKey{}) == true || IsRestricted(ctx) {
		headers.Set(RemotePluginHeader, "1")
	}
}

// Plugin network grants cannot be used to turn a remote request into a local
// owner request to Echo, including via redirects or encoded path components.
func checkRestrictedURL(ctx context.Context, u *url.URL) error {
	if ctx.Value(remoteKey{}) != true && !IsRestricted(ctx) {
		return nil
	}
	p := strings.ReplaceAll(u.Path, "\\", "/")
	for range 3 {
		decoded, err := url.PathUnescape(p)
		if err != nil || decoded == p {
			break
		}
		p = decoded
	}
	p = path.Clean("/" + p)
	if p == "/api/v1/session" || strings.HasPrefix(p, "/api/v1/swarm/") || strings.HasPrefix(p, "/api/v1/hosting/") {
		return errors.New("restricted swarm work cannot access owner API endpoints through the network broker")
	}
	return nil
}
