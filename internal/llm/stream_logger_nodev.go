//go:build !dev

package llm

import "github.com/brent/echo/internal/flowlog"

type streamLogger struct {
	runtime *runtimeStreamLogger
}

func newStreamLogger(_ string, trace *flowlog.RequestTrace) *streamLogger {
	runtime := newRuntimeStreamLogger(trace)
	if runtime == nil {
		return nil
	}
	return &streamLogger{runtime: runtime}
}

func (l *streamLogger) raw(data string) {
	if l != nil {
		l.runtime.raw(data)
	}
}

func (l *streamLogger) event(event StreamEvent) {
	if l != nil {
		l.runtime.event(event)
	}
}

func (l *streamLogger) finish() {
	if l != nil {
		l.runtime.finishResponse()
	}
}
