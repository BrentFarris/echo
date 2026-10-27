import { pluginAvailable } from './apps';
import { escapeHTML as esc } from './helpers';
import type { Bot, Plugin } from './types';

type API = <T = unknown>(path: string, method?: string, body?: unknown) => Promise<T>;
type View = { id: string; title: string; html: string; kind: string; digest: string };
type PluginWindow = { element: HTMLElement; frame: HTMLIFrameElement; pluginId: string; viewId: string; botId: string; digest: string; close: () => void };

export class PluginWindows {
  private windows = new Map<string, PluginWindow>();
  private pending = new Map<string, Promise<void>>();
  private layer?: HTMLElement;
  private signature = '';
  private revision = 0;

  constructor(private api: API) {
    // Pointer events inside an opaque iframe cannot bubble to the host.
    window.addEventListener('blur', () => queueMicrotask(() => {
      for (const win of this.windows.values()) if (document.activeElement === win.frame) { this.raise(win, false); break; }
    }));
  }

  sync(plugins: Plugin[], bots: Bot[]) {
    const signature = JSON.stringify([plugins.map(p => [p.id, p.digest, p.enabled, p.pending, p.scope, p.views?.map(v => [v.id, v.kind])]), bots.map(b => [b.id, b.plugins])]);
    if (signature !== this.signature) { this.signature = signature; ++this.revision; }
    for (const win of this.windows.values()) {
      const plugin = plugins.find(p => p.id === win.pluginId), bot = bots.find(b => b.id === win.botId);
      if (!pluginAvailable(plugin, bot) || (plugin?.digest || '') !== win.digest || !plugin?.views?.some(v => v.id === win.viewId && v.kind !== 'renderer')) win.close();
    }
  }

  open(pluginId: string, viewId: string, botId = 'echo'): Promise<void> {
    const key = JSON.stringify([pluginId, viewId, botId]);
    const pending = this.pending.get(key); if (pending) return pending;
    const revision = this.revision;
    const request = (async () => {
      const view = await this.api<View>(`/plugins/${encodeURIComponent(pluginId)}/views/${encodeURIComponent(viewId)}?botId=${encodeURIComponent(botId)}`);
      if (revision !== this.revision) throw new Error('Plugins changed while opening this app. Please try again.');
      if (view.kind === 'renderer') throw new Error('Renderer views cannot be opened as applications.');
      let existing = this.windows.get(key);
      if (existing && existing.digest !== view.digest) { existing.close(); existing = undefined; }
      const win = existing || this.create(key, pluginId, viewId, botId, view);
      this.raise(win, true);
    })().finally(() => { this.pending.delete(key); });
    this.pending.set(key, request);
    return request;
  }

  private raise(win: PluginWindow, focus: boolean) {
    const key = JSON.stringify([win.pluginId, win.viewId, win.botId]);
    this.windows.delete(key); this.windows.set(key, win);
    let index = 1; for (const entry of this.windows.values()) entry.element.style.zIndex = String(index++);
    if (focus) win.element.querySelector<HTMLElement>('header')!.focus({ preventScroll: true });
  }

