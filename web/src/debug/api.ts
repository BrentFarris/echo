import { api } from "../../js/api.js";
import type {
  AdapterProfile, AdapterTemplate, DebugImportPreview, DebugPersistentState,
  DebugSession, DebugSnapshot, WorkspaceDebugConfig,
} from "./types";

const workspaceBase = (workspaceId: string) => `/api/workspaces/${encodeURIComponent(workspaceId)}/debug`;

export async function loadDebugConfig(workspaceId: string): Promise<{ config: WorkspaceDebugConfig; profiles: AdapterProfile[]; templates: AdapterTemplate[] }> {
  return api(`${workspaceBase(workspaceId)}/config`, { method: "GET" });
}

export async function saveDebugConfig(workspaceId: string, config: WorkspaceDebugConfig): Promise<WorkspaceDebugConfig> {
  const result = await api(`${workspaceBase(workspaceId)}/config`, { method: "PUT", body: config });
  return result.config;
}

export async function loadDebugSnapshot(workspaceId: string): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/snapshot`, { method: "GET" });
  return result.snapshot;
}

export async function listDebugProcesses(workspaceId: string): Promise<Array<{ pid: number; name: string; commandLine?: string; execution: string }>> {
  const result = await api(`${workspaceBase(workspaceId)}/processes`, { method: "GET" });
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
  return result.snapshot;
}

export async function dapRequest<T = Record<string, unknown>>(
  workspaceId: string,
  sessionId: string,
  command: string,
  expectedRevision: number,
  stopGeneration: number,
  args: Record<string, unknown> = {},
): Promise<{ body: T; revision: number; stopGeneration: number }> {
  const result = await api(`${workspaceBase(workspaceId)}/sessions/${encodeURIComponent(sessionId)}/requests/${encodeURIComponent(command)}`, {
    method: "POST",
    body: { expectedRevision, stopGeneration, arguments: args },
  });
  return result.response;
}

export async function stopDebug(workspaceId: string, sessionId: string, expectedRevision: number, terminateDebuggee?: boolean): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/sessions/${encodeURIComponent(sessionId)}/stop`, {
    method: "POST", body: { expectedRevision, terminateDebuggee },
  });
  return result.snapshot;
}

export async function disconnectDebug(workspaceId: string, sessionId: string, expectedRevision: number): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/sessions/${encodeURIComponent(sessionId)}/disconnect`, { method: "POST", body: { expectedRevision } });
  return result.snapshot;
}

export async function terminateDebug(workspaceId: string, sessionId: string, expectedRevision: number): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/sessions/${encodeURIComponent(sessionId)}/terminate`, { method: "POST", body: { expectedRevision } });
  return result.snapshot;
}

export async function setDebugTrace(workspaceId: string, sessionId: string, expectedRevision: number, enabled: boolean): Promise<DebugSession> {
  const result = await api(`${workspaceBase(workspaceId)}/sessions/${encodeURIComponent(sessionId)}/trace`, { method: "POST", body: { expectedRevision, enabled } });
  return result.session;
}

export async function restartDebug(workspaceId: string, sessionId: string, expectedRevision: number): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/sessions/${encodeURIComponent(sessionId)}/restart`, {
    method: "POST", body: { expectedRevision },
  });
  return result.snapshot;
}

export async function stopDebugGroup(workspaceId: string, groupId: string, expectedRevisions: Record<string, number>): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/groups/${encodeURIComponent(groupId)}/stop`, { method: "POST", body: { expectedRevisions } });
  return result.snapshot;
}

export async function restartDebugGroup(workspaceId: string, groupId: string, expectedRevisions: Record<string, number>): Promise<DebugSnapshot> {
  const result = await api(`${workspaceBase(workspaceId)}/groups/${encodeURIComponent(groupId)}/restart`, { method: "POST", body: { expectedRevisions } });
  return result.snapshot;
}

export async function previewVSCodeImport(workspaceId: string): Promise<DebugImportPreview> {
  const result = await api(`${workspaceBase(workspaceId)}/import-vscode/preview`, { method: "POST", body: {} });
  return result.preview;
}

export async function addAdapterTemplate(templateId: string): Promise<AdapterProfile> {
  const result = await api("/api/debug/adapter-profiles", { method: "POST", body: { templateId } });
  return result.profile;
}

export async function addAdapterProfile(profile: AdapterProfile): Promise<AdapterProfile> {
  const result = await api("/api/debug/adapter-profiles", { method: "POST", body: { profile } });
  return result.profile;
}

export async function updateAdapterProfile(profile: AdapterProfile): Promise<AdapterProfile> {
  const result = await api(`/api/debug/adapter-profiles/${encodeURIComponent(profile.id)}`, { method: "PUT", body: profile });
  return result.profile;
}

export async function deleteAdapterProfile(profileId: string): Promise<void> {
  await api(`/api/debug/adapter-profiles/${encodeURIComponent(profileId)}`, { method: "DELETE" });
}

export async function diagnoseAdapter(workspaceId: string, profileId: string): Promise<{ available: boolean; execution: string; command?: string; message: string }> {
  const result = await api(`${workspaceBase(workspaceId)}/adapters/${encodeURIComponent(profileId)}/diagnostic`, { method: "POST", body: {} });
  return result.diagnostic;
}
