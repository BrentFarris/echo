---
name: max-chat-orchestration-rounds-setting
description: Max chat orchestration rounds setting controls how many assistant/tool call rounds a chat turn can execute before failing. Default 64, 0 disables the limit.
triggers:
    - chat rounds limit
    - orchestration rounds
    - max chat rounds
    - tool call rounds
    - chat.go orchestration
    - settings maxChatOrchestrationRounds
---

## Max Chat Orchestration Rounds Setting

Controls the maximum number of assistant/tool call rounds per chat turn before the chat fails with an error.

### Backend

- **Setting field**: `MaxChatOrchestrationRounds int` in `llm.Settings` (`internal/llm/settings.go`)
- **Default**: `DefaultMaxChatOrchestrationRounds = 64`
- **Normalization**: clamped to ≥ 0; value of 0 means "no limit"
- **Enforcement**: `internal/services/chat.go` in `runChatTurnWithHistory` — checks `settings.MaxChatOrchestrationRounds > 0 && orchestrationRounds > settings.MaxChatOrchestrationRounds` at the top of each loop iteration

### Frontend

- **UI label**: "Max chat rounds" in the Programming settings section, rendered after "Research agent concurrency"
- **Input**: numeric `<input>` with `name="maxChatOrchestrationRounds"`, `min="0"`, `step="1"`
- **Numeric field handler**: added to `numericFields` Set in `handleSettingsInput()` so values are parsed as numbers
- **TypeScript binding**: `maxChatOrchestrationRounds: number` in `frontend/wailsjs/go/models.ts` Settings class (both property declaration and constructor mapping)

### Persistence

Saved as a global setting via the existing `SaveSettings` flow — persists to `Echo/state.json` alongside other LLM settings.
