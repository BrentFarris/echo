import DOMPurify from 'dompurify';
import type { Message, Plugin } from './types';
import { localID } from './instances';

type Renderer = { key: string; pluginId: string; nonce: string; frame: HTMLIFrameElement; ready: boolean };
export class Renderers {
  private frames = new Map<string, Renderer>();
  private pending = new Map<string, { messageId: string; renderer: Renderer }>();
  private latest = new Map<string, string>();
  constructor(private enabled: (id: string, bot?: string) => boolean, private changed: () => void) {
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
      const content = document.getElementById('body-' + pending.messageId);
      if (!content) return;
      content.innerHTML = DOMPurify.sanitize(data.html, { FORBID_TAGS: ['img', 'iframe', 'form', 'input', 'button', 'style', 'svg', 'math', 'audio', 'video'], FORBID_ATTR: ['style', 'id', 'name'] });
      content.classList.add('markdown');
      for (const link of content.querySelectorAll('a')) { link.target = '_blank'; link.rel = 'noopener noreferrer'; }
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
  clearPending() { this.pending.clear(); this.latest.clear(); }
  paint(message: Message) {
    const previous = this.latest.get(message.id); if (previous) this.pending.delete(previous); this.latest.delete(message.id);
    if (message.role !== 'assistant') return;
    const eligible = [...this.frames.values()].filter(r => this.enabled(r.pluginId, message.botId)).sort((a, b) => Number(localID(a.pluginId) === 'markdown') - Number(localID(b.pluginId) === 'markdown') || a.key.localeCompare(b.key));
    const renderer = eligible[0]; if (!renderer?.ready) return;
    const id = crypto.randomUUID(); this.latest.set(message.id, id); this.pending.set(id, { messageId: message.id, renderer });
    renderer.frame.contentWindow!.postMessage({ type: 'render', nonce: renderer.nonce, id, text: message.content }, '*');
  }
}
