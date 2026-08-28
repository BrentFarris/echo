import { describe, expect, it, vi } from "vitest";
import {
  commandHistoryStorageKey,
  commandPalettePresentation,
  loadRecentCommandIds,
  parseRecentCommandIds,
  pruneRecentCommandIds,
  recordRecentCommandId,
  saveRecentCommandIds,
} from "./commandHistory";

const commands = [
  { id: "file.save", label: "File: Save" },
  { id: "explorer.refresh", label: "Explorer: Refresh" },
  { id: "explorer.collapse", label: "Explorer: Collapse All" },
  { id: "editor.undo", label: "Editor: Undo" },
];

describe("command history persistence", () => {
  it("parses only the first five unique command ids", () => {
    expect(parseRecentCommandIds(JSON.stringify(["a", "b", "a", 1, "", "c", "d", "e", "f"])))
      .toEqual(["a", "b", "c", "d", "e"]);
  });

  it("treats malformed or incorrectly shaped data as empty", () => {
    expect(parseRecentCommandIds("not-json")).toEqual([]);
    expect(parseRecentCommandIds(JSON.stringify({ id: "file.save" }))).toEqual([]);
  });

  it("promotes repeated commands and trims the history", () => {
    expect(recordRecentCommandId(["e", "d", "c", "b", "a"], "c"))
      .toEqual(["c", "e", "d", "b", "a"]);
    expect(recordRecentCommandId(["e", "d", "c", "b", "a"], "f"))
      .toEqual(["f", "e", "d", "c", "b"]);
  });

  it("drops commands that no longer exist", () => {
    expect(pruneRecentCommandIds(["removed", "editor.undo", "file.save"], ["file.save", "editor.undo"]))
      .toEqual(["editor.undo", "file.save"]);
  });

  it("loads, saves, and tolerates unavailable storage", () => {
    const values = new Map<string, string>();
    const storage = {
      getItem: vi.fn((key: string) => values.get(key) || null),
      setItem: vi.fn((key: string, value: string) => { values.set(key, value); }),
    };
    saveRecentCommandIds(["file.save", "editor.undo"], storage);
    expect(storage.setItem).toHaveBeenCalledWith(commandHistoryStorageKey, JSON.stringify(["file.save", "editor.undo"]));
    expect(loadRecentCommandIds(storage)).toEqual(["file.save", "editor.undo"]);

    const unavailable = {
      getItem: () => { throw new Error("blocked"); },
      setItem: () => { throw new Error("blocked"); },
    };
    expect(loadRecentCommandIds(unavailable)).toEqual([]);
    expect(() => saveRecentCommandIds(["file.save"], unavailable)).not.toThrow();
  });
});

describe("command palette presentation", () => {
  it("groups recent commands first without duplicating them", () => {
    const presentation = commandPalettePresentation(commands, ["editor.undo", "file.save"], "");
    expect(presentation.commands.map((command) => command.id)).toEqual([
      "editor.undo", "file.save", "explorer.refresh", "explorer.collapse",
    ]);
    expect(presentation.groups.map((group) => ({
      label: group.label,
      ids: group.commands.map((command) => command.id),
    }))).toEqual([
      { label: "Recent", ids: ["editor.undo", "file.save"] },
      { label: "Commands", ids: ["explorer.refresh", "explorer.collapse"] },
    ]);
  });

  it("omits headings when there is no history", () => {
    const presentation = commandPalettePresentation(commands, [], "");
    expect(presentation.groups).toEqual([{ label: null, commands }]);
  });

  it("filters by label and ranks matching recent commands first without headings", () => {
    const presentation = commandPalettePresentation(commands, ["explorer.collapse"], "explorer");
    expect(presentation.groups[0].label).toBeNull();
    expect(presentation.commands.map((command) => command.id))
      .toEqual(["explorer.collapse", "explorer.refresh"]);
  });
});
