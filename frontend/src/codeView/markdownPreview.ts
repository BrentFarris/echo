import { ReadWorkspaceMediaFile } from "../backend/services";
import {
  patchChildrenFromHtml,
  renderMarkdown,
  type MarkdownMediaKind,
} from "../markdown";
import { codeIcons } from "./icons";
import { findTab } from "./state";
import type {
  CodeFileTab,
  MarkdownPreviewMode,
} from "./types";
import {
  escapeAttribute,
  isMarkdownFile,
} from "./utils";

type CachedMarkdownMedia = {
  bytes: number;
  dataUrl: string;
  mimeType: string;
};

const maxMarkdownMediaCacheBytes = 100 * 1024 * 1024;
const markdownMediaCache = new Map<string, CachedMarkdownMedia>();
const markdownMediaLoads = new Map<string, Promise<CachedMarkdownMedia>>();
const pendingPreviewFrames = new Map<string, number>();
const previewModes: MarkdownPreviewMode[] = ["collapsed", "split", "full"];
let markdownMediaCacheBytes = 0;

export function isMarkdownPreviewTab(tab: CodeFileTab | null): tab is CodeFileTab {
  return Boolean(tab && !tab.isMedia && isMarkdownFile(tab.path));
}

export function markdownPreviewMode(tab: CodeFileTab): MarkdownPreviewMode {
  return previewModes.includes(tab.markdownPreviewMode ?? "split")
    ? tab.markdownPreviewMode ?? "split"
    : "split";
}

export function stepMarkdownPreviewMode(
  tab: CodeFileTab,
  direction: -1 | 1,
): boolean {
  const current = previewModes.indexOf(markdownPreviewMode(tab));
  const next = Math.min(
    previewModes.length - 1,
    Math.max(0, current + direction),
  );
  if (next === current) {
    return false;
  }
  tab.markdownPreviewMode = previewModes[next];
  return true;
}

export function renderMarkdownEditorFrame(
  workspaceID: string,
  tab: CodeFileTab,
): string {
  const mode = markdownPreviewMode(tab);
  const collapseTitle = mode === "full"
    ? "Return to split preview"
    : mode === "split"
      ? "Collapse preview"
      : "Preview is collapsed";
  const expandTitle = mode === "collapsed"
    ? "Open split preview"
    : mode === "split"
      ? "Expand preview"
      : "Preview is expanded";
  return `
    <div class="code-markdown-layout is-${mode}" data-code-markdown-layout data-markdown-preview-mode="${mode}">
      <div class="code-markdown-source" aria-label="Markdown source"${mode === "full" ? ` aria-hidden="true"` : ""}>
        <div class="code-editor-mount" data-code-editor-mount></div>
      </div>
      <section
        class="code-markdown-preview"
        aria-label="Markdown preview"
        data-code-markdown-preview
        data-code-markdown-workspace-id="${escapeAttribute(workspaceID)}"
        data-code-markdown-path="${escapeAttribute(tab.path)}"
      >
        <header class="code-markdown-preview-toolbar">
          <span class="code-markdown-preview-title">Preview</span>
          <span class="code-markdown-preview-actions">
            <button
              class="icon-button"
              type="button"
              title="${escapeAttribute(collapseTitle)}"
              aria-label="${escapeAttribute(collapseTitle)}"
              data-code-action="collapse-markdown-preview"
              ${mode === "collapsed" ? "disabled" : ""}
            >
              ${codeIcons.previewCollapse}
            </button>
            <button
              class="icon-button"
              type="button"
              title="${escapeAttribute(expandTitle)}"
              aria-label="${escapeAttribute(expandTitle)}"
              data-code-action="expand-markdown-preview"
              ${mode === "full" ? "disabled" : ""}
            >
              ${codeIcons.previewExpand}
            </button>
          </span>
        </header>
        <div class="code-markdown-preview-content markdown-body" tabindex="0" data-code-markdown-preview-content>
          ${renderMarkdownPreviewContent(workspaceID, tab)}
        </div>
      </section>
    </div>
  `;
}

export function mountMarkdownPreview(
  workspaceID: string,
  path: string,
) {
  patchMarkdownPreview(workspaceID, path);
}

