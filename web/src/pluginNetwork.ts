import { escapeHTML as esc } from './helpers';
import { InstanceClient, localID, splitID } from './instances';
import type { Plugin } from './types';

export function pluginNetworkMethod(method: unknown): method is string {
  return typeof method === 'string' && ['web.get', 'web.request', 'websocket.open', 'websocket.send', 'websocket.receive', 'websocket.close'].includes(method);
}

// Never interpolate arbitrary manifest text into a CSP attribute. The host also
// validates these values at installation; this check keeps the builder self-contained.
export function pluginCSP(origins: string[] = []) {
  const assets = origins.filter(raw => {
    try {
      const u = new URL(raw);
      return /^https?:$/.test(u.protocol) && u.origin === raw && /^[a-zA-Z0-9.:[\]-]+$/.test(u.host) && !u.username && !u.password;
    } catch { return false; }
  }).join(' ');
  return `default-src 'none'; script-src 'unsafe-inline' ${assets}; style-src 'unsafe-inline' ${assets}; img-src data: ${assets}; font-src ${assets || "'none'"}; media-src ${assets || "'none'"}; connect-src 'none'; frame-src 'none'; worker-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'`;
}

function localOrigin(origin: string) {
  const host = new URL(origin).hostname.toLowerCase().replace(/^\[|\]$/g, '');
  if (host === 'localhost' || host.endsWith('.localhost') || host.endsWith('.local') || host === '::1' || host.startsWith('fc') && host.includes(':') || host.startsWith('fd') && host.includes(':') || /^fe[89ab]/.test(host) && host.includes(':')) return true;
  const parts = host.split('.').map(Number);
  return parts.length === 4 && parts.every(p => Number.isInteger(p) && p >= 0 && p <= 255) && (parts[0] === 10 || parts[0] === 127 || parts[0] === 0 || parts[0] === 192 && parts[1] === 168 || parts[0] === 172 && parts[1] >= 16 && parts[1] <= 31 || parts[0] === 169 && parts[1] === 254);
}

export function pluginNetworkNotice(plugin: Plugin) {
  if (plugin.builtin) return '';
  const origins = plugin.network?.origins || [];
  const legacy = !origins.length && plugin.capabilities?.some(c => c === 'web.get' || c === 'web.request' || c.startsWith('websocket.'));
  if (!origins.length && !legacy) return '';
  return `<section class="plugin-network" aria-label="Network access"><h3>Network access</h3>${legacy ? '<p>Network calls are blocked. Update this plugin with network.origins and approve its destinations.</p>' : `<p>${plugin.pending ? 'Awaiting permission approval' : 'Approved destinations'}${!plugin.enabled && !plugin.pending ? ' · Plugin disabled' : ''}</p><ul>${origins.map(origin => `<li><code>${esc(origin)}</code>${plugin.requestedNetworkOrigins?.includes(origin) ? ' <strong>New permission requested</strong>' : ''}${localOrigin(origin) ? ' <strong>Local/private network</strong>' : ''}</li>`).join('')}</ul><p class="subtle">Each grant covers every path at that origin, including private-network addresses it resolves to. HTTP(S) grants allow API requests and remote scripts, styles, images, fonts and media. WS(S) grants allow WebSockets. Remote scripts run with this plugin’s permissions.</p><p class="subtle">API and WebSocket connections run on this plugin’s Echo machine. Assets load on the viewing device and follow browser restrictions.</p>`}</section>`;
}

export class PluginNetworkBridge {
  private socket?: WebSocket;
  private ready?: Promise<void>;
  private stopped = false;
  private activeCalls = 0;
  private requestBytes = 0;
  private pending = new Map<string, { resolve(value: unknown): void; reject(error: Error): void }>();
  constructor(private pluginId: string, private viewId: string, private botId: string, private digest: string) {}

  private connect() {
    if (this.stopped) return Promise.reject(new Error('Plugin network session closed'));
    if (this.ready) return this.ready;
    this.ready = new Promise<void>((resolve, reject) => {
      const [node, id] = splitID(this.pluginId);
      const query = new URLSearchParams({ botId: localID(this.botId), viewId: this.viewId, digest: this.digest });
      const socket = this.socket = new InstanceClient(node).events(`/plugins/${encodeURIComponent(id)}/network?${query}`);
      const timer = setTimeout(() => this.close('Plugin network session timed out'), 30_000);
      socket.onopen = () => { clearTimeout(timer); resolve(); };
      const fail = () => { clearTimeout(timer); reject(new Error('Plugin network session closed or permission revoked')); this.close(); };
      socket.onerror = fail; socket.onclose = fail;
      socket.onmessage = event => {
        if (typeof event.data !== 'string' || event.data.length > 2 * 1024 * 1024) { this.close('Invalid network response'); return; }
        try {
          const reply = JSON.parse(event.data), request = this.pending.get(reply.id);
          if (!request) return;
          this.pending.delete(reply.id);
          if (typeof reply.error === 'string') request.reject(new Error(reply.error)); else request.resolve(reply.result);
        } catch { this.close('Invalid network response'); }
      };
    });
    return this.ready;
  }

  async call(method: string, params: unknown) {
    if (!pluginNetworkMethod(method)) throw new Error('Unsupported network method');
    const id = crypto.randomUUID(), data = JSON.stringify({ id, method, params });
    const bytes = new TextEncoder().encode(data).length;
    if (bytes > 2 * 1024 * 1024) throw new Error('Network request exceeds bridge limit');
    if (this.activeCalls >= 32 || this.requestBytes + bytes > 4 * 1024 * 1024) { this.close('Plugin network queue limit exceeded'); throw new Error('Plugin network queue limit exceeded'); }
    this.activeCalls++; this.requestBytes += bytes;
    try {
      await this.connect();
      if (this.stopped || this.socket?.readyState !== WebSocket.OPEN) throw new Error('Plugin network session closed');
      if (this.socket.bufferedAmount + bytes > 4 * 1024 * 1024) { this.close('Plugin network queue limit exceeded'); throw new Error('Plugin network queue limit exceeded'); }
      return await new Promise((resolve, reject) => {
        this.pending.set(id, { resolve, reject });
        try { this.socket!.send(data); } catch (error) { this.pending.delete(id); reject(error); }
      });
    } finally { this.activeCalls--; this.requestBytes -= bytes; }
  }

  close(reason = 'Plugin network session closed') {
    if (this.stopped) return;
    this.stopped = true;
    for (const request of this.pending.values()) request.reject(new Error(reason));
    this.pending.clear(); this.socket?.close();
  }
}
