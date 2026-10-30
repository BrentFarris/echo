import { escapeHTML as esc } from './helpers';
import type { Endpoint } from './types';

const defaults: Partial<Endpoint> = { temperature: .6, topK: 20, topP: .95, minP: 0, contextTokens: 32768, maxTokens: 2048, frequencyPenalty: 0, presencePenalty: 0, repetitionPenalty: 1, compressionThreshold: 70, requestTimeoutSeconds: 600, streamIdleTimeoutSeconds: 120, thinkingTokenBudget: -1 };
const numbers: { key: keyof Endpoint; label: string; min: number; max?: number; step?: number }[] = [
  { key: 'temperature', label: 'Temperature', min: 0, max: 2, step: .01 },
  { key: 'topK', label: 'Top K', min: 0 },
  { key: 'topP', label: 'Top P', min: 0, max: 1, step: .01 },
  { key: 'minP', label: 'Min P', min: 0, max: 1, step: .01 },
  { key: 'contextTokens', label: 'Context Length', min: 1 },
  { key: 'maxTokens', label: 'Max Tokens', min: 1 },
  { key: 'frequencyPenalty', label: 'Frequency Penalty', min: -2, max: 2, step: .01 },
  { key: 'presencePenalty', label: 'Presence Penalty', min: -2, max: 2, step: .01 },
  { key: 'repetitionPenalty', label: 'Repetition Penalty', min: 0, max: 100, step: .01 },
  { key: 'requestTimeoutSeconds', label: 'Request Timeout (seconds)', min: 1, max: 86400 },
  { key: 'streamIdleTimeoutSeconds', label: 'Stream Idle Timeout (seconds)', min: -1, max: 86400 },
  { key: 'thinkingTokenBudget', label: 'Thinking Token Budget', min: -1 },
];
export function endpointForm(endpoint?: Endpoint, cloud = false) {
  const value = (key: keyof Endpoint) => endpoint ? endpoint[key] : cloud && ['topK', 'minP', 'repetitionPenalty', 'thinkingTokenBudget'].includes(key) ? undefined : defaults[key];
  const number = (field: typeof numbers[number]) => `<label>${field.label}<input name="${field.key}" type="number" min="${field.min}" ${field.max === undefined ? '' : `max="${field.max}"`} step="${field.step || 1}" value="${esc(value(field.key))}" placeholder="Provider default"></label>`;
  const threshold = `<label class="compression-setting"><span>Compression Threshold <output>${endpoint?.compressionThreshold || 70}%</output></span><input name="compressionThreshold" aria-label="Compression Threshold" type="range" min="1" max="99" value="${endpoint?.compressionThreshold || 70}"></label>`;
  return `<form class="form endpoint-form">
    <div class="endpoint-identity"><label>Name<input name="name" required value="${esc(endpoint?.name)}" placeholder="LM Studio"></label><label>Model<input name="model" required value="${esc(endpoint?.model)}" placeholder="Model identifier"></label>
    <label class="field-wide">Endpoint URL<input name="url" required type="url" value="${esc(endpoint?.url || 'http://localhost:1234/v1')}"></label></div>
    <div class="endpoint-grid">${numbers.slice(0, 5).map(number).join('')}${threshold}${numbers.slice(5, 11).map(number).join('')}
    <label>Reasoning Effort<select name="reasoningEffort">${['', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'].map(v => `<option value="${v}" ${endpoint?.reasoningEffort === v ? 'selected' : ''}>${v ? v[0].toUpperCase() + v.slice(1) : 'Default'}</option>`).join('')}</select></label>${number(numbers[11])}</div>
    <p class="subtle">Leave optional sampling fields blank to use provider defaults. The chat's temperature and thinking controls override endpoint defaults for that dispatch.</p>
    <p class="subtle">Stream idle timeout resets whenever provider data or an SSE heartbeat arrives. Set it to -1 to disable inactivity detection.</p>
    <p class="subtle">A named reasoning effort is sent as <code>reasoning_effort</code> and takes precedence over the local-model thinking-token budget. Unsupported values return the provider's request error without retrying or stepping down.</p>
    <label class="check"><input name="compact" type="checkbox" ${endpoint?.compact !== false ? 'checked' : ''}> Automatically compact model context; keep full transcript</label>
    <p class="subtle">This controls proactive compression. If a request exceeds the model's context window, Echo always tries to compress and continue. Tool results that still cannot fit are omitted from model context, while the full transcript stays available.</p>
    <label>API key<input name="key" type="password" autocomplete="new-password" placeholder="${endpoint?.credential ? 'Leave blank to keep saved credential' : 'Optional'}"><small>Saved in Windows Credential Manager.</small></label>
    <button class="primary">Save connection</button></form>`;
}
export function endpointValues(form: HTMLFormElement): Partial<Endpoint> {
  const data = new FormData(form), values: Record<string, unknown> = { name: data.get('name'), url: data.get('url'), model: data.get('model'), provider: data.get('provider'), compact: data.has('compact'), reasoningEffort: data.get('reasoningEffort'), compressionThreshold: Number(data.get('compressionThreshold')) };
  for (const { key } of numbers) {
    const input = form.elements.namedItem(key) as HTMLInputElement;
    if (input.value !== '') values[key] = Number(input.value);
  }
  return values;
}
