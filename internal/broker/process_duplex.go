package broker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type processDuplex struct {
	input, output *os.File
	started       chan struct{}
	aliases       map[string]string
}

// ProcessConnection owns a sandboxed process and its complete process tree.
// Wait completes only after native permission and scratch cleanup has finished.
type ProcessConnection struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Done   <-chan struct{}
	cancel context.CancelFunc
	result ProcessResult
	err    error
	once   sync.Once
	duplex *processDuplex
}

// ProcessPath and HostPath translate workspace/runtime aliases after Start.
func (p *ProcessConnection) ProcessPath(path string) string {
	return aliasPath(path, p.duplex.aliases, false)
}
func (p *ProcessConnection) HostPath(path string) string {
	return aliasPath(path, p.duplex.aliases, true)
}
func aliasPath(path string, aliases map[string]string, reverse bool) string {
	keys := make([]string, 0, len(aliases))
	for key := range aliases {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, key := range keys {
		from, to := key, aliases[key]
		if reverse {
			from, to = to, from
		}
		if Within(from, path) {
			rel, _ := filepath.Rel(from, path)
			return filepath.Join(to, rel)
		}
	}
	return path
}

func (p *ProcessConnection) Close() error {
	p.once.Do(func() { p.cancel(); p.Stdin.Close(); p.Stdout.Close() })
	return nil
}
func (p *ProcessConnection) Wait() (ProcessResult, error) { <-p.Done; return p.result, p.err }

// Start runs only structured host-owned executables. The caller must drain
// stdout; pipe backpressure bounds memory. Cancellation unblocks all pipe I/O.
func (b *ProcessBroker) Start(ctx context.Context, r ProcessRequest, stderr func(string)) (*ProcessConnection, error) {
	if r.Executable == "" || r.Command != "" || r.Input != "" || r.OutputFileArgument != "" || r.SandboxSession != "" {
		return nil, errors.New("persistent processes require a native executable and duplex stdio")
	}
	input, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	reader, output, err := os.Pipe()
	if err != nil {
		input.Close()
		writer.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	p := &ProcessConnection{Stdin: writer, Stdout: reader, Done: done, cancel: cancel}
	r.duplex = &processDuplex{input: input, output: output, started: make(chan struct{})}
	p.duplex = r.duplex
	stop := context.AfterFunc(ctx, func() { input.Close(); output.Close(); writer.Close() })
	go func() {
		defer close(done)
		defer cancel()
		defer stop()
		defer input.Close()
		defer output.Close()
		p.result, p.err = b.Run(ctx, r, stderr)
	}()
	select {
	case <-r.duplex.started:
		return p, nil
	case <-done:
		p.Close()
		if p.err != nil {
			return nil, p.err
		}
		return nil, errors.New("persistent process exited before startup")
	case <-ctx.Done():
		p.Close()
		<-done
		return nil, ctx.Err()
	}
}
