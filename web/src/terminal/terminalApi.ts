import { InstanceClient, splitID } from "../instances";
function api<T = any>(path: string, options: { method: string; body?: unknown }): Promise<T> {
 const match = /^\/plugins\/terminal\/bots\/([^/?]+)/.exec(path)!;
 const [node, id] = splitID(decodeURIComponent(match[1]));
 return new InstanceClient(node).json(path.replace(match[1], encodeURIComponent(id)), options.method, options.body);
}
const get = (path: string) => api(path, { method: "GET" });

export type TerminalOutputChunk = { sequence: number; data: string };
export type TerminalSnapshot = {
  botId: string;
  id: string;
  name?: string;
  kind?: "default" | "debug" | "test";
  ownerSessionId?: string;
  shell: string;
  workingDirectory: string;
  status: string;
  exitCode?: number;
  message?: string;
  taskStatus?: "running" | "passed" | "failed" | "stopped";
  lastSequence: number;
  reset?: boolean;
  output: TerminalOutputChunk[];
};
export type TerminalEvent = {
  type: "terminal_event";
  botId: string;
  sessionId: string;
  name?: string;
  kind?: "default" | "debug" | "test";
  ownerSessionId?: string;
  event: "started" | "data" | "exited";
  sequence?: number;
  data?: string;
  exitCode?: number;
  message?: string;
  taskStatus?: "running" | "passed" | "failed" | "stopped";
};
export type SavedCommand = { id: string; name: string; command: string; order: number };

function base(botId: string): string {
  return `/plugins/terminal/bots/${encodeURIComponent(botId)}`;
}
function sessionBase(botId: string, sessionId: string): string {
  return `${base(botId)}/sessions/${encodeURIComponent(sessionId)}`;
}

export function startTerminal(botId: string, cols: number, rows: number): Promise<TerminalSnapshot> {
  return api(`${base(botId)}/sessions`, { method: "POST", body: { cols, rows } });
}
export async function listTerminalSessions(botId: string): Promise<TerminalSnapshot[]> {
  const result = await get(`${base(botId)}/sessions`);
  return result.sessions || [];
}
export function syncTerminal(botId: string, sessionId: string, afterSequence: number): Promise<TerminalSnapshot> {
  return api(`${sessionBase(botId, sessionId)}?afterSequence=${encodeURIComponent(String(afterSequence))}`, { method: "GET" });
}
export async function writeTerminal(botId: string, sessionId: string, data: string): Promise<void> {
  await api(`${sessionBase(botId, sessionId)}/input`, { method: "POST", body: { data } });
}
export async function resizeTerminal(botId: string, sessionId: string, cols: number, rows: number): Promise<void> {
  await api(`${sessionBase(botId, sessionId)}/size`, { method: "PUT", body: { cols, rows } });
}
export async function stopTerminal(botId: string, sessionId: string): Promise<void> {
  await api(`${sessionBase(botId, sessionId)}/stop`, { method: "POST", body: {} });
}
export function restartTerminal(botId: string, sessionId: string, cols: number, rows: number): Promise<TerminalSnapshot> {
  return api(`${sessionBase(botId, sessionId)}/restart`, { method: "POST", body: { cols, rows } });
}
export async function listSavedCommands(botId: string): Promise<SavedCommand[]> {
  const result = await get(`${base(botId)}/saved-commands`);
  return result.commands || [];
}
export async function createSavedCommand(botId: string, name: string, command: string): Promise<SavedCommand> {
  const result = await api(`${base(botId)}/saved-commands`, { method: "POST", body: { name, command } });
  return result.command;
}
export async function updateSavedCommand(botId: string, id: string, name: string, command: string): Promise<SavedCommand> {
  const result = await api(`${base(botId)}/saved-commands/${encodeURIComponent(id)}`, { method: "PUT", body: { name, command } });
  return result.command;
}
export async function deleteSavedCommand(botId: string, id: string): Promise<void> {
  await api(`${base(botId)}/saved-commands/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export async function changeTerminalFolder(botId: string, sessionId: string, folder: string): Promise<void> {
 await api(`${sessionBase(botId, sessionId)}/folder`, {method:"POST",body:{folder}});
}
