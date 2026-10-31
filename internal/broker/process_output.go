package broker

import (
	"io"
	"sync"
)

// processOutput drains both streams even after reaching the capture bound.
type processOutput struct {
	mu       sync.Mutex
	result   ProcessResult
	emit     func(string)
	limit    int
	separate bool
	stdout   io.Writer
}
type processStream struct {
	output *processOutput
	stderr bool
}

func newProcessOutput(r ProcessRequest, emit func(string)) *processOutput {
	limit := r.OutputLimit
	if limit <= 0 {
		limit = 1 << 20
	}
	if limit > 64<<20 {
		limit = 64 << 20
	}
	o := &processOutput{limit: limit, separate: r.SeparateOutput, emit: emit}
	if r.duplex != nil {
		o.stdout = r.duplex.output
		o.separate = true
	}
	return o
}
func (w processStream) Write(data []byte) (int, error) {
	o := w.output
	if !w.stderr && o.stdout != nil {
		return o.stdout.Write(data)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(data)
	emitted := ""
	appendBounded := func(target *string, limit int) {
		remaining := limit - len(*target)
		take := len(data)
		if take > remaining {
			take = remaining
			o.result.Truncated = true
		}
		if take > 0 {
			emitted = string(data[:take])
			*target += emitted
		}
	}
	if o.separate {
		if w.stderr {
			appendBounded(&o.result.Stderr, 1<<20)
		} else {
			appendBounded(&o.result.Stdout, o.limit)
		}
	} else {
		appendBounded(&o.result.Output, o.limit)
	}
	if o.emit != nil && emitted != "" {
		o.emit(emitted)
	}
	return n, nil
}
func (o *processOutput) snapshot() ProcessResult { o.mu.Lock(); defer o.mu.Unlock(); return o.result }
