package services

const (
	ChatRuntimeEventName                = chatStreamEventName
	KanbanRuntimeEventName              = kanbanEventName
	FileChangesRuntimeEventName         = fileChangesEventName
	InlineCodeRuntimeEventName          = inlineCodePromptEventName
	AgentModeRuntimeEventName           = agentModeEventName
	TaskRuntimeEventName                = taskEventName
	HeartbeatRuntimeEventName           = heartbeatEventName
	LivenessRuntimeEventName            = livenessEventName
	WatchdogRuntimeEventName            = watchdogEventName
	WorkspaceTextSearchRuntimeEventName = workspaceTextSearchEventName
	LSPDiagnosticsEventName             = lspDiagnosticsEventName
)

const watchdogEventName = "echo:watchdog:event"
const lspDiagnosticsEventName = "echo:lsp:diagnostics"

type HeartbeatEvent struct {
	WorkspaceID string `json:"workspaceId"`
	Type        string `json:"type"` // "started", "stopped", "tick_no_eligible", "tick_no_budget", "budget_exceeded"
	Message     string `json:"message,omitempty"`
}

type WatchdogEvent struct {
	WorkspaceID string `json:"workspaceId"`
	CardID      string `json:"cardId,omitempty"`
	Type        string `json:"type"` // "started", "stopped", "check_complete", "repair_created"
	Message     string `json:"message,omitempty"`
}

type RuntimeEvent struct {
	Name string
	Data any
}

func SubscribeEvents(service *SystemService, buffer int) (<-chan RuntimeEvent, func()) {
	return service.subscribeEvents(buffer)
}

func (s *SystemService) subscribeEvents(buffer int) (<-chan RuntimeEvent, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan RuntimeEvent, buffer)

	s.eventMu.Lock()
	s.eventSeq++
	id := s.eventSeq
	s.eventSubscribers[id] = ch
	s.eventMu.Unlock()

	unsubscribe := func() {
		s.eventMu.Lock()
		if existing := s.eventSubscribers[id]; existing != nil {
			delete(s.eventSubscribers, id)
			close(existing)
		}
		s.eventMu.Unlock()
	}
	return ch, unsubscribe
}

func (s *SystemService) emitRuntimeEvent(name string, data any) {
	s.eventMu.Lock()
	subscribers := make([]chan RuntimeEvent, 0, len(s.eventSubscribers))
	for _, ch := range s.eventSubscribers {
		subscribers = append(subscribers, ch)
	}
	s.eventMu.Unlock()

	event := RuntimeEvent{Name: name, Data: data}
	for _, ch := range subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}
