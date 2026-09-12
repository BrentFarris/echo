# UI evaluation

The default tests exercise targeting and execution without contacting a model:

```bash
go test ./internal/sandbox ./internal/tools ./internal/server
cd web
npm ci
npx playwright install chromium
cd ..
node --test sandbox/images/runtime/browser-ui.test.mjs
```

Build the runtime and gateway as described in [sandbox.md](sandbox.md), then run the real Linux acceptance test:

```bash
ECHO_SANDBOX_INTEGRATION=1 go test ./internal/sandbox -run TestDockerIntegration -count=1 -v
```

These checks cover browser reference/context handling, action cancellation, iframe/shadow targets, pagination, intrinsic verification, native GTK controls, moved/closed windows, deduplication, observation ownership, image transforms, stale visual regions, custom permissions and planner/specialist media separation. They establish harness behavior; they do not measure an LLM's task success rate.

## Optional model comparison

`TestUILiveEvaluation` creates a disposable Docker workspace and executes 30 resettable cases three times per mode (270 runs). It uses the production tool schemas and manager for semantic control, the selected reasoning model for all modes, and an independently configured specialist for visual assistance.

| Mode | Targeting |
| --- | --- |
| `coordinates` | The reasoning model receives screenshots and requests physical coordinates. Requires an image-capable reasoning endpoint. |
| `semantic` | The reasoning model uses observe/act/verify with browser and native accessibility. |
| `semantic+vision` | Semantic control plus bounded `ui_locate` and explicit visual verification. |

The coordinate baseline shares the new runtime's cancellation and action-ID journal. It isolates the grounding choice; it is not a frozen reproduction of the old implementation. The fixtures comprise 20 browser cases (four variants each of repeated table controls with unrelated updates, forms, iframes, open shadow roots and inaccessible canvas targets) and ten native GTK form variants. Broaden these with representative user workflows before choosing a production model or claiming general reliability.

The harness reads OpenAI-compatible local endpoints explicitly from environment variables and never uses the user's saved conversations. Run it only when model usage is intended:

```bash
ECHO_UI_LIVE_EVAL=1 \
ECHO_UI_EVAL_ENDPOINT=http://127.0.0.1:11434/v1 \
ECHO_UI_EVAL_MODEL=your-reasoning-model \
ECHO_UI_EVAL_VISION_ENDPOINT=http://127.0.0.1:11434/v1 \
ECHO_UI_EVAL_VISION_MODEL=your-image-capable-model \
ECHO_UI_EVAL_OUTPUT=/absolute/new/path/ui-evaluation.jsonl \
go test ./internal/server -run '^TestUILiveEvaluation$' -count=1 -timeout=4h -v
```

On PowerShell, set the same variables using `$env:NAME='value'` before running `go test`. Optional `ECHO_UI_EVAL_API_KEY` and `ECHO_UI_EVAL_VISION_API_KEY` supply endpoint credentials; the chat key is reused only when both endpoint URLs are identical. `ECHO_SANDBOX_RUNTIME_IMAGE` and `ECHO_SANDBOX_GATEWAY_IMAGE` override the default `:dev` images. Output must name a new file; existing reports are never overwritten. Omit the output variable to use the test temporary directory (which is removed when tests finish).

Start with `ECHO_UI_EVAL_CASE=browser-form-0` and `ECHO_UI_EVAL_REPEATS=1` for a small run. Cases are matched by ID substring. Every run is limited to twenty model rounds and three minutes. A fixture oracle reads the actual submitted values and count; model claims do not determine success. JSONL records completion, wrong selection, duplicate submissions, action count, failed tool calls, latency, reasoning and specialist tokens, and endpoint errors. `retries` counts repeated identical action requests (excluding observation IDs) plus automatic semantic replacement recovery. Interpret it with the task context: repeated keys can be intentional. A missing or incompatible endpoint is recorded as a failure, not silently switched to another model.

Compare completion first, then wrong targets and duplicates. Inspect failures by case family before tuning speed or token cost. Keep the reasoning model, temperature and fixture versions fixed across comparisons. Report the distribution across all repeats, including timeouts and unsupported cases. No benchmark score is bundled with this change; a real model evaluation must be run against the chosen endpoints.
