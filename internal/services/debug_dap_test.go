package services

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/go-dap"
)

func TestDAPConnectionCorrelatesOutOfOrderResponses(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	connection := newDAPConnection(client, nil)
	defer connection.Close()

	serverDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(server)
		requests := make([]dapEnvelope, 0, 2)
		for len(requests) < 2 {
			data, err := dap.ReadBaseMessage(reader)
			if err != nil {
				serverDone <- err
				return
			}
			var request dapEnvelope
			if err := json.Unmarshal(data, &request); err != nil {
				serverDone <- err
				return
			}
			requests = append(requests, request)
		}
		for i := len(requests) - 1; i >= 0; i-- {
			body, _ := json.Marshal(map[string]string{"command": requests[i].Command})
			response, _ := json.Marshal(dapEnvelope{
				Seq:        100 + i,
				Type:       "response",
				RequestSeq: requests[i].Seq,
				Success:    true,
				Command:    requests[i].Command,
				Body:       body,
			})
			if err := dap.WriteBaseMessage(server, response); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	type result struct {
		command string
		err     error
	}
	results := make(chan result, 2)
	for _, command := range []string{"threads", "stackTrace"} {
		command := command
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			response, err := connection.request(ctx, command, map[string]any{})
			if err == nil {
				var body map[string]string
				err = json.Unmarshal(response.Body, &body)
				if err == nil && body["command"] != command {
					err = context.Canceled
				}
			}
			results <- result{command: command, err: err}
		}()
	}
	for range 2 {
		if result := <-results; result.err != nil {
			t.Fatalf("%s request failed: %v", result.command, result.err)
		}
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("fake server: %v", err)
	}
}

func TestDAPConnectionPropagatesAdapterFailure(t *testing.T) {
	client, server := net.Pipe()
	connection := newDAPConnection(client, nil)
	defer connection.Close()
	go func() {
		data, _ := dap.ReadBaseMessage(bufio.NewReader(server))
		var request dapEnvelope
		_ = json.Unmarshal(data, &request)
		response, _ := json.Marshal(dapEnvelope{
			Seq: 2, Type: "response", RequestSeq: request.Seq,
			Success: false, Command: request.Command, Message: "not available",
		})
		_ = dap.WriteBaseMessage(server, response)
		_ = server.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := connection.request(ctx, "variables", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("expected adapter error, got %v", err)
	}
}

func TestDAPConnectionRejectsReverseRequestWithSequencedResponse(t *testing.T) {
	client, server := net.Pipe()
	connection := newDAPConnection(client, nil)
	defer connection.Close()
	request, _ := json.Marshal(dapEnvelope{Seq: 42, Type: "request", Command: "runInTerminal"})
	if err := dap.WriteBaseMessage(server, request); err != nil {
		t.Fatal(err)
	}
	data, err := dap.ReadBaseMessage(bufio.NewReader(server))
	if err != nil {
		t.Fatal(err)
	}
	var response dapEnvelope
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if response.Seq <= 0 || response.RequestSeq != 42 || response.Success {
		t.Fatalf("unexpected reverse response: %#v", response)
	}
}
