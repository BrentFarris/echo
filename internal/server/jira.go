package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/jira"
	"github.com/brent/echo/internal/plugins"
)

func jiraSettings(m domain.PluginManifest, updates map[string]any, owner bool) (map[string]any, error) {
	config := map[string]any{}
	for k, v := range m.Config {
		if k != "credential" {
			config[k] = v
		}
	}
	for k, v := range updates {
		if k == "credential" {
			if v != m.Config[k] {
				return nil, errors.New("use the protected Jira credential field")
			}
			continue
		}
		config[k] = v
	}
	raw, _ := json.Marshal(config)
	if err := plugins.Validate(m.Settings, raw); err != nil {
		return nil, err
	}
	var current jira.Config
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil, err
	}
	current, err := jira.Normalize(current)
	if err != nil {
		return nil, err
	}
	raw, _ = json.Marshal(current)
	_ = json.Unmarshal(raw, &config)
	oldRaw, _ := json.Marshal(m.Config)
	var old jira.Config
	_ = json.Unmarshal(oldRaw, &old)
	old, _ = jira.Normalize(old)
	if !owner && current != old {
		return nil, errors.New("configure Jira connection settings directly in Settings → Jira")
	}
	if current == old && text(m.Config, "credential") != "" {
		config["credential"] = m.Config["credential"]
	}
	return config, nil
}

func (s *Server) jiraClient(ctx context.Context, m domain.PluginManifest) (*jira.Client, error) {
	config, err := jiraSettings(m, nil, true)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(config)
	var connection jira.Config
	_ = json.Unmarshal(raw, &connection)
	ref := text(m.Config, "credential")
	if ref == "" {
		return nil, errors.New("save a token in Settings → Jira")
	}
	token, err := s.Secrets.Get(ctx, ref)
	if err != nil {
		return nil, errors.New("Jira credential unavailable; save a token in Settings → Jira")
	}
	return &jira.Client{Config: connection, Token: token}, nil
}

func (s *Server) jiraOperation(ctx context.Context, scope domain.ExecutionScope, m domain.PluginManifest, method string, raw json.RawMessage) (any, error) {
	if scope.PluginID != "jira" || !m.Builtin {
		return nil, errors.New("Jira capabilities belong to the built-in Jira plugin")
	}
	var args struct {
		JQL      string   `json:"jql"`
		Issue    string   `json:"issue"`
		Fields   []string `json:"fields"`
		PageSize int      `json:"pageSize"`
		Cursor   string   `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	client, err := s.jiraClient(ctx, m)
	if err != nil {
		return nil, err
	}
	switch method {
	case "jira.search":
		return client.Search(ctx, args.JQL, args.Fields, args.PageSize, args.Cursor)
	case "jira.issue":
		return client.Issue(ctx, args.Issue, args.Fields)
	case "jira.comments":
		return client.Comments(ctx, args.Issue, args.PageSize, args.Cursor)
	}
	return nil, errors.New("unknown Jira operation")
}

func (s *Server) jiraRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/plugins/jira/credential", func(w http.ResponseWriter, r *http.Request) {
		s.jiraMu.Lock()
		defer s.jiraMu.Unlock()
		var request struct {
			Secret string `json:"secret"`
		}
		if err := decode(w, r, &request); err != nil {
			respond(w, nil, err)
			return
		}
		if err := jira.ValidateToken(request.Secret); err != nil {
			respond(w, nil, err)
			return
		}
		m, err := s.Plugins.Get("jira")
		if err != nil {
			respond(w, nil, err)
			return
		}
		config, err := jiraSettings(m, nil, true)
		if err != nil {
			respond(w, nil, err)
			return
		}
		if request.Secret != "" && (text(config, "url") == "" || text(config, "deployment") == "cloud" && text(config, "email") == "") {
			respond(w, nil, errors.New("save the Jira site and Cloud account email before setting a token"))
			return
		}
		old, ref := text(m.Config, "credential"), ""
		if request.Secret != "" {
			ref = "Echo2/" + s.Dir + "/plugins/jira/" + domain.ID()
			if err = s.Secrets.Set(r.Context(), ref, request.Secret); err != nil {
				respond(w, nil, errors.New("could not save Jira token in the credential vault"))
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
		if old != "" {
			_ = s.Secrets.Delete(r.Context(), old)
		}
		s.emit("changed", "", "", "", nil)
		respond(w, map[string]bool{"saved": ref != ""}, nil)
	})
	mux.HandleFunc("POST /api/v1/plugins/jira/test", func(w http.ResponseWriter, r *http.Request) {
		m, err := s.Plugins.Get("jira")
		if err != nil {
			respond(w, nil, err)
			return
		}
		client, err := s.jiraClient(r.Context(), m)
		if err != nil {
			respond(w, nil, err)
			return
		}
		identity, err := client.Myself(r.Context())
		respond(w, identity, err)
	})
}
