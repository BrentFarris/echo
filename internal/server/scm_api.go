package server

import (
	"errors"
	"github.com/brent/echo/internal/domain"
	"net/http"
	"strings"
)

func (s *Server) sourceControlRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/plugins/p4/credential", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Secret string `json:"secret"`
		}
		if e := decode(w, r, &request); e != nil {
			respond(w, nil, e)
			return
		}
		if len(request.Secret) > 4096 || strings.ContainsAny(request.Secret, "\r\n\x00") {
			respond(w, nil, errors.New("P4 ticket/password must be one line, at most 4096 bytes"))
			return
		}
		m, e := s.Plugins.Get("p4")
		if e != nil {
			respond(w, nil, e)
			return
		}
		if request.Secret != "" && (text(m.Config, "port") == "" || text(m.Config, "user") == "") {
			respond(w, nil, errors.New("save the P4 server and user before setting a credential"))
			return
		}
		old := text(m.Config, "credential")
		ref := ""
		if request.Secret != "" {
			ref = "Echo2/" + s.Dir + "/plugins/p4/" + domain.ID()
			if e = s.Secrets.Set(r.Context(), ref, request.Secret); e != nil {
				respond(w, nil, e)
				return
			}
		}
		m.Config["credential"] = ref
		if e = s.Plugins.Save(m); e != nil {
			if ref != "" {
				s.Secrets.Delete(r.Context(), ref)
			}
			respond(w, nil, e)
			return
		}
		if old != "" {
			s.Secrets.Delete(r.Context(), old)
		}
		s.emit("changed", "", "", "", nil)
		respond(w, map[string]bool{"saved": ref != ""}, nil)
	})
}
