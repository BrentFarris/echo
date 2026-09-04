import { api } from "../../js/api.js";
import * as gitAPI from "./gitApi";
import type { GitRepository, GitStatus } from "./gitTypes";
import type {
  SourceControlActionRequest, SourceControlActionResult, SourceControlDiffDocument, SourceControlDiffRequest, SourceControlHistory,
  SourceControlMetadata, SourceControlProvider, SourceControlRepository, SourceControlRevisionDetail, SourceControlStatus,
} from "./sourceControlTypes";
import { normalizeStatus } from "./sourceControlTypes";

// A workspace is put into legacy mode only when repository discovery proves
// that the provider-neutral route does not exist. Mutating requests are never
// retried after an arbitrary failure, which prevents duplicate Git actions.
const legacyGitWorkspaces = new Set<string>();

function legacyEndpointUnavailable(error: unknown): boolean {
  const candidate = error as { status?: number; message?: string } | null;
  return candidate?.status === 404 || candidate?.status === 405
    || candidate?.message === "Source Control repository response is unavailable";
}

function repositoryBase(workspaceId: string, repositoryId: string): string {
  return `/api/workspaces/${encodeURIComponent(workspaceId)}/source-control/repositories/${encodeURIComponent(repositoryId)}`;
}

export async function listRepositories(workspaceId: string): Promise<{ repositories: SourceControlRepository[]; providers: SourceControlProvider[]; searchParentRepositories: boolean }> {
  if (legacyGitWorkspaces.has(workspaceId)) return legacyRepositories(workspaceId);
  try {
    const response = await api(`/api/workspaces/${encodeURIComponent(workspaceId)}/source-control/repositories`, { method: "GET" }) as { repositories?: SourceControlRepository[]; providers?: SourceControlProvider[]; searchParentRepositories?: boolean } | undefined;
    if (!response || !Array.isArray(response.repositories)) throw new Error("Source Control repository response is unavailable");
    return { repositories: response.repositories, providers: response.providers || [], searchParentRepositories: response.searchParentRepositories === true };
  } catch (error) {
    if (!legacyEndpointUnavailable(error)) throw error;
    // One-release bridge for older hosts and extensions still mocking the Git
    // transport. New writes and successful requests always use Source Control.
    legacyGitWorkspaces.add(workspaceId);
    return legacyRepositories(workspaceId);
  }
}

export async function loadStatus(workspaceId: string, repositoryId: string): Promise<SourceControlStatus> {
  if (legacyGitWorkspaces.has(workspaceId)) return gitStatus(await gitAPI.loadStatus(workspaceId, repositoryId));
  const status = await api(`${repositoryBase(workspaceId, repositoryId)}/status`, { method: "GET" }) as Omit<SourceControlStatus, "conflicts" | "staged" | "unstaged" | "hiddenStagedCount">;
  return normalizeStatus(status);
}

export async function loadDiff(workspaceId: string, repositoryId: string, options: SourceControlDiffRequest & { scope?: string }, signal?: AbortSignal): Promise<SourceControlDiffDocument> {
  const groupId = options.groupId || (options.scope === "staged" ? "staged" : options.scope === "unstaged" ? "working" : options.scope === "conflict" ? "conflicts" : undefined);
  const kind = options.kind || (options.scope === "commit" ? "revision" : options.scope === "stash" ? "stash" : "change");
  let wire: SourceControlDiffDocument;
  if (legacyGitWorkspaces.has(workspaceId)) {
    wire = await gitAPI.loadDiff(workspaceId, repositoryId, {
      scope: (options.scope || (groupId === "staged" ? "staged" : "unstaged")) as "staged" | "unstaged" | "commit" | "stash",
      path: options.path, oldPath: options.oldPath, ref: options.ref,
    }, signal) as SourceControlDiffDocument;
  } else {
    wire = await api(`${repositoryBase(workspaceId, repositoryId)}/diff`, { method: "GET", query: { kind, groupId, path: options.path, oldPath: options.oldPath, baseRef: options.baseRef, ref: options.ref }, signal }) as SourceControlDiffDocument;
  }
  return {
    ...wire,
    scope: options.scope || (wire.target?.kind === "revision" ? "commit" : wire.target?.kind === "stash" ? "stash" : wire.target?.groupId === "staged" ? "staged" : "unstaged"),
    path: wire.target?.path || options.path,
    oldPath: wire.target?.oldPath || options.oldPath,
  } as SourceControlDiffDocument;
}

