import { InstanceClient, splitID } from '../instances';

const handlers = new Map<string, Set<(message: any) => void>>();
const sockets = new Map<string, { socket?: WebSocket; timer?: number; closed: boolean }>();
export function on(type: string, handler: (message: any) => void): void {
  if (!handlers.has(type)) handlers.set(type, new Set());
  handlers.get(type)!.add(handler);
}
// Each bot reconnects independently; there is no global socket state.
export function onState(_handler: (state: string) => void): void {}
function emit(type: string, message: object): void { handlers.get(type)?.forEach(handler => handler(message)); }
export function send(message: { type: string; botId: string }): void {
  const id = message.botId;
  if (sockets.has(id)) return;
  const state: { socket?: WebSocket; timer?: number; closed: boolean } = { closed: false };
  sockets.set(id, state);
  const connect = () => {
    if (state.closed) return;
    const [node, bot] = splitID(id);
    const socket = new InstanceClient(node).events(`/plugins/terminal/bots/${encodeURIComponent(bot)}/events`);
    state.socket = socket;
    socket.onmessage = event => {
      if (state.closed || state.socket !== socket) return;
      try {
        const message = JSON.parse(event.data);
        emit(message.type, { ...message, botId: id });
      } catch { emit('terminal_resync', { botId: id }); }
    };
    socket.onerror = () => socket.close();
    socket.onclose = () => {
      if (state.closed || state.socket !== socket) return;
      emit('terminal_offline', { botId: id });
      state.timer = window.setTimeout(connect, 1500);
    };
  };
  connect();
}
export function closeTerminalSocket(id: string): void {
  const state = sockets.get(id); if (!state) return;
  state.closed = true; clearTimeout(state.timer); state.socket?.close(); sockets.delete(id);
}
