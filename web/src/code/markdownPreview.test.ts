import { afterEach, describe, expect, it, vi } from "vitest";
import {
  isMarkdownPath, MARKDOWN_PREVIEW_DELAY_MS, MarkdownPreview, renderMarkdownPreview,
  resolveMarkdownTarget, restoreMarkdownViewState,
} from "./markdownPreview";
import type { MarkdownViewState } from "./types";

const ref = { rootId: "docs", path: "nested/guide.md" };
const mediaURL = (file: typeof ref) => `/media?${new URLSearchParams({ rootId: file.rootId, path: file.path })}`;
const render = (markdown: string) => {
  const host = document.createElement("div");
  host.appendChild(renderMarkdownPreview(markdown, { ref, mediaURL }));
  return host;
};

afterEach(() => { vi.useRealTimers(); document.body.replaceChildren(); });

describe("Markdown file state", () => {
  it.each(["README.md", "nested/file.markdown", "README.MD", "guide.MARKDOWN"])("recognizes %s", path => {
    expect(isMarkdownPath(path)).toBe(true);
  });
  it.each(["README.mdx", "notes.txt", "file.md.png", "markdown"])("excludes %s", path => {
    expect(isMarkdownPath(path)).toBe(false);
  });
  it("restores legacy defaults independently of temporary tab state", () => {
    expect(restoreMarkdownViewState()).toEqual({ markdownViewMode: "source", markdownSplitRatio: 0.5, markdownPreviewScrollTop: 0 });
    expect(restoreMarkdownViewState({ preview: true } as MarkdownViewState).markdownViewMode).toBe("source");
    expect(restoreMarkdownViewState({ markdownViewMode: "preview", markdownSplitRatio: 0.65, markdownPreviewScrollTop: 600 }))
      .toEqual({ markdownViewMode: "preview", markdownSplitRatio: 0.65, markdownPreviewScrollTop: 600 });
  });
  it("normalizes invalid persisted values", () => {
    expect(restoreMarkdownViewState({ markdownViewMode: "bad" as never, markdownSplitRatio: NaN, markdownPreviewScrollTop: Infinity }))
      .toEqual({ markdownViewMode: "source", markdownSplitRatio: 0.5, markdownPreviewScrollTop: 0 });
    expect(restoreMarkdownViewState({ markdownSplitRatio: 0, markdownPreviewScrollTop: -5 }).markdownSplitRatio).toBe(0.2);
    expect(restoreMarkdownViewState({ markdownSplitRatio: 3 }).markdownSplitRatio).toBe(0.8);
  });
});

describe("workspace Markdown URLs", () => {
  it("resolves relative, encoded, and root-relative paths in the same workspace root", () => {
    expect(resolveMarkdownTarget("../assets/my%20image.png?raw=1", ref)).toEqual({ kind: "file", ref: { rootId: "docs", path: "assets/my image.png" }, fragment: "" });
    expect(resolveMarkdownTarget("/README.md#hello-world", ref)).toEqual({ kind: "file", ref: { rootId: "docs", path: "README.md" }, fragment: "hello-world" });
    expect(resolveMarkdownTarget("./next.md", ref)).toEqual({ kind: "file", ref: { rootId: "docs", path: "nested/next.md" }, fragment: "" });
  });
  it("recognizes heading fragments and explicit same-file anchors", () => {
    expect(resolveMarkdownTarget("#hello%20world", ref)).toEqual({ kind: "anchor", fragment: "hello world" });
    expect(resolveMarkdownTarget("guide.md#intro", ref)).toEqual({ kind: "anchor", fragment: "intro" });
    expect(resolveMarkdownTarget("#", ref)).toEqual({ kind: "anchor", fragment: "" });
  });
  it.each(["../../secret.png", "%2e%2e/%2e%2e/secret.md", "//example.com/x", "%2f%2fexample.com/x", "javascript:alert(1)",
    "java\nscript:alert(1)", "data:text/html,hello", "file:///tmp/file.md", "C:/secret.md", "C%3A/secret.md", "..\\secret.md", "bad%00.md", "%zz.md"])("rejects %s", value => {
    expect(resolveMarkdownTarget(value, ref)).toBeNull();
  });
  it("accepts explicit HTTP(S) web links", () => {
    expect(resolveMarkdownTarget("https://example.com/docs?q=1#hello", ref)).toEqual({ kind: "external", url: "https://example.com/docs?q=1#hello" });
  });
});

