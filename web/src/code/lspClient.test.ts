import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const state = vi.hoisted(() => ({ socket: null as any }));
vi.mock('../instances', () => ({ route: () => ({ path: '/session', client: { events: () => state.socket } }) }));
import { LSPClient } from './lspClient';
beforeEach(() => { vi.useFakeTimers(); state.socket = { readyState: 1, send: vi.fn(), close: vi.fn(), onmessage: null, onclose: null }; });
afterEach(() => { vi.useRealTimers(); });
const receive = (value: unknown) => state.socket.onmessage({ data: JSON.stringify(value) });
describe('LSP transport', () => {
  it('correlates responses and forwards cancellation', async () => {
    const client = new LSPClient('/session', vi.fn(), vi.fn()); receive({ method: 'echo/status', params: { state: 'ready' } });
    const first = client.call('textDocument/hover', {}); await vi.advanceTimersByTimeAsync(0);
    expect(state.socket.send).toHaveBeenCalledTimes(1);
    const sent = JSON.parse(state.socket.send.mock.calls[0][0]); receive({ id: sent.id, result: { contents: 'hover' } });
    await expect(first).resolves.toEqual({ contents: 'hover' });
    const abort = new AbortController(); const second = client.call('textDocument/hover', {}, abort.signal); await vi.advanceTimersByTimeAsync(0);
    expect(state.socket.send).toHaveBeenCalledTimes(2);
    const rejected = expect(second).rejects.toThrow('cancelled'); abort.abort(); await rejected;
    expect(state.socket.send.mock.calls.some(([data]: string[]) => JSON.parse(data).method === '$/cancelRequest')).toBe(true);
    client.close();
  });
  it('rejects pending requests on disconnect and ignores late responses', async () => {
    const disconnected = vi.fn(); const client = new LSPClient('/session', vi.fn(), disconnected);
    receive({ method: 'echo/status', params: { state: 'ready' } });
    const pending = client.call('textDocument/definition', {}); await Promise.resolve();
    const rejected = expect(pending).rejects.toThrow('disconnected'); state.socket.onclose(); await rejected;
    receive({ id: 1, result: [] }); expect(disconnected).toHaveBeenCalledTimes(1); client.close(); expect(disconnected).toHaveBeenCalledTimes(1);
  });
  it('bounds startup when a server never becomes ready', async () => {
    const client = new LSPClient('/session', vi.fn(), vi.fn()); const rejected = expect(client.ready).rejects.toThrow('startup timed out');
    await vi.advanceTimersByTimeAsync(90001); await rejected; expect(state.socket.close).toHaveBeenCalled();
  });
});
