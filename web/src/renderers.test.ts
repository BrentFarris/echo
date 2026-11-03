import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { Renderers } from './renderers';
import type { Message, Plugin } from './types';

const plugin = { id: 'markdown', name: 'Markdown', enabled: true, builtin: true, renderers: ['main'] } as Plugin;
const message = (content: string, id = 'stream'): Message => ({
  id, content, chatId: 'chat', botId: 'echo', author: 'Echo', role: 'assistant', status: 'running', createdAt: '',
});

describe('streamed renderers', () => {
  let renderers: Renderers;
  let body: HTMLElement;
  let frame: HTMLIFrameElement;
  let requests: { id: string; nonce: string; text: string }[];
  let callbacks: Map<number, FrameRequestCallback>;
  let enabled: boolean;
  let listener: EventListener;
  let contentChanging: ReturnType<typeof vi.fn<() => void>>;
  const sandbox = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, 'sandbox');

  beforeAll(() => {
    // jsdom does not implement iframe sandbox tokens.
    if (!sandbox) Object.defineProperty(HTMLIFrameElement.prototype, 'sandbox', { configurable: true, value: { add() {} } });
  });
  afterAll(() => { if (!sandbox) Reflect.deleteProperty(HTMLIFrameElement.prototype, 'sandbox'); });

  const flush = () => {
    const pending = [...callbacks.values()]; callbacks.clear();
    for (const callback of pending) callback(0);
  };
  const receive = (data: object, source = frame.contentWindow) => {
    window.dispatchEvent(new MessageEvent('message', { source, data }));
  };
  const reply = (request: typeof requests[number], html: string) => receive({ ...request, type: 'rendered', html });
  const paint = (text: string) => renderers.paint(message(text), body);

  beforeEach(() => {
    callbacks = new Map(); requests = []; enabled = true; contentChanging = vi.fn();
    let nextFrame = 0;
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation(callback => { callbacks.set(++nextFrame, callback); return nextFrame; });
    vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(id => { callbacks.delete(id); });
    const listen = vi.spyOn(window, 'addEventListener');
    renderers = new Renderers(() => enabled, vi.fn(), contentChanging);
    listener = listen.mock.calls.find(([type]) => type === 'message')![1] as EventListener;
    body = document.createElement('div'); body.id = 'body-stream'; document.body.append(body);
    renderers.sync([plugin]); frame = document.querySelector('iframe')!;
    vi.spyOn(frame.contentWindow!, 'postMessage').mockImplementation(data => {
      if (data.type === 'render') requests.push(data);
    });
    receive({ type: 'renderer-ready' });
  });

  afterEach(() => {
    renderers.clearPending(); window.removeEventListener('message', listener);
    vi.restoreAllMocks(); document.body.replaceChildren();
  });

  it('retains formatted blocks, selection, disclosures and code scroll while new text is pending', () => {
    paint('# Heading\n\nAnswer'); flush();
    reply(requests[0], '<h1>Heading</h1><details><summary>More</summary><p>Work</p></details><pre><code>first line</code></pre><p>Answer</p>');
    const heading = body.querySelector('h1')!, text = heading.firstChild!;
    const details = body.querySelector('details')!, pre = body.querySelector('pre')!, code = pre.firstChild!;
    details.open = true; pre.scrollLeft = 80;
    const range = document.createRange(); range.setStart(text, 0); range.setEnd(text, 4);
    window.getSelection()!.removeAllRanges(); window.getSelection()!.addRange(range);

    paint('# Heading\n\nAnswer grows');
    expect(body.classList.contains('markdown')).toBe(true);
    expect(body.querySelector('h1')).toBe(heading);
    expect(body.querySelector(':scope > p:last-child')?.textContent).toBe('Answer');
    flush();
    reply(requests[1], '<h1>Heading</h1><details><summary>More</summary><p>Work</p></details><pre><code>first line\nnext line</code></pre><p>Answer grows</p>');
    expect(body.querySelector('h1')).toBe(heading);
    expect(heading.firstChild).toBe(text);
    expect(window.getSelection()!.toString()).toBe('Head');
    expect(body.querySelector('details')).toBe(details); expect(details.open).toBe(true);
    expect(body.querySelector('pre')).toBe(pre); expect(pre.firstChild).toBe(code); expect(pre.scrollLeft).toBe(80);
    expect(body.querySelector(':scope > p:last-child')?.textContent).toBe('Answer grows');
    expect(contentChanging).toHaveBeenCalledTimes(2);
  });

  it('coalesces token bursts and ignores replies superseded before the next frame', () => {
    paint('old'); flush();
    paint('new'); paint('newest');
    reply(requests[0], '<p>old</p>');
    expect(body.textContent).toBe('newest');
    expect(callbacks.size).toBe(1); flush();
    expect(requests.map(request => request.text)).toEqual(['old', 'newest']);
    reply(requests[1], '<p>newest</p>'); expect(body.textContent).toBe('newest');
  });

  it('rejects replies from another frame, with an invalid nonce, or for a replaced body', () => {
    paint('answer'); flush();
    receive({ ...requests[0], type: 'rendered', html: '<p>wrong</p>' }, window);
    reply({ ...requests[0], nonce: 'wrong' }, '<p>wrong</p>');
    expect(body.textContent).toBe('answer');
    const replacement = document.createElement('div'); replacement.id = body.id; body.replaceWith(replacement);
    reply(requests[0], '<p>stale</p>'); expect(replacement.textContent).toBe('');
  });

  it('sanitizes changed attributes and elements before patching existing output', () => {
    paint('safe'); flush(); reply(requests[0], '<a href="https://example.com">safe</a><pre class="old">code</pre>');
    const link = body.querySelector('a')!, pre = body.querySelector('pre')!;
    paint('hostile'); flush();
    reply(requests[1], '<a href="javascript:alert(1)" onclick="alert(1)" id="bad" style="color:red">changed</a><pre>code</pre><script>alert(1)</script><img src=x><iframe></iframe><input>');
    expect(body.querySelector('a')).toBe(link); expect(link.hasAttribute('href')).toBe(false);
    expect(link.getAttribute('target')).toBe('_blank'); expect(link.getAttribute('rel')).toBe('noopener noreferrer');
    expect(body.querySelector('pre')).toBe(pre); expect(pre.hasAttribute('class')).toBe(false);
    expect(body.querySelector('[onclick], [style], [id], script, img, iframe, input')).toBeNull();
  });

  it('keeps plain text streaming when renderers are disabled and cancels queued work on reset', () => {
    paint('formatted'); flush(); reply(requests[0], '<p>formatted</p>');
    enabled = false; paint('plain');
    expect(body.classList.contains('markdown')).toBe(false); expect(body.textContent).toBe('plain');
    const text = body.firstChild; paint('plain grows'); expect(body.firstChild).toBe(text);
    enabled = true; paint('queued'); renderers.clearPending(); flush();
    expect(requests).toHaveLength(1); expect(body.textContent).toBe('queued');
  });
});
