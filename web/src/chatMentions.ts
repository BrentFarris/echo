import type { FileRef } from "./code/types";

export type ChatReference = {
  workspaceId: string;
  ref: FileRef;
  kind: "file" | "directory";
  referencePath: string;
  label: string;
};

export type ComposerSegment =
  | { type: "text"; value: string }
  | ({ type: "reference" } & ChatReference);

export type ChatMentionMatch = {
  triggerStart: number;
  query: string;
  caret: number;
};

function appendText(segments: ComposerSegment[], value: string): void {
  if (!value) return;
  const last = segments.at(-1);
  if (last?.type === "text") last.value += value;
  else segments.push({ type: "text", value });
}

function referenceFromElement(element: HTMLElement): ChatReference | null {
  const workspaceId = element.dataset.workspaceId || "";
  const rootId = element.dataset.rootId || "";
  const path = element.dataset.workspacePath || "";
  const referencePath = element.dataset.referencePath || "";
  const kind = element.dataset.workspaceKind === "directory" ? "directory" : "file";
  if (!workspaceId || !rootId || !referencePath) return null;
  return {
    workspaceId,
    ref: { rootId, path },
    kind,
    referencePath,
    label: element.dataset.referenceLabel || referencePath.split("/").at(-1) || referencePath,
  };
}

function serializeNode(node: Node, segments: ComposerSegment[]): void {
  if (node.nodeType === Node.TEXT_NODE) {
    appendText(segments, node.nodeValue || "");
    return;
  }
  if (!(node instanceof HTMLElement)) return;
  if (node.hasAttribute("data-chat-file-mention")) {
    const reference = referenceFromElement(node);
    if (reference) segments.push({ type: "reference", ...reference });
    return;
  }
  if (node.tagName === "BR") {
    appendText(segments, "\n");
    return;
  }
  for (const child of Array.from(node.childNodes)) serializeNode(child, segments);
}

/** Captures editable text and reference chips without retaining pasted HTML. */
export function snapshotComposer(editor: HTMLElement): ComposerSegment[] {
  const segments: ComposerSegment[] = [];
  const children = Array.from(editor.childNodes);
  children.forEach((child, index) => {
    if (index > 0) {
      const previous = children[index - 1];
      const currentIsBlock = child instanceof HTMLElement && child.tagName === "DIV";
      const previousIsBlock = previous instanceof HTMLElement && (previous.tagName === "DIV" || previous.tagName === "BR");
      const last = segments.at(-1);
      const alreadyEndsInNewline = last?.type === "text" && last.value.endsWith("\n");
      if ((currentIsBlock || previousIsBlock) && !alreadyEndsInNewline) {
        appendText(segments, "\n");
      }
    }
    serializeNode(child, segments);
  });
  const last = segments.at(-1);
  if (last?.type === "text") {
    last.value = last.value.replace(/\n+$/, "");
    if (!last.value) segments.pop();
  }
  return segments;
}

export function formatReferencePath(path: string): string {
  if (!/\s/.test(path)) return `@${path}`;
  return `@"${path.replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"`;
}

export function composerText(editor: HTMLElement): string {
  return snapshotComposer(editor).map((segment) =>
    segment.type === "text" ? segment.value : formatReferencePath(segment.referencePath)
  ).join("");
}

export function restoreComposer(
  editor: HTMLElement,
  segments: ComposerSegment[],
  createChip: (reference: ChatReference) => HTMLElement,
): void {
  const fragment = document.createDocumentFragment();
  for (const segment of segments) {
    fragment.append(segment.type === "text" ? document.createTextNode(segment.value) : createChip(segment));
  }
  editor.replaceChildren(fragment);
}

function caretText(editor: HTMLElement): string {
  const selection = window.getSelection();
  if (!selection || selection.rangeCount === 0 || !editor.contains(selection.getRangeAt(0).startContainer)) {
    return composerText(editor);
  }
  const range = selection.getRangeAt(0).cloneRange();
  range.selectNodeContents(editor);
  range.setEnd(selection.getRangeAt(0).startContainer, selection.getRangeAt(0).startOffset);
  const container = document.createElement("div");
  container.append(range.cloneContents());
  return composerText(container);
}

export function activeMentionMatch(editor: HTMLElement): ChatMentionMatch | null {
  const beforeCaret = caretText(editor);
  const match = beforeCaret.match(/(^|\s)@([^\s@]*)$/);
  if (!match) return null;
  const query = match[2] || "";
  return {
    triggerStart: beforeCaret.length - query.length - 1,
    query,
    caret: beforeCaret.length,
  };
}

function removeTextBeforeCaret(editor: HTMLElement, count: number): Range {
  const selection = window.getSelection();
  let range: Range;
  if (selection?.rangeCount && editor.contains(selection.getRangeAt(0).startContainer)) {
    range = selection.getRangeAt(0).cloneRange();
  } else {
    range = document.createRange();
    range.selectNodeContents(editor);
    range.collapse(false);
  }
  const deepestLast = (candidate: Node): Node => {
    let current = candidate;
    while (current.lastChild) current = current.lastChild;
    return current;
  };
  const previousLeaf = (candidate: Node): Node | null => {
    let current: Node | null = candidate;
    while (current && current !== editor) {
      if (current.previousSibling) return deepestLast(current.previousSibling);
      current = current.parentNode;
    }
    return null;
  };

  let remaining = count;
  let node: Node | null = range.startContainer;
  let offset = range.startOffset;
  let anchorNode: Node = editor;
  let anchorOffset = editor.childNodes.length;
  while (remaining > 0 && node) {
    if (node.nodeType === Node.TEXT_NODE) {
      const value = node.nodeValue || "";
      const removable = Math.min(remaining, offset);
      if (removable) {
        node.nodeValue = value.slice(0, offset - removable) + value.slice(offset);
        offset -= removable;
        remaining -= removable;
        anchorNode = node;
        anchorOffset = offset;
      }
      if (remaining > 0) {
        node = previousLeaf(node);
        offset = node?.nodeType === Node.TEXT_NODE ? (node.nodeValue || "").length : node?.childNodes.length || 0;
      }
      continue;
    }
    if (offset > 0 && node.childNodes.length) {
      node = deepestLast(node.childNodes[Math.min(offset, node.childNodes.length) - 1]);
      offset = node.nodeType === Node.TEXT_NODE ? (node.nodeValue || "").length : node.childNodes.length;
      continue;
    }
    node = previousLeaf(node);
    offset = node?.nodeType === Node.TEXT_NODE ? (node.nodeValue || "").length : node?.childNodes.length || 0;
  }
  const result = document.createRange();
  result.setStart(anchorNode, anchorOffset);
  result.collapse(true);
  return result;
}

/** Replaces the active @query token with a chip and positions the caret after it. */
export function insertReferenceChip(editor: HTMLElement, match: ChatMentionMatch, chip: HTMLElement): void {
  const fullText = composerText(editor);
  const suffix = fullText.slice(match.caret);
  const trailingSpace = suffix.length === 0 || !/^\s/.test(suffix) ? " " : "";
  const range = removeTextBeforeCaret(editor, match.caret - match.triggerStart);
  const trailing = document.createTextNode(trailingSpace);
  range.insertNode(trailing);
  range.insertNode(chip);
  range.setStartAfter(trailing);
  range.collapse(true);
  const selection = window.getSelection();
  selection?.removeAllRanges();
  selection?.addRange(range);
}
