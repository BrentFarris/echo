package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRPCPeerRoutesCallsRequestsNotificationsAndCancellation(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	notifications := make(chan string, 4)
	server := newRPCPeer(serverConn, serverConn, serverConn, func(_ context.Context, method string, params json.RawMessage) (any, *RPCError) {
		if method == "slow" {
			time.Sleep(100 * time.Millisecond)
		}
		return map[string]any{"method": method, "params": json.RawMessage(params)}, nil
	}, func(method string, _ json.RawMessage) { notifications <- method })
	client := newRPCPeer(clientConn, clientConn, clientConn, func(_ context.Context, method string, _ json.RawMessage) (any, *RPCError) {
		return "client:" + method, nil
	}, nil)
	defer server.Close()
	defer client.Close()

	result, err := client.Call(context.Background(), "echo", map[string]any{"value": 7})
	if err != nil || !strings.Contains(string(result), `"method":"echo"`) {
		t.Fatalf("client call result=%s err=%v", result, err)
	}
	result, err = server.Call(context.Background(), "workspace/configuration", nil)
	if err != nil || string(result) != `"client:workspace/configuration"` {
		t.Fatalf("server request result=%s err=%v", result, err)
	}
	if err := client.Notify("textDocument/didOpen", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	select {
	case method := <-notifications:
		if method != "textDocument/didOpen" {
			t.Fatalf("unexpected notification %q", method)
		}
	case <-time.After(time.Second):
		t.Fatal("notification was not routed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := client.Call(ctx, "slow", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	select {
	case method := <-notifications:
		if method != "$/cancelRequest" {
			t.Fatalf("expected cancellation notification, got %q", method)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation was not forwarded")
	}
}

func TestRPCPeerSerializesConcurrentWrites(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	server := newRPCPeer(serverConn, serverConn, serverConn, func(_ context.Context, _ string, params json.RawMessage) (any, *RPCError) {
		return json.RawMessage(params), nil
	}, nil)
	client := newRPCPeer(clientConn, clientConn, clientConn, nil, nil)
	defer server.Close()
	defer client.Close()
	var wg sync.WaitGroup
	for index := 0; index < 40; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := client.Call(context.Background(), "echo", map[string]int{"index": index})
			if err != nil || len(result) == 0 {
				t.Errorf("call %d result=%s err=%v", index, result, err)
			}
		}(index)
	}
	wg.Wait()
}

func TestReadRPCFrameRejectsMalformedAndOversizeFrames(t *testing.T) {
	tests := []string{
		"Bad header\r\n\r\n{}",
		"Content-Type: application/json\r\n\r\n{}",
		"Content-Length: nope\r\n\r\n{}",
		"Content-Length: 33554433\r\n\r\n",
	}
	for _, input := range tests {
		if _, err := readRPCFrame(bufio.NewReader(strings.NewReader(input))); err == nil {
			t.Fatalf("expected malformed frame error for %q", input)
		}
	}
	if _, err := readRPCFrame(bufio.NewReader(io.LimitReader(strings.NewReader("Content-Length: 2\r\n\r\n{}"), 100))); err != nil {
		t.Fatalf("valid frame: %v", err)
	}
}
