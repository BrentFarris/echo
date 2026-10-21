---
name: decision-model-integration
description: 'How to add a new decision-model feature (Phase 5+ backlog: clarifying-question gate, research trigger, chat titles, vision routing, risk scoring, kanban triage, notifications, compression timing) following the established Phase 2-4 pattern.'
triggers:
    - decision model
    - decisionModel
    - internal/decision
    - decision_ feature file
    - fail-open classification
---

# Decision-model feature integration (Echo)

Echo has an isolated decision-model client in `internal/decision` (fluent builder: `client.Ask(ctx, state).Choice(...)/Noul(...)/Score(...).Do()`; nil/zero client is disabled and returns `ErrDisabled`). The server holds it as `Server.decisionModel`, built in `initLLM()` from `Settings.DecisionModelURL`/`DecisionModelName`. Each feature gate is a bool on `llm.Settings` (`DecisionModelLLMTool`, `DecisionModelAgentMode`, `DecisionModelTemperature`, `DecisionModelToolFiltering`) with a matching checkbox in `web/js/views/settings.js` (state.decisionModel).

## Adding a new feature (Phase 5+ pattern)

1. **One file per feature** in `internal/server`: `decision_<feature>.go` (see `decision_temperature.go`, `decision_agent_mode.go`, `decision_tool_filter.go`). Keep the chat pipeline seeing a single call.
2. **Contract**: method on `*Server` that returns `(result, ok bool)`; `ok=false` on feature disabled, endpoint unconfigured, timeout, endpoint error, or unusable answer — callers keep their normal behavior (fail-open). Use a 2s `context.WithTimeout(context.Background(), ...)`; cap the state string by runes (~2000) so long prompts stay cheap.
3. **Call site**: classify in `send`/`regenerateMessage` BEFORE `session.mu.Lock()` (a slow endpoint must not stall session events), then pass the result into `session.run` as a parameter if later rounds need it. Goal mode is exempt from per-prompt overrides (autonomous runs keep full control; the decision model can never select Goal).
4. **Visibility**: append a `decision/<feature>` trajectory entry and add a `decision<Feature>` key to the `turn_started` event when the override applied.
5. **Tests**: mirror `decision_temperature_test.go` / `decision_tool_filter_test.go` — `httptest` fake endpoint answering the builder's questions, `s.llmSettings.<Flag> = true`, `s.decisionModel = decision.New(server.URL, "")`, capturing streamer (`temperatureCapturingStreamer`), `sendChatAndWaitForTurn` helper; plus a fail-open test with `decision.New("http://127.0.0.1:1", "")`.

## Tool-filtering specifics (Phase 4)

- Category map + always-keep set live in `decision_tool_filter.go`; unclassifiable/plugin tools are never filtered. `filterToolSchemaByCategories(schema, kept)` is applied to every round's `toolSchema` in `run` (nil kept = no-op). A model calling a filtered tool gets a soft `tool_filtered` `ExecutionResult` (dispatch case in `run`), not a hard failure.
- Multi-select is done with one `noul` question per category (`needs_<category>`), keep threshold 0.5, missing answers keep the category.
