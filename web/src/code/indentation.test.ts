import { afterEach, describe, expect, it, vi } from "vitest";
import { editorSettingsWriter, indentationDefaults, indentationLabel, openIndentationPopover } from "./indentation";

describe("indentation preferences", () => {
  it("defaults legacy settings to tabs at four columns and accepts configured spaces", () => {
    expect(indentationDefaults({})).toEqual({ insertSpaces: false, tabSize: 4 });
    expect(indentationDefaults({ editorInsertSpaces: true, editorTabSize: 2 })).toEqual({ insertSpaces: true, tabSize: 2 });
    for (const size of [0, -1, 9, 2.5, "2", null, NaN]) {
      expect(indentationDefaults({ editorTabSize: size }).tabSize).toBe(4);
    }
    expect(indentationLabel({ insertSpaces: false, tabSize: 4 })).toBe("Tabs: 4");
    expect(indentationLabel({ insertSpaces: true, tabSize: 2 })).toBe("Spaces: 2");
  });

  it("serializes font and indentation saves while retaining unrelated settings", async () => {
    let stored: Record<string, unknown> = { theme: "dark", editorFontSize: 13.5 };
    let release!: () => void;
    const firstWrite = new Promise<void>((resolve) => { release = resolve; });
    const write = vi.fn(async (settings) => { await firstWrite; stored = settings; });
    const save = editorSettingsWriter(async () => ({ ...stored }), write);
    const font = save({ editorFontSize: 16 });
    const indentation = save({ editorInsertSpaces: true, editorTabSize: 2 });
    await vi.waitFor(() => expect(write).toHaveBeenCalledTimes(1));
    release();
    await Promise.all([font, indentation]);
    expect(stored).toEqual({ theme: "dark", editorFontSize: 16, editorInsertSpaces: true, editorTabSize: 2 });
  });

  it("reports failed saves and allows subsequent saves to recover", async () => {
    const write = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue(undefined);
    const save = editorSettingsWriter(async () => ({ theme: "light" }), write);
    await expect(save({ editorTabSize: 2 })).rejects.toThrow("offline");
    await save({ editorTabSize: 8 });
    expect(write).toHaveBeenLastCalledWith({ theme: "light", editorTabSize: 8 });
  });
});

describe("indentation popover", () => {
  let close: (() => void) | undefined;
  afterEach(() => { close?.(); document.body.replaceChildren(); });

  it("applies controls immediately and restores focus on Escape", () => {
    const button = document.createElement("button");
    document.body.append(button);
    const apply = vi.fn();
    close = openIndentationPopover(button, { insertSpaces: false, tabSize: 4 }, apply);
    const style = document.querySelector<HTMLSelectElement>("[data-indent-style]")!;
    const width = document.querySelector<HTMLSelectElement>("[data-indent-width]")!;
    expect(document.activeElement).toBe(style);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    style.value = "spaces";
    style.dispatchEvent(new Event("change", { bubbles: true }));
    width.value = "2";
    width.dispatchEvent(new Event("change", { bubbles: true }));
    expect(apply).toHaveBeenLastCalledWith({ insertSpaces: true, tabSize: 2 });
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(document.activeElement).toBe(button);
    expect(button.getAttribute("aria-expanded")).toBe("false");
  });

  it("dismisses outside the popover without changing preferences", () => {
    const button = document.createElement("button");
    document.body.append(button);
    const apply = vi.fn();
    close = openIndentationPopover(button, { insertSpaces: true, tabSize: 2 }, apply);
    document.body.dispatchEvent(new Event("pointerdown", { bubbles: true }));
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(apply).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(button);
  });
});
