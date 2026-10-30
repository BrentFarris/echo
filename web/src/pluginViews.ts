import { pluginAvailable } from './apps';
import { pluginCSP, PluginNetworkBridge, pluginNetworkMethod } from './pluginNetwork';
import { builtinApp } from './builtinApps';
import { escapeHTML as esc } from './helpers';
import type { Bot, Plugin, Generation } from './types';
import { InstanceClient, entryNode, resourceID, resourceAPI, sameMachine, localID, splitID, machineName } from './instances';

export type AppAPI = <T = unknown>(path: string, method?: string, body?: unknown) => Promise<T>;
export type NativeFileTarget = { botId: string; ref: { rootId: string; path: string }; kind: 'file' | 'directory' };
export type NativeApp = { update(bots: Bot[], plugin: Plugin): void; visible(value: boolean): void; beforeClose(): Promise<boolean>; dispose(): void; openFile?(target: NativeFileTarget): Promise<void> };
export type NativeAppContext = { api: AppAPI; client?: InstanceClient; bots: Bot[]; plugin: Plugin; botId: string; configureBot(id: string): void; generationFor?(node: string): Generation; openModelSettings?(node: string): void };
type View = { id: string; title: string; html: string; kind: string; digest: string; network?: { origins: string[] } };
type Mode = 'floating' | 'embedded';
type Options = { embeddedHost?: HTMLElement; navigate?(route: string | null): void; configureBot?(id: string): void; generationFor?(node: string): Generation; openModelSettings?(node: string): void };
type AppWindow = { key: string; element: HTMLElement; frame?: HTMLIFrameElement; native?: NativeApp; pluginId: string; viewId: string; botId: string; digest: string; mode: Mode; close(force?: boolean): Promise<void>; layout(): void };

const popOutIcon = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M14 3h7v7M21 3 11 13"/><path d="M10 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-5"/></svg>';

export function appRoute(pluginId: string, viewId: string, botId: string) { return '#app/' + [pluginId, viewId, botId].map(encodeURIComponent).join('/'); }
export function parseAppRoute(hash: string): { pluginId: string; viewId: string; botId: string } | null {
  const match = /^#app\/([^/]+)\/([^/]+)\/([^/]+)$/.exec(hash);
  if (!match) return null;
  try { return { pluginId: decodeURIComponent(match[1]), viewId: decodeURIComponent(match[2]), botId: decodeURIComponent(match[3]) }; } catch { return null; }
}

// Keep the same attached element in both presentations. Reparenting an iframe
// would reload its browsing context and discard the running application's state.
export class PluginWindows {
  private windows = new Map<string, AppWindow>();
  private pending = new Map<string, Promise<void>>();
  private layer?: HTMLElement;
  private plugins: Plugin[] = [];
  private bots: Bot[] = [];
  private signature = '';
  private revision = 0;
  private embeddedKey: string | null = null;

  constructor(private api: AppAPI, private options: Options = {}) {
    window.addEventListener('blur', () => queueMicrotask(() => {
      for (const win of this.windows.values()) if (win.frame && document.activeElement === win.frame) { this.raise(win, false); break; }
    }));
  }

  sync(plugins: Plugin[], bots: Bot[]) {
    this.plugins = plugins; this.bots = bots;
    const signature = JSON.stringify([plugins.map(p => [p.id, p.digest, p.enabled, p.pending, p.scope, p.views?.map(v => [v.id, v.kind])]), bots.map(b => [b.id, b.plugins])]);
    if (signature !== this.signature) { this.signature = signature; ++this.revision; }
    for (const win of this.windows.values()) {
      const plugin = plugins.find(p => p.id === win.pluginId), bot = bots.find(b => b.id === win.botId);
      if (!pluginAvailable(plugin, bot) || (plugin?.digest || '') !== win.digest || !plugin?.views?.some(v => v.id === win.viewId && v.kind !== 'renderer')) void win.close(true);
      else win.native?.update(bots.filter(b => sameMachine(b, plugin!.nodeId || entryNode)), plugin!);
    }
  }

