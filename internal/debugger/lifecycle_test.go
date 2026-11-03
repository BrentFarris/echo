package debugger

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/store"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type fixtureProvider struct {
	starts, cleanups atomic.Int32
	disconnects      chan bool
	gate             chan struct{}
}

func (*fixtureProvider) Metadata() debugconfig.AdapterProfile {
	return debugconfig.AdapterProfile{ID: "fixture", AdapterID: "fixture"}
}
func (*fixtureProvider) Configure(c debugconfig.Configuration) (debugconfig.Configuration, []debugconfig.Input) {
	return c, nil
}
func (p *fixtureProvider) Prepare(_ context.Context, _ LaunchContext, c debugconfig.Configuration) (*Launch, error) {
	if c.Arguments["bad"] == true {
		return nil, errors.New("invalid member")
	}
	return &Launch{Arguments: c.Arguments, Cleanup: func() { p.cleanups.Add(1) }, Start: func(ctx context.Context, _ func(string, string)) (*Adapter, error) {
		p.starts.Add(1)
		if c.Arguments["fail"] == true {
			return nil, errors.New("startup failed")
		}
		if p.gate != nil {
			select {
			case <-p.gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		client, adapter := net.Pipe()
		go func() {
			defer adapter.Close()
			reader := bufio.NewReader(adapter)
			for {
				raw, err := readDAPMessage(reader)
				if err != nil {
					return
				}
				var request dapEnvelope
				if json.Unmarshal(raw, &request) != nil {
					return
				}
				body := map[string]any{}
				if request.Command == "initialize" {
					body["supportsConfigurationDoneRequest"] = true
				}
				if request.Command == "disconnect" {
					var args map[string]any
					_ = json.Unmarshal(request.Arguments, &args)
					p.disconnects <- args["terminateDebuggee"] == true
				}
				if err := writeDAPFragments(adapter, map[string]any{"seq": request.Seq + 100, "type": "response", "success": true, "request_seq": request.Seq, "command": request.Command, "body": body}, 4, 2); err != nil {
					return
				}
				if request.Command == "launch" || request.Command == "attach" {
					_ = writeDAPFragments(adapter, map[string]any{"seq": 900, "type": "event", "event": "initialized"}, 3)
				}
			}
		}()
		return &Adapter{Transport: client, Stop: func() { client.Close(); adapter.Close() }}, nil
	}}, nil
}

func newFixtureService(t *testing.T, config *debugconfig.WorkspaceConfig) (*Service, *fixtureProvider, context.CancelFunc) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	p := &fixtureProvider{disconnects: make(chan bool, 32)}
	lease, revoke := context.WithCancel(context.Background())
	fs := editorfs.New(func(string) (domain.Bot, error) { return domain.Bot{ID: "bot", Roots: []string{root}}, nil }, "")
	s := New(Options{FS: fs, State: debugconfig.NewStateStore(db), Configurations: func(string) (debugconfig.WorkspaceConfig, error) { return *config, nil }, Provider: func(string, string) (Provider, error) { return p, nil }, Lease: func(parent context.Context, _, _ string) (context.Context, func(), error) {
		ctx, cancel := context.WithCancel(parent)
		stop := context.AfterFunc(lease, cancel)
		return ctx, func() { stop(); cancel() }, nil
	}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, p, revoke
}
func fixtureConfig(id, request string, args map[string]any) debugconfig.Configuration {
	return debugconfig.Configuration{ID: id, Name: id, AdapterProfileID: "fixture", Request: request, Arguments: args}
}

func TestCompoundValidationAndStartupRollback(t *testing.T) {
	config := debugconfig.WorkspaceConfig{Configurations: []debugconfig.Configuration{fixtureConfig("a", "launch", nil), fixtureConfig("b", "launch", map[string]any{"bad": true})}, Compounds: []debugconfig.Compound{{ID: "both", Name: "Both", ConfigurationIDs: []string{"a", "b"}}}}
	s, p, _ := newFixtureService(t, &config)
	if _, err := s.Start(context.Background(), "bot", StartRequest{CompoundID: "both"}); err == nil {
		t.Fatal("invalid compound started")
	}
	if p.starts.Load() != 0 || p.cleanups.Load() != 1 {
		t.Fatal("prepared member leaked", p.starts.Load(), p.cleanups.Load())
	}
	config.Configurations[1].Arguments = map[string]any{"fail": true}
	if _, err := s.Start(context.Background(), "bot", StartRequest{CompoundID: "both"}); err == nil {
		t.Fatal("startup failure ignored")
	}
	snapshot, _ := s.Snapshot("bot")
	for _, session := range snapshot.Sessions {
		if !isTerminalStatus(session.Status) {
			t.Fatal("rollback left member alive", session)
		}
	}
}

func TestReloadRevocationAndDetach(t *testing.T) {
	for _, request := range []string{"attach", "launch"} {
		t.Run(request, func(t *testing.T) {
			config := debugconfig.WorkspaceConfig{Configurations: []debugconfig.Configuration{fixtureConfig("one", request, nil)}}
			s, p, revoke := newFixtureService(t, &config)
			snapshot, err := s.Start(context.Background(), "bot", StartRequest{ConfigurationID: "one"})
			if err != nil {
				t.Fatal(err)
			}
			reloaded, _ := s.Snapshot("bot")
			if reloaded.Sessions[0].ID != snapshot.Sessions[0].ID || reloaded.Sessions[0].Status != StatusRunning {
				t.Fatal("reload lost session")
			}
			revoke()
			select {
			case terminate := <-p.disconnects:
				if terminate != (request == "launch") {
					t.Fatal("incorrect disconnect semantics")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("revocation did not disconnect")
			}
		})
	}
}

func TestHTTPDisconnectDoesNotCancelStartup(t *testing.T) {
	config := debugconfig.WorkspaceConfig{Configurations: []debugconfig.Configuration{fixtureConfig("one", "launch", nil)}}
	s, p, _ := newFixtureService(t, &config)
	p.gate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Start(ctx, "bot", StartRequest{ConfigurationID: "one"}); done <- err }()
	deadline := time.After(2 * time.Second)
	for p.starts.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("not started")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(p.gate)
	deadline = time.After(2 * time.Second)
	for {
		snapshot, _ := s.Snapshot("bot")
		if snapshot.Sessions[0].Status == StatusRunning {
			break
		}
		select {
		case <-deadline:
			t.Fatal(snapshot)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestSourceIdentityAndPrivateIsolation(t *testing.T) {
	s, current, adapter, cleanup := stoppedPipeSession(t, 1, 1)
	defer cleanup()
	_ = adapter
	path := filepath.Join(t.TempDir(), "external.c")
	if err := os.WriteFile(path, []byte("int value = 7;"), 0600); err != nil {
		t.Fatal(err)
	}
	s.options.AllowedSource = func(name string) bool { return name == path }
	key := s.registerSource(current, Source{Path: path})
	response, err := s.sourceRequest(context.Background(), current, ControlRequest{Arguments: map[string]any{"source": map[string]any{"echoSourceId": key, "path": "forged"}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Body) != `{"content":"int value = 7;"}` {
		t.Fatal(string(response.Body))
	}
	if _, err = s.sourceRequest(context.Background(), current, ControlRequest{Arguments: map[string]any{"source": map[string]any{"path": path}}}); err == nil {
		t.Fatal("arbitrary source accepted")
	}
	other := &session{id: "other"}
	otherKey := s.registerSource(other, Source{Path: path})
	if _, err = s.sourceRequest(context.Background(), current, ControlRequest{Arguments: map[string]any{"source": map[string]any{"echoSourceId": otherKey}}}); err == nil {
		t.Fatal("cross-session identity accepted")
	}
	private := s.registerSource(current, Source{Path: filepath.Join(t.TempDir(), "echo2.sqlite")})
	if _, err = s.sourceRequest(context.Background(), current, ControlRequest{Arguments: map[string]any{"source": map[string]any{"echoSourceId": private}}}); err == nil {
		t.Fatal("private source accepted")
	}
}

func TestDAPClosedCallbackMayCloseConnection(t *testing.T) {
	client, adapter := net.Pipe()
	done := make(chan struct{})
	var connection *dapConnection
	connection = newDAPConnection(client, nil, nil, func(error) { connection.Close(); close(done) })
	adapter.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reentrant close deadlocked")
	}
}
func TestDAPBlockedWriteCancels(t *testing.T) {
	client, adapter := net.Pipe()
	defer adapter.Close()
	connection := newDAPConnection(client, nil, nil, nil)
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := connection.request(ctx, "blocked", nil); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked write ignored cancellation")
	}
}

func TestRestartReadsChangedConfiguration(t *testing.T) {
	config := debugconfig.WorkspaceConfig{Configurations: []debugconfig.Configuration{fixtureConfig("one", "launch", nil)}}
	s, p, _ := newFixtureService(t, &config)
	snapshot, err := s.Start(context.Background(), "bot", StartRequest{ConfigurationID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	config.Configurations[0].Arguments = map[string]any{"bad": true}
	_, err = s.RestartChecked(context.Background(), "bot", snapshot.Sessions[0].ID, snapshot.Sessions[0].Revision)
	if err == nil || p.starts.Load() != 1 {
		t.Fatal(fmt.Sprint(err), p.starts.Load())
	}
}
