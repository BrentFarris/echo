import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  cancelMarkdownPatch,
  flushMarkdownPatch,
  MARKDOWN_PATCH_DELAY_MS,
  patchMarkdownElement,
  queueMarkdownPatch,
} from "./markdown";

describe("chat Markdown rendering", () => {
  beforeEach(() => {
    document.body.textContent = "";
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders GFM blocks and inline formatting", () => {
    const target = document.createElement("div");
    document.body.appendChild(target);
    patchMarkdownElement(target, [
      "# Heading",
      "",
      "**bold** *italic* ~~removed~~ and `inline()`",
      "",
      "> quoted",
      "",
      "- outer",
      "  - inner",
      "- [x] complete",
      "- [ ] pending",
      "",
      "| Name | Value |",
      "| --- | ---: |",
      "| Echo | 1 |",
      "",
      "```ts",
      "const answer = 42;",
      "```",
    ].join("\n"));

    expect(target.querySelector("h1")?.textContent).toBe("Heading");
    expect(target.querySelector("strong")?.textContent).toBe("bold");
    expect(target.querySelector("em")?.textContent).toBe("italic");
    expect(target.querySelector("del")?.textContent).toBe("removed");
    expect(target.querySelector("blockquote")?.textContent).toContain("quoted");
    expect(target.querySelector("li li")?.textContent).toBe("inner");
    expect(target.querySelectorAll('input[type="checkbox"]')).toHaveLength(2);
    expect(target.querySelector("table tbody td")?.textContent).toBe("Echo");
    expect(target.querySelector("pre code")?.textContent).toContain("const answer = 42;");
  });

  it("hardens links and supports safe remote and raster images", () => {
    const target = document.createElement("div");
    document.body.appendChild(target);
    patchMarkdownElement(target, [
      "[safe](https://example.com/docs)",
      "[unsafe](javascript:alert(1))",
      "![remote](https://example.com/echo.png)",
      "![inline](data:image/png;base64,aGVsbG8=)",
      "![svg](data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=)",
    ].join("\n\n"));

    const safeLink = Array.from(target.querySelectorAll("a"))
      .find((anchor) => anchor.textContent === "safe");
    const unsafeLink = Array.from(target.querySelectorAll("a"))
      .find((anchor) => anchor.textContent === "unsafe");
    expect(safeLink?.getAttribute("href")).toBe("https://example.com/docs");
    expect(safeLink?.getAttribute("target")).toBe("_blank");
    expect(safeLink?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(unsafeLink?.hasAttribute("href")).toBe(false);

    const images = Array.from(target.querySelectorAll("img"));
    expect(images.map((image) => image.alt)).toEqual(["remote", "inline"]);
    expect(images.every((image) => image.loading === "lazy" && image.decoding === "async")).toBe(true);
    expect(target.textContent).toContain("svg");
  });

  it("escapes raw HTML and strips executable markup", () => {
    const target = document.createElement("div");
    document.body.appendChild(target);
    patchMarkdownElement(
      target,
      '<script>globalThis.compromised = true</script>\n<img src="x" onerror="globalThis.compromised=true">',
    );

    expect(target.querySelector("script")).toBeNull();
    expect(target.querySelector("img")).toBeNull();
    expect(target.querySelector("[onerror]")).toBeNull();
    expect(target.textContent).toContain("<script>");
    expect((globalThis as typeof globalThis & { compromised?: boolean }).compromised).not.toBe(true);
  });

  it("batches streaming patches and supports flush and cancel", () => {
    vi.useFakeTimers();
    const target = document.createElement("div");
    document.body.appendChild(target);
    const afterPatch = vi.fn();

    queueMarkdownPatch(target, "**par", afterPatch);
    queueMarkdownPatch(target, "**parsed**", afterPatch);
    vi.advanceTimersByTime(MARKDOWN_PATCH_DELAY_MS - 1);
    expect(target.textContent).toBe("");
    vi.advanceTimersByTime(1);
    expect(target.querySelector("strong")?.textContent).toBe("parsed");
    expect(afterPatch).toHaveBeenCalledTimes(1);

    queueMarkdownPatch(target, "`flushed`", afterPatch);
    flushMarkdownPatch(target);
    expect(target.querySelector("code")?.textContent).toBe("flushed");
    expect(afterPatch).toHaveBeenCalledTimes(2);

    queueMarkdownPatch(target, "# canceled", afterPatch);
    cancelMarkdownPatch(target);
    vi.runAllTimers();
    expect(target.querySelector("h1")).toBeNull();
  });
});
