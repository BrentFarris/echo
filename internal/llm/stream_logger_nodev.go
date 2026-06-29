//go:build !dev

package llm

type streamLogger struct{}

func newStreamLogger(string, string) *streamLogger {
	return nil
}

func (*streamLogger) raw(string) {}

func (*streamLogger) event(StreamEvent) {}
