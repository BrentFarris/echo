import { describe, expect, it } from "vitest";
import { actionKeys, updateSelection } from "./gitSelection";

describe("Git change selection", () => {
  const keys = ["a", "b", "c", "d"];

  it("supports plain, toggle, and anchored range selection", () => {
    let state = updateSelection({ selected: new Set(), anchor: null }, keys, "b", { toggle: false, range: false });
    expect([...state.selected]).toEqual(["b"]);
    state = updateSelection(state, keys, "d", { toggle: false, range: true });
    expect([...state.selected]).toEqual(["b", "c", "d"]);
    state = updateSelection(state, keys, "c", { toggle: true, range: false });
    expect([...state.selected]).toEqual(["b", "d"]);
  });

  it("applies row actions to selection only when the clicked row is selected", () => {
    const state = { selected: new Set(["a", "b"]), anchor: "a" };
    expect(actionKeys(state, "a").sort()).toEqual(["a", "b"]);
    expect(actionKeys(state, "c")).toEqual(["c"]);
  });
});
