// mock-model is a deterministic compatible provider for browser acceptance tests.
// It is never used by the production server and does not require model downloads.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/brent/echo/internal/model"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type message = model.Message

func main() {
	http.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"echo-test"}]}`) })
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Messages) == 0 {
			http.Error(w, "bad request", 400)
			return
		}
		last := req.Messages[len(req.Messages)-1]
		user := ""
		for _, m := range req.Messages {
			if m.Role == "user" {
				user = m.Content
			}
		}
		lower := strings.ToLower(user)
		if strings.Contains(lower, "multitasking hold") {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Holding this request.\"}}]}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if strings.Contains(lower, "file change report") {
			w.Header().Set("Content-Type", "text/event-stream")
			delta := map[string]any{"content": "Finished the file change report."}
			if last.Role != "tool" {
				marker := "Change report folder: "
				if !strings.Contains(user, marker) {
					http.Error(w, "missing fixture folder", 400)
					return
				}
				root := strings.TrimSpace(strings.SplitN(user, marker, 2)[1])
				calls := []any{}
				for i, item := range []struct{ tool, path, content string }{{"file_write", "created.txt", "new"}, {"file_write", "created.txt", "updated"}, {"file_write", "edited.txt", "updated"}, {"file_delete", "deleted.txt", ""}} {
					args := map[string]any{"path": filepath.Join(root, item.path)}
					if item.tool == "file_write" {
						args["content"] = item.content
					}
					encoded, _ := json.Marshal(args)
					calls = append(calls, map[string]any{"index": i, "id": fmt.Sprintf("change-%d", i), "function": map[string]any{"name": item.tool, "arguments": string(encoded)}})
				}
				delta = map[string]any{"tool_calls": calls}
			}
			data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
			return
		}
		if strings.Contains(lower, "work disclosure") {
			w.Header().Set("Content-Type", "text/event-stream")
			emit := func(delta map[string]any) {
				data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta}}})
				fmt.Fprintf(w, "data: %s\n\n", data)
				w.(http.Flusher).Flush()
			}
			if last.Role != "tool" {
				emit(map[string]any{"reasoning_content": "First inspect the available bots.", "content": "I will inspect the configuration before preparing the report.", "tool_calls": []any{map[string]any{"index": 0, "id": "work-list", "function": map[string]any{"name": "bot_list", "arguments": "{}"}}}})
			} else {
				emit(map[string]any{"reasoning_content": "The configuration is ready; verify the final report."})
				for _, chunk := range []string{"Done. ", "The workspace ", "report is ready.\n\n", "- Checked the configuration.\n", "- Preserved the full work history."} {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(400 * time.Millisecond):
					}
					emit(map[string]any{"content": chunk})
				}
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		if strings.Contains(lower, "slow stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, word := range []string{"A ", "stream ", "that ", "survives ", "a ", "browser ", "reload."} {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(400 * time.Millisecond):
				}
				data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": word}}}})
				fmt.Fprintf(w, "data: %s\n\n", data)
				w.(http.Flusher).Flush()
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		system := req.Messages[0].Content
		content := "Completed the requested work."
		if len(last.Parts) > 0 {
			content = fmt.Sprintf("Received %d attachment parts.", len(last.Parts))
		}
		tool := ""
		args := map[string]any{}
		if last.Role == "tool" {
			previous := ""
			for i := len(req.Messages) - 2; i >= 0; i-- {
				if len(req.Messages[i].Calls) > 0 {
					previous = req.Messages[i].Calls[0].Function.Name
					break
				}
			}
			switch previous {
			case "bot_delete":
				content = "Deleted kaiju."
			case "file_read":
				var result map[string]any
				json.Unmarshal([]byte(last.Content), &result)
				tool = "file_write"
				path := strings.TrimSpace(strings.Split(user, "Referenced path (read with file tools): ")[1])
				args = map[string]any{"path": path, "content": fmt.Sprint(result["content"]) + "\n// transform gizmo\n", "revision": result["revision"]}
			case "file_write":
				content = "Added the transform gizmo."
			case "bot_save":
				content = "Created the bot."
			case "bot_list":
				content = "## Available bots\n\nYour configured bots are ready.\n\n" + last.Content
			case "schedule_save":
				content = "Scheduled the report. It belongs to the bot and survives chat deletion."
			case "plugin_install":
				content = "Created your draggable calculator plugin."
			case "bot_send":
				tool = "bot_wait"
				var result map[string]any
				json.Unmarshal([]byte(last.Content), &result)
				args["requestId"] = result["id"]
			case "bot_wait":
				content = "Wired up the shield REST API using the backend handoff."
			case "bot_reply":
				content = "The backend shield endpoint is ready."
			default:
				content = "Tool completed. " + last.Content
			}
		} else if strings.Contains(lower, "hostile markdown") {
			content = "## Safe heading\n<img src=x onerror=\"window.compromised=true\"><script>window.compromised=true</script><a href='javascript:alert(1)'>unsafe link</a>"
		} else if strings.Contains(lower, "transform gizmo") && strings.Contains(user, "Referenced path (read with file tools): ") {
			tool = "file_read"
			args = map[string]any{"path": strings.TrimSpace(strings.Split(user, "Referenced path (read with file tools): ")[1])}
		} else if strings.Contains(lower, "delete the kaiju bot") {
			tool = "bot_delete"
			args = map[string]any{"id": "kaiju"}
		} else if strings.HasPrefix(user, "Handoff request ") {
			parts := strings.Fields(user)
			tool = "bot_reply"
			args = map[string]any{"requestId": parts[2], "content": "POST /api/shield accepts {quantity: number}; response {balance: number}."}
		} else if strings.Contains(lower, "work with") && strings.Contains(system, "@kaiju.") {
			tool = "bot_send"
			args = map[string]any{"to": "backend", "content": "Please provide the shield REST API contract.", "requestId": "shield-api"}
		} else if strings.Contains(lower, "work with") && strings.Contains(system, "@backend.") {
			time.Sleep(100 * time.Millisecond)
			content = "Backend is ready to collaborate."
		} else if strings.Contains(lower, "calculator") {
			tool = "plugin_install"
			args = map[string]any{"id": "calculator", "name": "Calculator", "html": `<!doctype html><style>body{padding:20px;font:14px system-ui}input{width:90px;padding:8px}button{padding:8px}output{display:block;margin-top:20px;font-size:28px}</style><h2>Calculator</h2><label>A <input id="a" type="number" value="2"></label> <label>B <input id="b" type="number" value="3"></label><p><button id="add">Add</button> <button id="multiply">Multiply</button></p><output id="result">0</output><script>document.querySelector('#add').onclick=()=>document.querySelector('#result').textContent=Number(document.querySelector('#a').value)+Number(document.querySelector('#b').value);document.querySelector('#multiply').onclick=()=>document.querySelector('#result').textContent=Number(document.querySelector('#a').value)*Number(document.querySelector('#b').value);</script>`}
		} else if strings.Contains(lower, "every day") || strings.Contains(lower, "schedule") {
			tool = "schedule_save"
			args = map[string]any{"prompt": "Create a report showing how many game tests pass", "recurrence": "daily 15:00", "timezone": "America/Chicago"}
		} else if strings.Contains(lower, "create a bot") {
			re := regexp.MustCompile(`(?i)named\s+["']?([a-z][a-z0-9_-]*)`)
			match := re.FindStringSubmatch(user)
			name := "kaiju"
			if len(match) > 1 {
				name = match[1]
			}
			tool = "bot_save"
			args = map[string]any{"handle": name, "name": name, "prompt": "Help with this workspace."}
			if strings.Contains(lower, "multitasking enabled") {
				args["enableMultitasking"] = true
			}
			if strings.Contains(lower, "multitasking disabled") {
				args["enableMultitasking"] = false
			}
		} else if strings.Contains(lower, "list my bots") {
			tool = "bot_list"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		delta := map[string]any{"content": content}
		if tool != "" {
			arguments, _ := json.Marshal(args)
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call-" + tool, "function": map[string]any{"name": tool, "arguments": string(arguments)}}}}
		}
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": "stop"}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:3743", nil))
}
