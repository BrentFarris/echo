package server

import (
	"errors"
	"net"
	"net/http"

	"github.com/brent/echo/internal/broker"
)

func (s *Server) revealChangedFile(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	address := net.ParseIP(host)
	if address == nil || !address.IsLoopback() || !localHost(r.Host) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"Open Echo on this Windows computer to reveal files in File Explorer."}`))
		return
	}
	var request struct {
		MessageID string `json:"messageId"`
		Path      string `json:"path"`
	}
	if err := decode(w, r, &request); err != nil {
		respond(w, nil, err)
		return
	}
	messages, err := s.Store.Messages(r.PathValue("id"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	for _, message := range messages {
		if message.ID != request.MessageID || message.Role != "tool" {
			continue
		}
		for _, change := range message.FileChanges {
			if change.Path != request.Path {
				continue
			}
			bot, err := s.Bot(message.BotID)
			if err != nil {
				respond(w, nil, errors.New("this bot no longer exists; its historical file changes are still available"))
				return
			}
			target, err := s.Files.RevealTarget(bot, change.Path)
			if err == nil {
				reveal := s.revealFile
				if reveal == nil {
					reveal = broker.RevealInFileBrowser
				}
				err = reveal(target)
			}
			respond(w, target, err)
			return
		}
	}
	respond(w, nil, errors.New("this file is not a recorded change in this chat"))
}
