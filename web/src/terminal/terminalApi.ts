import { api, get } from "../../js/api.js";

export type TerminalOutputChunk = { sequence: number; data: string };
export type TerminalSnapshot = {
  workspaceId: string;
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
  workspaceId: string;
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

function base(workspaceId: string): string {
  return `/api/workspaces/${encodeURIComponent(workspaceId)}/terminal`;
}
function sessionBase(workspaceId: string, sessionId: string): string {
  return `${base(workspaceId)}/sessions/${encodeURIComponent(sessionId)}`;
}

export function startTerminal(workspaceId: string, cols: number, rows: number): Promise<TerminalSnapshot> {
  return api(`${base(workspaceId)}/sessions`, { method: "POST", body: { cols, rows } });
}
export async function listTerminalSessions(workspaceId: string): Promise<TerminalSnapshot[]> {
  const result = await get(`${base(workspaceId)}/sessions`);
  return result.sessions || [];
}
export function syncTerminal(workspaceId: string, sessionId: string, afterSequence: number): Promise<TerminalSnapshot> {
  return api(`${sessionBase(workspaceId, sessionId)}?afterSequence=${encodeURIComponent(String(afterSequence))}`, { method: "GET" });
}
export async function writeTerminal(workspaceId: string, sessionId: string, data: string): Promise<void> {
  await api(`${sessionBase(workspaceId, sessionId)}/input`, { method: "POST", body: { data } });
}
export async function resizeTerminal(workspaceId: string, sessionId: string, cols: number, rows: number): Promise<void> {
  await api(`${sessionBase(workspaceId, sessionId)}/size`, { method: "PUT", body: { cols, rows } });
}
export async function stopTerminal(workspaceId: string, sessionId: string): Promise<void> {
  await api(`${sessionBase(workspaceId, sessionId)}/stop`, { method: "POST", body: {} });
}
export function restartTerminal(workspaceId: string, sessionId: string, cols: number, rows: number): Promise<TerminalSnapshot> {
  return api(`${sessionBase(workspaceId, sessionId)}/restart`, { method: "POST", body: { cols, rows } });
}
export async function listSavedCommands(workspaceId: string): Promise<SavedCommand[]> {
  const result = await get(`${base(workspaceId)}/saved-commands`);
  return result.commands || [];
}
export async function createSavedCommand(workspaceId: string, name: string, command: string): Promise<SavedCommand> {
  const result = await api(`${base(workspaceId)}/saved-commands`, { method: "POST", body: { name, command } });
  return result.command;
}
export async function updateSavedCommand(workspaceId: string, id: string, name: string, command: string): Promise<SavedCommand> {
  const result = await api(`${base(workspaceId)}/saved-commands/${encodeURIComponent(id)}`, { method: "PUT", body: { name, command } });
  return result.command;
}
export async function deleteSavedCommand(workspaceId: string, id: string): Promise<void> {
  await api(`${base(workspaceId)}/saved-commands/${encodeURIComponent(id)}`, { method: "DELETE" });
}
