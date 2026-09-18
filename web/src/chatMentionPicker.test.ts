import { afterEach, describe, expect, it, vi } from "vitest";
import { installMentionPicker } from "./chatMentionPicker";
import { composerText } from "./chatMentions";

vi.mock("./code/editorApi", () => ({
  getRoots: vi.fn(async () => [{ id: "root", label: "Root", referenceLabel: "root" }]),
  searchEntries: vi.fn(async () => ({ items: [
    { ref: { rootId: "root", path: "one.ts" }, kind: "file", name: "one.ts" },
    { ref: { rootId: "root", path: "two.ts" }, kind: "file", name: "two.ts" },
  ] })),
}));

describe("shared mention picker", () => {
  const aborts: AbortController[] = [];
  afterEach(() => { aborts.splice(0).forEach(abort => abort.abort()); document.body.replaceChildren(); vi.useRealTimers(); });
  function mount() {
    const wrap = document.createElement("div"), input = document.createElement("div"), abort = new AbortController();
    aborts.push(abort);
    input.setAttribute("contenteditable", "true");
    wrap.append(input); document.body.append(wrap);
    const picker = installMentionPicker(input, wrap, { workspaceId: "workspace", signal: abort.signal });
    input.addEventListener("keydown", event => picker.handleKeydown(event));
    return { wrap, input, picker, abort };
  }
  it("uses independent IDs and supports arrows, Tab selection, and Escape", async () => {
    vi.useFakeTimers();
    const first = mount(), second = mount();
    for (const { input } of [first, second]) {
      input.textContent = "@";
      input.focus(); window.getSelection()?.setPosition(input.firstChild!, 1);
      input.dispatchEvent(new Event("input", { bubbles: true }));
      await vi.advanceTimersByTimeAsync(110);
    }
    const ids = [...document.querySelectorAll("[role=listbox]")].map(element => element.id);
    expect(ids).toHaveLength(2); expect(new Set(ids).size).toBe(2);
    second.input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    second.input.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
    expect(composerText(second.input)).toBe("@root/two.ts ");
    expect(second.wrap.querySelector("[role=listbox]")).toBeNull();
    first.input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(first.wrap.querySelector("[role=listbox]")).toBeNull();
  });
  it("does not reopen a disposed picker from a queued selection event", async () => {
    vi.useFakeTimers();
    const { input, picker, abort, wrap } = mount();
    input.textContent = "@"; abort.abort(); picker.sync();
    await vi.advanceTimersByTimeAsync(200);
    expect(wrap.querySelector("[role=listbox]")).toBeNull();
  });
});
