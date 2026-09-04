package debugger

import (
	"testing"

	"github.com/brent/echo/internal/debugconfig"
)

func TestTranslateSourceNodesPreservesVirtualSourcesAndAddsStableRefs(t *testing.T) {
	value := map[string]any{
		"stackFrames": []any{
			map[string]any{"source": map[string]any{"name": "main.go", "path": "/workspace/main.go", "sourceReference": float64(0)}},
			map[string]any{"source": map[string]any{"name": "generated.go", "sourceReference": float64(12)}},
		},
	}
	translateSourceNodes(value, func(path string) (string, *debugconfig.SourceRef) {
		if path == "/workspace/main.go" {
			return `C:\repo\main.go`, &debugconfig.SourceRef{RootID: "root", Path: "main.go"}
		}
		return path, nil
	})
	frames := value["stackFrames"].([]any)
	firstSource := frames[0].(map[string]any)["source"].(map[string]any)
	if firstSource["path"] != `C:\repo\main.go` {
		t.Fatalf("translated source path = %#v", firstSource)
	}
	ref, ok := firstSource["echoRef"].(*debugconfig.SourceRef)
	if !ok || ref.RootID != "root" || ref.Path != "main.go" {
		t.Fatalf("stable source ref = %#v", firstSource["echoRef"])
	}
	secondSource := frames[1].(map[string]any)["source"].(map[string]any)
	if secondSource["sourceReference"] != float64(12) || secondSource["echoRef"] != nil {
		t.Fatalf("virtual source changed = %#v", secondSource)
	}
}
