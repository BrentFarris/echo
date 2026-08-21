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
      id: "match-one", line: 4, column: 9, endLine: 4, endColumn: 23,
      preview: "    var cameraPosition = 1", previewMatchStart: 8, previewMatchEnd: 22,
      match: "cameraPosition", replacementPreview: "    var cameraLocation = 1",
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
        content: "    var cameraPosition = 1", hasBom: false,
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
      overlays: [expect.objectContaining({ content: "    var cameraPosition = 1" })],
    }), expect.any(AbortSignal));
    expect(document.querySelector("[data-search-summary]")?.textContent).toContain("1 result in 1 file");
    expect(document.querySelector(".code-search-preview mark")?.textContent).toBe("cameraPosition");
    const preview = [...document.querySelectorAll(".code-search-preview > span, .code-search-preview > mark")]
      .map((part) => part.textContent).join("");
    expect(preview).toBe("var cameraPosition = 1");

    document.querySelector<HTMLButtonElement>("[data-search-result]")!.click();
    expect(openResult).toHaveBeenCalledWith(
      { rootId: "root", path: "src/main.go" },
      expect.objectContaining({ line: 4, column: 9 }),
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

  it("cancels an in-flight search as soon as the input changes", async () => {
    let resolveFirst!: (value: TextSearchResponse) => void;
    const firstSearch = new Promise<TextSearchResponse>((resolve) => { resolveFirst = resolve; });
    vi.mocked(editorAPI.searchText).mockReturnValueOnce(firstSearch).mockResolvedValue(response);
    create();
    const input = document.querySelector<HTMLInputElement>("[data-search-query]")!;

    input.value = "camera";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(200);
    const firstSignal = vi.mocked(editorAPI.searchText).mock.calls[0][2]!;

    input.value = "cameraPosition";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    expect(firstSignal.aborted).toBe(true);
    resolveFirst(response);
    await Promise.resolve();
    expect(document.querySelector("[data-search-result]")).toBeNull();

    await vi.advanceTimersByTimeAsync(200);
    expect(editorAPI.searchText).toHaveBeenCalledTimes(2);
    expect(editorAPI.searchText).toHaveBeenLastCalledWith(
      "workspace",
      expect.objectContaining({ query: "cameraPosition" }),
      expect.any(AbortSignal),
    );
    expect(document.querySelector("[data-search-result]")).not.toBeNull();
  });

  it("renders large result sets in bounded batches", async () => {
    const matches = Array.from({ length: 450 }, (_, index) => ({
      ...response.files[0].matches[0],
      id: `match-${index}`,
      line: index + 1,
    }));
    vi.mocked(editorAPI.searchText).mockResolvedValue({
      ...response,
      files: [{ ...response.files[0], matches }],
      matchCount: matches.length,
    });
    create();
    const input = document.querySelector<HTMLInputElement>("[data-search-query]")!;
    input.value = "cameraPosition";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(200);

    expect(document.querySelectorAll("[data-search-result]")).toHaveLength(200);
    expect(document.querySelector("[data-search-summary]")?.textContent).toContain("showing first 200");
    document.querySelector<HTMLButtonElement>("[data-search-action=show-more]")!.click();
    expect(document.querySelectorAll("[data-search-result]")).toHaveLength(400);
    expect(document.querySelector("[data-search-action=show-more]")?.textContent).toContain("Show 50 more results");
    document.querySelector<HTMLButtonElement>("[data-search-action=show-more]")!.click();
    expect(document.querySelectorAll("[data-search-result]")).toHaveLength(450);
    expect(document.querySelector("[data-search-action=show-more]")).toBeNull();
  });
});