  hideEmbedded() {
    this.embeddedKey = null;
    for (const win of this.windows.values()) if (win.mode === 'embedded') { win.element.hidden = true; win.native?.visible(false); }
  }

  open(pluginId: string, viewId: string, botId = 'echo', presentation?: Mode): Promise<void> {
    const plugin = this.plugins.find(p => p.id === pluginId);
    botId = resourceID(plugin?.nodeId || entryNode, botId); const contextBot = botId;
    if (plugin?.scope === 'app') botId = resourceID(plugin.nodeId || entryNode, 'echo');
    const key = JSON.stringify([pluginId, viewId, botId]);
    const pending = this.pending.get(key); if (pending) return pending;
    const revision = this.revision;
    const request = (async () => {
      const view = await this.api<View>(`/plugins/${encodeURIComponent(pluginId)}/views/${encodeURIComponent(viewId)}?botId=${encodeURIComponent(botId)}`);
      if (revision !== this.revision) throw new Error('Plugins changed while opening this app. Please try again.');
      if (view.kind === 'renderer') throw new Error('Renderer views cannot be opened as applications.');
      let win = this.windows.get(key);
      if (win && win.digest !== view.digest) { await win.close(true); win = undefined; }
      if (!win) win = await this.create(key, pluginId, viewId, botId, contextBot, view);
      if (!win.element.isConnected) return;
      this.present(win, presentation || win.mode); this.raise(win, true);
    })().finally(() => this.pending.delete(key));
    this.pending.set(key, request); return request;
  }

  async openFile(pluginId: string, viewId: string, target: NativeFileTarget): Promise<void> {
    await this.open(pluginId, viewId, target.botId);
    const win = [...this.windows.values()].find(win => win.pluginId === pluginId && win.viewId === viewId && win.native?.openFile);
    if (!win?.native?.openFile) throw new Error('This app cannot open files in the built-in editor.');
    await win.native.openFile(target);
  }

  private present(win: AppWindow, mode: Mode) {
    const previous = win.mode; win.mode = mode;
    win.element.classList.toggle('embedded-plugin', mode === 'embedded');
    win.element.classList.toggle('floating-plugin', mode === 'floating');
    const presentation = win.element.querySelector<HTMLButtonElement>('[data-presentation]')!;
    const label = mode === 'embedded' ? 'Open in Window' : 'Open Embedded';
    presentation.innerHTML = mode === 'embedded' ? popOutIcon : label;
    presentation.setAttribute('aria-label', label); presentation.title = label;
    if (mode === 'embedded') {
      this.hideEmbedded(); this.embeddedKey = win.key;
      this.options.navigate?.(appRoute(win.pluginId, win.viewId, win.botId));
    } else if (previous === 'embedded' && this.embeddedKey === win.key) {
      this.embeddedKey = null; this.options.navigate?.(null);
    }
    win.element.hidden = false; win.native?.visible(true);
    try { localStorage.setItem(`echo2:presentation:${win.key}`, mode); } catch { /* Optional preference. */ }
    win.layout();
  }

  private raise(win: AppWindow, focus: boolean) {
    this.windows.delete(win.key); this.windows.set(win.key, win);
    let index = 1; for (const entry of this.windows.values()) entry.element.style.zIndex = String(entry.mode === 'floating' ? 100 + index++ : 1);
    if (focus) win.element.querySelector<HTMLElement>('header')!.focus({ preventScroll: true });
  }

