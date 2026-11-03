//go:build wasip1

// Build: GOOS=wasip1 GOARCH=wasm go build -o builtin.wasm .
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"unsafe"
)

//go:wasmimport echo call
func hostCall(ptr, size, out, capacity uint32) uint32
func main() {
	// Inline workflows are JSON strings nested inside the invocation JSON;
	// escaping can expand a 500 KiB workflow beyond the former 1 MiB limit.
	input, e := io.ReadAll(io.LimitReader(os.Stdin, (4<<20)+1))
	if e != nil || len(input) > 4<<20 || !json.Valid(input) {
		fmt.Print(`{"error":"invalid invocation"}`)
		return
	}
	// Convenience tools share their plugin's broker capability and supply only
	// their own fixed subcommand. The host still validates the execution scope.
	var request struct {
		Tool   string         `json:"tool"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if json.Unmarshal(input, &request) == nil {
		for _, id := range []string{"git", "fossil", "p4"} {
			for _, command := range []string{"status", "diff"} {
				if request.Tool == id+"_"+command && request.Method == id+".run" {
					request.Params["command"] = command
					input, _ = json.Marshal(request)
				}
			}
		}
	}
	ptr := uint32(uintptr(unsafe.Pointer(&input[0])))
	n := hostCall(ptr, uint32(len(input)), 0, 0)
	if n == 0 || n > 2<<20 {
		fmt.Print(`{"error":"host response rejected"}`)
		return
	}
	out := make([]byte, n)
	written := hostCall(0, 0, uint32(uintptr(unsafe.Pointer(&out[0]))), n)
	os.Stdout.Write(out[:written])
}
