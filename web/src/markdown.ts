import DOMPurify from "dompurify";
import { marked, Renderer } from "marked";

export const MARKDOWN_PATCH_DELAY_MS = 50;

type PendingMarkdownPatch = {
  markdown: string;
  afterPatch?: () => void;
  timeoutID: number;
};

const pendingMarkdownPatches = new WeakMap<HTMLElement, PendingMarkdownPatch>();
const renderer = new Renderer();

// Chat accepts Markdown, not arbitrary HTML. Escaping HTML tokens before the
// sanitizer runs keeps user and model output inside the supported Markdown
// vocabulary while DOMPurify remains a second line of defense.
renderer.html = ({ text }) => escapeHtml(text);

function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

export function renderMarkdown(markdown = ""): string {
  const rendered = marked.parse(markdown.replaceAll("\r\n", "\n"), {
    async: false,
    breaks: false,
    gfm: true,
    renderer,
  });
  return DOMPurify.sanitize(rendered, {
    USE_PROFILES: { html: true },
    FORBID_TAGS: ["form", "iframe", "object", "embed", "script", "style", "svg", "math"],
    FORBID_ATTR: ["style", "srcset"],
  });
}

export function patchMarkdownElement(element: HTMLElement, markdown = ""): void {
  const template = document.createElement("template");
  template.innerHTML = renderMarkdown(markdown);
  normalizeMarkdownFragment(template.content);
  morphChildren(element, template.content);
}

export function queueMarkdownPatch(
  element: HTMLElement,
  markdown: string,
  afterPatch?: () => void,
): void {
  const pending = pendingMarkdownPatches.get(element);
  if (pending) {
    pending.markdown = markdown;
    pending.afterPatch = afterPatch;
    return;
  }

  const next: PendingMarkdownPatch = {
    markdown,
    afterPatch,
    timeoutID: window.setTimeout(() => {
      if (pendingMarkdownPatches.get(element) !== next) return;
      pendingMarkdownPatches.delete(element);
      if (!element.isConnected) return;
      patchMarkdownElement(element, next.markdown);
      next.afterPatch?.();
    }, MARKDOWN_PATCH_DELAY_MS),
  };
  pendingMarkdownPatches.set(element, next);
}

export function flushMarkdownPatch(element: HTMLElement, markdown?: string): void {
  const pending = pendingMarkdownPatches.get(element);
  if (pending) {
    window.clearTimeout(pending.timeoutID);
    pendingMarkdownPatches.delete(element);
  }
  const source = markdown ?? pending?.markdown;
  if (source === undefined) return;
  patchMarkdownElement(element, source);
  pending?.afterPatch?.();
}

export function cancelMarkdownPatch(element: HTMLElement | null | undefined): void {
  if (!element) return;
  const pending = pendingMarkdownPatches.get(element);
  if (!pending) return;
  window.clearTimeout(pending.timeoutID);
  pendingMarkdownPatches.delete(element);
}

function normalizeMarkdownFragment(fragment: DocumentFragment): void {
  for (const anchor of fragment.querySelectorAll<HTMLAnchorElement>("a")) {
    const href = anchor.getAttribute("href")?.trim() ?? "";
    if (!isSafeHTTPURL(href)) {
      anchor.removeAttribute("href");
      anchor.removeAttribute("target");
      anchor.removeAttribute("rel");
      continue;
    }
    anchor.target = "_blank";
    anchor.rel = "noopener noreferrer";
  }

  for (const image of fragment.querySelectorAll<HTMLImageElement>("img")) {
    const source = image.getAttribute("src")?.trim() ?? "";
    if (!isSafeHTTPURL(source) && !isSafeRasterDataURL(source)) {
      image.replaceWith(document.createTextNode(image.alt));
      continue;
    }
    image.removeAttribute("srcset");
    image.loading = "lazy";
    image.decoding = "async";
  }

  // Convert File: path, line N patterns into clickable links.
  hyperlinksForFileReferences(fragment);
}

