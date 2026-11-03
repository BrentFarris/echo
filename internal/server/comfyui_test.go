package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

const comfyImage = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScLttAAAAABJRU5ErkJggg=="
const comfyWorkflow = `{"1":{"class_type":"TestNode","inputs":{"prompt":"{{PROMPT}}","negative":"{{NEGATIVE_PROMPT}}","model":"{{MODEL}}","image":"{{IMAGE}}","seed":"{{SEED}}","width":"{{WIDTH}}","height":"{{HEIGHT}}","steps":"{{STEPS}}","cfg":"{{CFG_SCALE}}","frames":"{{FRAMES}}","fps":"{{FPS}}","format":"{{FORMAT}}","duration":"{{DURATION}}","aspect":"{{ASPECT_RATIO}}","megapixels":"{{MEGAPIXELS}}"}}}`

func comfyScope(t *testing.T, s *Server) domain.ExecutionScope {
	t.Helper()
	bot, _ := s.Bot("echo")
	bot.Roots = []string{t.TempDir()}
	c := chat(t, s, domain.Generation{})
	m := domain.Message{ID: domain.ID(), ChatID: c.ID, BotID: bot.ID, Role: "tool", Status: "running", CreatedAt: domain.Now()}
	if err := s.Store.AddMessage(m); err != nil {
		t.Fatal(err)
	}
	return domain.ExecutionScope{Bot: bot, ChatID: c.ID, ToolMessageID: m.ID}
}

func comfyInvoke(t *testing.T, s *Server, scope domain.ExecutionScope, tool string, args map[string]any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	value, err := s.Plugins.Invoke(context.Background(), scope, tool, raw)
	result, _ := value.(map[string]any)
	return result, err
}

