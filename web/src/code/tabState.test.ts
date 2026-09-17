import { describe, expect, it } from "vitest";
import { restoreTabOpenState, setTabPinned, TabState } from "./tabState";

const tab = (id: string, path: string) => ({ id, ref: { rootId: "root", path }, title: path, keepOpen: false, pinned: false, dirty: false });

describe("TabState", () => {
  it("replaces only the clean preview", () => {
    const state = new TabState();
    state.openPreview(tab("one", "one.go"));
    expect(state.openPreview(tab("two", "two.go"))).toEqual({ replacedId: "one" });
    expect(state.tabs.map((item) => item.id)).toEqual(["two"]);
  });

  it("keeps an edited preview open without pinning it", () => {
    const state = new TabState();
    state.openPreview(tab("one", "one.go"));
    state.markDirty("one");
    state.openPreview(tab("two", "two.go"));
    expect(state.tabs.map((item) => [item.id, item.keepOpen, item.pinned])).toEqual([["one", true, false], ["two", false, false]]);
  });

  it("activates and keeps open an already-open path without pinning it", () => {
    const state = new TabState();
    state.openPreview(tab("one", "one.go"));
    state.openKeptOpen(tab("duplicate", "one.go"));
    expect(state.tabs).toHaveLength(1);
    expect(state.tabs[0].keepOpen).toBe(true);
    expect(state.tabs[0].pinned).toBe(false);
    expect(state.activeId).toBe("one");
  });

  it("keeps a pinned preview open and protects it until unpinned", () => {
    const state = new TabState();
    state.openPreview(tab("one", "one.go"));
    setTabPinned(state.tabs[0], true);
    state.openPreview(tab("two", "two.go"));
    state.close("one");
    expect(state.tabs.map((item) => item.id)).toEqual(["one", "two"]);
    setTabPinned(state.tabs[0], false);
    expect(state.tabs[0].keepOpen).toBe(true);
    state.close("one");
    expect(state.tabs.map((item) => item.id)).toEqual(["two"]);
  });

  it("restores legacy Keep Open state without adding close protection", () => {
    expect(restoreTabOpenState({ pinned: true })).toEqual({ keepOpen: true, pinned: false });
    expect(restoreTabOpenState({ pinned: false })).toEqual({ keepOpen: false, pinned: false });
  });

  it("restores pin protection and prevents pinned previews", () => {
    expect(restoreTabOpenState({ pinned: true, closeProtected: true })).toEqual({ keepOpen: true, pinned: true });
    expect(restoreTabOpenState({ pinned: false, closeProtected: true })).toEqual({ keepOpen: true, pinned: true });
    expect(restoreTabOpenState({ pinned: true, closeProtected: false })).toEqual({ keepOpen: true, pinned: false });
  });
});