export async function loadMetadata(workspaceId: string, repositoryId: string): Promise<SourceControlMetadata> {
  if (legacyGitWorkspaces.has(workspaceId)) return gitAPI.loadMetadata(workspaceId, repositoryId);
  return api(`${repositoryBase(workspaceId, repositoryId)}/metadata`, { method: "GET" });
}

export async function loadHistory(workspaceId: string, repositoryId: string, offset = 0): Promise<SourceControlHistory> {
  if (legacyGitWorkspaces.has(workspaceId)) return gitAPI.loadHistory(workspaceId, repositoryId, offset);
  return api(`${repositoryBase(workspaceId, repositoryId)}/history`, { method: "GET", query: { offset, limit: 100 } });
}

export async function loadRevisionDetail(workspaceId: string, repositoryId: string, ref: string, kind: "commit" | "stash" = "commit"): Promise<SourceControlRevisionDetail> {
  if (legacyGitWorkspaces.has(workspaceId)) return gitAPI.loadCommitDetail(workspaceId, repositoryId, ref, kind);
  return api(`${repositoryBase(workspaceId, repositoryId)}/detail`, { method: "GET", query: { ref, kind } });
}

export async function runAction(workspaceId: string, repositoryId: string, request: SourceControlActionRequest): Promise<SourceControlActionResult> {
  if (legacyGitWorkspaces.has(workspaceId)) return gitAPI.runAction(workspaceId, repositoryId, request as never);
  const result = await api(`${repositoryBase(workspaceId, repositoryId)}/actions`, { method: "POST", body: request }) as SourceControlActionResult | undefined;
  if (!result?.repositoryId) throw new Error("Source Control action response is unavailable");
  return result;
}

export async function setParentRepositorySearch(workspaceId: string, enabled: boolean): Promise<{ repositories: SourceControlRepository[] }> {
  if (legacyGitWorkspaces.has(workspaceId)) {
    const response = await gitAPI.setParentRepositorySearch(workspaceId, enabled);
    return { repositories: (response.repositories || []).map(gitRepository) };
  }
  return api(`/api/workspaces/${encodeURIComponent(workspaceId)}/source-control/settings`, { method: "PUT", body: { searchParentRepositories: enabled } });
}

// Fossil project creation is intentionally deferred; Git keeps its mature
// initialization and clone endpoints during the compatibility window.
export async function initializeGitRepository(workspaceId: string, rootId: string, path = ""): Promise<{ repositories: SourceControlRepository[] }> {
  return api(`/api/workspaces/${encodeURIComponent(workspaceId)}/git/initialize`, { method: "POST", body: { rootId, path } });
}

export async function cloneGitRepository(workspaceId: string, url: string, rootId: string, destination: string): Promise<{ repositories: SourceControlRepository[] }> {
  return api(`/api/workspaces/${encodeURIComponent(workspaceId)}/git/clone`, { method: "POST", body: { url, rootId, destination } });
}

const gitCapabilities: SourceControlProvider["capabilities"] = [
  "status", "diff", "history", "stage", "commitAll", "commitSelected", "sync", "pull", "push",
  "branches", "merge", "stashes", "initialize", "clone",
];

async function legacyRepositories(workspaceId: string): Promise<{ repositories: SourceControlRepository[]; providers: SourceControlProvider[]; searchParentRepositories: boolean }> {
  const legacy = await gitAPI.listRepositories(workspaceId);
  return {
    repositories: (legacy.repositories || []).map(gitRepository),
    providers: [{ id: "git", label: "Git", available: true, capabilities: gitCapabilities }],
    searchParentRepositories: legacy.searchParentGitRepositories,
  };
}

function gitRepository(repository: GitRepository): SourceControlRepository {
  return { ...repository, providerId: "git", providerLabel: "Git", available: true, capabilities: gitCapabilities };
}

function gitStatus(status: GitStatus): SourceControlStatus {
  const groups = [
    { id: "conflicts", label: "Merge Changes", role: "conflicts", actions: ["stage", "discard"], changes: status.conflicts.map((change) => ({ ...change, groupId: "conflicts" })) },
    { id: "staged", label: "Staged Changes", role: "included", actions: ["unstage", "commit_staged"], changes: status.staged.map((change) => ({ ...change, groupId: "staged" })) },
    { id: "unstaged", label: "Changes", role: "working", actions: ["stage", "discard"], changes: status.unstaged.map((change) => ({ ...change, groupId: "unstaged" })) },
  ];
  return normalizeStatus({ ...status, providerId: "git", groups, hiddenChangeCount: status.hiddenStagedCount });
}
