import { route, localID } from '../instances';
import type { DebugEvent } from './types';

export function subscribeDebug(botId: string, receive: (event: DebugEvent) => void, resync: () => void): () => void {
  let socket: WebSocket | undefined, stopped = false, retry = 0;
  const connect = () => {
    if (stopped) return;
    const target = route(`/plugins/monaco-editor/bots/${encodeURIComponent(botId)}/debug/events`);
    socket = target.client.events(target.path);
    socket.onopen = resync;
    socket.onmessage = event => {
      const data = JSON.parse(event.data);
      if (data.type === 'debug_subscribed' || data.type === 'debug_resync') { resync(); return; }
      if (data.workspaceId !== localID(botId)) return;
      receive({ ...data, workspaceId: botId, session: data.session ? { ...data.session, workspaceId: botId } : undefined });
    };
    socket.onclose = () => { if (!stopped) retry = window.setTimeout(connect, 2000); };
  };
  const poll = window.setInterval(() => { if (socket?.readyState !== WebSocket.OPEN) resync(); }, 3000);
  connect();
  return () => { stopped = true; clearInterval(poll); clearTimeout(retry); socket?.close(); };
}
