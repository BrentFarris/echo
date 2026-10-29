package server

import (
	"errors"
	"net"
	"net/http"
	"path/filepath"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
)

func (s *Server) revealChangedFile(w http.ResponseWriter, r *http.Request) {
	if !localRevealRequest(w, r) {
		return
	}
	s.openRecordedChange(w, r, false)
}

func (s *Server) openChangedFile(w http.ResponseWriter, r *http.Request) {
	s.openRecordedChange(w, r, true)
}

func localRevealRequest(w http.ResponseWriter, r *http.Request) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	address := net.ParseIP(host)
	if address == nil || !address.IsLoopback() || !localHost(r.Host) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"Open Echo on this Windows computer to reveal files in File Explorer."}`))
		return false
	}
	return true
}

func (s *Server) openRecordedChange(w http.ResponseWriter, r *http.Request, preferEditor bool) {
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
				if preferEditor && s.Plugins.Enabled(bot, "monaco-editor") {
					ref, err := s.changedFileEditorRef(bot.ID, target.Path)
					kind := "directory"
					if target.Select {
						kind = "file"
					}
					respond(w, map[string]any{"editor": map[string]any{"botId": bot.ID, "ref": ref, "kind": kind}}, err)
					return
				}
				if !localRevealRequest(w, r) {
					return
				}
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

// Convert only a revalidated recorded change into the editor's root-relative
// reference. Editor reads still enforce their own current filesystem boundary.
func (s *Server) changedFileEditorRef(botID, path string) (editorfs.FileRef, error) {
	roots, err := s.EditorFS.Roots(botID)
	if err != nil {
		return editorfs.FileRef{}, err
	}
	for _, root := range roots {
		if root.BlockedReason != "" || !broker.Within(root.HostPath, path) {
			continue
		}
		relative, err := filepath.Rel(root.HostPath, path)
		if err != nil {
			continue
		}
		if relative == "." {
			relative = ""
		}
		ref := editorfs.FileRef{RootID: root.ID, Path: filepath.ToSlash(relative)}
		if _, err := s.EditorFS.ResolveExistingHostPath(botID, ref, true); err != nil {
			return editorfs.FileRef{}, err
		}
		return ref, nil
	}
	return editorfs.FileRef{}, errors.New("this file is outside the editor's folders; configure this bot's folders to open it in the editor")
}

func (s *Server) revealReference(w http.ResponseWriter, path string, bots []domain.Bot) {
	var last error = errors.New("this reference is no longer available to any recipient")
	for _, bot := range bots {
		target, err := s.Files.RevealTarget(bot, path)
		if err != nil {
			last = err
			continue
		}
		_, statErr := s.Files.ReferenceKind(bot, path)
		reveal := s.revealFile
		if reveal == nil {
			reveal = broker.RevealInFileBrowser
		}
		err = reveal(target)
		respond(w, struct {
			broker.RevealTarget
			Fallback bool `json:"fallback"`
		}{target, statErr != nil}, err)
		return
	}
	respond(w, nil, last)
}

func (s *Server) revealDraftReference(w http.ResponseWriter, r *http.Request) {
	if !localRevealRequest(w, r) {
		return
	}
	var req struct {
		SendRequest
		Path string `json:"path"`
	}
	if err := decode(w, r, &req); err != nil {
		respond(w, nil, err)
		return
	}
	bots, err := s.recipients(req.SendRequest)
	if err != nil {
		respond(w, nil, err)
		return
	}
	for _, ref := range req.Segments {
		if ref.Type == "file" && ref.Path == req.Path && ref.Range != nil {
			s.revealReference(w, ref.Path, bots)
			return
		}
	}
	respond(w, nil, errors.New("this path is not a reference in the draft"))
}

func (s *Server) revealMessageReference(w http.ResponseWriter, r *http.Request) {
	if !localRevealRequest(w, r) {
		return
	}
	var req struct {
		MessageID string `json:"messageId"`
		Path      string `json:"path"`
	}
	if err := decode(w, r, &req); err != nil {
		respond(w, nil, err)
		return
	}
	messages, err := s.Store.Messages(r.PathValue("id"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	for _, message := range messages {
		if message.ID != req.MessageID || message.Role != "user" {
			continue
		}
		for _, ref := range message.Segments {
			if ref.Type != "file" || ref.Path != req.Path {
				continue
			}
			bots := []domain.Bot{}
			for _, id := range ref.AllowedBots {
				if bot, err := s.Bot(id); err == nil {
					bots = append(bots, bot)
				}
			}
			s.revealReference(w, ref.Path, bots)
			return
		}
	}
	respond(w, nil, errors.New("this path is not a recorded reference in this chat"))
}
