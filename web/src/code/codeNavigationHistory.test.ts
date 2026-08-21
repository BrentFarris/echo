import { beforeEach, describe, expect, it } from "vitest";
import { CODE_NAVIGATION_HISTORY_STATE_KEY } from "../navigation";
import {
  CodeNavigationHistory,
  isLargeCodeNavigationJump,
  type CodeNavigationEntry,
  type CodeNavigationLocation,
} from "./codeNavigationHistory";

const location = (path: string, line = 1): CodeNavigationLocation => ({
  workspaceId: "workspace",
  ref: { rootId: "root", path },
  selections: [{
    selectionStartLineNumber: line,
    selectionStartColumn: 1,
    positionLineNumber: line,
    positionColumn: 1,
  }],
  scrollTop: line * 10,
  scrollLeft: 0,
});

class FakeHistory {
  state: unknown = { preserved: true };
  readonly entries: unknown[] = [];
  pushes = 0;
  replaces = 0;

  pushState(data: unknown): void {
    this.state = data;
    this.entries.push(data);
    this.pushes++;
  }

  replaceState(data: unknown): void {
    this.state = data;
    this.replaces++;
  }
}

describe("CodeNavigationHistory", () => {
  beforeEach(() => sessionStorage.clear());

  it("replaces the initial browser entry and pushes later destinations", () => {
    const history = new FakeHistory();
    let id = 0;
    const navigation = new CodeNavigationHistory("workspace", {
      history, storage: sessionStorage, currentURL: () => "#/code", createId: () => `id-${++id}`,
    });
    navigation.attachInitial(location("main.go", 3));
    expect(history.replaces).toBe(1);
    expect(history.pushes).toBe(0);
    expect(history.state).toMatchObject({ preserved: true });
    navigation.recordTransition(location("main.go", 3), location("definition.go", 7));
    expect(history.pushes).toBe(1);
    expect(history.state).toMatchObject({ preserved: true });
    const entry = (history.state as Record<string, CodeNavigationEntry>)[CODE_NAVIGATION_HISTORY_STATE_KEY];
    expect(entry.location.ref.path).toBe("definition.go");
    expect(entry.sequence).toBe(1);
  });

  it("does not push duplicate locations", () => {
    const history = new FakeHistory();
    const navigation = new CodeNavigationHistory("workspace", { history, storage: sessionStorage, createId: () => "id" });
    navigation.attachInitial(location("main.go", 3));
    expect(navigation.recordTransition(location("main.go", 3), location("main.go", 3))).toBe(false);
    expect(history.pushes).toBe(0);
  });

  it("recognizes only same-file cursor jumps of at least ten lines", () => {
    expect(isLargeCodeNavigationJump(location("main.go", 1), location("main.go", 10))).toBe(false);
    expect(isLargeCodeNavigationJump(location("main.go", 1), location("main.go", 11))).toBe(true);
    expect(isLargeCodeNavigationJump(location("main.go", 1), location("other.go", 20))).toBe(false);
  });

  it("captures a departing location and restores it on forward traversal", () => {
    const history = new FakeHistory();
    let id = 0;
    const navigation = new CodeNavigationHistory("workspace", { history, storage: sessionStorage, createId: () => `id-${++id}` });
    const first = location("main.go", 3);
    const second = location("definition.go", 7);
    navigation.attachInitial(first);
    const firstState = history.state;
    navigation.recordTransition(first, second);
    const secondState = history.state;
    const movedSecond = location("definition.go", 22);
    expect(navigation.beginTraversal(firstState, movedSecond)?.location).toEqual(first);
    expect(navigation.beginTraversal(secondState, first)?.location).toEqual(movedSecond);
  });

  it("remaps files and descendants in stored locations", () => {
    const history = new FakeHistory();
    let id = 0;
    const navigation = new CodeNavigationHistory("workspace", { history, storage: sessionStorage, createId: () => `id-${++id}` });
    const first = location("src/main.go", 3);
    const second = location("src/nested/demo.go", 7);
    navigation.attachInitial(first);
    const firstState = history.state;
    navigation.recordTransition(first, second);
    const secondState = history.state;
    navigation.remapRef({ rootId: "root", path: "src" }, { rootId: "root", path: "app" });
    expect(navigation.beginTraversal(firstState, null)?.location.ref.path).toBe("app/main.go");
    expect(navigation.beginTraversal(secondState, null)?.location.ref.path).toBe("app/nested/demo.go");
  });
});
