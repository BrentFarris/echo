import { afterEach, describe, expect, it } from "vitest";
import { renderExplorerRows, type ExplorerRow } from "./explorerRows";

function row(key: string, overrides: Partial<ExplorerRow> = {}): ExplorerRow {
  return {
    key, name: `${key}.go`, attributes: { "data-tree-key": key, role: "treeitem", "aria-selected": "false" },
    chevronClass: "code-tree-spacer", iconClass: "codicon codicon-file-code", labelClass: "code-tree-label", renaming: false,
    ...overrides,
  };
}

afterEach(() => document.body.replaceChildren());

describe("explorer DOM reconciliation", () => {
  it("performs no DOM mutations for an unchanged virtual window", () => {
    const canvas = document.createElement("div");
    renderExplorerRows(canvas, [row("a"), row("b")]);
    const children = [...canvas.children];
    const observer = new MutationObserver(() => {});
    observer.observe(canvas, { attributes: true, childList: true, characterData: true, subtree: true });
    renderExplorerRows(canvas, [row("a"), row("b")]);
    expect([...canvas.children]).toEqual(children);
    expect(observer.takeRecords()).toEqual([]);
    observer.disconnect();
  });

  it("reuses rows across a shifted virtual window and patches decorations in place", () => {
    const canvas = document.createElement("div");
    renderExplorerRows(canvas, [row("a"), row("b"), row("c")]);
    const b = canvas.children[1];
    const label = b.children[2];
    const c = canvas.children[2];
    renderExplorerRows(canvas, [row("b", { labelClass: "code-tree-label has-diagnostic-error" }), row("c"), row("d")]);
    expect(canvas.children[0]).toBe(b);
    expect(canvas.children[1]).toBe(c);
    expect(b.children[2]).toBe(label);
    expect(label.className).toContain("has-diagnostic-error");
    expect([...canvas.children].map((child) => (child as HTMLElement).dataset.treeKey)).toEqual(["b", "c", "d"]);
  });

  it("preserves a rename draft, focus, and selection during refresh and diagnostic updates", () => {
    const canvas = document.createElement("div");
    document.body.append(canvas);
    const input = renderExplorerRows(canvas, [row("a", { renaming: true }), row("b")])!;
    input.focus();
    input.value = "unfinished.go";
    input.setSelectionRange(2, 6);
    const result = renderExplorerRows(canvas, [row("new"), row("a", { renaming: true }), row("b", { labelClass: "has-diagnostic-error" })]);
    expect(result).toBeUndefined();
    expect(canvas.querySelector("input")).toBe(input);
    expect(document.activeElement).toBe(input);
    expect(input.value).toBe("unfinished.go");
    expect([input.selectionStart, input.selectionEnd]).toEqual([2, 6]);
  });

  it("renders filenames as text and removes obsolete attributes", () => {
    const canvas = document.createElement("div");
    renderExplorerRows(canvas, [row("a", { attributes: { "data-tree-key": "a", "aria-expanded": "false" } })]);
    renderExplorerRows(canvas, [row("a", { name: "<img src=x onerror=alert(1)>" })]);
    expect(canvas.querySelector("img")).toBeNull();
    expect(canvas.textContent).toBe("<img src=x onerror=alert(1)>");
    expect(canvas.firstElementChild?.hasAttribute("aria-expanded")).toBe(false);
  });
});
