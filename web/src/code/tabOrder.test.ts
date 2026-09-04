import { describe, expect, it } from "vitest";
import { reorderTabs } from "./tabOrder";

const tabs = ["one", "two", "three", "four"].map((id) => ({ id }));
const ids = (items: readonly { id: string }[] | null) => items?.map((item) => item.id);

describe("reorderTabs", () => {
  it("moves a tab before a target", () => {
    expect(ids(reorderTabs(tabs, "three", "one", "before"))).toEqual(["three", "one", "two", "four"]);
  });

  it("moves a tab after a target", () => {
    expect(ids(reorderTabs(tabs, "one", "three", "after"))).toEqual(["two", "three", "one", "four"]);
  });

  it("moves a tab into trailing strip space", () => {
    expect(ids(reorderTabs(tabs, "two", null, "after"))).toEqual(["one", "three", "four", "two"]);
  });

  it("ignores drops that already describe the current order", () => {
    expect(reorderTabs(tabs, "two", "one", "after")).toBeNull();
    expect(reorderTabs(tabs, "two", "three", "before")).toBeNull();
    expect(reorderTabs(tabs, "four", null, "after")).toBeNull();
    expect(reorderTabs(tabs, "two", "two", "before")).toBeNull();
  });

  it("ignores unknown source and target ids", () => {
    expect(reorderTabs(tabs, "missing", "one", "before")).toBeNull();
    expect(reorderTabs(tabs, "one", "missing", "before")).toBeNull();
  });
});
