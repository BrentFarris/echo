import DOMPurify from "dompurify";
import { marked } from "marked";
import type { FileRef, MarkdownViewState } from "./types";
import { refKey } from "./types";
import { previewKindForPath } from "./preview";

export const MARKDOWN_PREVIEW_DELAY_MS = 50;

export function isMarkdownPath(path: string): boolean {
  return /\.(md|markdown)$/i.test(path);
}

export function restoreMarkdownViewState(state: MarkdownViewState = {}): Required<MarkdownViewState> {
  return {
    markdownViewMode: state.markdownViewMode === "split" || state.markdownViewMode === "preview" ? state.markdownViewMode : "source",
    markdownSplitRatio: Number.isFinite(state.markdownSplitRatio) ? Math.max(0.2, Math.min(0.8, state.markdownSplitRatio!)) : 0.5,
    markdownPreviewScrollTop: Number.isFinite(state.markdownPreviewScrollTop) ? Math.max(0, state.markdownPreviewScrollTop!) : 0,
  };
}

type MarkdownTarget =
  | { kind: "external"; url: string }
  | { kind: "anchor"; fragment: string }
  | { kind: "file"; ref: FileRef; fragment: string };

/** Resolve URLs without allowing browser-origin paths or host filesystem access. */
export function resolveMarkdownTarget(value: string, source: FileRef): MarkdownTarget | null {
  const href = value.trim();
  if (!href || /[\u0000-\u001f\u007f\\]/.test(href)) return null;
  if (/^https?:\/\//i.test(href)) {
    try { return { kind: "external", url: new URL(href).href }; } catch { return null; }
  }
  if (/^[a-z][a-z0-9+.-]*:/i.test(href) || href.startsWith("//")) return null;
  const hash = href.indexOf("#");
  let fragment: string, path: string;
  try {
    fragment = hash < 0 ? "" : decodeURIComponent(href.slice(hash + 1));
    path = decodeURIComponent((hash < 0 ? href : href.slice(0, hash)).split("?")[0]);
  } catch { return null; }
  if (/[\u0000-\u001f\u007f\\]/.test(path) || /^[a-z][a-z0-9+.-]*:/i.test(path) || path.startsWith("//")) return null;
  if (!path) return hash >= 0 ? { kind: "anchor", fragment } : null;
  const parts = path.startsWith("/") ? [] : source.path.split("/").slice(0, -1);
  for (const part of path.split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") {
      if (!parts.length) return null;
      parts.pop();
    } else parts.push(part);
  }
  if (!parts.length) return null;
  const ref = { rootId: source.rootId, path: parts.join("/") };
  return refKey(ref) === refKey(source) && hash >= 0
    ? { kind: "anchor", fragment }
    : { kind: "file", ref, fragment };
}

type RenderContext = { ref: FileRef; mediaURL(ref: FileRef): string };

export function renderMarkdownPreview(markdown: string, context: RenderContext): DocumentFragment {
  const html = marked.parse(markdown.replaceAll("\r\n", "\n"), { async: false, gfm: true, breaks: false });
  // A separate policy from chat: allow document HTML, but only img and a may
  // carry URLs. Their destinations are normalized below before DOM insertion.
  const fragment = DOMPurify.sanitize(html, {
    RETURN_DOM_FRAGMENT: true,
    ALLOWED_TAGS: ["a", "p", "br", "hr", "strong", "b", "em", "i", "del", "s", "blockquote", "pre", "code",
      "ul", "ol", "li", "h1", "h2", "h3", "h4", "h5", "h6", "table", "thead", "tbody", "tfoot", "tr", "th", "td",
      "img", "details", "summary", "div", "span", "kbd", "samp", "sub", "sup", "dl", "dt", "dd", "abbr", "input"],
    ALLOWED_ATTR: ["href", "src", "alt", "title", "width", "height", "align", "colspan", "rowspan", "start", "reversed",
      "type", "checked", "disabled", "open", "lang"],
    ALLOW_DATA_ATTR: false,
    ALLOW_ARIA_ATTR: false,
  });
  const slugs = new Set<string>();
  for (const heading of fragment.querySelectorAll<HTMLElement>("h1,h2,h3,h4,h5,h6")) {
    const base = (heading.textContent || "").trim().toLowerCase().replace(/[^\p{L}\p{N}\p{M}\s_-]/gu, "").replace(/\s/g, "-") || "section";
    let slug = base;
    for (let index = 1; slugs.has(slug); index++) slug = `${base}-${index}`;
    slugs.add(slug);
    heading.id = `echo-markdown-heading-${slug}`;
    heading.dataset.markdownAnchor = slug;
  }
  for (const anchor of fragment.querySelectorAll<HTMLAnchorElement>("a")) {
    const target = resolveMarkdownTarget(anchor.getAttribute("href") || "", context.ref);
    anchor.removeAttribute("href");
    if (target?.kind === "external") {
      anchor.href = target.url;
      anchor.target = "_blank";
      anchor.rel = "noopener noreferrer";
    } else if (target) {
      anchor.href = `#${encodeURIComponent(target.fragment)}`;
      anchor.dataset.markdownFragment = target.fragment;
      if (target.kind === "file") anchor.dataset.markdownPath = target.ref.path;
    }
  }
  for (const image of fragment.querySelectorAll<HTMLImageElement>("img")) {
    const src = image.getAttribute("src") || "";
    const target = resolveMarkdownTarget(src, context.ref);
    if (target?.kind === "external") image.src = target.url;
    else if (target?.kind === "file" && previewKindForPath(target.ref.path) === "image") image.src = context.mediaURL(target.ref);
    else if (!/^data:image\/(?:png|jpeg|webp|gif);base64,[a-z0-9+/=\s]+$/i.test(src)) {
      image.replaceWith(document.createTextNode(image.alt));
      continue;
    }
    image.loading = "lazy";
    image.decoding = "async";
    image.referrerPolicy = "no-referrer";
  }
  for (const input of fragment.querySelectorAll<HTMLInputElement>("input")) {
    if (input.type === "checkbox") input.disabled = true;
    else input.remove();
  }
  return fragment;
}

