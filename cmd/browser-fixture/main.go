// browser-fixture is only an acceptance-test server. It is not embedded in echo.exe.
package main

import (
	"flag"
	"fmt"
	"github.com/brent/echo/internal/calendar"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/plugins"
	"github.com/brent/echo/internal/server"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	data := flag.String("data", "../.test-data/browser", "test data")
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
	mux := http.NewServeMux()
	mux.Handle("/", app.Handler())
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
	log.Fatal(http.ListenAndServe("127.0.0.1:3742", mux))
}