/** Walks text nodes looking for "File: path, line N" and wraps the path in a link. */
function hyperlinksForFileReferences(root: Node): void {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, null);
  const texts: Text[] = [];
  while (walker.nextNode()) texts.push(walker.currentNode as Text);

  for (const text of texts) {
    // Avoid re-processing nodes we already converted.
    if (text.parentElement?.hasAttribute("data-file-link")) continue;
    const value = text.nodeValue ?? "";
    const link = fileReferenceLink(value);
    if (!link) continue;

    const fragment = document.createDocumentFragment();
    if (link.prefix) fragment.appendChild(document.createTextNode(link.prefix));

    const anchor = document.createElement("a");
    anchor.className = "chat-file-link";
    anchor.setAttribute("data-file-link", "");
    anchor.setAttribute("data-file-path", link.path);
    anchor.setAttribute("data-file-line", String(link.line));
    anchor.textContent = link.display;
    fragment.appendChild(anchor);

    if (link.suffix) fragment.appendChild(document.createTextNode(link.suffix));
    text.replaceWith(fragment);
  }
}

/** Parses a text span for "File: <path>, line ~<N>" and returns the decomposition. */
function fileReferenceLink(text: string): { prefix: string; path: string; display: string; line: number; suffix: string } | null {
  // Matches: "File: some/path/to/file.ext, line ~769" or "File: path, line 42"
  const match = text.match(/(?:^|[\s\n])(?:File[:：]\s*)([^\s,;]+(?:\.[a-zA-Z0-9]+))(?:\s*,\s*)?(?:line[:：]?\s*[~]?)?(\d+)/i);
  if (!match) return null;

  const path = match[1];
  const line = parseInt(match[2], 10);
  if (isNaN(line) || line < 1) return null;

  const fullMatch = match[0];
  const prefixEnd = text.indexOf(fullMatch);
  const suffixStart = prefixEnd + fullMatch.length;

  // Reconstruct display text: "File: path, line N"
  const display = fullMatch.trim();
  return {
    prefix: text.slice(0, prefixEnd),
    path,
    display,
    line,
    suffix: text.slice(suffixStart),
  };
}

function isSafeHTTPURL(value: string): boolean {
  if (!/^https?:\/\//i.test(value)) return false;
  try {
    const parsed = new URL(value);
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch {
    return false;
  }
}

function isSafeRasterDataURL(value: string): boolean {
  return /^data:image\/(?:png|jpeg|webp|gif);base64,[a-z0-9+/=\s]+$/i.test(value);
}

function morphChildren(target: Node, source: Node): void {
  let targetChild = target.firstChild;
  let sourceChild = source.firstChild;
  while (targetChild && sourceChild) {
    const nextTarget = targetChild.nextSibling;
    const nextSource = sourceChild.nextSibling;
    if (
      targetChild.nodeType !== sourceChild.nodeType ||
      targetChild.nodeName !== sourceChild.nodeName
    ) {
      target.replaceChild(sourceChild, targetChild);
    } else if (
      targetChild.nodeType === Node.TEXT_NODE ||
      targetChild.nodeType === Node.COMMENT_NODE
    ) {
      if (targetChild.nodeValue !== sourceChild.nodeValue) {
        targetChild.nodeValue = sourceChild.nodeValue;
      }
    } else if (
      targetChild.nodeType === Node.ELEMENT_NODE &&
      sourceChild.nodeType === Node.ELEMENT_NODE
    ) {
      morphElement(targetChild as Element, sourceChild as Element);
    }
    targetChild = nextTarget;
    sourceChild = nextSource;
  }

  while (targetChild) {
    const nextTarget = targetChild.nextSibling;
    target.removeChild(targetChild);
    targetChild = nextTarget;
  }

  while (sourceChild) {
    const nextSource = sourceChild.nextSibling;
    target.appendChild(sourceChild);
    sourceChild = nextSource;
  }
}

function morphElement(target: Element, source: Element): void {
  for (let index = target.attributes.length - 1; index >= 0; index--) {
    const name = target.attributes[index].name;
    if (!source.hasAttribute(name)) target.removeAttribute(name);
  }

  for (const attribute of Array.from(source.attributes)) {
    if (target.getAttribute(attribute.name) !== attribute.value) {
      target.setAttribute(attribute.name, attribute.value);
    }
  }

  morphChildren(target, source);
}
