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
			if err := s.requireSharedBot(s.requestAccess(r), message.BotID); err != nil {
				respond(w, nil, err)
				return
			}
			if preferEditor {
				if err := s.requireSharedPlugin(s.requestAccess(r), "monaco-editor"); err != nil {
					respond(w, nil, err)
					return
				}
			}
			bot, err := s.Bot(message.BotID)
			if err != nil {
				respond(w, nil, errors.New("this bot no longer exists; its historical file changes are still available"))
				return
			}
			target, err := s.Files.RevealTarget(bot, change.Path)
			if err == nil {
				if preferEditor && s.Plugins.Enabled(bot, "monaco-editor") {
					ref, err := s.fileEditorRef(bot.ID, target.Path)
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

// Convert only a revalidated path into the editor's root-relative
// reference. Editor reads still enforce their own current filesystem boundary.
func (s *Server) fileEditorRef(botID, path string) (editorfs.FileRef, error) {
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

func (s *Server) openReference(w http.ResponseWriter, r *http.Request, path string, bots []domain.Bot, preferEditor bool) {
	bots = s.sharedBots(s.requestAccess(r), bots)
	if preferEditor {
		if err := s.requireSharedPlugin(s.requestAccess(r), "monaco-editor"); err != nil {
			respond(w, nil, err)
			return
		}
	}
	var last error = errors.New("this reference is no longer available to any recipient")
	if preferEditor {
		editorEnabled := false
		for _, bot := range bots {
			if !s.Plugins.Enabled(bot, "monaco-editor") {
				continue
			}
			editorEnabled = true
			target, err := s.Files.RevealTarget(bot, path)
			if err != nil {
				last = err
				continue
			}
			ref, err := s.fileEditorRef(bot.ID, target.Path)
			if err != nil {
				last = err
				continue
			}
			kind := "directory"
			if target.Select {
				kind = "file"
			}
			_, statErr := s.Files.ReferenceKind(bot, path)
			respond(w, map[string]any{"editor": map[string]any{"botId": bot.ID, "ref": ref, "kind": kind}, "fallback": statErr != nil}, nil)
			return
		}
		if editorEnabled {
			respond(w, nil, last)
			return
		}
	}
	if !localRevealRequest(w, r) {
		return
	}
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
	s.openDraftReferenceRequest(w, r, false)
}

func (s *Server) openDraftReference(w http.ResponseWriter, r *http.Request) {
	s.openDraftReferenceRequest(w, r, true)
}

func (s *Server) openDraftReferenceRequest(w http.ResponseWriter, r *http.Request, preferEditor bool) {
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
			s.openReference(w, r, ref.Path, bots, preferEditor)
			return
		}
	}
	respond(w, nil, errors.New("this path is not a reference in the draft"))
}

func (s *Server) revealMessageReference(w http.ResponseWriter, r *http.Request) {
	if !localRevealRequest(w, r) {
		return
	}
	s.openMessageReferenceRequest(w, r, false)
}

func (s *Server) openMessageReference(w http.ResponseWriter, r *http.Request) {
	s.openMessageReferenceRequest(w, r, true)
}

func (s *Server) openMessageReferenceRequest(w http.ResponseWriter, r *http.Request, preferEditor bool) {
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
			s.openReference(w, r, ref.Path, bots, preferEditor)
			return
		}
	}
	respond(w, nil, errors.New("this path is not a recorded reference in this chat"))
}
