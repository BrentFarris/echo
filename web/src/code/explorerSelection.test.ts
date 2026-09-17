import { describe, expect, it } from "vitest";
import { updateSelection, type SelectionState } from "./sourceControlSelection";
import { canDeleteTreeNodes, runTreeOperation, topLevelTreeNodes, treeMoveDestination } from "./explorerSelection";
import type { TreeNode } from "./explorerTree";
import { refKey } from "./types";

const keys = ["a", "b", "c", "d", "e"];
const empty = (): SelectionState => ({ selected: new Set(), anchor: null });
const choose = (state: SelectionState, key: string, toggle = false, range = false) => updateSelection(state, keys, key, { toggle, range });

function node(path: string, overrides: Partial<TreeNode> = {}): TreeNode {
  const ref = { rootId: "root", path };
  return { ref, key: refKey(ref), name: path.split("/").pop() || "Root", hostPath: path,
    kind: "file", depth: 1, parentKey: refKey({ ...ref, path: path.split("/").slice(0, -1).join("/") }),
    isRoot: false, isSymlink: false, readOnly: false, loaded: false, loading: false, children: [], ...overrides };
}

describe("Explorer selection", () => {
  it("toggles items independently, including the last selected item", () => {
    const initial = choose(empty(), "b");
    const added = choose(initial, "d", true);
    expect([...initial.selected]).toEqual(["b"]);
    expect([...added.selected]).toEqual(["b", "d"]);
    const removed = choose(choose(added, "b", true), "d", true);
    expect([...removed.selected]).toEqual([]);
    expect(removed.anchor).toBe("d");
  });

  it("extends and shrinks ranges in both directions without moving the anchor", () => {
    let state = choose(empty(), "c");
    state = choose(state, "e", false, true);
    expect([...state.selected]).toEqual(["c", "d", "e"]);
    state = choose(state, "d", false, true);
    expect([...state.selected]).toEqual(["c", "d"]);
    state = choose(state, "a", false, true);
    expect([...state.selected]).toEqual(["a", "b", "c"]);
    expect(state.anchor).toBe("c");
  });

  it("adds ranges to a disjoint selection and resets on a plain click", () => {
    const state = choose(choose(choose(empty(), "a"), "c", true), "e", true, true);
    expect([...state.selected]).toEqual(["a", "c", "d", "e"]);
    expect([...choose(state, "b").selected]).toEqual(["b"]);
  });

  it("selects the target when a range has no usable anchor", () => {
    for (const anchor of [null, "missing"]) {
      expect(choose({ selected: new Set(["a"]), anchor }, "d", false, true)).toEqual({ selected: new Set(["d"]), anchor: "d" });
    }
  });

  it("uses the full supplied row order, including virtualized rows and folders", () => {
    const ordered = Array.from({ length: 150 }, (_, index) => `item-${index}`);
    const state = updateSelection({ selected: new Set([ordered[2]]), anchor: ordered[2] }, ordered, ordered[120], { range: true, toggle: false });
    expect([...state.selected]).toEqual(ordered.slice(2, 121));
  });
});

describe("Explorer group operations", () => {
  it("deduplicates descendants regardless of selection order, without matching path prefixes or other roots", () => {
    const folder = node("folder", { kind: "directory" });
    const sibling = node("folder-other/a.txt");
    const external = node("folder/a.txt", { ref: { rootId: "other", path: "folder/a.txt" }, key: "other:folder/a.txt" });
    expect(topLevelTreeNodes([node("folder/a.txt"), node("folder/nested", { kind: "directory" }), folder, sibling, external]))
      .toEqual([folder, sibling, external]);
  });

  it("rejects deletion for an empty or partially protected selection", () => {
    expect(canDeleteTreeNodes([])).toBe(false);
    expect(canDeleteTreeNodes([node("a"), node("folder", { kind: "directory" })])).toBe(true);
    for (const protectedNode of [node("", { isRoot: true }), node(".echo", { readOnly: true })]) {
      expect(canDeleteTreeNodes([node("a"), protectedNode])).toBe(false);
    }
  });

  it("validates every drag source and the destination", () => {
    const destination = node("target", { kind: "directory" });
    const sources = [node("a"), node("folder", { kind: "directory" }), node("folder/child")];
    expect(treeMoveDestination(sources, destination)).toBe(destination);
    for (const invalid of [node("", { isRoot: true }), node("a", { readOnly: true }), node("a", { blockedReason: "blocked" }),
      node("target/already-here"), node("other", { ref: { rootId: "other", path: "other" } })]) {
      expect(treeMoveDestination([...sources, invalid], destination)).toBeNull();
    }
    for (const target of [undefined, node("file"), node("folder", { kind: "directory" }),
      node("folder/nested", { kind: "directory" }), { ...destination, readOnly: true }, { ...destination, blockedReason: "blocked" }]) {
      expect(treeMoveDestination(sources, target)).toBeNull();
    }
    expect(treeMoveDestination([], destination)).toBeNull();
  });

  it("runs sequentially and reports successes and failures separately", async () => {
    let inFlight = 0;
    const calls: string[] = [];
    const result = await runTreeOperation(["a", "b", "c"], async (item) => {
      expect(inFlight++).toBe(0);
      calls.push(item);
      await Promise.resolve();
      inFlight--;
      if (item === "b") throw new Error("collision");
      return item.toUpperCase();
    });
    expect(calls).toEqual(["a", "b", "c"]);
    expect(result.succeeded).toEqual([{ item: "a", value: "A" }, { item: "c", value: "C" }]);
    expect(result.failed).toEqual([{ item: "b", error: "collision" }]);
  });
});
