import type { FileRef } from './types';
const listeners = new Map<string, Set<(value: any) => void>>();
export function on(type: string, listener: (value: any) => void): () => void {
  if (!listeners.has(type)) listeners.set(type, new Set());
  listeners.get(type)!.add(listener); return () => { listeners.get(type)?.delete(listener); };
}
export function onState(listener: (state: string) => void) { return on('connection', listener); }
function emit(type: string, value: any) { for (const listener of listeners.get(type) || []) listener(value); }

export function watchFiles(botId: string, refresh: () => void, refs: () => FileRef[] = () => []): () => void {
  let socket: WebSocket | undefined, stopped = false, retry = 0, sequence = 0, previousRefs = '';
  const connect = () => {
    if (stopped) return;
    socket = new WebSocket(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/v1/plugins/monaco-editor/bots/${encodeURIComponent(botId)}/events`);
    socket.onopen = () => { sequence = 0; previousRefs = ''; emit('connection', 'open'); refresh(); sendRefs(); };
    socket.onmessage = event => {
      const data = JSON.parse(event.data);
      if (data.botId !== botId) return;
      emit(data.type, { ...data, workspaceId: botId });
      if (data.type === 'bookmarks_changed') return;
      if (data.sequence) { if (sequence && data.sequence !== sequence + 1) refresh(); sequence = data.sequence; }
      refresh();
    };
    socket.onclose = () => { if (!stopped) retry = window.setTimeout(connect, 2500); };
  };
  const sendRefs = () => { const value = JSON.stringify({ refs: refs() }); if (socket?.readyState === WebSocket.OPEN && value !== previousRefs) { socket.send(value); previousRefs = value; } };
  const poll = window.setInterval(() => { if (socket?.readyState !== WebSocket.OPEN) refresh(); else sendRefs(); }, 2000);
  connect();
  return () => { stopped = true; clearInterval(poll); clearTimeout(retry); socket?.close(); };
}
