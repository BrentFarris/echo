import type { EditorContextDiff, EditorContextPayload } from "../chatSurface";
import type { FileRef } from "./types";

export const CODE_CHAT_MAX_TABS = 64;
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
    })),
    truncated: truncated || undefined,
  };
}

function limitUTF8(value: string, maximumBytes: number): { value: string; bytes: number; truncated: boolean } {
  if (maximumBytes <= 0) return { value: "", bytes: 0, truncated: value.length > 0 };
  const destination = new Uint8Array(maximumBytes);
  const { read, written } = new TextEncoder().encodeInto(value, destination);
  return { value: value.slice(0, read), bytes: written, truncated: read < value.length };
}
