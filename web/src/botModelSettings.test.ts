import { expect, test, vi } from 'vitest';
import { botModelSettingsMarkup, botModelSettingsValues, botModelSummary, mountBotModelSettings } from './botModelSettings';
import { generationThinkingLevels } from './generationSettings';
import type { Bot, Endpoint } from './types';

const http = { id: 'http', name: 'HTTP', model: 'default' } as Endpoint;
const cli = { id: 'cli', name: 'Codex', kind: 'codex-cli' } as Endpoint;
const claude = { id: 'claude', name: 'Claude', kind: 'claude-code-cli' } as Endpoint;
function setup(bot?: Bot, owner = false) {
  const form = document.createElement('form');
  form.innerHTML = botModelSettingsMarkup(bot, [http, cli, claude], owner) + (owner ? '<input name="swarmAvailable" type="checkbox" checked>' : '');
  const discover = vi.fn(async () => ['discovered']); mountBotModelSettings(form, bot, [http, cli, claude], discover);
  return { form, discover, connection: form.elements.namedItem('botModelEndpoint') as HTMLSelectElement, identifier: form.elements.namedItem('botModelIdentifier') as HTMLInputElement, thinking: form.elements.namedItem('botThinking') as HTMLSelectElement };
}
test('only the owner can grant a shared CLI and changing the pin requires a new opt-in', () => {
  const bot = { modelOverride: { endpointId: 'cli' }, sharedCliEndpointId: 'cli' } as Bot;
  const remote = setup(bot);
  expect(remote.form.elements.namedItem('sharePinnedCLI')).toBeNull();
  expect(botModelSettingsValues(remote.form)).not.toHaveProperty('sharedCliEndpointId');
  const { form, connection } = setup(bot, true);
  document.body.append(form);
  const grant = form.elements.namedItem('sharePinnedCLI') as HTMLInputElement;
  const shared = form.elements.namedItem('swarmAvailable') as HTMLInputElement;
  const group = form.querySelector<HTMLElement>('[data-cli-sharing]')!;
  expect(grant.checked).toBe(true);
  expect(group.hidden).toBe(false);
  expect(botModelSettingsValues(form).sharedCliEndpointId).toBe('cli');
  connection.value = 'claude'; connection.dispatchEvent(new Event('change'));
  expect(grant.checked).toBe(false);
  grant.checked = true;
  expect(botModelSettingsValues(form).sharedCliEndpointId).toBe('claude');
  shared.click();
  expect(group.hidden).toBe(true);
  expect(botModelSettingsValues(form).sharedCliEndpointId).toBe('');
  shared.click();
  expect(grant.checked).toBe(false);
  for (const id of ['http', '']) {
    connection.value = id; connection.dispatchEvent(new Event('change'));
    expect(group.hidden).toBe(true);
  }
  expect(setup(undefined, true).form.querySelector<HTMLElement>('[data-cli-sharing]')!.hidden).toBe(true);
  form.remove();
});
test('new bot inherits both settings and overrides can be cleared independently', () => {
  const { form, connection, identifier, thinking, discover } = setup();
  expect(botModelSettingsValues(form)).toEqual({ modelOverride: null, thinkingOverride: null });
  expect(identifier.disabled).toBe(true); expect(discover).not.toHaveBeenCalled();
  connection.value = 'cli'; connection.dispatchEvent(new Event('change'));
  identifier.value = 'custom-model'; thinking.value = 'high';
  expect(form.querySelector<HTMLElement>('[data-bot-external-notice]')!.hidden).toBe(false);
  expect(botModelSettingsValues(form)).toEqual({ modelOverride: { endpointId: 'cli', model: 'custom-model' }, thinkingOverride: 'high' });
  connection.value = ''; connection.dispatchEvent(new Event('change'));
  expect(botModelSettingsValues(form)).toEqual({ modelOverride: null, thinkingOverride: 'high' });
});
test('saved settings survive mounting, HTTP discovery, and unavailable connections', async () => {
  const bot = { modelOverride: { endpointId: 'http', model: 'saved' }, thinkingOverride: 'none' } as Bot;
  const { form, discover } = setup(bot);
  expect(botModelSettingsValues(form)).toEqual({ modelOverride: bot.modelOverride, thinkingOverride: 'none' });
  expect(discover).toHaveBeenCalledWith(http); await Promise.resolve();
  expect(form.querySelector('datalist option')!.getAttribute('value')).toBe('discovered');
  expect(botModelSummary(bot, [http])).toContain('HTTP · saved');
  bot.modelOverride!.endpointId = 'removed';
  const missing = setup(bot);
  expect(botModelSettingsValues(missing.form).modelOverride?.endpointId).toBe('removed');
  expect(botModelSummary(bot, [])).toContain('Unavailable connection');
});
test('CLI effort choices use their native names', () => {
  expect(generationThinkingLevels({ kind: 'claude-code-cli' } as Endpoint)).toEqual(['', 'low', 'medium', 'high', 'xhigh', 'max']);
  expect(generationThinkingLevels(cli)).toContain('none');
  const { connection, thinking } = setup();
  connection.value = 'claude'; connection.dispatchEvent(new Event('change'));
  expect([...thinking.options].map(option => option.value)).toEqual(generationThinkingLevels(claude));
  const saved = setup({ thinkingOverride: 'none' } as Bot);
  saved.connection.value = 'claude'; saved.connection.dispatchEvent(new Event('change'));
  expect(saved.thinking.value).toBe('none');
  expect(saved.thinking.selectedOptions[0].textContent).toContain('unsupported');
});
