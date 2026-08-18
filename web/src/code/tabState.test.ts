import { describe, expect, it } from "vitest";
import { TabState } from "./tabState";

const tab = (id: string, path: string) => ({ id, ref: { rootId: "root", path }, title: path, pinned: false, dirty: false });

describe("TabState", () => {
  it("replaces only the clean preview", () => {
    const state = new TabState();
    state.openPreview(tab("one", "one.go"));
    expect(state.openPreview(tab("two", "two.go"))).toEqual({ replacedId: "one" });
    expect(state.tabs.map((item) => item.id)).toEqual(["two"]);
  });

  it("pins a preview on edit and keeps it when another preview opens", () => {
    const state = new TabState();
    state.openPreview(tab("one", "one.go"));
    state.markDirty("one");
    state.openPreview(tab("two", "two.go"));
    expect(state.tabs.map((item) => [item.id, item.pinned])).toEqual([["one", true], ["two", false]]);
  });

  it("activates and pins an already-open path", () => {
    const state = new TabState();
    state.openPreview(tab("one", "one.go"));
    state.openPinned(tab("duplicate", "one.go"));
    expect(state.tabs).toHaveLength(1);
    expect(state.tabs[0].pinned).toBe(true);
    expect(state.activeId).toBe("one");
  });
});
