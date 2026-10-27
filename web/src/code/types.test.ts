import { describe, expect, it } from "vitest";
import { isRefWithin, joinRef, refKey } from "./types";

describe("FileRef helpers", () => {
  it("joins normalized workspace-relative paths", () => {
    expect(joinRef({ rootId: "root", path: "src" }, "main.go")).toEqual({ rootId: "root", path: "src/main.go" });
    expect(joinRef({ rootId: "root", path: "" }, "main.go")).toEqual({ rootId: "root", path: "main.go" });
  });

  it("does not confuse sibling prefixes with descendants", () => {
    expect(isRefWithin({ rootId: "root", path: "src/main.go" }, { rootId: "root", path: "src" })).toBe(true);
    expect(isRefWithin({ rootId: "root", path: "src-old/main.go" }, { rootId: "root", path: "src" })).toBe(false);
    expect(isRefWithin({ rootId: "other", path: "src/main.go" }, { rootId: "root", path: "src" })).toBe(false);
  });

  it("treats every path in a root as a descendant of that root", () => {
    expect(isRefWithin({ rootId: "root", path: "src/main.go" }, { rootId: "root", path: "" })).toBe(true);
    expect(isRefWithin({ rootId: "other", path: "src/main.go" }, { rootId: "root", path: "" })).toBe(false);
  });

  it("keys the same path independently per root", () => {
    expect(refKey({ rootId: "one", path: "main.go" })).not.toBe(refKey({ rootId: "two", path: "main.go" }));
  });
});
