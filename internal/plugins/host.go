package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

type Broker func(context.Context, domain.ExecutionScope, string, json.RawMessage) (any, error)
type Host struct {
	Store    *store.Store
	Dir      string
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	broker   Broker
	mu       sync.Mutex
	active   map[string]map[string]context.CancelFunc
}
type invocation struct {
	scope    domain.ExecutionScope
	manifest domain.PluginManifest
	broker   Broker
	response []byte
}
type invocationKey struct{}

var validID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func New(ctx context.Context, s *store.Store, dir string, guest []byte, broker Broker) (*Host, error) {
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(1024).WithCloseOnContextDone(true))
	h := &Host{Store: s, Dir: dir, runtime: r, broker: broker, active: map[string]map[string]context.CancelFunc{}}
	_, e := wasi_snapshot_preview1.Instantiate(ctx, r)
	if e != nil {
		r.Close(ctx)
		return nil, e
	}
	_, e = r.NewHostModuleBuilder("echo").NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, ptr, n, out, capacity uint32) uint32 {
		v, ok := ctx.Value(invocationKey{}).(*invocation)
		if !ok {
			return 0
		}
		if capacity == 0 {
			b, ok := m.Memory().Read(ptr, n)
			if !ok || n > 4<<20 {
				return 0
			}
			var req struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(b, &req) != nil {
				return 0
			}
			var result any
			var err error
			if !slices.Contains(v.manifest.Capabilities, req.Method) {
				err = fmt.Errorf("undeclared capability: %s", req.Method)
			} else {
				result, err = v.broker(ctx, v.scope, req.Method, req.Params)
			}
			response := map[string]any{"result": result}
			if err != nil {
				response = map[string]any{"error": err.Error()}
			}
			v.response, _ = json.Marshal(response)
			if len(v.response) > 2<<20 {
				v.response = []byte(`{"error":"plugin response exceeds limit"}`)
			}
			return uint32(len(v.response))
		}
		if uint32(len(v.response)) > capacity || !m.Memory().Write(out, v.response) {
			return 0
		}
		return uint32(len(v.response))
	}).Export("call").Instantiate(ctx)
	if e != nil {
		r.Close(ctx)
		return nil, e
	}
	h.compiled, e = r.CompileModule(ctx, guest)
	if e != nil {
		r.Close(ctx)
		return nil, e
	}
	for _, m := range Builtins() {
		var current domain.PluginManifest
		if s.Get("plugins", m.ID, &current) == nil {
			m.Enabled = current.Enabled
			m.Config = current.Config
		}
		{
			if e = s.Put("plugins", m.ID, m); e != nil {
				r.Close(ctx)
				return nil, e
			}
		}
	}
	return h, nil
}
func (h *Host) Close(ctx context.Context) error { return h.runtime.Close(ctx) }
func (h *Host) Save(m domain.PluginManifest) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, cancel := range h.active[m.ID] {
		cancel()
	}
	return h.Store.Put("plugins", m.ID, m)
}
func (h *Host) List() ([]domain.PluginManifest, error) {
	raw, e := h.Store.List("plugins")
	if e != nil {
		return nil, e
	}
	return store.Decode[domain.PluginManifest](raw)
}
func (h *Host) Get(id string) (domain.PluginManifest, error) {
	var m domain.PluginManifest
	e := h.Store.Get("plugins", id, &m)
	return m, e
}
func (h *Host) Enabled(bot domain.Bot, id string) bool {
	m, e := h.Get(id)
	if e != nil || !m.Enabled || m.Pending {
		return false
	}
	if m.Scope == "echo" && bot.ID != "echo" {
		return false
	}
	if m.Scope == "app" {
		return true
	}
	v, ok := bot.Plugins[id]
	return !ok || v
}
func (h *Host) Tools(bot domain.Bot) []domain.Tool {
	ms, _ := h.List()
	out := []domain.Tool{}
	for _, m := range ms {
		if h.Enabled(bot, m.ID) {
			for _, t := range m.Tools {
				if !slices.Contains(m.Providers, t.Name) {
					out = append(out, t)
				}
			}
		}
	}
	return out
}
func (h *Host) Provider(bot domain.Bot, name string) (domain.PluginManifest, error) {
	ms, e := h.List()
	if e != nil {
		return domain.PluginManifest{}, e
	}
	for _, m := range ms {
		if slices.Contains(m.Providers, name) {
			if !h.Enabled(bot, m.ID) {
				return m, errors.New("model provider is disabled for @" + bot.Handle)
			}
			return m, nil
		}
	}
	return domain.PluginManifest{}, fmt.Errorf("unknown provider %q", name)
}
func (h *Host) Invoke(ctx context.Context, scope domain.ExecutionScope, name string, args json.RawMessage) (any, error) {
	ms, e := h.List()
	if e != nil {
		return nil, e
	}
	for _, m := range ms {
		for _, t := range m.Tools {
			if t.Name != name {
				continue
			}
			if !h.Enabled(scope.Bot, m.ID) {
				return nil, errors.New("plugin is disabled for this bot")
			}
			if e = Validate(t.Parameters, args); e != nil {
				return nil, e
			}
			scope.PluginID = m.ID
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			h.mu.Lock()
			current, lookupErr := h.Get(m.ID)
			if lookupErr != nil || !h.Enabled(scope.Bot, m.ID) || current.Digest != m.Digest {
				h.mu.Unlock()
				return nil, errors.New("plugin changed before execution; retry with the current version")
			}
			invocationID := domain.ID()
			if h.active[m.ID] == nil {
				h.active[m.ID] = map[string]context.CancelFunc{}
			}
			h.active[m.ID][invocationID] = cancel
			h.mu.Unlock()
			defer func() { h.mu.Lock(); delete(h.active[m.ID], invocationID); h.mu.Unlock() }()
			v := &invocation{scope: scope, manifest: m, broker: h.broker}
			ctx = context.WithValue(ctx, invocationKey{}, v)
			input, _ := json.Marshal(map[string]any{"method": t.Capability, "params": json.RawMessage(args), "tool": name})
			var output limitedWriter
			compiled := h.compiled
			if !m.Builtin {
				if m.Backend == "" {
					return nil, errors.New("tool plugin has no backend")
				}
				wasm, e := os.ReadFile(filepath.Join(h.Dir, m.ID, m.Digest, "backend.wasm"))
				if e != nil {
					return nil, e
				}
				compiled, e = h.runtime.CompileModule(ctx, wasm)
				if e != nil {
					return nil, e
				}
				defer compiled.Close(ctx)
			}
			mod, e := h.runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(domain.ID()).WithStdin(bytes.NewReader(input)).WithStdout(&output))
			if mod != nil {
				mod.Close(ctx)
			}
			if e != nil {
				return nil, fmt.Errorf("plugin %s: %w", m.ID, e)
			}
			var reply struct {
				Result any    `json:"result"`
				Error  string `json:"error"`
			}
			if e = json.Unmarshal(output.Bytes(), &reply); e != nil {
				return nil, errors.New("invalid plugin response")
			}
			if reply.Error != "" {
				return nil, errors.New(reply.Error)
			}
			return reply.Result, nil
		}
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

type limitedWriter struct{ bytes.Buffer }

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.Len()+len(p) > 2<<20 {
		return 0, errors.New("plugin output exceeds 2 MiB")
	}
	return w.Buffer.Write(p)
}

