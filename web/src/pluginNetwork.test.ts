import { afterEach, expect, test, vi } from 'vitest';
import { pluginCSP, pluginNetworkNotice, PluginNetworkBridge } from './pluginNetwork';
import type { Plugin } from './types';

afterEach(() => vi.unstubAllGlobals());

test('CSP allows declared assets and keeps direct connections and sandbox escape routes blocked', () => {
  const csp = pluginCSP(['https://assets.example.com', 'http://localhost:8080', 'wss://stream.example.com', `https://bad.test'; connect-src *`, 'https://user:pass@example.com', 'https://example.com/path']);
  expect(csp).toContain("script-src 'unsafe-inline' https://assets.example.com http://localhost:8080");
  expect(csp).toContain("connect-src 'none'");
  expect(csp).toContain("frame-src 'none'");
  expect(csp).toContain("worker-src 'none'");
  expect(csp).not.toContain('bad.test'); expect(csp).not.toContain('stream.example'); expect(csp).not.toContain('pass@');
  expect(pluginCSP()).toContain("font-src 'none'");
});

test('notice identifies pending additions, local destinations, approved grants and blocked legacy access', () => {
  const plugin = { builtin: false, enabled: false, pending: true, capabilities: ['web.request'], network: { origins: ['https://api.example.com', 'http://127.0.0.1:8080', 'ws://[::1]:8080'] }, requestedNetworkOrigins: ['http://127.0.0.1:8080'] } as Plugin;
  const notice = pluginNetworkNotice(plugin);
  expect(notice).toContain('Awaiting permission approval'); expect(notice).toContain('New permission requested');
  expect(notice.match(/Local\/private network/g)).toHaveLength(2);
  expect(pluginNetworkNotice({ ...plugin, pending: false, enabled: true, requestedNetworkOrigins: [] })).toContain('Approved destinations');
  expect(pluginNetworkNotice({ ...plugin, network: undefined })).toContain('Network calls are blocked');
  expect(pluginNetworkNotice({ ...plugin, builtin: true })).toBe('');
});

test('relay stays on the plugin owner, correlates replies and rejects work on disposal', async () => {
  const sockets: FakeSocket[] = [];
  class FakeSocket {
    static OPEN = 1;
    readyState = 1; bufferedAmount = 0;
    onopen?: () => void; onclose?: () => void; onerror?: () => void; onmessage?: (event: { data: string }) => void;
    sent: string[] = [];
    constructor(public url: URL) { sockets.push(this); queueMicrotask(() => this.onopen?.()); }
    send(data: string) { this.sent.push(data); }
    close() { this.readyState = 3; queueMicrotask(() => this.onclose?.()); }
  }
  vi.stubGlobal('WebSocket', FakeSocket);
  const owner = 'a'.repeat(32), bridge = new PluginNetworkBridge(`${owner}:weather`, 'main', `${owner}:echo`, 'digest');
  const result = bridge.call('web.request', { url: 'https://api.example.com' });
  await vi.waitFor(() => expect(sockets[0]?.sent).toHaveLength(1));
  expect(sockets[0].url.pathname).toBe(`/api/v1/swarm/instances/${owner}/plugins/weather/network`);
  expect(sockets[0].url.searchParams.get('botId')).toBe('echo');
  expect(sockets[0].url.searchParams.get('digest')).toBe('digest');
  const call = JSON.parse(sockets[0].sent[0]);
  sockets[0].onmessage?.({ data: JSON.stringify({ id: 'wrong', result: 'wrong' }) });
  sockets[0].onmessage?.({ data: JSON.stringify({ id: call.id, result: { status: 200 } }) });
  await expect(result).resolves.toEqual({ status: 200 });
  const pending = bridge.call('websocket.receive', { handle: 'one' });
  const rejected = expect(pending).rejects.toThrow('closed');
  await vi.waitFor(() => expect(sockets[0].sent).toHaveLength(2));
  bridge.close(); await rejected;
  await expect(bridge.call('web.request', {})).rejects.toThrow('closed');
});
