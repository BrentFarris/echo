import { api } from "../code/api";
import type {
  AdapterProfile, DebugPersistentState,
  DebugSession, DebugSnapshot, WorkspaceDebugConfig,
} from "./types";

const workspaceBase = (workspaceId: string) => `/api/v1/plugins/monaco-editor/bots/${encodeURIComponent(workspaceId)}/debug`;

export async function loadDebugConfig(workspaceId: string): Promise<{ config: WorkspaceDebugConfig; profiles: AdapterProfile[]; roots: import("../code/types").WorkspaceRoot[] }> {
  return api(`${workspaceBase(workspaceId)}/config`, { method: "GET" });
}

export async function loadDebugSnapshot(workspaceId: string): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/snapshot`, { method: "GET" });
  return { ...result.snapshot, workspaceId, sessions: result.snapshot.sessions.map((session: DebugSession) => ({ ...session, workspaceId })) };
}

export async function listDebugProcesses(workspaceId: string, input: import("./types").DebugInput): Promise<Array<{ pid: number; name: string; commandLine?: string; execution: string }>> {
  const result = await api(`/api/v1/plugins/${encodeURIComponent(input.pluginId || "")}/bots/${encodeURIComponent(workspaceId)}/debug/processes?${new URLSearchParams({ filter: input.filter || "", goOnly: String(input.goOnly || false) })}`, { method: "GET" });
  return result.processes || [];
}

export async function saveDebugState(workspaceId: string, expectedRevision: number, state: DebugPersistentState): Promise<DebugPersistentState> {
  const result = await api(`${workspaceBase(workspaceId)}/state`, { method: "PUT", body: { expectedRevision, state } });
  return result.state;
}

export async function startDebug(workspaceId: string, request: {
  configurationId?: string;
  compoundId?: string;
  currentFile?: { rootId: string; path: string };
  selectedText?: string;
  inputs?: Record<string, string>;
  noDebug?: boolean;
}): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/sessions`, { method: "POST", body: request });
  return { ...result.snapshot, workspaceId, sessions: result.snapshot.sessions.map((session: DebugSession) => ({ ...session, workspaceId })) };
}

export async function dapRequest<T = Record<string, unknown>>(
  workspaceId: string,
  sessionId: string,
  command: string,
  expectedRevision: number,
  stopGeneration: number,
  args: Record<string, unknown> = {},
  signal?: AbortSignal,
): Promise<{ body: T; revision: number; stopGeneration: number }> {
  const result = await api(`${workspaceBase(workspaceId)}/sessions/${encodeURIComponent(sessionId)}/requests/${encodeURIComponent(command)}`, {
    method: "POST",
    body: { expectedRevision, stopGeneration, arguments: args }, signal,
  });
  return result.response;
}

export async function stopDebug(workspaceId: string, sessionId: string, expectedRevision: number, terminateDebuggee?: boolean): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/sessions/${encodeURIComponent(sessionId)}/stop`, {
    method: "POST", body: { expectedRevision, terminateDebuggee },
  });
  return { ...result.snapshot, workspaceId, sessions: result.snapshot.sessions.map((session: DebugSession) => ({ ...session, workspaceId })) };
}

export async function disconnectDebug(workspaceId: string, sessionId: string, expectedRevision: number): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/sessions/${encodeURIComponent(sessionId)}/disconnect`, { method: "POST", body: { expectedRevision } });
  return { ...result.snapshot, workspaceId, sessions: result.snapshot.sessions.map((session: DebugSession) => ({ ...session, workspaceId })) };
}

export async function terminateDebug(workspaceId: string, sessionId: string, expectedRevision: number): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/sessions/${encodeURIComponent(sessionId)}/terminate`, { method: "POST", body: { expectedRevision } });
  return { ...result.snapshot, workspaceId, sessions: result.snapshot.sessions.map((session: DebugSession) => ({ ...session, workspaceId })) };
}

export async function setDebugTrace(workspaceId: string, sessionId: string, expectedRevision: number, enabled: boolean): Promise<DebugSession> {
  const result = await api(`${workspaceBase(workspaceId)}/sessions/${encodeURIComponent(sessionId)}/trace`, { method: "POST", body: { expectedRevision, enabled } });
  return result.session;
}

export async function restartDebug(workspaceId: string, sessionId: string, expectedRevision: number, restart?: RestartInputs): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/sessions/${encodeURIComponent(sessionId)}/restart`, {
    method: "POST", body: { expectedRevision, restart },
  });
  return { ...result.snapshot, workspaceId, sessions: result.snapshot.sessions.map((session: DebugSession) => ({ ...session, workspaceId })) };
}

export async function stopDebugGroup(workspaceId: string, groupId: string, expectedRevisions: Record<string, number>): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/groups/${encodeURIComponent(groupId)}/stop`, { method: "POST", body: { expectedRevisions } });
  return { ...result.snapshot, workspaceId, sessions: result.snapshot.sessions.map((session: DebugSession) => ({ ...session, workspaceId })) };
}

export async function restartDebugGroup(workspaceId: string, groupId: string, expectedRevisions: Record<string, number>, restart?: RestartInputs): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/groups/${encodeURIComponent(groupId)}/restart`, { method: "POST", body: { expectedRevisions, restart } });
  return { ...result.snapshot, workspaceId, sessions: result.snapshot.sessions.map((session: DebugSession) => ({ ...session, workspaceId })) };
}


export type RestartInputs = { inputs: Record<string,string>; currentFile?: { rootId: string; path: string }; selectedText?: string };
