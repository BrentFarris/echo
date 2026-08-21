import { api } from "../../js/api.js";
import type {
  FileRef, FileSnapshot, FsEntry, SearchResult, TextReplaceResponse, TextReplaceTarget, TextReplaceUpdate,
  TextSearchRequest, TextSearchResponse, TrashItem, WorkspaceRoot,
} from "./types";
import type { LSPProfile, WorkspaceLSPConfig, WorkspaceLSPResponse } from "./lspTypes";

export type APIError = Error & {
  status?: number;
  payload?: { code?: string; details?: { current?: FileSnapshot; updated?: TextReplaceUpdate[] } };
};

function base(workspaceId: string): string {
  return `/api/workspaces/${encodeURIComponent(workspaceId)}/fs`;
}

export async function getRoots(workspaceId: string): Promise<WorkspaceRoot[]> {
  const result = await api(`${base(workspaceId)}/roots`, { method: "GET" });
  return result.roots || [];
}

export async function listEntries(workspaceId: string, ref: FileRef): Promise<FsEntry[]> {
  const query = new URLSearchParams({ rootId: ref.rootId, path: ref.path });
  const result = await api(`${base(workspaceId)}/entries?${query}`, { method: "GET" });
  return result.entries || [];
}

export async function readFile(workspaceId: string, ref: FileRef): Promise<FileSnapshot> {
  const query = new URLSearchParams({ rootId: ref.rootId, path: ref.path });
  return api(`${base(workspaceId)}/file?${query}`, { method: "GET" });
}

// mediaURL points at the raw image/video stream used by the preview surface.
// It is fetched by <img>/<video> elements, not the JSON envelope helpers.
export function mediaURL(workspaceId: string, ref: FileRef): string {
  const query = new URLSearchParams({ rootId: ref.rootId, path: ref.path });
  return `${base(workspaceId)}/media?${query}`;
}

export async function saveFile(workspaceId: string, request: {
  ref: FileRef;
  content: string;
  expectedRevision: string;
  createOnly?: boolean;
  hasBom?: boolean;
}): Promise<FileSnapshot> {
  return api(`${base(workspaceId)}/file`, { method: "PUT", body: request });
}

export async function createEntry(workspaceId: string, request: {
  parent: FileRef;
  name: string;
  kind: "file" | "directory";
  content?: string;
  hasBom?: boolean;
}): Promise<{ entry: FsEntry; file?: FileSnapshot }> {
  return api(`${base(workspaceId)}/entries`, { method: "POST", body: request });
}

export async function renameEntry(workspaceId: string, ref: FileRef, newName: string): Promise<{ entry: FsEntry; previousRef: FileRef }> {
  return api(`${base(workspaceId)}/entry`, { method: "PATCH", body: { ref, newName } });
}

export async function moveEntry(workspaceId: string, ref: FileRef, destinationParent: FileRef): Promise<{ entry: FsEntry; previousRef: FileRef }> {
  return api(`${base(workspaceId)}/entry`, { method: "PATCH", body: { ref, destinationParent } });
}

export async function trashEntry(workspaceId: string, ref: FileRef): Promise<TrashItem> {
  const result = await api(`${base(workspaceId)}/entry`, { method: "DELETE", body: { ref } });
  return result.trash;
}

export async function listTrash(workspaceId: string): Promise<TrashItem[]> {
  const result = await api(`${base(workspaceId)}/trash`, { method: "GET" });
  return result.items || [];
}

export async function restoreTrash(workspaceId: string, id: string): Promise<FsEntry> {
  const result = await api(`${base(workspaceId)}/trash/${encodeURIComponent(id)}/restore`, { method: "POST", body: {} });
  return result.entry;
}

export async function purgeTrash(workspaceId: string, id: string): Promise<void> {
  await api(`${base(workspaceId)}/trash/${encodeURIComponent(id)}?confirmed=true`, { method: "DELETE" });
}

export async function revealEntry(workspaceId: string, ref: FileRef): Promise<void> {
  await api(`${base(workspaceId)}/reveal`, { method: "POST", body: { ref } });
}

export async function searchFiles(workspaceId: string, queryText: string): Promise<{
  items: SearchResult[];
  indexing: boolean;
  indexed: number;
  truncated: boolean;
}> {
  const query = new URLSearchParams({ q: queryText, limit: "200" });
  return api(`${base(workspaceId)}/search?${query}`, { method: "GET" });
}

export async function searchEntries(workspaceId: string, queryText: string, limit = 12): Promise<{
  items: SearchResult[];
  indexing: boolean;
  indexed: number;
  truncated: boolean;
}> {
  const query = new URLSearchParams({
    q: queryText,
    limit: String(limit),
    includeDirectories: "true",
  });
  return api(`${base(workspaceId)}/search?${query}`, { method: "GET" });
}

export async function searchText(workspaceId: string, request: TextSearchRequest, signal?: AbortSignal): Promise<TextSearchResponse> {
  return api(`${base(workspaceId)}/text-search`, { method: "POST", body: request, signal });
}

export async function replaceText(workspaceId: string, request: {
  search: TextSearchRequest;
  scope: "match" | "file" | "all";
  targets: TextReplaceTarget[];
}): Promise<TextReplaceResponse> {
  return api(`${base(workspaceId)}/text-replace`, { method: "POST", body: request });
}

export async function getLSPProfiles(): Promise<{ profiles: LSPProfile[]; templates: Array<{ id: string; description: string; profile: LSPProfile }> }> {
  return api("/api/lsp/profiles", { method: "GET" });
}

export async function getWorkspaceLSPConfig(workspaceId: string): Promise<WorkspaceLSPResponse> {
  return api(`/api/workspaces/${encodeURIComponent(workspaceId)}/lsp/config`, { method: "GET" });
}

export async function saveWorkspaceLSPConfig(workspaceId: string, config: WorkspaceLSPConfig): Promise<WorkspaceLSPResponse> {
  return api(`/api/workspaces/${encodeURIComponent(workspaceId)}/lsp/config`, { method: "PUT", body: { config } });
}

export async function restartLanguageServer(workspaceId: string, profileId: string): Promise<void> {
  await api(`/api/workspaces/${encodeURIComponent(workspaceId)}/lsp/${encodeURIComponent(profileId)}/restart`, { method: "POST", body: {} });
}
