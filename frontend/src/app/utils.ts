
import { services } from "../../wailsjs/go/models";
import { kanbanLaneLabels } from "./state";

export function workspaceFolderSummary(workspace: services.Workspace): string {
  const folders = workspace.folders ?? [];
  if (!folders.length) {
    return "No folders";
  }
  return folders
    .map((folder) => `${folder.label}: ${folder.path}${folder.missing ? " (missing)" : ""}`)
    .join(" | ");
}

export function workspaceFolderStatus(folder: services.WorkspaceFolder): string {
  if (folder.missing) {
    return folder.error?.trim() || "Folder unavailable";
  }
  return "Available";
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

export function fileName(path: string): string {
  return path.split("/").pop() || path;
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

export function laneLabel(lane = "ready"): string {
  return kanbanLaneLabels[lane] ?? "Ready";
}

export async function copyTextToClipboard(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // Fall through to the textarea copy path for webviews without clipboard permission.
    }
  }

  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.top = "0";
  textarea.style.left = "-9999px";
  document.body.appendChild(textarea);
  textarea.focus();
  textarea.select();
  textarea.setSelectionRange(0, textarea.value.length);
  try {
    if (!document.execCommand("copy")) {
      throw new Error("Clipboard copy was not accepted.");
    }
  } finally {
    textarea.remove();
  }
}

export function changeOperationLabel(operation = ""): string {
  switch (operation) {
    case "created":
      return "Created";
    case "deleted":
      return "Deleted";
    case "edited":
      return "Edited";
    case "renamed":
      return "Renamed";
    case "copied":
      return "Copied";
    case "conflicted":
      return "Conflicted";
    default:
      return operation || "Changed";
  }
}

export function changeSourceLabel(source: services.WorkspaceChangeSource): string {
  if (source.type === "kanban") {
    return `Kanban ${source.cardTitle || source.cardId || "card"}`;
  }
  if (source.type === "inline") {
    return "Inline code";
  }
  if (source.type === "chat") {
    return "Chat";
  }
  return source.type || "AI";
}

export function formatElapsedTime(milliseconds: number): string {
  const totalSeconds = Math.max(0, Math.floor(milliseconds / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const paddedSeconds = String(seconds).padStart(2, "0");
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, "0")}:${paddedSeconds}`;
  }
  return `${minutes}:${paddedSeconds}`;
}

export function generateUUID(): string {
  if (typeof crypto?.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  // Fallback for non-secure contexts (HTTP web access)
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (crypto.getRandomValues(new Uint8Array(1))[0] % 16);
    const v = c === 'x' ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}

export function errorMessage(error: unknown): string {
  const raw = error instanceof Error ? error.message : String(error);
  if (raw.includes("send chat request") || raw.includes("connection refused") || raw.includes("No connection could be made")) {
    return `Could not reach the LLM endpoint. Check Settings and try again. ${raw}`;
  }
  if (raw.includes("context deadline exceeded") || raw.includes("Client.Timeout")) {
    return `The LLM endpoint timed out. Increase Timeout Seconds or check the endpoint. ${raw}`;
  }
  return raw;
}
