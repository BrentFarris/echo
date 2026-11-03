package languageservice

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestFramingBoundsAndFragmentation(t *testing.T) {
	for _, header := range []string{"\r\n", "Content-Length: -1\r\n\r\n", "Content-Length: 16777217\r\n\r\n", "Content-Length: 2\r\nContent-Length: 2\r\n\r\n{}", strings.Repeat("x", 9000) + "\r\n\r\n"} {
		if _, err := ReadMessage(bufio.NewReader(strings.NewReader(header))); err == nil {
			t.Fatal("accepted invalid frame")
		}
	}
	r, w := io.Pipe()
	defer r.Close()
	data := `{"jsonrpc":"2.0","method":"hello","params":{"text":"日本語"}}`
	go func() {
		defer w.Close()
		for _, part := range []string{fmt.Sprintf("Content-Length: %d\r", len(data)), "\n\r\n", data[:5], data[5:]} {
			io.WriteString(w, part)
		}
	}()
	m, err := ReadMessage(bufio.NewReader(r))
	if err != nil || m.Method != "hello" {
		t.Fatal(m, err)
	}
}

type pipePair struct{ a, b io.Closer }

func (p pipePair) Close() error { p.a.Close(); p.b.Close(); return nil }
func TestClientResponsesCancellationAndServerRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverInput, input := io.Pipe()
	output, serverOutput := io.Pipe()
	c := New(ctx, input, output, pipePair{input, output})
	defer c.Close()
	defer serverInput.Close()
	defer serverOutput.Close()
	read := bufio.NewReader(serverInput)
	write := func(message Message) {
		data, _ := json.Marshal(message)
		fmt.Fprintf(serverOutput, "Content-Length: %d\r\n\r\n%s", len(data), data)
	}
	result := make(chan error, 1)
	go func() {
		raw, err := c.Call(ctx, "test", map[string]any{})
		if err == nil && string(raw) != `{"ok":true}` {
			err = fmt.Errorf("bad reply %s", raw)
		}
		result <- err
	}()
	m, err := ReadMessage(read)
	if err != nil {
		t.Fatal(err)
	}
	write(Message{ID: m.ID, Result: json.RawMessage(`{"ok":true}`)})
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	requestCtx, stop := context.WithCancel(ctx)
	go func() { _, e := c.Call(requestCtx, "slow", nil); result <- e }()
	if _, err = ReadMessage(read); err != nil {
		t.Fatal(err)
	}
	stop()
	if err = <-result; err != context.Canceled {
		t.Fatal(err)
	}
	m, err = ReadMessage(read)
	if err != nil || m.Method != "$/cancelRequest" {
		t.Fatal(m, err)
	}
	write(Message{ID: json.RawMessage(`"server-1"`), Method: "workspace/applyEdit", Params: json.RawMessage(`{}`)})
	m, err = ReadMessage(read)
	if err != nil || !strings.Contains(string(m.Result), `"applied":false`) {
		t.Fatal(m, err)
	}
}
