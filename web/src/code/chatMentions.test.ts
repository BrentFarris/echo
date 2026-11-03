import { describe, expect, it } from "vitest";
import {
  activeMentionMatch, composerText, formatReferencePath, insertReferenceChip,
  restoreComposer, snapshotComposer, type ChatReference,
} from "./chatMentions";

const reference: ChatReference = {
  workspaceId: "workspace",
  ref: { rootId: "root", path: "docs/My File.md" },
  kind: "file",
  referencePath: "my-project/docs/My File.md",
  label: "My File.md",
};

function chip(value: ChatReference): HTMLElement {
  const element = document.createElement("span");
  element.dataset.chatFileMention = "";
  element.dataset.workspaceId = value.workspaceId;
  element.dataset.rootId = value.ref.rootId;
  element.dataset.workspacePath = value.ref.path;
  element.dataset.workspaceKind = value.kind;
  element.dataset.referencePath = value.referencePath;
  element.dataset.referenceLabel = value.label;
  element.textContent = value.label;
  return element;
}

function placeCaretAtEnd(element: HTMLElement): void {
  const range = document.createRange();
  range.selectNodeContents(element);
  range.collapse(false);
  const selection = window.getSelection()!;
  selection.removeAllRanges();
  selection.addRange(range);
}

describe("chat composer references", () => {
  it("quotes reference paths containing whitespace", () => {
    expect(formatReferencePath("echo/main.go")).toBe("@echo/main.go");
    expect(formatReferencePath("my-project/docs/My File.md")).toBe('@"my-project/docs/My File.md"');
  });

  it("round-trips structured chips without retaining arbitrary markup", () => {
    const editor = document.createElement("div");
    editor.append("Review ", chip(reference), document.createTextNode("\nthen test"));
    const segments = snapshotComposer(editor);
    expect(composerText(editor)).toBe('Review @"my-project/docs/My File.md"\nthen test');

    const restored = document.createElement("div");
    restoreComposer(restored, segments, chip);
    expect(restored.querySelector("[data-chat-file-mention]")?.textContent).toBe("My File.md");
    expect(composerText(restored)).toBe('Review @"my-project/docs/My File.md"\nthen test');
  });

  it("preserves contenteditable block line breaks", () => {
    const editor = document.createElement("div");
    editor.innerHTML = "first<div>second<br>third</div><div>fourth</div>";
    expect(composerText(editor)).toBe("first\nsecond\nthird\nfourth");
  });

  it("detects and replaces the active @ query", () => {
    const editor = document.createElement("div");
    editor.textContent = "Review @main";
    document.body.append(editor);
    placeCaretAtEnd(editor);
    const match = activeMentionMatch(editor);
    expect(match).toMatchObject({ query: "main", triggerStart: 7 });

    insertReferenceChip(editor, match!, chip({
      ...reference,
      ref: { rootId: "root", path: "main.go" },
      referencePath: "echo/main.go",
      label: "main.go",
    }));
    expect(composerText(editor)).toBe("Review @echo/main.go ");
    editor.remove();
  });
});
