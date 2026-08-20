import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./editorApi", () => ({
  searchText: vi.fn(),
  replaceText: vi.fn(),
}));

import * as editorAPI from "./editorApi";
import { SearchView } from "./searchView";
import type { TextSearchResponse } from "./types";

const response: TextSearchResponse = {
  files: [{
    ref: { rootId: "root", path: "src/main.go" },
    name: "main.go",
    referencePath: "project/src/main.go",
    revision: "disk-revision",
    contentRevision: "content-revision",
    overlay: true,
    matches: [{
      id: "match-one", line: 4, column: 5, endLine: 4, endColumn: 19,
      preview: "var cameraPosition = 1", previewMatchStart: 4, previewMatchEnd: 18,
      match: "cameraPosition", replacementPreview: "cameraLocation",
    }],
  }],
  matchCount: 1,
  filesSearched: 1,
  filesSkipped: 0,
  indexing: false,
  indexed: 1,
  truncated: false,
};

describe("workspace Search view", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    document.body.innerHTML = '<aside id="search"></aside>';
    vi.mocked(editorAPI.searchText).mockResolvedValue(response);
    vi.mocked(editorAPI.replaceText).mockResolvedValue({ updated: [] });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  function create() {
    const controller = new AbortController();
    const openResult = vi.fn();
    const confirmReplace = vi.fn().mockResolvedValue(true);
    const applyUpdates = vi.fn();
    const view = new SearchView(document.querySelector("#search")!, {
      workspaceId: "workspace",
      signal: controller.signal,
      getOverlays: () => [{
        ref: { rootId: "root", path: "src/main.go" }, revision: "disk-revision",
        content: "var cameraPosition = 1", hasBom: false,
      }],
      openResult,
      confirmReplace,
      applyUpdates,
      focusEditor: vi.fn(),
    });
    return { view, controller, openResult, confirmReplace, applyUpdates };
  }

  it("searches with VS Code-style controls and opens exact results", async () => {
    const { openResult } = create();
    const input = document.querySelector<HTMLInputElement>("[data-search-query]")!;
    input.value = "cameraPosition";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(200);

    expect(editorAPI.searchText).toHaveBeenCalledWith("workspace", expect.objectContaining({
      query: "cameraPosition", caseSensitive: false, wholeWord: false, regex: false,
      overlays: [expect.objectContaining({ content: "var cameraPosition = 1" })],
    }), expect.any(AbortSignal));
    expect(document.querySelector("[data-search-summary]")?.textContent).toContain("1 result in 1 file");
    expect(document.querySelector(".code-search-preview mark")?.textContent).toBe("cameraPosition");

    document.querySelector<HTMLButtonElement>("[data-search-result]")!.click();
    expect(openResult).toHaveBeenCalledWith(
      { rootId: "root", path: "src/main.go" },
      expect.objectContaining({ line: 4, column: 5 }),
      false,
    );

    document.querySelector<HTMLButtonElement>("[data-search-toggle=case]")!.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(editorAPI.searchText).toHaveBeenLastCalledWith("workspace", expect.objectContaining({ caseSensitive: true }), expect.any(AbortSignal));
  });

  it("previews replacement and confirms saving dirty overlays", async () => {
    const { confirmReplace, applyUpdates } = create();
    const input = document.querySelector<HTMLInputElement>("[data-search-query]")!;
    input.value = "cameraPosition";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(200);

    document.querySelector<HTMLButtonElement>("[data-search-action=toggle-replace]")!.click();
    await vi.advanceTimersByTimeAsync(0);
    const replacement = document.querySelector<HTMLInputElement>("[data-search-replacement]")!;
    replacement.value = "cameraLocation";
    replacement.dispatchEvent(new Event("input", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(200);

    document.querySelector<HTMLButtonElement>("[data-search-match-replace]")!.click();
    await vi.runAllTimersAsync();
    expect(confirmReplace).toHaveBeenCalledWith({ scope: "match", matches: 1, files: 1, dirtyFiles: 1 });
    expect(editorAPI.replaceText).toHaveBeenCalledWith("workspace", expect.objectContaining({
      scope: "match",
      targets: [expect.objectContaining({ matchIds: ["match-one"] })],
      search: expect.objectContaining({ query: "cameraPosition", replacement: "cameraLocation" }),
    }));
    expect(applyUpdates).toHaveBeenCalledWith([]);
  });
});
