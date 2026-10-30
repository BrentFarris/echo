package broker

import (
	"io"
	"os"
	"path/filepath"
)

// A native client can request byte-exact file output (P4 print -o performs
// charset/BOM translation). The file stays inside the invocation's existing
// scratch grant, never in a host temp directory outside its sandbox.
func captureArguments(r ProcessRequest, scratch string) []string {
	args := append([]string(nil), r.Args...)
	if r.OutputFileArgument != "" {
		for i, arg := range args {
			if arg == r.OutputFileArgument {
				args[i] = filepath.Join(scratch, "captured-output")
			}
		}
	}
	return args
}
func captureFileResult(r ProcessRequest, scratch string, result *ProcessResult) error {
	if r.OutputFileArgument == "" {
		return nil
	}
	root, err := os.OpenRoot(scratch)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.Open("captured-output")
	if err != nil {
		return err
	}
	defer file.Close()
	limit := r.OutputLimit
	if limit <= 0 {
		limit = 1 << 20
	}
	if limit > 64<<20 {
		limit = 64 << 20
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return err
	}
	if len(data) > limit {
		data = data[:limit]
		result.Truncated = true
	}
	result.Stdout = string(data)
	return nil
}