  private create(key: string, pluginId: string, viewId: string, botId: string, view: View): PluginWindow {
    if (!this.layer) { this.layer = document.createElement('div'); this.layer.className = 'plugin-window-layer'; document.body.append(this.layer); }
    const returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const element = document.createElement('section'); element.className = 'floating-plugin';
    element.dataset.pluginId = pluginId; element.dataset.viewId = viewId; element.dataset.botId = botId;
    element.setAttribute('role', 'region'); element.setAttribute('aria-label', view.title);
    element.innerHTML = `<header tabindex="0" aria-label="Move ${esc(view.title)} with arrow keys"><strong>${esc(view.title)}</strong><button type="button" class="icon" aria-label="Close plugin">×</button></header>`;
    const header = element.querySelector<HTMLElement>('header')!;
    const frame = document.createElement('iframe'); frame.title = view.title; frame.sandbox.add('allow-scripts');
    const theme = () => ({ background: getComputedStyle(document.documentElement).getPropertyValue('--surface'), text: getComputedStyle(document.documentElement).getPropertyValue('--text') });
    frame.srcdoc = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; connect-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'"><style>:root{font-family:system-ui;background:${theme().background};color:${theme().text}}</style>${view.html}`;
    const layoutKey = `echo2:window:${pluginId}:${viewId}`, nonce = crypto.randomUUID();
    try {
      const p = JSON.parse(localStorage.getItem(layoutKey) || 'null');
      if (p && [p.x, p.y, p.w, p.h].every(Number.isFinite)) {
        element.style.left = Math.max(0, Math.min(p.x, innerWidth - 240)) + 'px'; element.style.top = Math.max(0, Math.min(p.y, innerHeight - 100)) + 'px';
        element.style.width = Math.max(240, Math.min(p.w, innerWidth)) + 'px'; element.style.height = Math.max(180, Math.min(p.h, innerHeight)) + 'px';
      }
    } catch { /* Invalid or unavailable layout storage does not block the app. */ }
    const save = () => {
      const r = element.getBoundingClientRect();
      try { localStorage.setItem(layoutKey, JSON.stringify({ x: r.x, y: r.y, w: r.width, h: r.height })); } catch { /* Browser storage may be unavailable. */ }
    };
    let drag: { x: number; y: number; left: number; top: number } | null = null;
    header.onpointerdown = event => {
      if ((event.target as HTMLElement).closest('button')) return;
      const r = element.getBoundingClientRect(); drag = { x: event.clientX, y: event.clientY, left: r.left, top: r.top }; header.setPointerCapture(event.pointerId);
    };
    header.onpointermove = event => {
      if (!drag) return;
      element.style.left = Math.max(0, Math.min(innerWidth - 120, drag.left + event.clientX - drag.x)) + 'px';
      element.style.top = Math.max(0, Math.min(innerHeight - 50, drag.top + event.clientY - drag.y)) + 'px';
    };
    header.onpointerup = header.onpointercancel = () => { drag = null; save(); };
    header.onkeydown = event => {
      if (event.target !== header) return;
      const vector = ({ ArrowLeft: [-10, 0], ArrowRight: [10, 0], ArrowUp: [0, -10], ArrowDown: [0, 10] } as Record<string, number[]>)[event.key];
      if (!vector) return;
      event.preventDefault(); const r = element.getBoundingClientRect();
      if (event.shiftKey) { element.style.width = Math.max(240, r.width + vector[0]) + 'px'; element.style.height = Math.max(180, r.height + vector[1]) + 'px'; }
      else { element.style.left = Math.max(0, Math.min(innerWidth - 120, r.left + vector[0])) + 'px'; element.style.top = Math.max(0, Math.min(innerHeight - 50, r.top + vector[1])) + 'px'; }
      save();
    };
    const observer = new ResizeObserver(save);
    const listener = async (event: MessageEvent) => {
      if (event.source !== frame.contentWindow || !event.data || !element.isConnected) return;
      const data = event.data;
      if (data.type === 'echo-plugin-ready') { frame.contentWindow!.postMessage({ type: 'echo-plugin-init', nonce, pluginId, viewId, botId, theme: theme() }, '*'); return; }
      if (data.nonce !== nonce || data.pluginId !== pluginId || data.viewId !== viewId || data.type !== 'echo-plugin-request') return;
      if (data.method === 'window.close') { close(); return; }
      try {
        const result = await this.api(`/plugins/${encodeURIComponent(pluginId)}/bridge`, 'POST', { botId, method: data.method, params: data.params });
        if (element.isConnected) frame.contentWindow!.postMessage({ type: 'echo-plugin-response', nonce, id: data.id, result }, '*');
      } catch (error) { if (element.isConnected) frame.contentWindow!.postMessage({ type: 'echo-plugin-response', nonce, id: data.id, error: String(error) }, '*'); }
    };
    const close = () => {
      const hadFocus = element.contains(document.activeElement);
      save(); observer.disconnect(); removeEventListener('message', listener); this.windows.delete(key); element.remove();
      if (hadFocus && returnFocus?.isConnected) returnFocus.focus({ preventScroll: true });
    };
    const win: PluginWindow = { element, frame, pluginId, viewId, botId, digest: view.digest || '', close };
    element.onpointerdown = () => this.raise(win, false); header.querySelector('button')!.onclick = close;
    this.windows.set(key, win); addEventListener('message', listener); element.append(frame); this.layer.append(element); observer.observe(element);
    return win;
  }
}
