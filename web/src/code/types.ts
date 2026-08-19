export type FileRef = { rootId: string; path: string };

export type WorkspaceRoot = {
  id: string;
  label: string;
  referenceLabel: string;
  hostPath: string;
  blockedReason?: string;
};

export type FsEntry = {
  ref: FileRef;
  name: string;
  hostPath: string;
  kind: "file" | "directory";
  isSymlink: boolean;
  blockedReason?: string;
  size?: number;
  modifiedAt: string;
};

export type FileSnapshot = {
  ref: FileRef;
  hostPath: string;
  content: string;
  revision: string;
  size: number;
  modifiedAt: string;
  encoding: "utf-8";
  eol: "lf" | "crlf";
  hasBom: boolean;
};

export type TrashItem = {
  id: string;
  workspaceId: string;
  ref: FileRef;
  name: string;
  kind: "file" | "directory";
  hostPath: string;
  deletedAt: string;
};

export type SearchResult = {
  ref: FileRef;
  name: string;
  hostPath: string;
  referencePath: string;
  kind: "file" | "directory";
  score: number;
};

export type PersistedTab = {
  kind?: "file" | "diff";
  id: string;
  ref: FileRef | null;
  title: string;
  hostPath: string;
  pinned: boolean;
  preview: boolean;
  dirty: boolean;
  deleted: boolean;
  revision: string;
  hasBom: boolean;
  eol: "lf" | "crlf";
  content?: string;
  cursor?: { lineNumber: number; column: number };
  scrollTop?: number;
  diff?: {
    repository: {
      id: string;
      label: string;
      rootRef?: FileRef;
      parent: boolean;
      scopes: Array<{ rootId: string; rootLabel: string; repoPrefix: string }>;
      revision: number;
    };
    scope: "staged" | "unstaged" | "commit" | "stash";
    reviewRef?: string;
    fileRef?: FileRef;
    oldPath?: string;
    path: string;
    editable: boolean;
  };
};

export type PersistedWorkspaceSession = {
  version: 1 | 2 | 3;
  activeTabId: string | null;
  tabs: PersistedTab[];
  expanded: string[];
  selectedTreeKey?: string | null;
  explorerWidth: number;
  codeChatWidth?: number;
  treeScrollTop: number;
};

export function refKey(ref: FileRef): string {
  return `${ref.rootId}:${ref.path}`;
}

export function isRefWithin(candidate: FileRef, parent: FileRef): boolean {
  return candidate.rootId === parent.rootId &&
    (parent.path === "" || candidate.path === parent.path || candidate.path.startsWith(`${parent.path}/`));
}

export function joinRef(parent: FileRef, name: string): FileRef {
  return { rootId: parent.rootId, path: parent.path ? `${parent.path}/${name}` : name };
}