type PreviewModel = { getValue(): string; onDidChangeContent(listener: () => void): { dispose(): void } };
type PreviewDocument = { model: PreviewModel; ref: FileRef; state: MarkdownViewState };

export class MarkdownPreview {
  private readonly abort = new AbortController();
  private current: PreviewDocument | null = null;
  private subscription: { dispose(): void } | null = null;
  private timer = 0;
  private key = "";
  private readonly content: HTMLElement;

  constructor(private readonly host: HTMLElement, private readonly options: {
    mediaURL(ref: FileRef): string;
    openFile(ref: FileRef, fragment: string): void;
    changed(): void;
  }) {
    this.content = document.createElement("div");
    this.content.className = "code-markdown-content";
    host.appendChild(this.content);
    const signal = this.abort.signal;
    host.addEventListener("scroll", () => { this.captureScroll(); options.changed(); }, { signal });
    const followLink = (event: MouseEvent) => {
      if (event.button > 1) return;
      const anchor = (event.target as Element | null)?.closest<HTMLAnchorElement>("a[data-markdown-fragment]");
      if (!anchor || !this.current) return;
      event.preventDefault();
      event.stopPropagation();
      const fragment = anchor.dataset.markdownFragment || "";
      const path = anchor.dataset.markdownPath;
      if (path) options.openFile({ rootId: this.current.ref.rootId, path }, fragment);
      else this.scrollToAnchor(fragment);
    };
    host.addEventListener("click", followLink, { signal });
    host.addEventListener("auxclick", followLink, { signal });
    host.addEventListener("error", event => {
      const image = event.target;
      if (image instanceof HTMLImageElement) image.replaceWith(document.createTextNode(image.alt || "Image unavailable"));
    }, { signal, capture: true });
  }

  show(next: PreviewDocument | null): void {
    const key = next ? refKey(next.ref) : "";
    if (this.current?.state === next?.state && this.current?.model === next?.model && this.key === key) return;
    this.captureScroll();
    this.subscription?.dispose();
    this.subscription = null;
    window.clearTimeout(this.timer);
    this.timer = 0;
    this.current = next;
    this.key = key;
    this.content.replaceChildren();
    this.host.hidden = !next;
    if (!next) return;
    this.render();
    this.host.scrollTop = restoreMarkdownViewState(next.state).markdownPreviewScrollTop;
    this.subscription = next.model.onDidChangeContent(() => {
      if (this.timer) return;
      this.timer = window.setTimeout(() => { this.timer = 0; this.render(); }, MARKDOWN_PREVIEW_DELAY_MS);
    });
  }

  captureScroll(): void {
    if (this.current && !this.host.hidden) this.current.state.markdownPreviewScrollTop = this.host.scrollTop;
  }

  scrollToAnchor(fragment: string): void {
    const heading = [...this.content.querySelectorAll<HTMLElement>("[data-markdown-anchor]")]
      .find(element => element.dataset.markdownAnchor === fragment);
    if (!fragment) this.host.scrollTop = 0;
    else if (heading) this.host.scrollTop += heading.getBoundingClientRect().top - this.host.getBoundingClientRect().top - 20;
    this.host.focus({ preventScroll: true });
    this.captureScroll();
    this.options.changed();
  }

  private render(): void {
    if (!this.current) return;
    const scrollTop = this.host.scrollTop;
    try {
      patchChildren(this.content, renderMarkdownPreview(this.current.model.getValue(), {
        ref: this.current.ref, mediaURL: this.options.mediaURL,
      }));
    } catch {
      this.content.textContent = "Unable to render this Markdown. You can continue editing in Source.";
    }
    this.host.scrollTop = scrollTop;
  }

  dispose(): void {
    this.show(null);
    this.abort.abort();
  }
}

// Keep unchanged images and text nodes mounted while typing. Unlike replacing
// innerHTML this also preserves a reader's expanded details and text selection.
function patchChildren(target: Node, source: Node): void {
  let old = target.firstChild;
  for (const next of [...source.childNodes]) {
    if (!old) { target.appendChild(next); continue; }
    const following = old.nextSibling;
    if (old.nodeType !== next.nodeType || old.nodeName !== next.nodeName) target.replaceChild(next, old);
    else if (old instanceof Element && next instanceof Element) {
      for (const attribute of [...old.attributes]) {
        if (old instanceof HTMLDetailsElement && attribute.name === "open") continue;
        if (!next.hasAttribute(attribute.name)) old.removeAttribute(attribute.name);
      }
      for (const attribute of [...next.attributes]) {
        if (old.getAttribute(attribute.name) !== attribute.value) old.setAttribute(attribute.name, attribute.value);
      }
      patchChildren(old, next);
    } else if (old.nodeValue !== next.nodeValue) old.nodeValue = next.nodeValue;
    old = following;
  }
  while (old) { const following = old.nextSibling; target.removeChild(old); old = following; }
}