export function scheduleMarkdownPreviewPatch(
  workspaceID: string,
  path: string,
) {
  const key = markdownPreviewKey(workspaceID, path);
  if (pendingPreviewFrames.has(key)) {
    return;
  }
  const frame = window.requestAnimationFrame(() => {
    pendingPreviewFrames.delete(key);
    patchMarkdownPreview(workspaceID, path);
  });
  pendingPreviewFrames.set(key, frame);
}

function patchMarkdownPreview(workspaceID: string, path: string) {
  const tab = findTab(workspaceID, path);
  const preview = markdownPreviewElement(workspaceID, path);
  const content = preview?.querySelector<HTMLElement>(
    "[data-code-markdown-preview-content]",
  );
  if (!tab || !content || !isMarkdownPreviewTab(tab)) {
    return;
  }
  patchChildrenFromHtml(content, renderMarkdownPreviewContent(workspaceID, tab));
  hydrateMarkdownPreviewMedia(workspaceID, tab, content);
}

function renderMarkdownPreviewContent(
  workspaceID: string,
  tab: CodeFileTab,
): string {
  return renderMarkdown(tab.content, {
    enableMedia: true,
    emptyPlaceholder: "Nothing to preview yet.",
    headingOffset: 0,
    resolveMediaURL: (source, kind) =>
      cachedMarkdownMediaURL(workspaceID, tab, source, kind),
  });
}

function markdownPreviewElement(
  workspaceID: string,
  path: string,
): HTMLElement | null {
  return Array.from(
    document.querySelectorAll<HTMLElement>("[data-code-markdown-preview]"),
  ).find((element) =>
    element.dataset.codeMarkdownWorkspaceId === workspaceID &&
    element.dataset.codeMarkdownPath === path
  ) ?? null;
}

function hydrateMarkdownPreviewMedia(
  workspaceID: string,
  tab: CodeFileTab,
  content: HTMLElement,
) {
  content
    .querySelectorAll<HTMLElement>("[data-markdown-media-source]")
    .forEach((element) => {
      void hydrateMarkdownPreviewMediaElement(workspaceID, tab, element);
    });
}

async function hydrateMarkdownPreviewMediaElement(
  workspaceID: string,
  tab: CodeFileTab,
  element: HTMLElement,
) {
  const source = element.dataset.markdownMediaSource ?? "";
  const kind = element.dataset.markdownMediaKind as MarkdownMediaKind;
  if (!source || !["image", "audio", "video"].includes(kind)) {
    showMarkdownMediaError(element, "This media reference is invalid.");
    return;
  }
  if (tab.external) {
    showMarkdownMediaError(
      element,
      "Local media can only be resolved for files inside this workspace.",
    );
    return;
  }
  const path = resolveMarkdownWorkspaceMediaPath(tab.path, source);
  if (!path) {
    showMarkdownMediaError(
      element,
      `Could not resolve ${source} inside this workspace.`,
    );
    return;
  }
  const key = markdownMediaCacheKey(workspaceID, path);
  try {
    const media = markdownMediaCache.get(key) ??
      await loadMarkdownMedia(workspaceID, path);
    if (!element.isConnected || element.dataset.markdownMediaSource !== source) {
      return;
    }
    if (!media.mimeType.startsWith(`${kind}/`)) {
      showMarkdownMediaError(
        element,
        `${source} resolved to ${media.mimeType}, not ${kind} media.`,
      );
      return;
    }
    const target = element.querySelector<HTMLElement>(
      "[data-markdown-media-target]",
    );
    if (!target) {
      return;
    }
    if (target.getAttribute("src") !== media.dataUrl) {
      target.setAttribute("src", media.dataUrl);
    }
    target.hidden = false;
    element.querySelector("[data-markdown-media-status]")?.remove();
  } catch (error) {
    if (!element.isConnected || element.dataset.markdownMediaSource !== source) {
      return;
    }
    showMarkdownMediaError(
      element,
      `Could not load ${source}: ${markdownMediaErrorMessage(error)}`,
    );
  }
}

