import { describe, expect, it } from "vitest";
import { beginMruCycle, nextMruCycle, pruneMruCycle, removeFromMru } from "./mruTabOrder";

describe("beginMruCycle", () => {
  it("puts the initiating tab first, followed by most-recent candidates", () => {
    // Most recent: c, then b, then a. Active is c.
    const state = beginMruCycle(["c", "b", "a"], "c", ["a", "b", "c"]);
    expect(state.order).toEqual(["c", "b", "a"]);
    expect(state.index).toBe(-1);
    expect(state.sourceId).toBe("c");
  });

  it("excludes ids that are no longer open", () => {
    const state = beginMruCycle(["c", "d", "b", "a"], "a", ["a", "b", "c"]);
    expect(state.order).toEqual(["a", "c", "b"]);
  });

  it("falls back to every other open tab when restored tabs have no history", () => {
    const state = beginMruCycle([], "b", ["a", "b", "c", "d"]);
    expect(state.order).toEqual(["b", "a", "c", "d"]);
  });

  it("appends tabs missing from partial history in tab-strip order", () => {
    const state = beginMruCycle(["d", "b"], "d", ["a", "b", "c", "d", "e"]);
    expect(state.order).toEqual(["d", "b", "a", "c", "e"]);
  });

  it("deduplicates history before appending fallback tabs", () => {
    const state = beginMruCycle(["c", "a", "a", "missing"], "c", ["a", "b", "c"]);
    expect(state.order).toEqual(["c", "a", "b"]);
  });

  it("retains the source when it is the only open tab", () => {
    const state = beginMruCycle(["a"], "a", ["a"]);
    expect(state.order).toEqual(["a"]);
  });
});

describe("nextMruCycle", () => {
  it("first press lands on the most recent other tab", () => {
    let state = beginMruCycle(["c", "b", "a"], "c", ["a", "b", "c"]);
    const { nextId, state: next } = nextMruCycle(state, false);
    expect(nextId).toBe("b");
    state = next;
  });

  it("held taps walk backward through the MRU snapshot", () => {
    let state = beginMruCycle(["c", "b", "a"], "c", ["a", "b", "c"]);
    let seen: string[] = [];
    for (let i = 0; i < 3; i++) {
      const { nextId, state: next } = nextMruCycle(state, false);
      seen.push(nextId!);
      state = next;
    }
    // c's previous is b, then a, then wraps back to the initiating tab.
    expect(seen).toEqual(["b", "a", "c"]);
  });

  it("reverse (Shift) reverses stepping from the current walk position", () => {
    // active=c, MRU c > b > a; order = [c, b, a].
    let state = beginMruCycle(["c", "b", "a"], "c", ["a", "b", "c"]);
    // Forward twice: b (idx 1) then a (idx 2).
    state = nextMruCycle(state, false).state;
    expect(nextMruCycle(state, false).nextId).toBe("a");
    const atA = nextMruCycle(state, false).state;
    // A reverse press from a step returns to b.
    expect(nextMruCycle(atA, true).nextId).toBe("b");
  });

  it("first reverse press is indistinguishable from forward (lands on most recent)", () => {
    let state = beginMruCycle(["c", "b", "a"], "c", ["a", "b", "c"]);
    const forward = nextMruCycle(state, false);
    const reverse = nextMruCycle(state, true);
    expect(forward.nextId).toBe("b");
    expect(reverse.nextId).toBe("b");
  });

  it("returns null when there are no candidates and never mutates", () => {
    const state = beginMruCycle([], null, []);
    const { nextId, state: next } = nextMruCycle(state, false);
    expect(nextId).toBeNull();
    expect(next).toEqual(state);
  });
});

describe("pruneMruCycle", () => {
  it("drops closed tabs from the walk order", () => {
    const state = beginMruCycle(["c", "b", "a"], "c", ["a", "b", "c"]);
    const pruned = pruneMruCycle(state, ["a", "c"]); // b closed
    expect(pruned.order).toEqual(["c", "a"]);
  });

  it("resets the index when the cursor is past the pruned order", () => {
    let state = beginMruCycle(["c", "b", "a"], "c", ["a", "b", "c"]);
    state = nextMruCycle(state, false).state; // index 1 -> b
    const pruned = pruneMruCycle(state, ["c", "a"]); // b closed
    expect(pruned.index).toBe(-1);
  });
});

describe("removeFromMru", () => {
  it("removes a closed id from the recency list", () => {
    expect(removeFromMru(["c", "b", "a"], "b")).toEqual(["c", "a"]);
    expect(removeFromMru(["a"], "a")).toEqual([]);
    expect(removeFromMru(["c", "b"], "x")).toEqual(["c", "b"]);
  });
});
