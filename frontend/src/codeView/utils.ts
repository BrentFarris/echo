import type { EditorState } from "@codemirror/state";
import { services } from "../../wailsjs/go/models";

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
  return path.split("/").pop() || path;
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
