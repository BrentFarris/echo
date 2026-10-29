import type { Message, Plugin } from './types';
import { localID } from './instances';
import { patchRenderedContent, updateStreamedText } from './messageContent';

type Renderer = { key: string; pluginId: string; nonce: string; frame: HTMLIFrameElement; ready: boolean };
type RenderRequest = { content: HTMLElement; text: string; renderer: Renderer };
export class Renderers {
  private frames = new Map<string, Renderer>();
  private pending = new Map<string, RenderRequest & { messageId: string }>();
  private latest = new Map<string, string>();
  private queued = new Map<string, RenderRequest>();
  private frame = 0;
  constructor(private enabled: (id: string, bot?: string) => boolean, private changed: () => void, private contentChanging: () => void = () => {}) {
    addEventListener('message', event => {
      const renderer = [...this.frames.values()].find(r => r.frame.contentWindow === event.source);
      if (!renderer || !event.data) return;
      const data = event.data;
      if (data.type === 'renderer-ready' && !renderer.ready) {
        renderer.ready = true;
        renderer.frame.contentWindow!.postMessage({ type: 'init', nonce: renderer.nonce, theme: { background: getComputedStyle(document.documentElement).getPropertyValue('--surface'), text: getComputedStyle(document.documentElement).getPropertyValue('--text') } }, '*');
        this.changed(); return;
      }
      if (data.type !== 'rendered' || data.nonce !== renderer.nonce || typeof data.html !== 'string' || data.html.length > 2 * 1024 * 1024) return;
      const pending = this.pending.get(data.id);
      if (!pending || pending.renderer !== renderer || this.latest.get(pending.messageId) !== data.id) return;
      this.pending.delete(data.id); this.latest.delete(pending.messageId);
      const content = pending.content;
      if (!content.isConnected || document.getElementById('body-' + pending.messageId) !== content) return;
      this.contentChanging();
      patchRenderedContent(content, data.html);
    });
  }
  sync(plugins: Plugin[]) {
    const wanted = new Set<string>();
    for (const plugin of [...plugins].sort((a, b) => a.id.localeCompare(b.id))) {
      if (!plugin.enabled || plugin.pending) continue;
      for (const id of plugin.renderers || []) {
        const key = `${plugin.id}:${plugin.digest || plugin.id}:${id}`; wanted.add(key);
        if (this.frames.has(key)) continue;
        const frame = document.createElement('iframe'); frame.hidden = true; frame.sandbox.add('allow-scripts'); frame.title = plugin.name + ' renderer';
        if (localID(plugin.id) === 'markdown' && plugin.builtin) frame.src = '/renderer.html';
        else {
          const view = plugin.views?.find(v => v.id === id && v.kind === 'renderer'); if (!view) continue;
          frame.srcdoc = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'">${view.html}`;
        }
        this.frames.set(key, { key, pluginId: plugin.id, nonce: crypto.randomUUID(), frame, ready: false }); document.body.append(frame);
      }
    }
    for (const [key, renderer] of this.frames) if (!wanted.has(key)) { renderer.frame.remove(); this.frames.delete(key); }
    this.clearPending();
  }
  clearPending() {
    if (this.frame) cancelAnimationFrame(this.frame);
    this.frame = 0; this.queued.clear(); this.pending.clear(); this.latest.clear();
  }
  paint(message: Message, content: HTMLElement) {
    const previous = this.latest.get(message.id); if (previous) this.pending.delete(previous); this.latest.delete(message.id);
    this.queued.delete(message.id);
    if (message.role !== 'assistant') return;
    const eligible = [...this.frames.values()].filter(r => this.enabled(r.pluginId, message.botId)).sort((a, b) => Number(localID(a.pluginId) === 'markdown') - Number(localID(b.pluginId) === 'markdown') || a.key.localeCompare(b.key));
    const renderer = eligible[0];
    if (!renderer?.ready) {
      content.classList.remove('markdown'); updateStreamedText(content, message.content); return;
    }
    // Keep first output readable even if a renderer is slow or never replies.
    if (!content.classList.contains('markdown')) updateStreamedText(content, message.content);
    // Leave the last formatted output visible while the next render is pending.
    // Multiple deltas in one display frame only need one renderer request.
    this.queued.set(message.id, { content, text: message.content, renderer });
    if (this.frame) return;
    this.frame = requestAnimationFrame(() => {
      this.frame = 0;
      for (const [messageId, request] of this.queued) {
        const id = crypto.randomUUID(); this.latest.set(messageId, id); this.pending.set(id, { ...request, messageId });
        request.renderer.frame.contentWindow!.postMessage({ type: 'render', nonce: request.renderer.nonce, id, text: request.text }, '*');
      }
      this.queued.clear();
    });
  }
}
