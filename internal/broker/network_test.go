package broker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNetworkOriginPolicy(t *testing.T) {
	got, err := NormalizeNetworkOrigins([]string{"HTTPS://EXAMPLE.COM:443/", "https://example.com", "ws://localhost:0080", "http://[::1]:8080"})
	if err != nil || !slices.Equal(got, []string{"http://[::1]:8080", "https://example.com", "ws://localhost"}) {
		t.Fatal(got, err)
	}
	for _, raw := range []string{"https://*.example.com", "https://user:pass@example.com", "https://example.com/path", "https://example.com/?", "https://example.com/#", "https://example.com?x=1", "file:///tmp/a", "https://example.com:0", "http://example.com:", "https://example.com/'; script-src *", "https://example.com/%2f"} {
		if _, err := NormalizeNetworkOrigins([]string{raw}); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"http://example.com", "https://sub.example.com", "https://example.com:444", "https://example.com.evil.test", "https://user@example.com"} {
		if err := CheckNetworkURL(raw, []string{"https://example.com"}, false); err == nil {
			t.Errorf("allowed %q", raw)
		}
	}
	if err := CheckNetworkURL("https://example.com/any/path?ok=1", []string{"https://example.com"}, false); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkHTTPGrantsRedirectsAndCredentials(t *testing.T) {
	var seen http.Header
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Set-Cookie", "upstream=secret")
		io.Copy(w, r.Body)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/result", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	args := NetworkRequest{URL: redirect.URL, Method: "POST", Headers: map[string]string{"Authorization": "Bearer sensitive", "X-Custom-Secret": "secret"}, Body: "hello"}
	if _, err := NetworkFetch(context.Background(), args, []string{redirect.URL}); err == nil || seen != nil {
		t.Fatal("redirect escaped grant", err, seen)
	}
	result, err := NetworkFetch(context.Background(), args, []string{redirect.URL, target.URL})
	if err != nil || result["content"] != "hello" {
		t.Fatal(result, err)
	}
	if seen.Get("Authorization") != "" || seen.Get("X-Custom-Secret") != "" || seen.Get("Cookie") != "" {
		t.Fatal("credentials leaked", seen)
	}
	if result["headers"].(http.Header).Get("Set-Cookie") != "" {
		t.Fatal("cookie returned to plugin")
	}
	args.URL = target.URL
	if _, err := NetworkFetch(context.Background(), args, nil); err == nil || !strings.Contains(err.Error(), "network.origins") {
		t.Fatal(err)
	}
	if _, err := NetworkFetch(context.Background(), args, []string{target.URL}); err != nil || seen.Get("Authorization") != "Bearer sensitive" {
		t.Fatal("explicit auth missing", err)
	}
	args.Headers["Cookie"] = "echo2-session=secret"
	if _, err := NetworkFetch(context.Background(), args, []string{target.URL}); err == nil {
		t.Fatal("accepted cookies")
	}
}

func TestNetworkHTTPBoundsBinaryAndCancellation(t *testing.T) {
	started := make(chan struct{})
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/large":
			io.WriteString(w, strings.Repeat("x", NetworkMessageLimit+1))
		case "/wait":
			close(started)
			<-r.Context().Done()
		default:
			w.Write([]byte{0xff, 0x00})
		}
	}))
	defer host.Close()
	result, err := NetworkFetch(context.Background(), NetworkRequest{URL: host.URL}, []string{host.URL})
	if err != nil || result["encoding"] != "base64" || result["content"] != "/wA=" {
		t.Fatal(result, err)
	}
	if _, err = NetworkFetch(context.Background(), NetworkRequest{URL: host.URL + "/large"}, []string{host.URL}); err == nil {
		t.Fatal("oversized response")
	}
	if _, err = NetworkFetch(context.Background(), NetworkRequest{URL: host.URL, Body: strings.Repeat("x", NetworkMessageLimit+1)}, []string{host.URL}); err == nil {
		t.Fatal("oversized request")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NetworkFetch(ctx, NetworkRequest{URL: host.URL + "/wait"}, []string{host.URL})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("request survived cancellation")
	}
}

func TestNetworkWebSocketSessionIsolationAndBounds(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if conn.WriteMessage(kind, data) != nil {
				return
			}
		}
	}))
	defer host.Close()
	origin := "ws" + strings.TrimPrefix(host.URL, "http")
	ctx, closeSession := WithNetworkSession(context.Background())
	defer closeSession()
	call := func(ctx context.Context, method string, args any) (any, error) {
		raw, _ := json.Marshal(args)
		return NetworkSocketCall(ctx, method, raw, []string{origin})
	}
	value, err := call(ctx, "websocket.open", map[string]any{"url": origin})
	if err != nil {
		t.Fatal(err)
	}
	handle := value.(map[string]any)["handle"].(string)
	other, closeOther := WithNetworkSession(context.Background())
	defer closeOther()
	if _, err = call(other, "websocket.send", map[string]any{"handle": handle, "data": "wrong"}); err == nil {
		t.Fatal("cross-session handle")
	}
	for _, binary := range []bool{false, true} {
		if _, err = call(ctx, "websocket.send", map[string]any{"handle": handle, "data": "hello", "binary": binary}); err != nil {
			t.Fatal(err)
		}
		value, err = call(ctx, "websocket.receive", map[string]any{"handle": handle})
		if err != nil || value.(map[string]any)["binary"] != binary {
			t.Fatal(value, err)
		}
	}
	if _, err = call(ctx, "websocket.send", map[string]any{"handle": handle, "data": strings.Repeat("x", NetworkMessageLimit+1)}); err == nil {
		t.Fatal("oversized message")
	}
	closeSession()
	if _, err = call(ctx, "websocket.receive", map[string]any{"handle": handle}); err == nil {
		t.Fatal("session survived close")
	}
}

func TestNetworkWebSocketRejectsRedirectAndSlowReceiver(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/socket", 302)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for i := 0; i < 6; i++ {
			if conn.WriteMessage(websocket.BinaryMessage, make([]byte, NetworkMessageLimit)) != nil {
				return
			}
		}
		conn.ReadMessage()
	}))
	defer host.Close()
	origin := "ws" + strings.TrimPrefix(host.URL, "http")
	ctx, done := WithNetworkSession(context.Background())
	defer done()
	for _, path := range []string{"/redirect", "/socket"} {
		raw, _ := json.Marshal(map[string]any{"url": origin + path})
		value, err := NetworkSocketCall(ctx, "websocket.open", raw, []string{origin})
		if path == "/redirect" {
			if err == nil {
				t.Fatal("followed redirect")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		session := ctx.Value(networkSessionKey{}).(*NetworkSession)
		socket := session.sockets[value.(map[string]any)["handle"].(string)]
		deadline := time.Now().Add(3 * time.Second)
		for {
			socket.mu.Lock()
			err = socket.err
			bytes := socket.bytes
			socket.mu.Unlock()
			if bytes > 4<<20 {
				t.Fatal("queue grew beyond limit")
			}
			if err != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("slow consumer was not closed")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !strings.Contains(err.Error(), "queue") {
			t.Fatal(err)
		}
	}
}
