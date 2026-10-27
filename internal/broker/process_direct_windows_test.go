//go:build windows

package broker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestNativeDirectArgumentsInputAndNetworkContainment(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("native execution")
	}
	root := t.TempDir()
	source := filepath.Join(root, "probe.go")
	binary := filepath.Join(root, "probe.exe")
	os.WriteFile(source, []byte(`package main
import("encoding/json";"io";"os";"unsafe";"golang.org/x/sys/windows")
func main(){input,_:=io.ReadAll(os.Stdin);_,err:=os.ReadFile(os.Getenv("ECHO2_DENIED"));var size uint32;token:=windows.GetCurrentProcessToken();windows.GetTokenInformation(token,30,nil,0,&size);buffer:=make([]byte,size);caps:=[]string{};if size>0 && windows.GetTokenInformation(token,30,&buffer[0],size,&size)==nil{for _,group:=range (*windows.Tokengroups)(unsafe.Pointer(&buffer[0])).AllGroups(){caps=append(caps,group.Sid.String())}};json.NewEncoder(os.Stdout).Encode(map[string]any{"args":os.Args[1:],"input":string(input),"denied":err!=nil,"capabilities":caps})}`), 0600)
	command := exec.Command("go", "build", "-o", binary, source)
	if output, e := command.CombinedOutput(); e != nil {
		t.Fatal(string(output), e)
	}
	private := filepath.Join(t.TempDir(), "private.txt")
	os.WriteFile(private, []byte("must not read"), 0600)
	b := &ProcessBroker{Dir: filepath.Join(t.TempDir(), "runtime")}
	args := []string{"", "two words", `"quotes"`, `C:\trailing\`, "; $(Write-Output bad) & |", "日本語"}
	input := "Form:\n\tvalue\n"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, e := b.Run(ctx, ProcessRequest{BotID: "direct", Roots: []string{root}, Executable: binary, Args: args, Input: input, Internet: true, PrivateNetwork: true, Environment: map[string]string{"ECHO2_DENIED": private}}, nil)
	var value struct {
		Args         []string `json:"args"`
		Input        string   `json:"input"`
		Denied       bool     `json:"denied"`
		Capabilities []string `json:"capabilities"`
	}
	decodeErr := json.Unmarshal([]byte(result.Output), &value)
	if e != nil || result.ExitCode != 0 || decodeErr != nil || !reflect.DeepEqual(args, value.Args) || value.Input != input || !value.Denied {
		t.Fatal(result, e, decodeErr, value)
	}
	// TokenCapabilities is separate from TokenGroups (and whoami /groups).
	if !reflect.DeepEqual(value.Capabilities, []string{"S-1-15-3-1", "S-1-15-3-3"}) {
		t.Fatal("network capabilities missing", e, result.Output)
	}
}
