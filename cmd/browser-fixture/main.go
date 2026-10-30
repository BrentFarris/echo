// browser-fixture is only an acceptance-test server. It is not embedded in echo.exe.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/brent/echo/internal/calendar"
	"github.com/brent/echo/internal/credentials"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/plugins"
	"github.com/brent/echo/internal/server"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// Acceptance profiles never write test credentials to the user's host vault.
type fixtureSecrets struct {
	sync.Mutex
	values map[string]string
}

func (s *fixtureSecrets) Available(context.Context) bool { return true }
func (s *fixtureSecrets) Get(_ context.Context, key string) (string, error) {
	s.Lock()
	defer s.Unlock()
	v, ok := s.values[key]
	if !ok {
		return "", credentials.ErrSecretNotFound
	}
	return v, nil
}
func (s *fixtureSecrets) Set(_ context.Context, key, value string) error {
	s.Lock()
	defer s.Unlock()
	s.values[key] = value
	return nil
}
func (s *fixtureSecrets) Delete(_ context.Context, key string) error {
	s.Lock()
	defer s.Unlock()
	delete(s.values, key)
	return nil
}

func main() {
	data := flag.String("data", "../.test-data/browser", "test data")
	port := flag.Int("port", 3742, "test HTTP port")
	name := flag.String("name", "desktop", "test Swarm name")
	password := flag.String("password", "", "test owner password")
	flag.Parse()
	guest, e := os.ReadFile("../internal/plugins/guest/builtin.wasm")
	if e != nil {
		log.Fatal(e)
	}
	app, e := server.New(*data, os.DirFS("dist"), guest)
	if e != nil {
		log.Fatal(e)
	}
	defer app.Close()
	app.Secrets = &fixtureSecrets{values: map[string]string{}}
	if *password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(*password), bcrypt.MinCost)
		if err != nil {
			log.Fatal(err)
		}
		if err = app.Store.Put("auth", "password", string(hash)); err != nil {
			log.Fatal(err)
		}
	}
	if err := app.Swarm.Configure(*name, ""); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", app.Handler())
	// Network acceptance endpoints are only part of this test fixture binary.
	mux.HandleFunc("/__fixtures/network/http", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"method": r.Method, "cookie": r.Header.Get("Cookie"), "machine": *name})
	})
	mux.HandleFunc("/__fixtures/network/asset.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, `document.body.dataset.networkAsset='loaded'`)
	})
	mux.HandleFunc("/__fixtures/network/socket", func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if conn.WriteMessage(kind, data) != nil {
				return
			}
		}
	})
	mux.HandleFunc("POST /__fixtures/app", func(w http.ResponseWriter, r *http.Request) {
		var manifest domain.PluginManifest
		if err := json.NewDecoder(r.Body).Decode(&manifest); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		installed, err := app.Plugins.Install(manifest, nil)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(installed)
	})
	mux.HandleFunc("POST /__fixtures/renderer", func(w http.ResponseWriter, r *http.Request) {
		template, _ := plugins.Template("renderer")
		m := template.(map[string]any)["manifest"].(domain.PluginManifest)
		m.ID = "test-renderer"
		m.Name = "Test renderer"
		m.Views[0].HTML = `<script>let nonce;addEventListener('message',e=>{if(e.source!==parent)return;if(e.data.type==='init'){nonce=e.data.nonce;return;}if(e.data.type!=='render'||e.data.nonce!==nonce)return;let isolated=false;try{parent.document.body.dataset.compromised='yes'}catch{isolated=true}const escaped=String(e.data.text).replace(/[&<>]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;'}[c]));parent.postMessage({type:'rendered',nonce,id:e.data.id,html:'<h2>'+(isolated?'Isolated renderer':'BROKEN')+'</h2><p>'+escaped+'</p><img src=x onerror="window.compromised=true"><a href="javascript:alert(1)">bad link</a><svg onload="window.compromised=true"></svg>'},'*')});parent.postMessage({type:'renderer-ready'},'*')</script>`
		if _, e := app.Plugins.Install(m, nil); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("POST /__fixtures/missed", func(w http.ResponseWriter, r *http.Request) {
		generation := domain.Generation{EndpointID: "test", Model: "echo-test", Temperature: .6}
		schedule, e := app.Calendar.Save(calendar.Schedule{BotID: "echo", Prompt: "Report fixture", Recurrence: "daily 15:00", Timezone: "America/Chicago", Generation: generation}, time.Now())
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		for i := 1; i <= 3; i++ {
			id := domain.ID()
			app.Store.Put("calendar:occurrences", id, calendar.Occurrence{ID: id, ScheduleID: schedule.ID, BotID: "echo", Prompt: fmt.Sprintf("Missed report %d", i), IntendedAt: time.Now().Add(-time.Duration(i) * 24 * time.Hour), Generation: generation, Status: "missed"})
		}
		fmt.Fprint(w, `{"ok":true}`)
	})
	log.Fatal(http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *port), mux))
}
