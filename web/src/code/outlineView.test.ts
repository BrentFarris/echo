import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OutlineView } from "./outlineView";
import type { OutlineSymbol, OutlineTarget } from "./outlineTypes";

let view: OutlineView | undefined;
beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
});
afterEach(() => { view?.dispose(); view = undefined; document.body.innerHTML = ""; vi.useRealTimers(); vi.unstubAllGlobals(); });
const symbol = (name: string, line: number, children: OutlineSymbol[] = []): OutlineSymbol => ({ name, detail: "()", kind: 11, children,
  range: { startLineNumber: line, startColumn: 1, endLineNumber: line, endColumn: 10 },
  selectionRange: { startLineNumber: line, startColumn: 5, endLineNumber: line, endColumn: 9 },
});

function mount(symbols = [symbol("First", 2, [symbol("child", 3)])]) {
  document.body.innerHTML = '<aside><section></section></aside>';
  const host = document.querySelector("section")!;
  Object.defineProperty(host.parentElement, "clientHeight", { value: 700 });
  let selection: OutlineTarget[] = [{ ref: { rootId: "r", path: "a.ts" }, kind: "file" }];
  const resolve = vi.fn(async (_ref: { rootId: string; path: string }, _signal: AbortSignal) => ({ status: "ready" as const, symbols }));
  const navigate = vi.fn(async () => true);
  const saveSize = vi.fn();
  view = new OutlineView(host, { selection: () => selection, list: async () => [], resolve, label: (ref) => ref.path, navigate, saveSize });
  const tree = host.querySelector<HTMLElement>('[role="tree"]')!;
  Object.defineProperty(tree, "clientHeight", { value: 140 });
  const toggle = host.querySelector<HTMLButtonElement>("button")!;
  return { host, tree, toggle, resolve, navigate, saveSize, select: (value: OutlineTarget[]) => { selection = value; } };
}
const frame = () => vi.advanceTimersByTimeAsync(32);

describe("Outline panel", () => {
  it("starts collapsed, loads only on reopening, and keeps the current snapshot", async () => {
    const { toggle, resolve, select, host } = mount();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(resolve).not.toHaveBeenCalled();
    toggle.click(); await frame();
    expect(host.textContent).toContain("First");
    expect(resolve).toHaveBeenCalledTimes(1);
    select([{ ref: { rootId: "r", path: "b.ts" }, kind: "file" }]);
    await frame();
    expect(resolve).toHaveBeenCalledTimes(1);
    toggle.click(); toggle.click(); await frame();
    expect(resolve).toHaveBeenCalledTimes(2);
    expect(resolve.mock.calls.at(-1)?.[0]).toEqual({ rootId: "r", path: "b.ts" });
  });

  it("supports nested keyboard expansion and declaration navigation", async () => {
    const { toggle, tree, navigate } = mount();
    toggle.click(); await frame();
    expect(tree.querySelectorAll('[role="treeitem"]')).toHaveLength(1);
    tree.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    expect(tree.querySelectorAll('[role="treeitem"]')).toHaveLength(2);
    tree.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    tree.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(navigate).toHaveBeenCalledWith({ rootId: "r", path: "a.ts" }, expect.objectContaining({ startLineNumber: 3, startColumn: 5 }));
  });

  it("virtualizes large outlines and keeps End-key navigation usable", async () => {
    const { toggle, tree } = mount(Array.from({ length: 3000 }, (_, index) => symbol(`symbol${index}`, index + 1)));
    toggle.click(); await frame();
    expect(tree.querySelectorAll('[role="treeitem"]').length).toBeLessThan(25);
    tree.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true }));
    expect(tree.textContent).toContain("symbol2999");
    expect(tree.querySelectorAll('[role="treeitem"]').length).toBeLessThan(25);
  });

  it("sorts file groups and restores only size, never expanded state", async () => {
    const { toggle, select, tree, host, saveSize } = mount();
    select([{ ref: { rootId: "r", path: "z.ts" }, kind: "file" }, { ref: { rootId: "r", path: "a.ts" }, kind: "file" }]);
    view!.restoreSize(0.5);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    toggle.click(); await frame();
    expect([...tree.querySelectorAll('[aria-level="1"]')].map((row) => row.textContent?.trim())).toEqual(["a.ts", "z.ts"]);
    const separator = host.querySelector('[role="separator"]')!;
    separator.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true }));
    expect(view!.sizeRatio).toBeCloseTo(0.55);
    expect(saveSize).toHaveBeenCalledOnce();
  });
});
