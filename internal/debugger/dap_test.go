package debugger

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestDAPConnectionCorrelatesFragmentedOutOfOrderResponses(t *testing.T) {
	client, adapter := net.Pipe()
	events := make(chan dapEnvelope, 1)
	connection := newDAPConnection(client, func(event dapEnvelope) { events <- event }, nil, nil)
	t.Cleanup(func() { _ = connection.Close(); _ = adapter.Close() })

	adapterErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(adapter)
		requests := make([]dapEnvelope, 0, 2)
		for len(requests) < 2 {
			data, err := readDAPMessage(reader)
			if err != nil {
				adapterErr <- err
				return
			}
			var request dapEnvelope
			if err := json.Unmarshal(data, &request); err != nil {
				adapterErr <- err
				return
			}
			requests = append(requests, request)
		}
		if err := writeDAPFragments(adapter, dapEnvelope{Seq: 50, Type: "event", Event: "initialized", Body: json.RawMessage(`{"early":true}`)}, 1, 2, 5); err != nil {
			adapterErr <- err
			return
		}
		for index := len(requests) - 1; index >= 0; index-- {
			request := requests[index]
			body, _ := json.Marshal(map[string]any{"command": request.Command})
			if err := writeDAPFragments(adapter, dapEnvelope{Seq: 51 + index, Type: "response", RequestSeq: request.Seq, Success: true, Command: request.Command, Body: body}, 3, 1, 7); err != nil {
				adapterErr <- err
				return
			}
		}
		adapterErr <- nil
	}()

	type result struct {
		command string
		body    map[string]any
		err     error
	}
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(2)
	for _, command := range []string{"threads", "modules"} {
		command := command
		go func() {
			start.Done()
			start.Wait()
			response, err := connection.request(context.Background(), command, map[string]any{})
			var body map[string]any
			_ = json.Unmarshal(response.Body, &body)
			results <- result{command: command, body: body, err: err}
		}()
	}
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatalf("%s request failed: %v", got.command, got.err)
		}
		if got.body["command"] != got.command {
			t.Fatalf("%s received body %#v", got.command, got.body)
		}
	}
	select {
	case event := <-events:
		if event.Event != "initialized" {
			t.Fatalf("event = %q", event.Event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for early event")
	}
	if err := <-adapterErr; err != nil {
		t.Fatalf("fake adapter: %v", err)
	}
}

func TestDAPConnectionRejectsUnknownReverseRequestWithoutClosing(t *testing.T) {
	client, adapter := net.Pipe()
	connection := newDAPConnection(client, nil, func(_ context.Context, request dapEnvelope) (any, error) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, request.Command)
	}, nil)
	t.Cleanup(func() { _ = connection.Close(); _ = adapter.Close() })

	if err := writeDAPFragments(adapter, dapEnvelope{Seq: 9, Type: "request", Command: "proprietaryThing"}, 2, 4); err != nil {
		t.Fatal(err)
	}
	data, err := readDAPMessage(bufio.NewReader(adapter))
	if err != nil {
		t.Fatal(err)
	}
	var response dapEnvelope
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "response" || response.RequestSeq != 9 || response.Success || response.Message == "" {
		t.Fatalf("unexpected reverse response: %#v", response)
	}

	adapterDone := make(chan error, 1)
	go func() {
		requestData, readErr := readDAPMessage(bufio.NewReader(adapter))
		if readErr != nil {
			adapterDone <- readErr
			return
		}
		var request dapEnvelope
		_ = json.Unmarshal(requestData, &request)
		adapterDone <- writeDAPFragments(adapter, dapEnvelope{Seq: 11, Type: "response", RequestSeq: request.Seq, Success: true, Command: request.Command}, 6)
	}()
	if _, err := connection.request(context.Background(), "threads", nil); err != nil {
		t.Fatalf("connection did not survive unsupported reverse request: %v", err)
	}
	if err := <-adapterDone; err != nil {
		t.Fatal(err)
	}
}

func TestDAPConnectionCancellationRemovesPendingRequest(t *testing.T) {
	client, adapter := net.Pipe()
	connection := newDAPConnection(client, nil, nil, nil)
	t.Cleanup(func() { _ = connection.Close(); _ = adapter.Close() })
	read := make(chan struct{})
	go func() {
		_, _ = readDAPMessage(bufio.NewReader(adapter))
		close(read)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := connection.request(ctx, "variables", map[string]any{"variablesReference": 1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request error = %v", err)
	}
	<-read
	connection.mu.Lock()
	pending := len(connection.pending)
	connection.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending request count = %d", pending)
	}
}

func TestDAPConnectionSendsCancelForTimedOutRequestWhenSupported(t *testing.T) {
	client, adapter := net.Pipe()
	connection := newDAPConnection(client, nil, nil, nil)
	connection.setSupportsCancel(true)
	defer connection.Close()
	defer adapter.Close()

	requests := make(chan dapEnvelope, 2)
	go func() {
		reader := bufio.NewReader(adapter)
		for index := 0; index < 2; index++ {
			payload, err := readDAPMessage(reader)
			if err != nil {
				return
			}
			var request dapEnvelope
			if json.Unmarshal(payload, &request) == nil {
				requests <- request
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := connection.request(ctx, "variables", map[string]any{"variablesReference": 1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request error = %v, want deadline exceeded", err)
	}
	first := <-requests
	second := <-requests
	if first.Command != "variables" || second.Command != "cancel" {
		t.Fatalf("commands = %q, %q; want variables, cancel", first.Command, second.Command)
	}
	var arguments map[string]int
	if err := json.Unmarshal(second.Arguments, &arguments); err != nil || arguments["requestId"] != first.Seq {
		t.Fatalf("cancel arguments = %s, %v", second.Arguments, err)
	}
}

func TestDAPFailureMessageUsesStructuredAdapterDetail(t *testing.T) {
	message := dapEnvelope{
		Message: "Failed to launch",
		Body: json.RawMessage(`{"error":{"id":3000,"format":"Failed to launch: invalid configuration {reason}","variables":{"reason":"unsupported mode"},"showUser":true}}`),
	}
	if got := dapFailureMessage(message); got != "Failed to launch: invalid configuration unsupported mode" {
		t.Fatalf("failure message = %q", got)
	}
}

func TestReadDAPMessageRejectsMalformedAndOversizedFrames(t *testing.T) {
	for name, input := range map[string]string{
		"invalid length": "Content-Length: nope\r\n\r\n",
		"missing length": "Content-Type: application/json\r\n\r\n{}",
		"oversized":      fmt.Sprintf("Content-Length: %d\r\n\r\n", maxDAPMessageBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readDAPMessage(bufio.NewReader(bytes.NewBufferString(input))); err == nil {
				t.Fatal("expected malformed frame to fail")
			}
		})
	}
}

func writeDAPFragments(writer io.Writer, message any, sizes ...int) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	framed := append([]byte(fmt.Sprintf("Content-Length: %d\r\nX-Echo-Test: true\r\n\r\n", len(data))), data...)
	for _, size := range sizes {
		if len(framed) == 0 {
			break
		}
		if size > len(framed) {
			size = len(framed)
		}
		if _, err := writer.Write(framed[:size]); err != nil {
			return err
		}
		framed = framed[size:]
	}
	if len(framed) > 0 {
		_, err = writer.Write(framed)
	}
	return err
}
