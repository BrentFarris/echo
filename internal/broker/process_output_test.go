package broker

import "testing"

func TestStreamCallbacksRemainSeparatedBeyondCaptureLimit(t *testing.T) {
	var stdout, stderr string
	out := newProcessOutput(ProcessRequest{SeparateOutput: true, OutputLimit: 2, Stdout: func(b []byte) { stdout += string(b) }, Stderr: func(b []byte) { stderr += string(b) }}, nil)
	_, _ = (processStream{output: out}).Write([]byte("first"))
	_, _ = (processStream{output: out, stderr: true}).Write([]byte("diagnostic"))
	_, _ = (processStream{output: out}).Write([]byte("second"))
	if stdout != "firstsecond" || stderr != "diagnostic" || out.snapshot().Stdout != "fi" {
		t.Fatal(stdout, stderr, out.snapshot())
	}
}

func TestSeparatedProcessOutputPreservesBytes(t *testing.T) {
	out := newProcessOutput(ProcessRequest{SeparateOutput: true, OutputLimit: 8}, nil)
	_, _ = (processStream{output: out}).Write([]byte{'a', 0, 'b', '\r', '\n'})
	_, _ = (processStream{output: out, stderr: true}).Write([]byte("warning\n"))
	got := out.snapshot()
	if got.Stdout != "a\x00b\r\n" || got.Stderr != "warning\n" || got.Truncated {
		t.Fatalf("%+v", got)
	}
	_, _ = (processStream{output: out}).Write([]byte("123456"))
	got = out.snapshot()
	if len(got.Stdout) != 8 || !got.Truncated {
		t.Fatalf("%+v", got)
	}
}
