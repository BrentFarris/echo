package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/brent/echo/internal/confluence"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/plugins"
)

func confluenceSettings(m domain.PluginManifest, updates map[string]any, owner bool) (map[string]any, error) {
	config := map[string]any{}
	for k, v := range m.Config {
		if k != "credential" {
			config[k] = v
		}
	}
	for k, v := range updates {
		if k == "credential" {
			if v != m.Config[k] {
				return nil, errors.New("use the protected Confluence token field")
			}
			continue
		}
		config[k] = v
	}
	raw, _ := json.Marshal(config)
	if err := plugins.Validate(m.Settings, raw); err != nil {
		return nil, err
	}
	var current, old confluence.Config
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil, err
	}
	current, err := confluence.Normalize(current)
	if err != nil {
		return nil, err
	}
	raw, _ = json.Marshal(m.Config)
	_ = json.Unmarshal(raw, &old)
	old, _ = confluence.Normalize(old)
	if !owner && current != old {
		return nil, errors.New("configure Confluence connection settings directly in Settings → Confluence")
	}
	raw, _ = json.Marshal(current)
	config = map[string]any{}
	_ = json.Unmarshal(raw, &config)
	if current == old && text(m.Config, "credential") != "" {
		config["credential"] = m.Config["credential"]
	}
	return config, nil
}

// Caller holds confluenceMu. This also cancels owner connection tests, which do
// not run through the WASI invocation registry.
func (s *Server) cancelConfluenceLocked() {
	for _, cancel := range s.confluenceActive {
		cancel()
	}
}

func (s *Server) beginConfluence(ctx context.Context) (*confluence.Client, context.Context, func(), error) {
	s.confluenceMu.Lock()
	defer s.confluenceMu.Unlock()
	m, err := s.Plugins.Get("confluence")
	if err != nil {
		return nil, ctx, nil, err
	}
	if !m.Enabled || m.Pending {
		return nil, ctx, nil, errors.New("Confluence plugin is disabled")
	}
	config, err := confluenceSettings(m, nil, true)
	if err != nil {
		return nil, ctx, nil, err
	}
	raw, _ := json.Marshal(config)
	var connection confluence.Config
	_ = json.Unmarshal(raw, &connection)
	if err = connection.Ready(); err != nil {
		return nil, ctx, nil, err
	}
	ref := text(m.Config, "credential")
	if ref == "" {
		return nil, ctx, nil, errors.New("save a token in Settings → Confluence")
	}
	token, err := s.Secrets.Get(ctx, ref)
	if err != nil {
		return nil, ctx, nil, errors.New("Confluence credential unavailable; save a token in Settings → Confluence")
	}
	ctx, cancel := context.WithCancel(ctx)
	id := domain.ID()
	if s.confluenceActive == nil {
		s.confluenceActive = map[string]context.CancelFunc{}
	}
	s.confluenceActive[id] = cancel
	done := func() { cancel(); s.confluenceMu.Lock(); delete(s.confluenceActive, id); s.confluenceMu.Unlock() }
	return &confluence.Client{Config: connection, Token: token}, ctx, done, nil
}

func (s *Server) confluenceOperation(ctx context.Context, scope domain.ExecutionScope, m domain.PluginManifest, method string, raw json.RawMessage) (any, error) {
	if scope.PluginID != "confluence" || !m.Builtin {
		return nil, errors.New("Confluence capabilities belong to the built-in Confluence plugin")
	}
	var args struct {
		CQL          string `json:"cql"`
		Query        string `json:"query"`
		ID           string `json:"id"`
		Format       string `json:"format"`
		Limit        int    `json:"limit"`
		Continuation string `json:"continuation"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	client, ctx, done, err := s.beginConfluence(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	switch method {
	case "confluence.search":
		return client.Search(ctx, args.CQL, args.Limit, args.Continuation)
	case "confluence.read":
		return client.Read(ctx, args.ID, args.Format, args.Continuation)
	case "confluence.users.search":
		return client.SearchUsers(ctx, args.Query, args.Limit, args.Continuation)
	case "confluence.users.current":
		return client.CurrentUser(ctx)
	default:
		return nil, errors.New("unknown Confluence operation")
	}
}

func (s *Server) confluenceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/plugins/confluence/credential", func(w http.ResponseWriter, r *http.Request) {
		s.confluenceMu.Lock()
		defer s.confluenceMu.Unlock()
		var request struct {
			Secret string `json:"secret"`
		}
		if err := decode(w, r, &request); err != nil {
			respond(w, nil, err)
			return
		}
		if err := confluence.ValidateToken(request.Secret); err != nil {
			respond(w, nil, err)
			return
		}
		m, err := s.Plugins.Get("confluence")
		if err != nil {
			respond(w, nil, err)
			return
		}
		config, err := confluenceSettings(m, nil, true)
		if err != nil {
			respond(w, nil, err)
			return
		}
		if request.Secret != "" {
			raw, _ := json.Marshal(config)
			var connection confluence.Config
			_ = json.Unmarshal(raw, &connection)
			if err = connection.Ready(); err != nil {
				respond(w, nil, err)
				return
			}
		}
		old, ref := text(m.Config, "credential"), ""
		if request.Secret != "" {
			ref = "Echo2/" + s.Dir + "/plugins/confluence/" + domain.ID()
			if err = s.Secrets.Set(r.Context(), ref, request.Secret); err != nil {
				respond(w, nil, errors.New("could not save Confluence token in the credential vault"))
				return
			}
		}
		config["credential"] = ref
		m.Config = config
		if err = s.Plugins.Save(m); err != nil {
			if ref != "" {
				_ = s.Secrets.Delete(r.Context(), ref)
			}
			respond(w, nil, err)
			return
		}
		s.cancelConfluenceLocked()
		if old != "" {
			_ = s.Secrets.Delete(r.Context(), old)
		}
		s.emit("changed", "", "", "", nil)
		respond(w, map[string]bool{"saved": ref != ""}, nil)
	})
	mux.HandleFunc("POST /api/v1/plugins/confluence/test", func(w http.ResponseWriter, r *http.Request) {
		client, ctx, done, err := s.beginConfluence(r.Context())
		if err != nil {
			respond(w, nil, err)
			return
		}
		defer done()
		_, err = client.Search(ctx, "type = page", 1, "")
		respond(w, map[string]bool{"connected": err == nil}, err)
	})
}
