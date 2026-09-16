import { describe, expect, it, vi } from "vitest";
import { ExplorerDirectoryLoader, needsDirectoryRefresh, preservedTreeScrollTop, reconcileDirectory, type TreeNode } from "./explorerTree";
import { refKey, type FsEntry } from "./types";

function entry(path: string, kind: FsEntry["kind"] = "file"): FsEntry {
  return { ref: { rootId: "root", path }, name: path.split("/").pop()!, hostPath: `/workspace/${path}`, kind, isSymlink: false, modifiedAt: "now" };
}

function fixture() {
  const root: TreeNode = {
    key: "root:", ref: { rootId: "root", path: "" }, name: "workspace", hostPath: "/workspace", kind: "directory",
    isRoot: true, isSymlink: false, readOnly: false, depth: 0, parentKey: null, loaded: false, loading: false, children: [],
  };
  const nodes = new Map([[root.key, root]]);
  const expanded = new Set([root.key, "root:src"]);
  const apply = (parent: TreeNode, entries: FsEntry[]) => reconcileDirectory(nodes, expanded, parent, entries);
  apply(root, [entry("src", "directory"), entry("main.go")]);
  const src = nodes.get("root:src")!;
  apply(src, [entry("src/child.go")]);
  return { root, nodes, expanded, apply, src };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

describe("explorer directory reconciliation", () => {
  it("retains unchanged nodes, expanded descendants, and loaded contents while updating metadata", () => {
    const { nodes, expanded, root, src, apply } = fixture();
    const child = nodes.get("root:src/child.go");
    const main = nodes.get("root:main.go");
    apply(root, [entry("src", "directory"), { ...entry("main.go"), readOnly: true }]);
    expect(nodes.get(src.key)).toBe(src);
    expect(src.loaded).toBe(true);
    expect(src.children).toEqual(["root:src/child.go"]);
    expect(nodes.get("root:src/child.go")).toBe(child);
    expect(nodes.get("root:main.go")).toBe(main);
    expect(main?.readOnly).toBe(true);
    expect([...expanded]).toEqual(["root:", "root:src"]);
  });

  it("adds and removes real entries and prunes a directory replaced by a file", () => {
    const { nodes, expanded, root, apply } = fixture();
    apply(root, [entry("src"), entry("new.go")]);
    expect(root.children).toEqual(["root:src", "root:new.go"]);
    expect(nodes.has("root:src/child.go")).toBe(false);
    expect(nodes.has("root:main.go")).toBe(false);
    expect(nodes.get("root:src")?.kind).toBe("file");
    expect(expanded.has("root:src")).toBe(false);
    apply(root, []);
    expect([...nodes.keys()]).toEqual([root.key]);
  });

  it("does not retain children when a directory becomes blocked", () => {
    const { nodes, root, apply } = fixture();
    apply(root, [{ ...entry("src", "directory"), blockedReason: "Outside workspace" }]);
    expect(nodes.has("root:src/child.go")).toBe(false);
    expect(nodes.get("root:src")?.children).toEqual([]);
  });

  it("only skips content writes to known files, including no suppression of atomic replacement events", () => {
    const { nodes } = fixture();
    expect(needsDirectoryRefresh({ op: "write", ref: entry("main.go").ref }, nodes)).toBe(false);
    for (const op of ["create", "rename", "delete", "metadata"]) {
      expect(needsDirectoryRefresh({ op, ref: entry("main.go").ref }, nodes)).toBe(true);
    }
    expect(needsDirectoryRefresh({ op: "write", ref: entry("unknown.go").ref }, nodes)).toBe(true);
    expect(needsDirectoryRefresh({ op: "write", ref: entry("src").ref }, nodes)).toBe(true);
    expect(needsDirectoryRefresh({ op: "write", ref: entry("main.go").ref, isDirectory: true }, nodes)).toBe(true);
  });
});

describe("explorer directory requests", () => {
  function setup() {
    const tree = fixture();
    const request = deferred<FsEntry[]>();
    let active = true;
    const listEntries = vi.fn(() => request.promise);
    const render = vi.fn();
    const onError = vi.fn();
    const loader = new ExplorerDirectoryLoader({
      isCurrent: (node) => active && tree.nodes.get(node.key) === node,
      listEntries, apply: tree.apply, render, onError,
    });
    return { ...tree, request, loader, listEntries, render, onError, dispose: () => { active = false; } };
  }

  it("keeps loaded children visible without a spinner and coalesces requests into a trailing refresh", async () => {
    const t = setup();
    const next = deferred<FsEntry[]>();
    t.listEntries.mockImplementationOnce(() => t.request.promise).mockImplementationOnce(() => next.promise);
    const pending = t.loader.load(t.root, true);
    await Promise.resolve();
    expect(t.root.loading).toBe(false);
    expect(t.src.children).toEqual(["root:src/child.go"]);
    expect(t.render).not.toHaveBeenCalled();
    expect(t.loader.load(t.root)).toBe(pending);
    expect(t.loader.load(t.root, true)).toBe(pending);
    expect(t.loader.load(t.root, true)).toBe(pending);
    expect(t.listEntries).toHaveBeenCalledTimes(1);
    t.request.resolve([entry("src", "directory"), entry("main.go")]);
    await vi.waitFor(() => expect(t.listEntries).toHaveBeenCalledTimes(2));
    next.resolve([entry("src", "directory"), entry("main.go"), entry("latest.go")]);
    await pending;
    expect(t.nodes.has("root:latest.go")).toBe(true);
    expect(t.nodes.get(t.src.key)).toBe(t.src);
    expect(t.listEntries).toHaveBeenCalledTimes(2);
  });

  it("shows loading only for the first load and shares that request", async () => {
    const t = setup();
    t.src.loaded = false;
    t.src.children = [];
    const pending = t.loader.load(t.src);
    await Promise.resolve();
    expect(t.src.loading).toBe(true);
    expect(t.loader.load(t.src)).toBe(pending);
    t.request.resolve([entry("src/new.go")]);
    await pending;
    expect(t.src.loading).toBe(false);
    expect(t.src.loaded).toBe(true);
    expect(t.listEntries).toHaveBeenCalledTimes(1);
  });

  it("retains displayed contents on failure and allows a retry", async () => {
    const t = setup();
    const pending = t.loader.load(t.root, true);
    t.request.reject(new Error("Offline"));
    await pending;
    expect(t.root.children).toEqual(["root:src", "root:main.go"]);
    expect(t.nodes.get(t.src.key)).toBe(t.src);
    expect(t.onError).toHaveBeenCalledOnce();
    t.listEntries.mockResolvedValue([entry("retry.go")]);
    await t.loader.load(t.root, true);
    expect(t.root.children).toEqual(["root:retry.go"]);
  });

  it.each(["removed", "replaced", "disposed"])("ignores a response for a %s node", async (reason) => {
    const t = setup();
    const pending = t.loader.load(t.src, true);
    await Promise.resolve();
    if (reason === "removed") t.nodes.delete(t.src.key);
    if (reason === "replaced") t.nodes.set(t.src.key, { ...t.src });
    if (reason === "disposed") t.dispose();
    t.request.resolve([entry("src/stale.go")]);
    await pending;
    expect(t.nodes.has("root:src/stale.go")).toBe(false);
    expect(t.render).not.toHaveBeenCalled();
  });
});

describe("explorer viewport anchoring", () => {
  const rows = Array.from({ length: 100 }, (_, i) => ({ key: refKey(entry(`${i}.go`).ref) }) as TreeNode);
  it("keeps the precise scroll offset on unchanged listings", () => {
    expect(preservedTreeScrollTop(rows, [...rows], 1107, 220, 22)).toBe(1107);
  });
  it("keeps the visible row in place after insertions and removals above it", () => {
    expect(preservedTreeScrollTop(rows, [{ key: "new" } as TreeNode, ...rows], 1107, 220, 22)).toBe(1129);
    expect(preservedTreeScrollTop(rows, rows.slice(5), 1107, 220, 22)).toBe(997);
  });
  it("clamps when the anchor disappears or the remaining contents cannot fill the viewport", () => {
    expect(preservedTreeScrollTop(rows, rows.slice(0, 20), 1107, 220, 22)).toBe(220);
    expect(preservedTreeScrollTop(rows, [], 1107, 220, 22)).toBe(0);
  });
});
