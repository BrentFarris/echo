import type { FileRef } from "./types";

export type GitScope = "conflict" | "staged" | "unstaged" | "commit" | "stash";

export type GitRepository = {
  id: string;
  label: string;
  rootRef?: FileRef;
  parent: boolean;
  scopes: Array<{ rootId: string; rootLabel: string; repoPrefix: string }>;
  revision: number;
};

export type GitChange = {
  path: string;
  oldPath?: string;
  ref?: FileRef;
  status: string;
  statusCode: string;
  indexStatus?: string;
  worktreeStatus?: string;
  scope: "conflict" | "staged" | "unstaged";
  submodule?: boolean;
};

export type GitStatus = {
  workspaceId: string;
  repositoryId: string;
  revision: number;
  branch: string;
  head?: string;
  detached: boolean;
  upstream?: string;
  ahead: number;
  behind: number;
  stashCount?: number;
  conflicts: GitChange[];
  staged: GitChange[];
  unstaged: GitChange[];
  hiddenStagedCount?: number;
  truncated?: boolean;
  totalChangeCount: number;
  state: { mergeInProgress?: boolean; rebaseInProgress?: boolean; cherryPickInProgress?: boolean };
};

export type GitDiffSide = { label: string; content: string; exists: boolean; eol: "lf" | "crlf"; hasBom?: boolean };

export type GitDiffDocument = {
  repositoryId: string;
  scope: GitScope;
  path: string;
  oldPath?: string;
  ref?: FileRef;
  revision: number;
  modifiedRevision?: string;
  original: GitDiffSide;
  modified: GitDiffSide;
  editable: boolean;
  kind: "text" | "binary" | "too-large" | "submodule";
  unavailableReason?: string;
};

export type GitMetadata = {
  branches: Array<{ name: string; current?: boolean; remote?: boolean }>;
  remoteBranches: Array<{ name: string; current?: boolean; remote?: boolean }>;
  remotes: Array<{ name: string; fetchUrl?: string; pushUrl?: string }>;
  tags: string[];
  stashes: Array<{ ref: string; hash: string; message: string }>;
};

export type GitCommit = {
  hash: string;
  parents: string[];
  author: string;
  authoredAt: string;
  refs: string[];
  subject: string;
};

export type GitHistory = { commits: GitCommit[]; nextOffset?: number; hasMore: boolean };
export type GitCommitDetail = { ref: string; files: Array<{ path: string; oldPath?: string; status: string }> };

type RequestBase = { requestId: string };
export type GitActionRequest =
  | (RequestBase & { action: "stage" | "unstage"; paths: string[] })
  | (RequestBase & { action: "stage_all" | "unstage_all" })
  | (RequestBase & { action: "discard"; paths: string[]; confirmed: true })
  | (RequestBase & { action: "discard_all"; confirmed: true })
  | (RequestBase & { action: "commit_staged" | "commit_all" | "commit_staged_amend" | "commit_all_amend" | "commit_staged_signoff" | "commit_all_signoff"; message: string })
  | (RequestBase & { action: string; paths?: string[]; message?: string; ref?: string; startPoint?: string; name?: string; remote?: string; branch?: string; url?: string; confirmed?: boolean });

export type GitActionResult = {
  requestId: string;
  repositoryId: string;
  revision: number;
  affectedPaths?: string[];
  trashIds?: string[];
};

export type GitOperationEvent = {
  workspaceId: string;
  repositoryId: string;
  requestId: string;
  action: string;
  state: "running" | "completed" | "failed";
  error?: string;
};
