import type { FileRef } from "./types";
import { refKey } from "./types";

export type Bookmark = { id: string; ref: FileRef; line: number; label?: string; preview: string };
export type BookmarkState = { version: 1; revision: number; bookmarks: Bookmark[] };
export type BookmarkPosition = Pick<Bookmark, "id" | "ref" | "line" | "preview">;
export type BookmarkAction =
  | { action: "toggle"; ref: FileRef; line: number; preview: string }
  | { action: "rename"; id: string; label: string }
  | { action: "delete"; id: string }
  | { action: "positions"; positions: BookmarkPosition[] }
  | { action: "remap"; previousRef: FileRef; nextRef: FileRef };

export function bookmarkLabel(bookmark: Bookmark): string {
  return bookmark.label || bookmark.preview.trim() || `Line ${bookmark.line}`;
}

export function groupBookmarks(bookmarks: Bookmark[]): Array<{ ref: FileRef; bookmarks: Bookmark[] }> {
  const groups = new Map<string, { ref: FileRef; bookmarks: Bookmark[] }>();
  for (const bookmark of bookmarks) {
    const key = refKey(bookmark.ref);
    const group = groups.get(key) || { ref: bookmark.ref, bookmarks: [] };
    group.bookmarks.push(bookmark);
    groups.set(key, group);
  }
  return [...groups.values()]
    .sort((a, b) => a.ref.path.localeCompare(b.ref.path) || a.ref.rootId.localeCompare(b.ref.rootId))
    .map(group => ({ ...group, bookmarks: group.bookmarks.sort((a, b) => a.line - b.line || a.id.localeCompare(b.id)) }));
}
