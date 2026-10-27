import { refKey, type FileRef, type FsEntry } from "./types";

export type TreeNode = {
  key: string;
  ref: FileRef;
  name: string;
  hostPath: string;
  kind: "file" | "directory";
  isRoot: boolean;
  isSymlink: boolean;
  readOnly: boolean;
  blockedReason?: string;
  depth: number;
  parentKey: string | null;
  loaded: boolean;
  loading: boolean;
  children: string[];
};

export function reconcileDirectory(
  nodes: Map<string, TreeNode>, expanded: Set<string>, parent: TreeNode, entries: FsEntry[],
): void {
  const removeBranch = (key: string) => {
    nodes.get(key)?.children.forEach(removeBranch);
    nodes.delete(key);
    expanded.delete(key);
  };
  const keys = new Set(entries.map((entry) => refKey(entry.ref)));
  for (const key of parent.children) if (!keys.has(key)) removeBranch(key);
  parent.children = entries.map((entry) => {
    const key = refKey(entry.ref);
    let child = nodes.get(key);
    // A replaced directory or changed symlink/access boundary cannot reuse its contents.
    if (child && (child.kind !== entry.kind || child.isSymlink !== entry.isSymlink || child.blockedReason !== entry.blockedReason)) {
      removeBranch(key);
      child = undefined;
    }
    const metadata = {
      key, ref: entry.ref, name: entry.name, hostPath: entry.hostPath, kind: entry.kind,
      isRoot: false, isSymlink: entry.isSymlink, readOnly: Boolean(entry.readOnly),
      blockedReason: entry.blockedReason, depth: parent.depth + 1, parentKey: parent.key,
    };
    if (child) Object.assign(child, metadata);
    else nodes.set(key, { ...metadata, loaded: false, loading: false, children: [] });
    return key;
  });
  parent.loaded = true;
}

type DirectoryLoad = { promise: Promise<void>; again: boolean };

/** One request per node at a time, with a trailing refresh for changes received in flight. */
export class ExplorerDirectoryLoader {
  private readonly pending = new Map<TreeNode, DirectoryLoad>();

  constructor(private readonly options: {
    isCurrent(node: TreeNode): boolean;
    listEntries(ref: FileRef): Promise<FsEntry[]>;
    apply(node: TreeNode, entries: FsEntry[]): void;
    render(): void;
    onError(error: unknown): void;
  }) {}

  load(node: TreeNode, force = false): Promise<void> {
    if (node.kind !== "directory" || node.blockedReason || !this.options.isCurrent(node)) return Promise.resolve();
    const current = this.pending.get(node);
    if (current) {
      current.again ||= force;
      return current.promise;
    }
    if (node.loaded && !force) return Promise.resolve();
    const request: DirectoryLoad = { promise: Promise.resolve(), again: false };
    this.pending.set(node, request);
    // Install the pending entry before any render callbacks can request this node again.
    request.promise = Promise.resolve().then(async () => {
      try {
        do {
          request.again = false;
          if (!this.options.isCurrent(node)) return;
          node.loading = !node.loaded;
          if (node.loading) this.options.render();
          try {
            const entries = await this.options.listEntries(node.ref);
            if (!this.options.isCurrent(node)) return;
            this.options.apply(node, entries);
          } catch (error) {
            if (this.options.isCurrent(node)) this.options.onError(error);
          } finally {
            node.loading = false;
            if (this.options.isCurrent(node)) this.options.render();
          }
        } while (request.again);
      } finally {
        this.pending.delete(node);
      }
    });
    return request.promise;
  }
}

/** Keep the top visible item at the same pixel offset, or clamp if it disappeared. */
export function preservedTreeScrollTop(
  previous: readonly TreeNode[], next: readonly TreeNode[], scrollTop: number, viewportHeight: number, rowHeight: number,
): number {
  const index = Math.floor(scrollTop / rowHeight);
  const key = previous[index]?.key;
  const nextIndex = key === undefined ? -1 : next.findIndex((node) => node.key === key);
  const offset = nextIndex < 0 ? scrollTop : nextIndex * rowHeight + scrollTop - index * rowHeight;
  return Math.max(0, Math.min(offset, Math.max(0, next.length * rowHeight - viewportHeight)));
}

export type FilesystemChange = { op: string; ref: FileRef; isDirectory?: boolean };

export function needsDirectoryRefresh(change: FilesystemChange, nodes: Map<string, TreeNode>): boolean {
  return change.op !== "write" || Boolean(change.isDirectory) || nodes.get(refKey(change.ref))?.kind !== "file";
}