func comfyMock(t *testing.T, s *Server, video bool, check func(map[string]any)) *httptest.Server {
	t.Helper()
	image, _ := base64.StdEncoding.DecodeString(comfyImage)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/upload/image":
			file, header, err := r.FormFile("image")
			if err != nil {
				t.Error(err)
				http.Error(w, "upload", 400)
				return
			}
			file.Close()
			if !strings.HasPrefix(header.Filename, "echo_input_") {
				t.Error(header.Filename)
			}
			json.NewEncoder(w).Encode(map[string]any{"name": "input.png", "subfolder": "inputs"})
		case "/prompt":
			var payload struct {
				Prompt map[string]any `json:"prompt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if check != nil {
				check(payload.Prompt)
			}
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "test"})
		case "/history/test":
			key, filename, storage := "images", "result.png", "output"
			if video {
				key, filename, storage = "gifs", "result.gif", "custom"
			}
			json.NewEncoder(w).Encode(map[string]any{"test": map[string]any{"outputs": map[string]any{"1": map[string]any{key: []any{map[string]any{"filename": filename, "subfolder": "nested/results", "type": storage}}}}}})
		case "/view":
			if r.URL.Query().Get("subfolder") != "nested/results" {
				t.Error("nested subfolder lost", r.URL)
			}
			if video {
				if r.URL.Query().Get("type") != "custom" {
					t.Error("custom storage lost")
				}
				w.Write([]byte("GIF89a" + strings.Repeat("\x00", 20)))
			} else {
				w.Write(image)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	m, _ := s.Plugins.Get("comfyui")
	m.Config = map[string]any{"url": server.URL, "defaultCheckpoint": "default.safetensors"}
	if err := s.Plugins.Save(m); err != nil {
		t.Fatal(err)
	}
	return server
}

func TestComfyUIWorkflowSelectionAndMediaPersistence(t *testing.T) {
	for _, mode := range []string{"txt2img", "img2img", "attached", "inline", "path", "fallback", "video"} {
		t.Run(mode, func(t *testing.T) {
			s := testServer(t)
			scope := comfyScope(t, s)
			args := map[string]any{"prompt": "a fox", "seed": 42, "width": 768, "height": 1024, "steps": 30, "cfgScale": 6.5}
			video := mode == "video"
			comfyMock(t, s, video, func(workflow map[string]any) {
				if mode == "fallback" {
					if len(workflow) != 7 {
						t.Error(workflow)
					}
					return
				}
				inputs := workflow["1"].(map[string]any)["inputs"].(map[string]any)
				for k, expected := range map[string]any{"prompt": "a fox", "negative": "", "model": "default.safetensors", "seed": float64(42), "width": float64(768), "height": float64(1024), "steps": float64(30), "cfg": 6.5} {
					if inputs[k] != expected {
						t.Errorf("%s: got %v want %v", k, inputs[k], expected)
					}
				}
				if (mode == "img2img" || mode == "attached") && inputs["image"] != "inputs/input.png" {
					t.Error("input image lost", inputs)
				}
				if video {
					for k, expected := range map[string]any{"frames": float64(81), "fps": 24.0, "format": "gif", "duration": 3.5, "aspect": "3:4 (Portrait Standard)", "megapixels": 0.8} {
						if inputs[k] != expected {
							t.Error(k, inputs[k], expected)
						}
					}
				}
			})
			workflowFile := filepath.Join(scope.Bot.Roots[0], "workflow.json")
			if err := os.WriteFile(workflowFile, []byte(comfyWorkflow), 0600); err != nil {
				t.Fatal(err)
			}
			m, _ := s.Plugins.Get("comfyui")
			tool := "comfyui_generate"
			switch mode {
			case "txt2img":
				m.Config["txt2imgWorkflow"] = "workflow.json"
			case "img2img":
				m.Config["txt2imgWorkflow"] = "missing.json"
				m.Config["img2imgWorkflow"] = "workflow.json"
				image, _ := base64.StdEncoding.DecodeString(comfyImage)
				os.WriteFile(filepath.Join(scope.Bot.Roots[0], "input.png"), image, 0600)
				args["imagePath"] = "input.png"
				args["attachedImageIndex"] = -1 // explicit path wins
			case "attached":
				m.Config["img2imgWorkflow"] = "workflow.json"
				user := domain.Message{ID: domain.ID(), ChatID: scope.ChatID, Role: "user", Recipients: []string{"echo"}, Status: "completed"}
				s.Store.AddMessage(user)
				image, _ := base64.StdEncoding.DecodeString(comfyImage)
				a := domain.Attachment{ID: domain.ID(), ChatID: scope.ChatID, Name: "pasted.png", MediaType: "image/png", Size: int64(len(image))}
				if err := s.Store.AddGeneratedAttachment(a, user.ID, image); err != nil {
					t.Fatal(err)
				}
				user.Attachments = []domain.Attachment{a}
				if err := s.Store.FinishMessage(user); err != nil {
					t.Fatal(err)
				}
				args["attachedImageIndex"] = 0
			case "inline":
				args["workflowJSON"] = comfyWorkflow
				args["workflowPath"] = "missing.json"
			case "path":
				args["workflowPath"] = "workflow.json"
				m.Config["txt2imgWorkflow"] = "missing.json"
			case "video":
				tool = "comfyui_generate_video"
				m.Config["videoWorkflow"] = "workflow.json"
				args["frames"] = 81
				args["fps"] = 24
				args["format"] = "gif"
				args["duration"] = 3.5
				args["aspectRatio"] = "3:4 (Portrait Standard)"
				args["megapixels"] = 0.8
			}
			if err := s.Plugins.Save(m); err != nil {
				t.Fatal(err)
			}
			result, err := comfyInvoke(t, s, scope, tool, args)
			if err != nil {
				t.Fatal(err)
			}
			idKey, saveTool, ext := "imageId", "save_image", ".png"
			if video {
				idKey, saveTool, ext = "videoId", "save_video", ".gif"
			}
			id, _ := result[idKey].(string)
			if id == "" {
				t.Fatal(result)
			}
			ms, err := s.Store.Messages(scope.ChatID)
			if err != nil || len(ms[0].Attachments) != 1 {
				t.Fatal(ms, err)
			}
			_, data, err := s.Store.Attachment(scope.ChatID, id, false)
			if err != nil || len(data) == 0 {
				t.Fatal(err)
			}
			if _, _, err := s.Store.Attachment(scope.ChatID, id, true); err == nil {
				t.Fatal("generated result became pending upload")
			}
			collector := &fileChangeCollector{}
			raw, _ := json.Marshal(map[string]any{idKey: id, "path": "saved/result" + ext})
			_, err = s.Plugins.Invoke(context.WithValue(context.Background(), fileChangesKey{}, collector), scope, saveTool, raw)
			if err != nil {
				t.Fatal(err)
			}
			if len(collector.changes) != 1 || collector.changes[0].Operation != "created" {
				t.Fatal(collector.changes)
			}
			saved, err := os.ReadFile(filepath.Join(scope.Bot.Roots[0], "saved/result"+ext))
			if err != nil || !bytes.Equal(saved, data) {
				t.Fatal("saved media differs", err)
			}
			if _, err := s.Plugins.Invoke(context.Background(), scope, saveTool, raw); err == nil {
				t.Fatal("overwrote without permission")
			}
			if _, err := comfyInvoke(t, s, scope, saveTool, map[string]any{idKey: id, "path": "saved/result" + ext, "overwrite": true}); err != nil {
				t.Fatal(err)
			}
			outside := scope
			outside.Bot.ID = "other"
			if _, err := s.Plugins.Invoke(context.Background(), outside, saveTool, raw); err == nil {
				t.Fatal("media leaked between bots")
			}
			if _, err := comfyInvoke(t, s, scope, saveTool, map[string]any{idKey: id, "path": filepath.Join(t.TempDir(), "outside"+ext)}); err == nil {
				t.Fatal("escaped workspace")
			}
			mux := http.NewServeMux()
			s.attachmentRoutes(mux)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest("GET", result["url"].(string), nil))
			if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), data) {
				t.Fatal("inline download failed", response.Code)
			}
			if err := s.Store.DeleteChat(scope.ChatID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Store.Attachment(scope.ChatID, id, false); err == nil {
				t.Fatal("chat deletion retained media")
			}
		})
	}
}

func TestComfyUIValidationPermissionsAndCancellation(t *testing.T) {
	s := testServer(t)
	scope := comfyScope(t, s)
	m, _ := s.Plugins.Get("comfyui")
	if !m.Builtin || m.Scope != "bot" || len(m.Tools) != 4 || len(m.Settings["properties"].(map[string]any)) != 5 {
		t.Fatal(m)
	}
	if _, err := comfyInvoke(t, s, scope, "comfyui_generate", map[string]any{"prompt": "test"}); err == nil {
		t.Fatal("missing server accepted")
	}
	for _, base := range []string{"file:///etc/passwd", "http://user:password@localhost:8188", "http://localhost:8188?other=1"} {
		if _, err := comfyUIConfig(m, map[string]any{"url": base}, true); err == nil {
			t.Fatal(base)
		}
	}
	if _, err := comfyUIConfig(m, map[string]any{"url": "http://localhost:8188"}, false); err == nil {
		t.Fatal("model changed endpoint grant")
	}
	var calls atomic.Int64
	started := make(chan struct{}, 1)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		calls.Add(1)
		if r.URL.Path == "/prompt" {
			started <- struct{}{}
			<-r.Context().Done()
		} else {
			w.Write([]byte(`{}`))
		}
	}))
	defer remote.Close()
	m.Config = map[string]any{"url": remote.URL}
	s.Plugins.Save(m)
	for _, args := range []map[string]any{{"prompt": " "}, {"prompt": "test", "workflowJSON": "{}"}, {"prompt": "test", "workflowPath": filepath.Join(t.TempDir(), "outside.json")}, {"prompt": "test", "attachedImageIndex": 0}} {
		if _, err := comfyInvoke(t, s, scope, "comfyui_generate", args); err == nil {
			t.Fatal(args)
		}
	}
	if _, err := comfyInvoke(t, s, scope, "comfyui_generate_video", map[string]any{"prompt": "test"}); err == nil {
		t.Fatal("missing video workflow")
	}
	scope.Bot.Plugins = map[string]bool{"comfyui": false}
	if _, err := comfyInvoke(t, s, scope, "comfyui_generate", map[string]any{"prompt": "test"}); err == nil {
		t.Fatal("disabled bot invoked tool")
	}
	if calls.Load() != 0 {
		t.Fatal("validation contacted remote")
	}
	scope.Bot.Plugins = nil
	done := make(chan error, 1)
	go func() {
		_, err := s.Plugins.Invoke(context.Background(), scope, "comfyui_generate", json.RawMessage(`{"prompt":"test"}`))
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("generation did not start")
	}
	m.Enabled = false
	s.Plugins.Save(m)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disable did not cancel HTTP request")
	}
}

func TestComfyUIChatLoopIncludesImageAndKeepsVideoOutOfModel(t *testing.T) {
	for _, video := range []bool{false, true} {
		t.Run(map[bool]string{false: "image", true: "video"}[video], func(t *testing.T) {
			s := testServer(t)
			comfyMock(t, s, video, nil)
			var finalHistory []model.Message
			g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Messages []model.Message `json:"messages"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				for _, m := range req.Messages {
					if m.Role == "tool" {
						finalHistory = req.Messages
						stream(w, "Generated.", nil)
						return
					}
				}
				name := "comfyui_generate"
				if video {
					name = "comfyui_generate_video"
				}
				a, _ := json.Marshal(map[string]any{"prompt": "test", "workflowJSON": comfyWorkflow})
				stream(w, "", []domain.ToolCall{{ID: "generate", Type: "function", Function: domain.FunctionCall{Name: name, Arguments: string(a)}}})
			})
			c := chat(t, s, g)
			d, err := s.send(c.ID, SendRequest{Content: "generate media", Generation: g})
			if err != nil {
				t.Fatal(err)
			}
			waitDispatch(t, d)
			ms, err := s.Store.Messages(c.ID)
			if err != nil {
				t.Fatal(err)
			}
			var tool domain.Message
			for _, m := range ms {
				if m.Role == "tool" {
					tool = m
				}
			}
			if tool.Status != "completed" || len(tool.Attachments) != 1 {
				t.Fatal(tool)
			}
			check := func(history []model.Message) {
				t.Helper()
				images := 0
				for _, m := range history {
					for _, p := range m.Parts {
						if p.ImageURL != nil {
							images++
						}
						if p.VideoURL != nil {
							t.Fatal("generated video entered model context")
						}
					}
				}
				want := 1
				if video {
					want = 0
				}
				if images != want {
					t.Fatal("wrong generated image count", images, want)
				}
			}
			check(finalHistory)
			bot, _ := s.Bot("echo")
			history, err := s.history(bot, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			check(history)
		})
	}
}
