//go:build windows

package broker

import (
	"io"
	"os"
	"testing"
	"time"
)

func TestDrainProcessOutput(t *testing.T) {
	for _, keepOpen := range []bool{false, true} {
		name := "closed"
		if keepOpen {
			name = "inherited writer remains open"
		}
		t.Run(name, func(t *testing.T) {
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			defer write.Close()
			var output []byte
			done := make(chan struct{})
			go func() {
				defer close(done)
				output, _ = io.ReadAll(read)
			}()
			if _, err := write.WriteString("partial command output"); err != nil {
				t.Fatal(err)
			}
			if !keepOpen {
				write.Close()
			}
			start := time.Now()
			err = drainProcessOutput(read, done, 100*time.Millisecond)
			if (err != nil) != keepOpen {
				t.Fatalf("keepOpen=%v: %v", keepOpen, err)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("output drain exceeded its grace period")
			}
			if string(output) != "partial command output" {
				t.Fatalf("lost output: %q", output)
			}
		})
	}
}