  private async create(key: string, pluginId: string, viewId: string, botId: string, contextBot: string, view: View): Promise<AppWindow> {
    if (!this.layer) { this.layer = document.createElement('div'); this.layer.className = 'plugin-window-layer'; document.body.append(this.layer); }
    const returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const element = document.createElement('section'); element.className = 'floating-plugin';
    element.dataset.pluginId = pluginId; element.dataset.viewId = viewId; element.dataset.botId = botId;
    element.setAttribute('role', 'region'); element.setAttribute('aria-label', view.title);
    element.innerHTML = `<header tabindex="0" aria-label="Move ${esc(view.title)} with arrow keys"><strong>${esc(view.title)}${splitID(pluginId)[0] !== entryNode ? `<small class="machine-chip">${esc(machineName(splitID(pluginId)[0]))}</small>` : ''}</strong><button type="button" data-presentation>Open Embedded</button><button type="button" class="icon" data-close aria-label="Close plugin" title="Close">×</button></header><div class="plugin-content"></div>`;
    const header = element.querySelector<HTMLElement>('header')!, content = element.querySelector<HTMLElement>('.plugin-content')!;
    const nonce = crypto.randomUUID(), layoutKey = `echo2:window:${pluginId}:${viewId}`;
    const network = new PluginNetworkBridge(pluginId, viewId, botId, view.digest);
    let geometry = { x: 300, y: 100, w: localID(pluginId) === 'monaco-editor' ? 1000 : 400, h: localID(pluginId) === 'monaco-editor' ? 700 : 400 };
    let mode: Mode = view.kind === 'embedded' ? 'embedded' : 'floating';
    try {
      const saved = JSON.parse(localStorage.getItem(layoutKey) || 'null');
      if (saved && [saved.x, saved.y, saved.w, saved.h].every(Number.isFinite)) geometry = saved;
      const preference = localStorage.getItem(`echo2:presentation:${key}`); if (preference === 'embedded' || preference === 'floating') mode = preference;
    } catch { /* Invalid storage does not block launch. */ }
    let closed = false, closing = false;
    const win: AppWindow = { key, element, pluginId, viewId, botId, digest: view.digest || '', mode,
      layout: () => {
        if (win.mode === 'embedded') {
          const r = this.options.embeddedHost?.getBoundingClientRect() || { x: 0, y: 0, width: innerWidth, height: innerHeight };
          Object.assign(element.style, { left: r.x + 'px', top: r.y + 'px', width: r.width + 'px', height: r.height + 'px' });
        } else Object.assign(element.style, { left: Math.max(0, Math.min(geometry.x, innerWidth - 240)) + 'px', top: Math.max(0, Math.min(geometry.y, innerHeight - 100)) + 'px', width: Math.max(240, Math.min(geometry.w, innerWidth)) + 'px', height: Math.max(180, Math.min(geometry.h, innerHeight)) + 'px' });
      },
      close: async (force = false) => {
        if (closed || (closing && !force)) return; closing = true;
        if (!force && win.native && !await win.native.beforeClose()) { closing = false; return; }
        if (closed) return; closed = true;
        const hadFocus = element.contains(document.activeElement);
        save(); network.close(); observer.disconnect(); removeEventListener('resize', layout); removeEventListener('message', listener);
        win.native?.dispose(); this.windows.delete(key); element.remove();
        if (this.embeddedKey === key) { this.embeddedKey = null; this.options.navigate?.(null); }
        if (hadFocus && returnFocus?.isConnected) returnFocus.focus({ preventScroll: true });
      },
    };
    const layout = () => win.layout();
    const save = () => {
      if (win.mode !== 'floating' || element.hidden) return;
      const r = element.getBoundingClientRect(); geometry = { x: r.x, y: r.y, w: r.width, h: r.height };
      try { localStorage.setItem(layoutKey, JSON.stringify(geometry)); } catch { /* Optional storage. */ }
    };
    const observer = new ResizeObserver(entries => {
      if (entries.some(entry => entry.target === this.options.embeddedHost)) win.layout(); else save();
    });
    const theme = () => ({ background: getComputedStyle(document.documentElement).getPropertyValue('--surface'), text: getComputedStyle(document.documentElement).getPropertyValue('--text') });
    const listener = async (event: MessageEvent) => {
      if (!win.frame || event.source !== win.frame.contentWindow || !event.data || closed) return;
      const data = event.data;
      if (data.type === 'echo-plugin-ready') { win.frame.contentWindow!.postMessage({ type: 'echo-plugin-init', nonce, pluginId, viewId, botId, theme: theme() }, '*'); return; }
      if (data.nonce !== nonce || data.pluginId !== pluginId || data.viewId !== viewId || data.type !== 'echo-plugin-request') return;
      if (data.method === 'window.close') { await win.close(); return; }
      try {
        const result = pluginNetworkMethod(data.method)
          ? await network.call(data.method, data.params)
          : await this.api(`/plugins/${encodeURIComponent(pluginId)}/bridge`, 'POST', { botId, digest: view.digest, method: data.method, params: data.params });
        if (!closed) win.frame.contentWindow!.postMessage({ type: 'echo-plugin-response', nonce, id: data.id, result }, '*');
      } catch (error) { if (!closed) win.frame.contentWindow!.postMessage({ type: 'echo-plugin-response', nonce, id: data.id, error: String(error) }, '*'); }
    };
    let drag: { x: number; y: number; left: number; top: number } | null = null;
    header.onpointerdown = event => {
      if (win.mode !== 'floating' || (event.target as HTMLElement).closest('button')) return;
      const r = element.getBoundingClientRect(); drag = { x: event.clientX, y: event.clientY, left: r.left, top: r.top }; header.setPointerCapture(event.pointerId);
    };
    header.onpointermove = event => { if (drag) { element.style.left = Math.max(0, Math.min(innerWidth - 120, drag.left + event.clientX - drag.x)) + 'px'; element.style.top = Math.max(0, Math.min(innerHeight - 50, drag.top + event.clientY - drag.y)) + 'px'; } };
    header.onpointerup = header.onpointercancel = () => { drag = null; save(); };
    header.onkeydown = event => {
      if (event.target !== header || win.mode !== 'floating') return;
      const vector = ({ ArrowLeft: [-10, 0], ArrowRight: [10, 0], ArrowUp: [0, -10], ArrowDown: [0, 10] } as Record<string, number[]>)[event.key];
      if (!vector) return; event.preventDefault();
      if (event.shiftKey) { geometry.w += vector[0]; geometry.h += vector[1]; } else { geometry.x += vector[0]; geometry.y += vector[1]; }
      win.layout(); save();
    };
    header.querySelector<HTMLButtonElement>('[data-presentation]')!.onclick = () => { save(); this.present(win, win.mode === 'floating' ? 'embedded' : 'floating'); };
    header.querySelector<HTMLButtonElement>('[data-close]')!.onclick = () => { void win.close(); };
    element.onpointerdown = () => this.raise(win, false);
    this.windows.set(key, win); this.layer.append(element); win.layout();
    observer.observe(element); if (this.options.embeddedHost) observer.observe(this.options.embeddedHost);
    addEventListener('resize', layout); addEventListener('message', listener);
    try {
      const plugin = this.plugins.find(p => p.id === pluginId);
      const load = builtinApp(plugin, viewId);
      if (load) {
        const module = await load();
        const node = plugin?.nodeId || entryNode;
        if (!closed) win.native = await module.mount(content, { api: (path, method, body) => resourceAPI(path, method, body, node), client: new InstanceClient(node), bots: this.bots.filter(b => sameMachine(b, node)), plugin: plugin!, botId: contextBot, configureBot: id => this.options.configureBot?.(id), generationFor: node => this.options.generationFor?.(node) || { endpointId: "", model: "", thinking: "", temperature: .6 }, openModelSettings: node => this.options.openModelSettings?.(node) });
        if (closed) win.native?.dispose();
      } else {
        const frame = document.createElement('iframe'); frame.title = view.title; frame.sandbox.add('allow-scripts'); win.frame = frame;
        frame.srcdoc = `<meta http-equiv="Content-Security-Policy" content="${pluginCSP(view.network?.origins)}"><style>:root{font-family:system-ui;background:${theme().background};color:${theme().text}}</style>${view.html}`;
        content.append(frame);
      }
    } catch (error) { await win.close(true); throw error; }
    return win;
  }
}
