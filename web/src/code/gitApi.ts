import { api } from "../../js/api.js";
import type {
  GitActionRequest, GitActionResult, GitCommitDetail, GitDiffDocument, GitHistory,
  GitMetadata, GitRepository, GitStatus,
} from "./gitTypes";

function repositoryBase(workspaceId: string, repositoryId: string): string {
  return `/api/workspaces/${encodeURIComponent(workspaceId)}/git/repositories/${encodeURIComponent(repositoryId)}`;
}

export async function listRepositories(workspaceId: string): Promise<{ repositories: GitRepository[]; searchParentGitRepositories: boolean }> {
  return api(`/api/workspaces/${encodeURIComponent(workspaceId)}/git/repositories`, { method: "GET" });
}

export async function loadStatus(workspaceId: string, repositoryId: string): Promise<GitStatus> {
  return api(`${repositoryBase(workspaceId, repositoryId)}/status`, { method: "GET" });
}

export async function loadDiff(workspaceId: string, repositoryId: string, options: { scope: string; path: string; oldPath?: string; ref?: string }, signal?: AbortSignal): Promise<GitDiffDocument> {
  return api(`${repositoryBase(workspaceId, repositoryId)}/diff`, { method: "GET", query: options, signal });
}

export async function loadMetadata(workspaceId: string, repositoryId: string): Promise<GitMetadata> {
  return api(`${repositoryBase(workspaceId, repositoryId)}/metadata`, { method: "GET" });
}

export async function loadHistory(workspaceId: string, repositoryId: string, offset = 0): Promise<GitHistory> {
  return api(`${repositoryBase(workspaceId, repositoryId)}/history`, { method: "GET", query: { offset, limit: 100 } });
}

export async function loadCommitDetail(workspaceId: string, repositoryId: string, ref: string, kind: "commit" | "stash" = "commit"): Promise<GitCommitDetail> {
  return api(`${repositoryBase(workspaceId, repositoryId)}/detail`, { method: "GET", query: { ref, kind } });
}

export async function runAction(workspaceId: string, repositoryId: string, request: GitActionRequest): Promise<GitActionResult> {
  return api(`${repositoryBase(workspaceId, repositoryId)}/actions`, { method: "POST", body: request });
}

export async function initializeRepository(workspaceId: string, rootId: string, path = ""): Promise<{ repositories: GitRepository[] }> {
  return api(`/api/workspaces/${encodeURIComponent(workspaceId)}/git/initialize`, { method: "POST", body: { rootId, path } });
}

export async function cloneRepository(workspaceId: string, url: string, rootId: string, destination: string): Promise<{ repositories: GitRepository[] }> {
  return api(`/api/workspaces/${encodeURIComponent(workspaceId)}/git/clone`, { method: "POST", body: { url, rootId, destination } });
}

export async function setParentRepositorySearch(workspaceId: string, enabled: boolean): Promise<{ repositories: GitRepository[] }> {
  return api(`/api/workspaces/${encodeURIComponent(workspaceId)}/git/settings`, { method: "PUT", body: { searchParentGitRepositories: enabled } });
}
