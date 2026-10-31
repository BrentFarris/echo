import { route } from '../instances';

export type LSPMessage = { id?: number; method?: string; params?: any; result?: any; error?: { message: string } };

// Browser transport is independent of Monaco and the selected language server.
export class LSPClient {
  private socket: WebSocket;
  private sequence = 0;
  private pending = new Map<number, { resolve(value: any): void; reject(error: Error): void; cleanup(): void }>();
  private heartbeat: number;
  private startup: number;
  readonly ready: Promise<void>;
  private resolveReady!: () => void;
  private rejectReady!: (error: Error) => void;
  private closed = false;
  constructor(path: string, private event: (message: LSPMessage) => void, private disconnected: () => void) {
    this.ready = new Promise((resolve, reject) => { this.resolveReady = resolve; this.rejectReady = reject; });
    void this.ready.catch(() => {});
    const target = route(path); this.socket = target.client.events(target.path);
    this.startup = window.setTimeout(() => { this.rejectReady(new Error('Language server startup timed out')); this.close(); }, 90000);
    this.socket.onmessage = ({ data }) => {
      let message: LSPMessage;
      try { message = JSON.parse(data); } catch { this.close(); return; }
      if (message.id !== undefined) {
        const pending = this.pending.get(message.id); if (!pending) return;
        this.pending.delete(message.id); pending.cleanup();
        if (message.error) pending.reject(new Error(message.error.message)); else pending.resolve(message.result);
      } else {
        if (message.method === 'echo/status') {
          if (message.params.state === 'ready') { clearTimeout(this.startup); this.resolveReady(); }
          if (message.params.state === 'failed') this.rejectReady(new Error(message.params.message));
        }
        this.event(message);
      }
    };
    this.socket.onclose = () => this.finish();
    this.socket.onerror = () => this.socket.close();
    this.heartbeat = window.setInterval(() => { if (this.socket.readyState === WebSocket.OPEN) this.notify('echo/ping', {}); }, 30000);
  }
  notify(method: string, params: unknown): void {
    if (this.socket.readyState !== WebSocket.OPEN) throw new Error('Language service disconnected');
    this.socket.send(JSON.stringify({ method, params }));
  }
  async call(method: string, params: unknown, signal?: AbortSignal): Promise<any> {
    await this.ready; signal?.throwIfAborted();
    const id = ++this.sequence;
    return new Promise((resolve, reject) => {
      const cancel = () => {
        if (!this.pending.delete(id)) return;
        cleanup();
        if (this.socket.readyState === WebSocket.OPEN) this.notify('$/cancelRequest', { id });
        reject(new Error('Language request cancelled or timed out'));
      };
      const timer = window.setTimeout(cancel, 60000);
      const cleanup = () => { clearTimeout(timer); signal?.removeEventListener('abort', cancel); };
      this.pending.set(id, { resolve, reject, cleanup }); signal?.addEventListener('abort', cancel, { once: true });
      try {
        if (this.socket.readyState !== WebSocket.OPEN) throw new Error('Language service disconnected');
        this.socket.send(JSON.stringify({ id, method, params }));
      } catch (error) { this.pending.delete(id); cleanup(); reject(error); }
    });
  }
  close(): void { this.socket.close(); this.finish(); }
  private finish(): void {
    if (this.closed) return; this.closed = true; clearInterval(this.heartbeat); clearTimeout(this.startup);
    const error = new Error('Go language service disconnected; restart to reconnect');
    this.rejectReady(error);
    for (const pending of this.pending.values()) { pending.cleanup(); pending.reject(error); }
    this.pending.clear(); this.disconnected();
  }
}