func Validate(schema map[string]any, args json.RawMessage) error {
	var v map[string]any
	if json.Unmarshal(args, &v) != nil || v == nil {
		return errors.New("tool arguments must be an object")
	}
	required, _ := schema["required"].([]any)
	for _, key := range required {
		if _, ok := v[fmt.Sprint(key)]; !ok {
			return fmt.Errorf("missing argument: %v", key)
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	for k, value := range v {
		prop, ok := properties[k]
		if !ok {
			if schema["additionalProperties"] == false {
				return fmt.Errorf("unknown argument: %s", k)
			}
			continue
		}
		p, _ := prop.(map[string]any)
		good := true
		switch p["type"] {
		case "string":
			_, good = value.(string)
		case "number", "integer":
			n, ok := value.(float64)
			good = ok
			if p["type"] == "integer" && ok {
				good = n == float64(int64(n))
			}
		case "boolean":
			_, good = value.(bool)
		case "array":
			values, ok := value.([]any)
			good = ok
			if items, ok := p["items"].(map[string]any); ok && items["type"] == "string" {
				for _, item := range values {
					if _, ok := item.(string); !ok {
						good = false
					}
				}
			}
		case "object":
			_, good = value.(map[string]any)
		}
		if !good {
			return fmt.Errorf("invalid type for %s", k)
		}
	}
	return nil
}

// Install never executes package scripts. The immutable digest includes every
// executable input. New capabilities remain pending until a direct owner grant.
func (h *Host) Install(m domain.PluginManifest, wasm []byte) (domain.PluginManifest, error) {
	if !validID.MatchString(m.ID) || m.API != 1 || strings.TrimSpace(m.Name) == "" {
		return m, errors.New("plugin needs API 1, a lowercase kebab-case id, and a name")
	}
	if len(wasm) > 32<<20 {
		return m, errors.New("backend exceeds 32 MiB")
	}
	old, oldErr := h.Get(m.ID)
	if old.Builtin {
		return m, errors.New("built-in packages cannot be replaced")
	}
	granted := old.Capabilities
	if old.Pending {
		granted = nil
		var previous domain.PluginManifest
		if h.Store.Get("plugin-previous", m.ID, &previous) == nil && !previous.Pending {
			granted = previous.Capabilities
		}
	}
	if len(m.Tools) > 0 && len(wasm) == 0 {
		return m, errors.New("tools require a WASI backend")
	}
	if len(wasm) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, e := h.runtime.CompileModule(ctx, wasm)
		if e != nil {
			return m, e
		}
		c.Close(ctx)
	}
	toolNames := map[string]bool{}
	for _, t := range m.Tools {
		if !strings.HasPrefix(t.Name, strings.ReplaceAll(m.ID, "-", "_")+"_") {
			return m, errors.New("tool names must use the plugin id prefix")
		}
		if t.Capability != "" && !slices.Contains(m.Capabilities, t.Capability) {
			return m, errors.New("tool uses an undeclared capability")
		}
		if toolNames[t.Name] {
			return m, errors.New("tool names must be unique")
		}
		toolNames[t.Name] = true
		if t.Parameters["type"] != "object" {
			return m, errors.New("tool parameters must be an object schema")
		}
	}
	viewNames := map[string]bool{}
	for i := range m.Views {
		v := &m.Views[i]
		if v.Kind == "" {
			v.Kind = "floating"
		}
		if !validID.MatchString(v.ID) || viewNames[v.ID] || (v.Kind != "floating" && v.Kind != "renderer") {
			return m, errors.New("views need unique lowercase ids and floating or renderer kind")
		}
		viewNames[v.ID] = true
		if len(v.HTML) > 2<<20 {
			return m, errors.New("view source exceeds 2 MiB")
		}
	}
	for _, provider := range m.Providers {
		found := false
		for _, t := range m.Tools {
			if t.Name == provider {
				found = true
			}
		}
		if !found {
			return m, errors.New("each provider must name a declared backend tool")
		}
	}
	for _, renderer := range m.Renderers {
		found := false
		for _, v := range m.Views {
			if v.ID == renderer && v.Kind == "renderer" {
				found = true
			}
		}
		if !found {
			return m, errors.New("each renderer must name a renderer view")
		}
	}
	if m.Config == nil {
		m.Config = map[string]any{}
	}
	if m.Settings == nil {
		m.Settings = Schema(map[string]any{})
	}
	m.Builtin = false
	m.Pending = false
	m.Enabled = true
	if m.Scope == "" {
		m.Scope = "bot"
	}
	if m.Scope != "bot" && m.Scope != "echo" && m.Scope != "app" {
		return m, errors.New("plugin scope must be bot, echo, or app")
	}
	if oldErr == nil && !old.Pending {
		m.Config = old.Config
	}
	for _, cap := range m.Capabilities {
		if cap != "storage.get" && cap != "storage.set" && cap != "storage.delete" && !slices.Contains(granted, cap) {
			m.Pending = true
			m.Enabled = false
		}
	}
	m.Digest = ""
	m.Backend = ""
	if len(wasm) > 0 {
		m.Backend = "backend.wasm"
	}
	data, _ := json.Marshal(m)
	sum := sha256.Sum256(append(data, wasm...))
	m.Digest = hex.EncodeToString(sum[:])
	dir := filepath.Join(h.Dir, m.ID, m.Digest)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return m, e
	}
	if len(wasm) > 0 {
		if e := os.WriteFile(filepath.Join(dir, "backend.wasm"), wasm, 0600); e != nil {
			return m, e
		}
	}
	data, _ = json.MarshalIndent(m, "", "  ")
	if e := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0600); e != nil {
		return m, e
	}
	if oldErr == nil && !old.Pending {
		if e := h.Store.Put("plugin-previous", m.ID, old); e != nil {
			return m, e
		}
	}
	return m, h.Save(m)
}
func (h *Host) Approve(id string) (domain.PluginManifest, error) {
	m, e := h.Get(id)
	if e != nil {
		return m, e
	}
	return h.ApproveDigest(id, m.Digest)
}
func (h *Host) ApproveDigest(id, digest string) (domain.PluginManifest, error) {
	m, e := h.Get(id)
	if e != nil {
		return m, e
	}
	if digest == "" || m.Digest != digest {
		return m, errors.New("plugin changed since review; review its current permissions and digest")
	}
	m.Pending = false
	m.Enabled = true
	return m, h.Save(m)
}
func (h *Host) Rollback(id string) error {
	var previous domain.PluginManifest
	if e := h.Store.Get("plugin-previous", id, &previous); e != nil {
		return e
	}
	current, e := h.Get(id)
	if e != nil {
		return e
	}
	if e = h.Save(previous); e != nil {
		return e
	}
	return h.Store.Put("plugin-previous", id, current)
}
