import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const css = readFileSync(resolve(process.cwd(), "css/app.css"), "utf8");

describe("chat tab visual contracts", () => {
  it("keeps labels slim and truncated", () => {
    expect(css).toMatch(/\.chat-tab-item\s*\{[\s\S]*?min-width:\s*120px;[\s\S]*?max-width:\s*220px;/);
    expect(css).toMatch(/\.chat-tab-label\s*\{[\s\S]*?text-overflow:\s*ellipsis;[\s\S]*?white-space:\s*nowrap;/);
  });

  it("turns the busy animation off for reduced motion", () => {
    expect(css).toMatch(/@media \(prefers-reduced-motion:\s*reduce\)[\s\S]*?\.chat-tab-busy-dot\s*\{\s*animation:\s*none;/);
  });

  it("renders the close glyph and provides explicit overflow controls", () => {
    expect(css).toMatch(/\.chat-tab-close svg\s*\{[\s\S]*?stroke:\s*currentColor;/);
    expect(css).toMatch(/\.chat-tabs-shell\.has-overflow\s*\{[\s\S]*?grid-template-columns:/);
    expect(css).toMatch(/\.chat-tabs-shell\.has-overflow \.chat-tabs-scroll\s*\{\s*display:\s*grid;/);
  });

  it("keeps trajectory controls separate from its scrolling ledger", () => {
    expect(css).toMatch(/\.trajectory-view\s*\{[\s\S]*?display:\s*grid;[\s\S]*?grid-template-rows:\s*auto minmax\(0, 1fr\);[\s\S]*?overflow:\s*hidden;/);
    expect(css).toMatch(/\.trajectory-scroll-region\s*\{[\s\S]*?min-height:\s*0;[\s\S]*?overflow:\s*auto;/);
    const overviewRule = css.match(/\.trajectory-overview\s*\{[\s\S]*?\}/)?.[0] || "";
    expect(overviewRule).not.toContain("position: sticky");
  });
});
