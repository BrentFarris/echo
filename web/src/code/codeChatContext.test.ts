import { describe, expect, it, vi } from "vitest";
import {
  CODE_CHAT_MAX_INLINE_BYTES,
  CODE_CHAT_MAX_TABS,
  buildCodeChatEditorContext,
  runCodeChatSavePreflight,
  type CodeChatContextSource,
} from "./codeChatContext";

describe("Code Chat editor context", () => {
  it("caps tabs while retaining the active tab", () => {
    const tabs: CodeChatContextSource[] = Array.from({ length: CODE_CHAT_MAX_TABS + 1 }, (_, index) => ({
      id: `tab-${index}`,
      kind: "file",
      title: `file-${index}.ts`,
      ref: { rootId: "root", path: `file-${index}.ts` },
      reference: `echo/file-${index}.ts`,
    }));

    const context = buildCodeChatEditorContext(tabs, `tab-${CODE_CHAT_MAX_TABS}`);

    expect(context.tabs).toHaveLength(CODE_CHAT_MAX_TABS);
    expect(context.tabs.some((tab) => tab.title === `file-${CODE_CHAT_MAX_TABS}.ts` && tab.active)).toBe(true);
    expect(context.tabs.some((tab) => tab.title === `file-${CODE_CHAT_MAX_TABS - 1}.ts`)).toBe(false);
    expect(context.truncated).toBe(true);
  });

  it("prioritizes active untitled content within the UTF-8 byte budget", () => {
    const content = "😀".repeat(50_000);
    const context = buildCodeChatEditorContext([
      { id: "inactive", kind: "untitled", title: "Untitled-1", dirty: true, content },
      { id: "active", kind: "untitled", title: "Untitled-2", dirty: true, content },
    ], "active");

    const active = context.tabs.find((tab) => tab.active)!;
    const inactive = context.tabs.find((tab) => !tab.active)!;
    const encoder = new TextEncoder();
    expect(active.content).toBe(content);
    expect(encoder.encode((active.content || "") + (inactive.content || "")).byteLength).toBeLessThanOrEqual(CODE_CHAT_MAX_INLINE_BYTES);
    expect(inactive.content?.endsWith("\ud83d")).toBe(false);
    expect(context.truncated).toBe(true);
  });

  it("aborts sequential saving on failure and verifies all dirty tabs were cleared", async () => {
    const tabs = [{ id: "one", dirty: true }, { id: "two", dirty: true }, { id: "three", dirty: true }];
    const save = vi.fn(async (tab: { id: string; dirty: boolean }) => {
      if (tab.id === "two") return false;
      tab.dirty = false;
      return true;
    });

    await expect(runCodeChatSavePreflight(tabs, (tab) => tab.dirty, save)).resolves.toBe(false);
    expect(save.mock.calls.map(([tab]) => tab.id)).toEqual(["one", "two"]);

    const unchanged = [{ dirty: true }];
    await expect(runCodeChatSavePreflight(unchanged, (tab) => tab.dirty, async () => true)).resolves.toBe(false);
  });
});