describe("file Markdown rendering", () => {
  it("renders GFM and safe HTML, including badges and details", () => {
    const host = render('# Heading\r\n\r\n**bold** ~~old~~ `code`\n\n> quote\n\n- [x] done\n- [ ] next\n\n| A | B |\n| - | - |\n| 1 | 2 |\n\n```ts\nconst x = 1;\n```\n\n<details><summary>More</summary><p>Body</p></details>\n\n<p align="center"><img alt="badge" src="https://example.com/badge.svg"></p>');
    expect(host.querySelector("h1")?.textContent).toBe("Heading");
    expect(host.querySelectorAll('input[type="checkbox"]')).toHaveLength(2);
    expect([...host.querySelectorAll("input")].every(input => input.disabled)).toBe(true);
    expect(host.querySelector("strong")?.textContent).toBe("bold");
    expect(host.querySelector("del")?.textContent).toBe("old");
    expect(host.querySelector("table td")?.textContent).toBe("1");
    expect(host.querySelector("pre code")?.textContent).toContain("const x = 1;");
    expect(host.querySelector("details summary")?.textContent).toBe("More");
    expect(host.querySelector('p[align="center"] img')?.getAttribute("src")).toBe("https://example.com/badge.svg");
  });
  it("strips executable markup, custom UI attributes, and unsupported controls", () => {
    const host = render('<script>window.compromised=true</script><style>body{display:none}</style><iframe src="/api/settings"></iframe><form action="/api/settings"><input name="password"></form><img src="javascript:alert(1)" onerror="alert(1)"><a href="javascript:alert(1)" data-markdown-path="secret">bad</a><div id="code-chat-dock" class="code-modal-overlay" style="position:fixed" data-nav="settings">text</div><svg onload="alert(1)"></svg>');
    expect(host.querySelector("script,style,iframe,form,input,svg,[onerror],[onload],[style],[data-nav],[id],[class]")).toBeNull();
    expect(host.querySelector("a")?.hasAttribute("href")).toBe(false);
    expect(host.querySelector("a")?.hasAttribute("data-markdown-path")).toBe(false);
    expect(host.querySelector("img")).toBeNull();
  });
  it("resolves local images and hardens external links", () => {
    const host = render('![local](../assets/image.png)\n![bad](../../secret.png)\n![inline](data:image/png;base64,aGVsbG8=)\n![svg](data:image/svg+xml;base64,PHN2Zz4=)\n\n[web](https://example.com) [local](next.md) [anchor](#intro)');
    expect([...host.querySelectorAll("img")].map(image => image.alt)).toEqual(["local", "inline"]);
    expect(host.querySelector("img")?.getAttribute("src")).toBe(mediaURL({ rootId: "docs", path: "assets/image.png" }));
    expect(host.querySelector('a[target="_blank"]')?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(host.querySelector('[data-markdown-path="nested/next.md"]')).not.toBeNull();
    expect(host.querySelector('[data-markdown-fragment="intro"]')).not.toBeNull();
  });
  it("generates unique heading anchors, including Unicode and repeated headings", () => {
    const host = render("# Hello, *World*!\n# Hello, World!\n# Hello World-1\n# Café 日本語\n# !!!");
    expect([...host.querySelectorAll("h1")].map(heading => heading.dataset.markdownAnchor))
      .toEqual(["hello-world", "hello-world-1", "hello-world-1-1", "café-日本語", "section"]);
    expect(host.querySelector("h1")?.id).toBe("echo-markdown-heading-hello-world");
  });
});

function documentModel(initial: string) {
  let text = initial;
  const listeners = new Set<() => void>();
  return {
    getValue: () => text,
    onDidChangeContent: (listener: () => void) => { listeners.add(listener); return { dispose: () => { listeners.delete(listener); } }; },
    edit: (value: string) => { text = value; listeners.forEach(listener => listener()); },
    listeners,
  };
}

describe("live preview lifecycle", () => {
  it("batches unsaved edits and preserves unchanged images, expanded details, and scroll", () => {
    vi.useFakeTimers();
    const host = document.createElement("section");
    document.body.appendChild(host);
    const model = documentModel('# One\n\n![image](../image.png)\n\n<details><summary>More</summary>Body</details>');
    const preview = new MarkdownPreview(host, { mediaURL, openFile: vi.fn(), changed: vi.fn() });
    preview.show({ model, ref, state: {} });
    const image = host.querySelector("img");
    host.querySelector("details")!.open = true;
    host.scrollTop = 120;
    model.edit(model.getValue().replace("One", "Two"));
    model.edit(model.getValue().replace("Two", "Three"));
    expect(host.querySelector("h1")?.textContent).toBe("One");
    vi.advanceTimersByTime(MARKDOWN_PREVIEW_DELAY_MS);
    expect(host.querySelector("h1")?.textContent).toBe("Three");
    expect(host.querySelector("img")).toBe(image);
    expect(host.querySelector("details")?.open).toBe(true);
    expect(host.scrollTop).toBe(120);
    preview.dispose();
  });
  it("restores scroll per document and cancels stale subscriptions and scheduled renders", () => {
    vi.useFakeTimers();
    const host = document.createElement("section");
    document.body.appendChild(host);
    const first = { model: documentModel("# First"), ref, state: {} as MarkdownViewState };
    const second = { model: documentModel("# Second"), ref: { ...ref, path: "second.md" }, state: { markdownPreviewScrollTop: 30 } };
    const preview = new MarkdownPreview(host, { mediaURL, openFile: vi.fn(), changed: vi.fn() });
    preview.show(first);
    host.scrollTop = 150;
    first.model.edit("# Stale");
    preview.show(second);
    vi.runAllTimers();
    expect(host.querySelector("h1")?.textContent).toBe("Second");
    expect(first.model.listeners.size).toBe(0);
    expect(first.state.markdownPreviewScrollTop).toBe(150);
    expect(host.scrollTop).toBe(30);
    preview.show(first);
    expect(host.scrollTop).toBe(150);
    first.model.edit("# Canceled");
    preview.show(null);
    vi.runAllTimers();
    expect(host.hidden).toBe(true);
    expect(host.textContent).toBe("");
    expect(first.model.listeners.size).toBe(0);
    preview.dispose();
  });
  it("routes local links and anchors without changing the app route", () => {
    const host = document.createElement("section");
    host.tabIndex = 0;
    document.body.appendChild(host);
    const openFile = vi.fn();
    const preview = new MarkdownPreview(host, { mediaURL, openFile, changed: vi.fn() });
    preview.show({ model: documentModel("[next](next.md#intro) [here](#intro)\n\n# Intro"), ref, state: {} });
    host.querySelector<HTMLAnchorElement>("a")!.click();
    expect(openFile).toHaveBeenCalledWith({ rootId: "docs", path: "nested/next.md" }, "intro");
    const hash = location.hash;
    host.querySelectorAll<HTMLAnchorElement>("a")[1].click();
    expect(location.hash).toBe(hash);
    expect(document.activeElement).toBe(host);
    preview.dispose();
  });
  it("rebinds after Save As or rename and reports broken images as text", () => {
    const host = document.createElement("section");
    const model = documentModel("![missing](image.png)");
    const state = {};
    const preview = new MarkdownPreview(host, { mediaURL, openFile: vi.fn(), changed: vi.fn() });
    preview.show({ model, ref, state });
    preview.show({ model, ref: { ...ref, path: "new/location.md" }, state });
    expect(host.querySelector("img")?.getAttribute("src")).toBe(mediaURL({ rootId: "docs", path: "new/image.png" }));
    host.querySelector("img")!.dispatchEvent(new Event("error"));
    expect(host.textContent).toContain("missing");
    expect(host.querySelector("img")).toBeNull();
    preview.dispose();
  });
});
