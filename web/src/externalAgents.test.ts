import { expect, test, vi } from 'vitest';
import { externalAgentForm, externalAgentValues, externalAgentNotice, isExternalAgent } from './externalAgents';
import { GenerationSettings, generationSettingsMarkup } from './generationSettings';
import { conversationItems } from './conversation';
import type { Endpoint, Message } from './types';

test('external connection form has native settings and no HTTP or sampling fields', () => {
  const root = document.createElement('div'); root.innerHTML = externalAgentForm();
  const form = root.querySelector('form')!;
  expect(root.textContent).toContain(externalAgentNotice);
  for (const name of ['url', 'key', 'temperature', 'compact']) expect(form.elements.namedItem(name)).toBeNull();
  (form.elements.namedItem('name') as HTMLInputElement).value = 'Codex';
  expect(externalAgentValues(form)).toMatchObject({ kind: 'codex-cli', name: 'Codex', model: '', executable: '', cloud: false });
  expect(isExternalAgent({ kind: 'http' })).toBe(false);
});

test('selector skips HTTP discovery and hides unsupported tuning for CLI connections', () => {
  const endpoint: Endpoint = { id: 'cli', name: 'Codex', kind: 'codex-cli', url: '', model: '', cloud: false, compact: false, contextTokens: 0 };
  const root = document.createElement('div'); root.innerHTML = generationSettingsMarkup(); document.body.append(root);
  const discover = vi.fn(async () => []); const resetExternal = vi.fn();
  const settings = new GenerationSettings(root.querySelector('details')!, { state: () => ({ generation: { endpointId: 'cli', model: '', thinking: '', temperature: .6 }, endpoints: [endpoint], node: 'local', machines: [{ id: 'local', name: 'Local' }] }), change: vi.fn(), manage: vi.fn(), discover, resetExternal });
  expect(discover).not.toHaveBeenCalled();
  expect(settings.popup.querySelector<HTMLInputElement>('[data-setting="temperature"]')!.closest('label')!.hidden).toBe(true);
  expect(settings.popup.querySelector<HTMLElement>('[data-setting="external-notice"]')!.hidden).toBe(false);
  settings.popup.querySelector<HTMLButtonElement>('[data-setting="reset-external"]')!.click(); expect(resetExternal).toHaveBeenCalledOnce();
  settings.dispose(); settings.popup.remove(); root.remove();
});

test('external activity stays with the final response after transcript reload', () => {
  const base = { chatId: 'chat', runId: 'run', botId: 'bot', author: 'Bot', status: 'completed', content: 'done', createdAt: '' };
  const messages: Message[] = [{ ...base, id: 'activity', role: 'activity' }, { ...base, id: 'final', role: 'assistant' }];
  const items = conversationItems(messages, [{ id: 'run', chatId: 'chat', botId: 'bot', status: 'completed' }]);
  expect(items).toHaveLength(1); expect(items[0].kind === 'response' && items[0].group.final?.id).toBe('final');
});
