import { describe, expect, it, vi } from "vitest";
import {
  CODE_CHAT_MAX_INLINE_BYTES,
  CODE_CHAT_MAX_SELECTIONS,
  CODE_CHAT_MAX_TABS,
  buildCodeChatEditorContext,
  formatCodeChatSelectionNotice,
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

  it("includes every non-empty active-tab selection and ignores inactive selections", () => {
    const context = buildCodeChatEditorContext([
      {
        id: "inactive", kind: "file", title: "inactive.ts",
        selections: [{ startLine: 1, startColumn: 1, endLine: 1, endColumn: 4, text: "old" }],
      },
      {
        id: "active", kind: "diff", title: "active.ts",
        selections: [
          { side: "original", startLine: 2, startColumn: 1, endLine: 2, endColumn: 1, text: "" },
          { side: "original", startLine: 3, startColumn: 2, endLine: 4, endColumn: 5, text: "first" },
          { side: "original", startLine: 8, startColumn: 1, endLine: 8, endColumn: 7, text: "second" },
        ],
      },
    ], "active");

    expect(context.tabs[0].selections).toBeUndefined();
    expect(context.tabs[1].selections).toEqual([
      { side: "original", startLine: 3, startColumn: 2, endLine: 4, endColumn: 5, text: "first" },
      { side: "original", startLine: 8, startColumn: 1, endLine: 8, endColumn: 7, text: "second" },
    ]);
  });

  it("prioritizes selected text and caps ranges within the shared inline budget", () => {
    const selections = Array.from({ length: CODE_CHAT_MAX_SELECTIONS + 1 }, (_, index) => ({
      startLine: index + 1, startColumn: 1, endLine: index + 1, endColumn: 2,
      text: index === 0 ? "😀".repeat(CODE_CHAT_MAX_INLINE_BYTES) : "x",
    }));
    const context = buildCodeChatEditorContext([
      { id: "active", kind: "untitled", title: "Untitled-1", content: "draft", selections },
    ], "active");

    expect(context.tabs[0].selections).toHaveLength(CODE_CHAT_MAX_SELECTIONS);
    expect(context.tabs[0].selections?.[0].text.endsWith("\ud83d")).toBe(false);
    expect(context.tabs[0].content).toBe("");
    expect(context.truncated).toBe(true);
  });

  it("formats a compact file, side, and line summary", () => {
    expect(formatCodeChatSelectionNotice("main.go", [
      { side: "modified", startLine: 12, startColumn: 1, endLine: 27, endColumn: 2, text: "one" },
      { side: "modified", startLine: 31, startColumn: 1, endLine: 31, endColumn: 5, text: "two" },
      { side: "modified", startLine: 40, startColumn: 1, endLine: 42, endColumn: 5, text: "three" },
      { side: "modified", startLine: 50, startColumn: 1, endLine: 50, endColumn: 5, text: "four" },
    ])).toBe("Selected context: main.go (modified), lines 12\u201327, line 31, lines 40\u201342 +1 more will be included.");
    expect(formatCodeChatSelectionNotice("main.go", [])).toBeNull();
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
