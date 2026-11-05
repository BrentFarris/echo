package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/brent/echo/internal/calendar"
	"github.com/brent/echo/internal/domain"
)

func TestRunScheduleAPIUsesSavedSettingsAndServerLifetime(t *testing.T) {
	s := testServer(t)
	setRemotePassword(t, s)
	cookie := remoteLogin(t, s)
	g, started := controlledModel(t, s)
	bot, err := s.saveBot(domain.Bot{Handle: "worker", Name: "Worker"})
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := s.Calendar.Save(calendar.Schedule{BotID: bot.ID, Prompt: "saved manual calendar task", Recurrence: "every 5 minutes", Timezone: "UTC", Generation: g, Paused: true}, domain.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := "/schedules/" + schedule.ID + "/run"
	if response := remoteRequest(s, "POST", path, "", nil); response.Code != http.StatusUnauthorized {
		t.Fatal(response.Code, response.Body.String())
	}
	ctx, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequest("POST", "https://echo.example/api/v1"+path, nil).WithContext(ctx)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	cancel()
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	var result struct {
		Schedule   calendar.Schedule   `json:"schedule"`
		Occurrence calendar.Occurrence `json:"occurrence"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Schedule.Paused || result.Occurrence.Generation != g || result.Occurrence.BotID != bot.ID || !result.Occurrence.Manual || result.Occurrence.Status != "queued" || !result.Schedule.Next.Equal(result.Occurrence.IntendedAt.Add(5*time.Minute)) {
		t.Fatal(result)
	}
	var call modelInvocation
	select {
	case call = <-started:
		if call.bot != "worker" || call.prompt != schedule.Prompt {
			t.Fatal(call)
		}
	case <-time.After(10 * time.Second):
		jobs, _ := s.Calendar.Occurrences()
		chats, _ := s.Store.Chats()
		t.Fatalf("model did not start: occurrences=%+v chats=%+v", jobs, chats)
	}
	if busy := remoteRequest(s, "POST", path, "", cookie); busy.Code != http.StatusConflict {
		t.Fatal(busy.Code, busy.Body.String())
	}
	call.finish <- "done"
	eventually(t, func() bool {
		var o calendar.Occurrence
		return s.Store.Get("calendar:occurrences", result.Occurrence.ID, &o) == nil && o.Status == "completed"
	})
	chats, err := s.Store.Chats()
	if err != nil || len(chats) != 1 || chats[0].Generation != g {
		t.Fatal(chats, err)
	}
	messages, err := s.messages(chats[0].ID)
	if err != nil || len(messages) < 2 || messages[0].Content != schedule.Prompt || messages[len(messages)-1].BotID != bot.ID {
		t.Fatal(messages, err)
	}
}

func TestRunScheduleAPIErrors(t *testing.T) {
	s := testServer(t)
	setRemotePassword(t, s)
	cookie := remoteLogin(t, s)
	if response := remoteRequest(s, "POST", "/schedules/missing/run", "", cookie); response.Code != http.StatusNotFound {
		t.Fatal(response.Code, response.Body.String())
	}
	schedule, err := s.Calendar.Save(calendar.Schedule{BotID: "missing-bot", Prompt: "task", Recurrence: "daily 17:00", Timezone: "UTC"}, domain.Now())
	if err != nil {
		t.Fatal(err)
	}
	if response := remoteRequest(s, "POST", "/schedules/"+schedule.ID+"/run", "", cookie); response.Code != http.StatusNotFound {
		t.Fatal(response.Code, response.Body.String())
	}
	bot, err := s.Bot("echo")
	if err != nil {
		t.Fatal(err)
	}
	bot.Plugins["calendar"] = false
	if _, err = s.saveBot(bot); err != nil {
		t.Fatal(err)
	}
	schedule.BotID = bot.ID
	schedule, err = s.Calendar.Save(schedule, domain.Now())
	if err != nil {
		t.Fatal(err)
	}
	if response := remoteRequest(s, "POST", "/schedules/"+schedule.ID+"/run", "", cookie); response.Code != http.StatusConflict {
		t.Fatal(response.Code, response.Body.String())
	}
	jobs, err := s.Calendar.Occurrences()
	if err != nil || len(jobs) != 0 {
		t.Fatal(jobs, err)
	}
}
