import type { FileRef } from "./types";

export type SourceControlCapability =
  | "status" | "diff" | "history" | "stage" | "track" | "commitAll" | "commitSelected"
  | "update" | "sync" | "pull" | "push" | "branches" | "merge" | "stashes" | "initialize" | "clone";

export type SourceControlProvider = {
  id: string;
  label: string;
  available: boolean;
  version?: string;
  diagnostic?: string;
  capabilities: SourceControlCapability[];
};

export type SourceControlRepository = {
  id: string;
  providerId: string;
  providerLabel: string;
  label: string;
  rootRef?: FileRef;
  parent: boolean;
  scopes: Array<{ rootId: string; rootLabel: string; repoPrefix: string }>;
  revision: number;
  available: boolean;
  diagnostic?: string;
  capabilities: SourceControlCapability[];
};

export type SourceControlChange = {
  path: string;
  oldPath?: string;
  ref?: FileRef;
  status: string;
  statusCode: string;
  kind?: string;
  groupId: string;
  // Presentation scope keeps the mature virtualized renderer independent of
  // provider-native group IDs.
  scope: string;
  submodule?: boolean;
};

export type SourceControlChangeGroup = {
  id: string;
  label: string;
  role: "conflicts" | "included" | "working" | "untracked" | string;
  changes: Array<Omit<SourceControlChange, "scope">>;
  actions: string[];
};

export type SourceControlStatus = {
  workspaceId: string;
  repositoryId: string;
  providerId: string;
  revision: number;
  branch: string;
  head?: string;
  detached: boolean;
  upstream?: string;
  ahead: number;
  behind: number;
  stashCount?: number;
  groups: SourceControlChangeGroup[];
  hiddenChangeCount?: number;
  truncated?: boolean;
  totalChangeCount: number;
  state: { mergeInProgress?: boolean; rebaseInProgress?: boolean; cherryPickInProgress?: boolean };
  conflicts: SourceControlChange[];
  staged: SourceControlChange[];
  unstaged: SourceControlChange[];
  hiddenStagedCount?: number;
};

export type SourceControlDiffSide = {
  label: string;
  content: string;
  exists: boolean;
  eol: "lf" | "crlf";
  hasBom?: boolean;
};

export type SourceControlDiffTarget = {
  kind: string;
  groupId?: string;
  path: string;
  oldPath?: string;
  baseRef?: string;
  ref?: string;
};

export type SourceControlDiffRequest = SourceControlDiffTarget & { fileRef?: FileRef };

export type SourceControlDiffDocument = {
  repositoryId: string;
  providerId: string;
  target: SourceControlDiffTarget;
  // These presentation fields are derived from target by the API adapter and
  // keep editor tabs independent of the provider wire representation.
  scope: string;
  path: string;
  oldPath?: string;
  ref?: FileRef;
  revision: number;
  modifiedRevision?: string;
  original: SourceControlDiffSide;
  modified: SourceControlDiffSide;
  editable: boolean;
  kind: "text" | "binary" | "too-large" | "submodule";
  unavailableReason?: string;
};

export type SourceControlMetadata = {
  branches: Array<{ name: string; current?: boolean; remote?: boolean; closed?: boolean }>;
  remoteBranches: Array<{ name: string; current?: boolean; remote?: boolean; closed?: boolean }>;
  remotes: Array<{ name: string; fetchUrl?: string; pushUrl?: string }>;
  tags: string[];
  stashes: Array<{ ref: string; hash?: string; message: string }>;
};

export type SourceControlCommit = {
  hash: string;
  parents: string[];
  author: string;
  authoredAt: string;
  refs: string[];
  subject: string;
};

export type SourceControlHistory = { commits: SourceControlCommit[]; nextOffset?: number; hasMore: boolean };
export type SourceControlRevisionDetail = { ref: string; files: Array<{ path: string; oldPath?: string; status: string }> };

// Providers validate the inputs belonging to their declared action IDs. The
// common envelope stays extensible without adding provider fields to the core
// renderer every time another VCS is registered.
export type SourceControlActionRequest = {
  requestId: string;
  action: string;
  expectedRevision?: number;
  paths?: string[];
  message?: string;
  ref?: string;
  startPoint?: string;
  name?: string;
  remote?: string;
  branch?: string;
  url?: string;
  confirmed?: boolean;
};

export type SourceControlActionResult = {
  requestId: string;
  repositoryId: string;
  revision: number;
  affectedPaths?: string[];
  trashIds?: string[];
};

export type SourceControlOperationEvent = {
  workspaceId: string;
  repositoryId: string;
  providerId?: string;
  requestId: string;
  action: string;
  state: "running" | "completed" | "failed";
  error?: string;
};

export function normalizeStatus(status: Omit<SourceControlStatus, "conflicts" | "staged" | "unstaged" | "hiddenStagedCount">): SourceControlStatus {
  const legacy = status as unknown as Partial<SourceControlStatus>;
  const groups = Array.isArray(status.groups) ? status.groups : [
    { id: "conflicts", label: "Merge Changes", role: "conflicts", actions: ["stage", "discard"], changes: (legacy.conflicts || []).map((change) => ({ ...change, groupId: "conflicts" })) },
    { id: "staged", label: "Staged Changes", role: "included", actions: ["unstage", "commit_staged"], changes: (legacy.staged || []).map((change) => ({ ...change, groupId: "staged" })) },
    { id: "unstaged", label: "Changes", role: "working", actions: ["stage", "discard"], changes: (legacy.unstaged || []).map((change) => ({ ...change, groupId: "unstaged" })) },
  ];
  const convert = (group: SourceControlChangeGroup, scope: string): SourceControlChange[] =>
    (group.changes || []).map((change) => ({ ...change, groupId: group.id, scope }));
  const conflictGroups = groups.filter((group) => group.role === "conflicts");
  const includedGroups = groups.filter((group) => group.role === "included");
  const workingGroups = groups.filter((group) => group.role === "working" || group.role === "untracked");
  return {
    ...status,
    providerId: status.providerId || "git",
    groups,
    conflicts: conflictGroups.flatMap((group) => convert(group, (status.providerId || "git") === "git" ? "conflict" : group.id)),
    staged: includedGroups.flatMap((group) => convert(group, (status.providerId || "git") === "git" ? "staged" : group.id)),
    unstaged: workingGroups.flatMap((group) => convert(group, (status.providerId || "git") === "git" ? "unstaged" : group.id)),
    hiddenStagedCount: (status.providerId || "git") === "git" ? status.hiddenChangeCount ?? legacy.hiddenStagedCount : 0,
  };
}
