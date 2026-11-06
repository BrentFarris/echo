import { escapeHTML as esc } from './helpers';
import { thinkingLevels, generationThinkingLevels } from './generationSettings';
import { externalAgentNotice, isExternalAgent } from './externalAgents';
import type { Bot, Endpoint } from './types';
import './botModelSettings.css';

export function botModelSummary(bot: Bot, endpoints: Endpoint[]): string {
  const endpoint = endpoints.find(e => e.id === bot.modelOverride?.endpointId);
  const model = bot.modelOverride ? `${endpoint?.name || 'Unavailable connection'} · ${bot.modelOverride.model || endpoint?.model || 'CLI default'}` : 'Use chat model';
  return model + (bot.thinkingOverride ? ` · Thinking: ${bot.thinkingOverride}` : ' · Use chat thinking');
}

export function botModelSettingsMarkup(bot: Bot | undefined, endpoints: Endpoint[], owner = false): string {
  const missing = bot?.modelOverride && !endpoints.some(e => e.id === bot.modelOverride!.endpointId);
  return `<fieldset data-bot-model-settings><legend>Model and thinking</legend>
    <label>Model connection<select name="botModelEndpoint" aria-label="Model connection"><option value="">Use chat model</option>${endpoints.map(e => `<option value="${esc(e.id)}">${esc(e.name)}${isExternalAgent(e) ? ' · CLI' : ''}</option>`).join('')}${missing ? `<option value="${esc(bot!.modelOverride!.endpointId)}">Unavailable connection</option>` : ''}</select></label>
    <label>Model identifier<input name="botModelIdentifier" list="bot-model-options" placeholder="Connection default"></label><datalist id="bot-model-options"></datalist>
    <p class="subtle" data-bot-external-notice hidden>${externalAgentNotice}</p>
    ${owner ? `<div data-cli-sharing hidden><label class="check"><input type="checkbox" name="sharePinnedCLI" aria-describedby="shared-cli-hint">Allow peers to run this bot’s CLI without Full access</label>
    <small id="shared-cli-hint">Peers who can run this bot can give the CLI tasks that execute with your OS account’s permissions.</small></div>` : ''}
    <label>Thinking<select name="botThinking" aria-label="Thinking"><option value="">Use chat thinking</option>${thinkingLevels.filter(Boolean).map(level => `<option value="${level}">${level[0].toUpperCase() + level.slice(1)}</option>`).join('')}</select></label>
    <small>Overrides apply to new runs. Unset choices use the chat settings.</small></fieldset>`;
}

export function mountBotModelSettings(form: HTMLFormElement, bot: Bot | undefined, endpoints: Endpoint[], discover: (endpoint: Endpoint) => Promise<string[]>): void {
  const connection = form.elements.namedItem('botModelEndpoint') as HTMLSelectElement;
  const identifier = form.elements.namedItem('botModelIdentifier') as HTMLInputElement;
  const thinking = form.elements.namedItem('botThinking') as HTMLSelectElement;
  connection.value = bot?.modelOverride?.endpointId || '';
  identifier.value = bot?.modelOverride?.model || '';
  thinking.value = bot?.thinkingOverride || '';
  const shared = form.querySelector<HTMLInputElement>('[name=swarmAvailable]');
  const grant = form.querySelector<HTMLInputElement>('[name=sharePinnedCLI]');
  if (grant) grant.checked = !!connection.value && bot?.sharedCliEndpointId === connection.value;
  const renderSharing = () => {
    if (!grant) return;
    const visible = shared?.checked && isExternalAgent(endpoints.find(e => e.id === connection.value));
    grant.closest<HTMLElement>('[data-cli-sharing]')!.hidden = !visible;
    grant.disabled = !visible;
    if (!visible) grant.checked = false;
  };
  shared?.addEventListener('change', renderSharing);
  const render = () => {
    const endpoint = endpoints.find(e => e.id === connection.value);
    const selectedThinking = thinking.value, levels = generationThinkingLevels(endpoint);
    thinking.innerHTML = `<option value="">Use chat thinking</option>${levels.filter(Boolean).map(level => `<option value="${level}">${level[0].toUpperCase() + level.slice(1)}</option>`).join('')}${selectedThinking && !levels.includes(selectedThinking) ? `<option value="${esc(selectedThinking)}">${esc(selectedThinking)} (unsupported)</option>` : ''}`;
    thinking.value = selectedThinking;
    identifier.disabled = !connection.value;
    identifier.placeholder = isExternalAgent(endpoint) ? 'CLI default' : 'Connection default';
    form.querySelector<HTMLElement>('[data-bot-external-notice]')!.hidden = !isExternalAgent(endpoint);
    const models = form.querySelector<HTMLDataListElement>('#bot-model-options')!;
    models.innerHTML = '';
    if (endpoint && !isExternalAgent(endpoint)) void discover(endpoint).then(values => {
      if (connection.value === endpoint.id) models.innerHTML = values.map(value => `<option value="${esc(value)}"></option>`).join('');
    }).catch(() => { /* Manual identifiers remain available. */ });
  };
  connection.onchange = () => {
    identifier.value = '';
    if (grant) grant.checked = false;
    renderSharing();
    render();
  };
  renderSharing();
  render();
}

export function botModelSettingsValues(form: HTMLFormElement): Pick<Bot, 'modelOverride' | 'thinkingOverride' | 'sharedCliEndpointId'> {
  const data = new FormData(form), endpointId = String(data.get('botModelEndpoint') || '');
  const settings: Pick<Bot, 'modelOverride' | 'thinkingOverride' | 'sharedCliEndpointId'> = {
    modelOverride: endpointId ? { endpointId, model: String(data.get('botModelIdentifier') || '').trim() } : null,
    thinkingOverride: String(data.get('botThinking') || '') || null,
  };
  if (form.elements.namedItem('sharePinnedCLI')) {
    settings.sharedCliEndpointId = data.has('sharePinnedCLI') ? endpointId : '';
  }
  return settings;
}
