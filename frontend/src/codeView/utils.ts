import type { EditorState } from "@codemirror/state";
import { services } from "../../wailsjs/go/models";
import type { CodeFileTab } from "./types";

export const untitledCodeTabPrefix = "untitled://";

export function sleep(delay: number) {
  return new Promise<void>((resolve) => window.setTimeout(resolve, delay));
}

export function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  if (bytes < 1024 * 1024) {
    return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`;
  }
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function fileName(path: string): string {
  return path.replaceAll("\\", "/").split("/").pop() || path;
}

export function codeTabName(tab: Pick<CodeFileTab, "path" | "untitled">): string {
  if (tab.untitled && tab.path.startsWith(untitledCodeTabPrefix)) {
    return tab.path.slice(untitledCodeTabPrefix.length);
  }
  return fileName(tab.path);
}

export function isUntitledCodePath(path: string): boolean {
  return path.startsWith(untitledCodeTabPrefix);
}

export function detectLineSeparator(content: string): string {
  let crlf = 0;
  let lf = 0;
  for (let index = 0; index < content.length; index++) {
    const char = content[index];
    if (char === "\r") {
      if (content[index + 1] === "\n") {
        crlf++;
        index++;
      } else {
        lf++;
      }
    } else if (char === "\n") {
      lf++;
    }
  }
  return crlf > 0 && crlf >= lf ? "\r\n" : "\n";
}

export function editableWorkspaceFile(file: services.WorkspaceFile) {
  const lineSeparator = detectLineSeparator(file.content);
  const content = normalizeEditorLineBreaks(file.content, lineSeparator);
  return {
    content,
    lineSeparator,
    bytes: new TextEncoder().encode(content).length,
  };
}

export function normalizeEditorLineBreaks(content: string, lineSeparator: string): string {
  const normalized = content.replace(/\r\n?|\n|\u0085|\u2028|\u2029/g, "\n");
  return lineSeparator === "\r\n" ? normalized.replaceAll("\n", "\r\n") : normalized;
}

export function editorStateToFileContent(state: EditorState): string {
  return state.sliceDoc(0);
}

export function editorPositionToFileContentOffset(state: EditorState, position: number): number {
  const editorPosition = clamp(position, 0, state.doc.length);
  const line = state.doc.lineAt(editorPosition);
  const lineBreakExtra = Math.max(0, state.lineBreak.length - 1);
  return editorPosition + (line.number - 1) * lineBreakExtra;
}

export function fileContentOffsetToEditorPosition(
  content: string,
  lineSeparator: string,
  position: number,
): number {
  const target = clamp(position, 0, content.length);
  if (lineSeparator.length <= 1) {
    return target;
  }

  let contentOffset = 0;
  let editorPosition = 0;
  while (contentOffset < target) {
    if (content.startsWith(lineSeparator, contentOffset)) {
      if (contentOffset + lineSeparator.length > target) {
        return editorPosition;
      }
      contentOffset += lineSeparator.length;
      editorPosition++;
      continue;
    }
    contentOffset++;
    editorPosition++;
  }
  return editorPosition;
}

export function editorDocumentLengthForFileContent(content: string, lineSeparator: string): number {
  return fileContentOffsetToEditorPosition(content, lineSeparator, content.length);
}

export function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

export function escapeAttribute(value: string): string {
  return escapeHtml(value).replaceAll("`", "&#096;");
}

const IMAGE_EXTENSIONS = new Set([
  "jpg",
  "jpeg",
  "png",
  "gif",
  "bmp",
  "webp",
  "svg",
  "ico",
  "tiff",
  "tif",
]);

const VIDEO_EXTENSIONS = new Set([
  "mp4",
  "webm",
  "ogv",
  "mov",
  "avi",
  "mkv",
  "flv",
  "wmv",
]);

const AUDIO_EXTENSIONS = new Set([
  "mp3",
  "wav",
  "ogg",
  "oga",
  "m4a",
  "aac",
  "flac",
  "opus",
  "wma",
  "weba",
]);

function fileExtension(path: string): string {
  const name = fileName(path).toLowerCase();
  const dotIndex = name.lastIndexOf(".");
  return dotIndex === -1 ? "" : name.slice(dotIndex + 1);
}

export function isImageFile(path: string): boolean {
  return IMAGE_EXTENSIONS.has(fileExtension(path));
}

export function isVideoFile(path: string): boolean {
  return VIDEO_EXTENSIONS.has(fileExtension(path));
}

export function isAudioFile(path: string): boolean {
  return AUDIO_EXTENSIONS.has(fileExtension(path));
}

export function isMediaFile(path: string): boolean {
  return isImageFile(path) || isVideoFile(path) || isAudioFile(path);
}

export function mediaKind(path: string): string {
  if (isImageFile(path)) return "image";
  if (isVideoFile(path)) return "video";
  if (isAudioFile(path)) return "audio";
  return "";
}
