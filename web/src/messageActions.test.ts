import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';
import { MessageActionUI } from './messageActions';
import type { Message } from './types';

const message: Message = { id: 'message', chatId: 'chat', role: 'assistant', author: 'Echo', content: '**raw** markdown', status: 'completed', createdAt: '' };
const clipboard = vi.fn().mockResolvedValue(undefined);
let touch = false;
beforeEach(() => {
  document.body.replaceChildren(); touch = false;
  vi.stubGlobal('matchMedia', () => ({ matches: touch }));
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: clipboard } });
  clipboard.mockClear();
});
afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers(); });
function setup(m = message) {
  const options = { busy: vi.fn(() => false), notice: vi.fn(), save: vi.fn().mockResolvedValue(undefined), remove: vi.fn().mockResolvedValue(undefined) };
  const ui = new MessageActionUI(options);
  const container = document.createElement('section'), body = document.createElement('article'); container.append(body); document.body.append(container);
  ui.mount(container, body, m);
  const click = (action: string) => container.querySelector<HTMLButtonElement>(`[data-action="${action}"]`)!.click();
  return { ui, options, container, body, click };
}
describe('message actions', () => {
  it.each(['user', 'assistant'])('renders ordered actions for %s and leaves fork inert', role => {
    const { container, options, click } = setup({ ...message, role });
    expect([...container.querySelectorAll('button')].map(b => b.dataset.action)).toEqual(['copy', 'fork', 'edit', 'trash']);
    click('fork'); expect(options.save).not.toHaveBeenCalled(); expect(options.remove).not.toHaveBeenCalled(); expect(container.querySelector('form')).toBeNull();
  });
  it('copies raw final content and resets feedback after repeated copies', async () => {
    vi.useFakeTimers(); const { click, container } = setup();
    click('copy'); await vi.advanceTimersByTimeAsync(500); click('copy'); await vi.advanceTimersByTimeAsync(0);
    expect(clipboard).toHaveBeenLastCalledWith(message.content);
    expect(container.querySelector('[aria-label="Copied"]')).not.toBeNull();
    await vi.advanceTimersByTimeAsync(1600);
    expect(container.querySelector('[aria-label="Copy final response"]')).not.toBeNull();
  });
  it('reports clipboard failures', async () => {
    clipboard.mockRejectedValueOnce(new Error('clipboard denied'));
    const { click, options } = setup(); click('copy'); await vi.waitFor(() => expect(options.notice).toHaveBeenCalled());
  });
  it('confirms deletion and respects cancellation', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false); const { click, options } = setup();
    click('trash'); expect(options.remove).not.toHaveBeenCalled(); confirm.mockReturnValue(true); click('trash');
    await vi.waitFor(() => expect(options.remove).toHaveBeenCalledWith(message));
  });
  it('preserves drafts across repaints and supports Escape', () => {
    const { click, body, ui, container } = setup(); click('edit');
    const textarea = body.querySelector('textarea')!; expect(textarea.value).toBe(message.content); textarea.value = 'draft';
    ui.mount(container, body, { ...message }); expect(body.querySelector('textarea')).toBe(textarea);
    expect(textarea.value).toBe('draft'); textarea.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(body.querySelector('form')).toBeNull(); expect(container.classList.contains('is-editing')).toBe(false);
  });
  it.each(['user', 'assistant'])('saves %s with keyboard shortcut and stable retry id', async role => {
    const m = { ...message, role }; const { click, body, options } = setup(m); click('edit');
    options.save.mockRejectedValueOnce(new Error('offline'));
    const textarea = body.querySelector('textarea')!; textarea.value = 'edited';
    textarea.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true }));
    await vi.waitFor(() => expect(options.notice).toHaveBeenCalled());
    expect(body.querySelector('textarea')!.value).toBe('edited');
    textarea.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', metaKey: true }));
    await vi.waitFor(() => expect(body.querySelector('form')).toBeNull());
    expect(options.save.mock.calls[0]).toEqual(options.save.mock.calls[1]);
    expect(options.save.mock.calls[0].slice(0, 2)).toEqual([m, 'edited']);
  });
  it('disables actions during a run, including an editor opened before the run', async () => {
    const { ui, options, container, body, click } = setup(); click('edit'); options.busy.mockReturnValue(true);
    body.querySelector('form')!.requestSubmit(); await vi.waitFor(() => expect(options.notice).toHaveBeenCalled()); expect(options.save).not.toHaveBeenCalled();
    ui.mount(container, body, message); expect(container.querySelectorAll('[aria-disabled="true"]')).toHaveLength(4);
    click('copy'); click('trash'); expect(clipboard).not.toHaveBeenCalled(); expect(options.remove).not.toHaveBeenCalled();
  });
  it('toggles on touch and closes on outside tap without hijacking links', () => {
    touch = true; const { container } = setup(); container.click(); expect(container.classList.contains('is-open')).toBe(true);
    container.click(); expect(container.classList.contains('is-open')).toBe(false);
    const link = document.createElement('a'); link.href = '#'; container.append(link); link.click(); expect(container.classList.contains('is-open')).toBe(false);
    container.click(); document.body.click(); expect(container.classList.contains('is-open')).toBe(false);
  });
});
