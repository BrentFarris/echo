import { afterEach, describe, expect, it, vi } from "vitest";
import { bookmarkLabel, groupBookmarks, type Bookmark } from "./bookmarkTypes";
import { BookmarksView } from "./bookmarksView";

const mark = (id: string, path = "src/main.go", line = 4): Bookmark => ({
  id, ref: { rootId: "root-1", path }, line, preview: "  func main() {  ",
});

afterEach(() => { document.body.replaceChildren(); });

describe("bookmark presentation", () => {
  it("groups by complete file identity, orders files and lines, and falls back for blank lines", () => {
    const marks = [mark("a", "z.go", 9), mark("b", "a.go", 8), mark("c", "a.go", 2), { ...mark("d", "a.go"), ref: { rootId: "root-2", path: "a.go" } }];
    expect(groupBookmarks(marks).map(group => group.bookmarks.map(item => item.id))).toEqual([["c", "b"], ["d"], ["a"]]);
    expect(bookmarkLabel({ ...mark("x"), preview: "   " })).toBe("Line 4");
    expect(bookmarkLabel({ ...mark("x"), label: "Entry" })).toBe("Entry");
    expect(bookmarkLabel(mark("x"))).toBe("func main() {");
  });

  it("opens the exact bookmark while rename and delete actions never navigate", () => {
    const root = document.createElement("aside");
    document.body.append(root);
    const actions = { roots: () => [], toggle: vi.fn(), open: vi.fn(), rename: vi.fn(), remove: vi.fn(), retry: vi.fn() };
    const view = new BookmarksView(root, actions);
    const bookmark = { ...mark("one"), preview: "<script>bad()</script>" };
    view.update([bookmark], true, "");
    expect(root.querySelector("script")).toBeNull();
    root.querySelector<HTMLButtonElement>("[data-bookmark-open]")!.click();
    expect(actions.open).toHaveBeenCalledWith(bookmark);
    root.querySelector<HTMLButtonElement>("[data-bookmark-rename]")!.click();
    const input = root.querySelector<HTMLInputElement>("[data-bookmark-input]")!;
    input.value = "Entry point";
    view.update([{ ...bookmark, line: 7 }], true, "");
    expect(root.querySelector("[data-bookmark-input]")).toBe(input);
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(actions.rename).toHaveBeenCalledExactlyOnceWith("one", "Entry point");
    root.querySelector<HTMLButtonElement>("[data-bookmark-remove]")!.click();
    expect(actions.remove).toHaveBeenCalledExactlyOnceWith("one");
    expect(actions.open).toHaveBeenCalledTimes(1);
    view.dispose();
  });

  it("cancels rename with Escape, saves blank names on blur, and collapses file groups", () => {
    const root = document.createElement("aside");
    document.body.append(root);
    const rename = vi.fn();
    const view = new BookmarksView(root, { roots: () => [], toggle() {}, open() {}, rename, remove() {}, retry() {} });
    view.update([mark("one")], true, "");
    const begin = () => {
      root.querySelector<HTMLButtonElement>("[data-bookmark-rename]")!.click();
      return root.querySelector<HTMLInputElement>("[data-bookmark-input]")!;
    };
    const canceled = begin();
    canceled.value = "Changed";
    canceled.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(rename).not.toHaveBeenCalled();
    const blank = begin();
    blank.value = " ";
    blank.blur();
    expect(rename).toHaveBeenCalledExactlyOnceWith("one", "");
    root.querySelector<HTMLButtonElement>("[data-bookmark-group]")!.click();
    expect(root.querySelector<HTMLElement>(".code-bookmark-children")!.hidden).toBe(true);
    view.dispose();
  });
});
