import type { EditorContextDiff, EditorContextPayload, EditorContextSelection } from "../chatSurface";
import type { FileRef } from "./types";

export const CODE_CHAT_MAX_TABS = 64;
export const CODE_CHAT_MAX_SELECTIONS = 64;
export const CODE_CHAT_MAX_INLINE_BYTES = 256 << 10;

export type CodeChatContextSource = {
  id: string;
  kind: "file" | "diff" | "untitled";
  title: string;
  dirty?: boolean;
  ref?: FileRef;
  reference?: string;
  content?: string;
  diff?: EditorContextDiff;
  selections?: EditorContextSelection[];
};

export async function runCodeChatSavePreflight<T>(
  tabs: readonly T[],
  requiresSave: (tab: T) => boolean,
  save: (tab: T) => Promise<boolean>,
): Promise<boolean> {
  for (const tab of tabs) {
    if (requiresSave(tab) && !(await save(tab))) return false;
  }
  return !tabs.some(requiresSave);
}

export function buildCodeChatEditorContext(
  tabs: readonly CodeChatContextSource[],
  activeTabId: string | null,
): EditorContextPayload {
  let selected = tabs.slice(0, CODE_CHAT_MAX_TABS);
  const active = activeTabId ? tabs.find((tab) => tab.id === activeTabId) : undefined;
  if (active && !selected.includes(active)) {
    selected = [...selected.slice(0, CODE_CHAT_MAX_TABS - 1), active];
  }
  selected.sort((left, right) => tabs.indexOf(left) - tabs.indexOf(right));

  let truncated = selected.length < tabs.length;
  let remaining = CODE_CHAT_MAX_INLINE_BYTES;
  const selectedRanges = new Map<string, EditorContextSelection[]>();
  if (active) {
    const ranges = (active.selections || []).filter((selection) => (
      selection.startLine !== selection.endLine || selection.startColumn !== selection.endColumn
    ));
    truncated ||= ranges.length > CODE_CHAT_MAX_SELECTIONS;
    const limitedRanges = ranges.slice(0, CODE_CHAT_MAX_SELECTIONS).map((selection) => {
      const limited = limitUTF8(selection.text, remaining);
      remaining -= limited.bytes;
      truncated ||= limited.truncated;
      return { ...selection, text: limited.value };
    });
    if (limitedRanges.length) selectedRanges.set(active.id, limitedRanges);
  }
  const inline = new Map<string, string>();
  const inlineSources = [...selected].sort(
    (left, right) => Number(right.id === activeTabId) - Number(left.id === activeTabId),
  );
  for (const tab of inlineSources) {
    if (typeof tab.content !== "string") continue;
    const limited = limitUTF8(tab.content, remaining);
    inline.set(tab.id, limited.value);
    remaining -= limited.bytes;
    truncated ||= limited.truncated;
  }

  return {
    tabs: selected.map(({ id, content: _content, ...tab }) => ({
      ...tab,
      active: id === activeTabId || undefined,
      dirty: tab.dirty || undefined,
      content: inline.get(id),
      selections: selectedRanges.get(id),
    })),
    truncated: truncated || undefined,
  };
}

export function formatCodeChatSelectionNotice(
  title: string,
  selections: readonly EditorContextSelection[],
): string | null {
  if (!selections.length) return null;
  const visible = selections.slice(0, 3).map((selection) => (
    selection.startLine === selection.endLine
      ? `line ${selection.startLine}`
      : `lines ${selection.startLine}\u2013${selection.endLine}`
  ));
  const more = selections.length > visible.length ? ` +${selections.length - visible.length} more` : "";
  const side = selections.every((selection) => selection.side === selections[0].side)
    ? selections[0].side
    : undefined;
  return `Selected context: ${title}${side ? ` (${side})` : ""}, ${visible.join(", ")}${more} will be included.`;
}

function limitUTF8(value: string, maximumBytes: number): { value: string; bytes: number; truncated: boolean } {
  if (maximumBytes <= 0) return { value: "", bytes: 0, truncated: value.length > 0 };
  const destination = new Uint8Array(maximumBytes);
  const { read, written } = new TextEncoder().encodeInto(value, destination);
  return { value: value.slice(0, read), bytes: written, truncated: read < value.length };
}