function loadMarkdownMedia(
  workspaceID: string,
  path: string,
): Promise<CachedMarkdownMedia> {
  const key = markdownMediaCacheKey(workspaceID, path);
  const pending = markdownMediaLoads.get(key);
  if (pending) {
    return pending;
  }
  const load = ReadWorkspaceMediaFile(workspaceID, path)
    .then((media) => {
      const cached = {
        bytes: media.bytes,
        dataUrl: media.dataUrl,
        mimeType: media.mimeType,
      };
      cacheMarkdownMedia(key, cached);
      return cached;
    })
    .finally(() => {
      markdownMediaLoads.delete(key);
    });
  markdownMediaLoads.set(key, load);
  return load;
}

function cachedMarkdownMediaURL(
  workspaceID: string,
  tab: CodeFileTab,
  source: string,
  kind: MarkdownMediaKind,
): string {
  if (tab.external) {
    return "";
  }
  const path = resolveMarkdownWorkspaceMediaPath(tab.path, source);
  if (!path) {
    return "";
  }
  const media = markdownMediaCache.get(
    markdownMediaCacheKey(workspaceID, path),
  );
  if (media) {
    const key = markdownMediaCacheKey(workspaceID, path);
    markdownMediaCache.delete(key);
    markdownMediaCache.set(key, media);
  }
  return media?.mimeType.startsWith(`${kind}/`) ? media.dataUrl : "";
}

export function resolveMarkdownWorkspaceMediaPath(
  markdownPath: string,
  source: string,
): string {
  let decodedSource = source.trim().replace(/[?#].*$/, "");
  try {
    decodedSource = decodeURIComponent(decodedSource);
  } catch {
    return "";
  }
  decodedSource = decodedSource.replaceAll("\\", "/");
  if (
    !decodedSource ||
    /^[a-z][a-z\d+.-]*:/i.test(decodedSource) ||
    decodedSource.startsWith("//")
  ) {
    return "";
  }

  const markdownParts = markdownPath.replaceAll("\\", "/").split("/").filter(Boolean);
  if (markdownParts.length < 2) {
    return "";
  }
  const root = markdownParts[0];
  const sourceParts = decodedSource.split("/").filter((part) => part !== "");
  let resolved = decodedSource.startsWith("/")
    ? [root]
    : sourceParts[0] === root
      ? []
      : markdownParts.slice(0, -1);

  for (const part of sourceParts) {
    if (part === ".") {
      continue;
    }
    if (part === "..") {
      if (resolved.length <= 1) {
        return "";
      }
      resolved.pop();
      continue;
    }
    resolved.push(part);
  }
  if (resolved[0] !== root || resolved.length < 2) {
    return "";
  }
  return resolved.join("/");
}

function showMarkdownMediaError(element: HTMLElement, message: string) {
  let status = element.querySelector<HTMLElement>(
    "[data-markdown-media-status]",
  );
  if (!status) {
    status = document.createElement("span");
    status.className = "markdown-media-status";
    status.dataset.markdownMediaStatus = "";
    element.prepend(status);
  }
  status.classList.add("is-error");
  status.textContent = message;
  const target = element.querySelector<HTMLElement>(
    "[data-markdown-media-target]",
  );
  if (target) {
    target.hidden = true;
  }
}

function markdownMediaCacheKey(workspaceID: string, path: string): string {
  return `${workspaceID}\u0000${path}`;
}

function cacheMarkdownMedia(key: string, media: CachedMarkdownMedia) {
  const existing = markdownMediaCache.get(key);
  if (existing) {
    markdownMediaCacheBytes -= existing.bytes;
    markdownMediaCache.delete(key);
  }
  markdownMediaCache.set(key, media);
  markdownMediaCacheBytes += media.bytes;
  while (
    markdownMediaCacheBytes > maxMarkdownMediaCacheBytes &&
    markdownMediaCache.size > 1
  ) {
    const oldestKey = markdownMediaCache.keys().next().value;
    if (typeof oldestKey !== "string") {
      break;
    }
    const oldest = markdownMediaCache.get(oldestKey);
    markdownMediaCache.delete(oldestKey);
    markdownMediaCacheBytes -= oldest?.bytes ?? 0;
  }
}

function markdownPreviewKey(workspaceID: string, path: string): string {
  return `${workspaceID}\u0000${path}`;
}

function markdownMediaErrorMessage(error: unknown): string {
  if (error instanceof Error && error.message) {
    return error.message;
  }
  const message = String(error ?? "").trim();
  return message || "unknown error";
}
