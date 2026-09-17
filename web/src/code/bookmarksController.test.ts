import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BookmarkState } from "./bookmarkTypes";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  models: [] as unknown[],
  events: new Map<string, (value: object) => void>(),
  created: null as null | ((model: unknown) => void),
}));
vi.mock("../../js/api.js", () => ({ api: mocks.api }));
vi.mock("../../js/ws.js", () => ({
  on: (event: string, listener: (value: object) => void) => { mocks.events.set(event, listener); return () => mocks.events.delete(event); },
  onState: () => () => {},
}));
vi.mock("./language", () => ({ monaco: {
  Range: class { constructor(public startLineNumber: number, public startColumn: number, public endLineNumber: number, public endColumn: number) {} },
  editor: {
    getModels: () => mocks.models,
    onDidCreateModel: (listener: (model: unknown) => void) => { mocks.created = listener; return { dispose() { mocks.created = null; } }; },
    GlyphMarginLane: { Left: 1 }, TrackedRangeStickiness: { NeverGrowsWhenTypingAtEdges: 1 },
  },
} }));
import { BookmarksController } from "./bookmarksController";

class Model {
  listeners: Array<() => void> = [];
  disposeListeners: Array<() => void> = [];
  decorations = new Map<string, { range: { startLineNumber: number }; options: unknown }>();
  next = 0;
  isDisposed() { return false; }
  onDidChangeContent(listener: () => void) { this.listeners.push(listener); return { dispose() {} }; }
  onWillDispose(listener: () => void) { this.disposeListeners.push(listener); return { dispose() {} }; }
  getLineCount() { return 30; }
  getLineContent(line: number) { return "code at " + line; }
  getDecorationRange(id: string) { return this.decorations.get(id)?.range; }
  getDecorationOptions(id: string) { return this.decorations.get(id)?.options; }
  deltaDecorations(old: string[], added: Array<{ range: { startLineNumber: number }; options: unknown }>) {
    old.forEach(id => this.decorations.delete(id));
    return added.map(value => { const id = String(++this.next); this.decorations.set(id, value); return id; });
  }
  move(line: number) {
    for (const value of this.decorations.values()) value.range.startLineNumber = line;
    this.listeners.forEach(listener => listener());
  }
}
const initial = (): BookmarkState => ({
  version: 1, revision: 1, bookmarks: [{ id: "bookmark-1", ref: { rootId: "root-1", path: "main.go" }, line: 4, preview: "code at 4", label: "Entry" }],
});

describe("bookmark synchronization", () => {
  let controller: BookmarksController;
  let model: Model;
  let state: BookmarkState;
  let changed = vi.fn();
  let reportError = vi.fn();

  beforeEach(async () => {
    state = initial();
    model = new Model();
    mocks.models = [model];
    mocks.api.mockReset();
    mocks.api.mockImplementation(async (_path: string, options?: { body?: { action: string; positions?: Array<{ id: string; line: number; preview: string }> } }) => {
      const action = options?.body;
      if (action?.action === "positions") {
        state = { ...state, revision: state.revision + 1, bookmarks: state.bookmarks.map(mark => ({ ...mark, ...action.positions?.find(position => position.id === mark.id) })) };
      }
      return structuredClone(state);
    });
    changed = vi.fn();
    reportError = vi.fn();
    controller = new BookmarksController({ workspaceId: "workspace-1", refForModel: () => ({ rootId: "root-1", path: "main.go" }), changed, reportError });
    await vi.waitFor(() => expect(controller.bookmarks).toHaveLength(1));
  });
  afterEach(async () => { controller.dispose(); await Promise.resolve(); });

  it("tracks model locations, preserves custom names, and ignores older server snapshots", async () => {
    model.move(8);
    expect(controller.bookmarks[0]).toMatchObject({ line: 8, label: "Entry" });
    await controller.flush();
    expect(state.bookmarks[0]).toMatchObject({ line: 8, label: "Entry" });
    mocks.events.get("bookmarks_changed")!({ workspaceId: "workspace-1", state: initial() });
    expect(controller.bookmarks[0].line).toBe(8);
    mocks.events.get("bookmarks_changed")!({ workspaceId: "other", state: { version: 1, revision: 99, bookmarks: [] } });
    expect(controller.bookmarks).toHaveLength(1);
  });

  it("retains failed position updates for retry without clearing bookmarks", async () => {
    model.move(9);
    mocks.api.mockRejectedValueOnce(new Error("offline"));
    expect(await controller.flush()).toBe(false);
    expect(controller.bookmarks[0].line).toBe(9);
    expect(reportError).toHaveBeenCalledWith(expect.stringContaining("offline"));
    await controller.retry();
    expect(state.bookmarks[0].line).toBe(9);
    expect(changed.mock.lastCall?.[2]).toBe("");
  });

  it("keeps edits made while a position write is in flight and flushes them afterward", async () => {
    let finish!: (value: BookmarkState) => void;
    mocks.api.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    model.move(7);
    const first = controller.flush();
    await vi.waitFor(() => expect(finish).toBeDefined());
    model.move(4);
    finish({ ...state, revision: 2, bookmarks: [{ ...state.bookmarks[0], line: 7, preview: "code at 7" }] });
    await first;
    expect(controller.bookmarks[0].line).toBe(4);
    await controller.flush();
    expect(state.bookmarks[0].line).toBe(4);
  });

  it("removes deleted markers without resurrecting them and disposes subscriptions", async () => {
    model.move(10);
    mocks.events.get("bookmarks_changed")!({ workspaceId: "workspace-1", state: { version: 1, revision: 3, bookmarks: [] } });
    expect(controller.bookmarks).toEqual([]);
    expect(model.decorations.size).toBe(0);
    controller.dispose();
    expect(mocks.events.has("bookmarks_changed")).toBe(false);
    expect(mocks.created).toBeNull();
  });

  it("does not retry a failed rename after a newer name has been saved", async () => {
    mocks.api.mockRejectedValueOnce(new Error("offline"));
    await controller.rename("bookmark-1", "Old");
    await controller.rename("bookmark-1", "Latest");
    await controller.retry();
    const names = mocks.api.mock.calls.flatMap(([, options]) => options?.body?.action === "rename" ? [options.body.label] : []);
    expect(names).toEqual(["Old", "Latest"]);
  });

  it("persists failed edits before retrying a file move", async () => {
    model.move(8);
    mocks.api.mockRejectedValueOnce(new Error("offline"));
    await controller.remap({ rootId: "root-1", path: "main.go" }, { rootId: "root-1", path: "renamed.go" });
    expect(mocks.api.mock.calls.some(([, options]) => options?.body?.action === "remap")).toBe(false);
    await controller.retry();
    const operations = mocks.api.mock.calls.flatMap(([, options]) => options?.body ? [options.body.action] : []);
    expect(operations).toEqual(["positions", "positions", "remap"]);
  });
});
