import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { explorerDiagnosticPresentation, updateExplorerDiagnostic } from "./explorerDiagnostics";

describe("explorer diagnostic decorations", () => {
  it("maps URI diagnostics to stable explorer keys and clears them", () => {
    const diagnostics = new Map();
    const resolveURI = (uri: string) => uri.startsWith("file:///workspace/")
      ? { rootId: "root", path: uri.slice("file:///workspace/".length) }
      : null;

    expect(updateExplorerDiagnostic(diagnostics, "file:///workspace/src/main.go", "warning", resolveURI)).toBe(true);
    expect(diagnostics.get("root:src/main.go")).toBe("warning");

    // Re-rendering or rebuilding the virtualized tree uses the same stable node key.
    expect(diagnostics.get("root:src/main.go")).toBe("warning");
    expect(updateExplorerDiagnostic(diagnostics, "file:///workspace/src/main.go", null, resolveURI)).toBe(true);
    expect(diagnostics.size).toBe(0);
    expect(updateExplorerDiagnostic(diagnostics, "https://example.com/main.go", "error", resolveURI)).toBe(false);
  });

  it("provides visual and accessible presentations for supported severities", () => {
    expect(explorerDiagnosticPresentation("error")).toEqual({ className: "has-diagnostic-error", description: "has errors" });
    expect(explorerDiagnosticPresentation("warning")).toEqual({ className: "has-diagnostic-warning", description: "has warnings" });
    expect(explorerDiagnosticPresentation(undefined)).toEqual({ className: "", description: "" });
  });

  it("keeps diagnostic text readable on normal and selected rows in both themes", () => {
    const css = readFileSync(resolve(process.cwd(), "src/code/code.css"), "utf8");
    const light = css.match(/^:root\s*\{([^}]*)\}/m)?.[1] || "";
    const dark = css.match(/@media \(prefers-color-scheme: dark\)\s*\{\s*:root\s*\{([^}]*)\}/m)?.[1] || "";
    const themes = [
      { block: light, colors: { error: "#b42332", warning: "#875a00" } },
      { block: dark, colors: { error: "#ff7b8b", warning: "#d29922" } },
    ];

    for (const theme of themes) {
      expect(cssProperty(theme.block, "--code-diagnostic-error")).toBe(theme.colors.error);
      expect(cssProperty(theme.block, "--code-diagnostic-warning")).toBe(theme.colors.warning);
      const backgrounds = [cssProperty(theme.block, "--code-sidebar-bg"), cssProperty(theme.block, "--code-selected")];
      for (const foreground of Object.values(theme.colors)) {
        for (const background of backgrounds) expect(contrastRatio(foreground, background)).toBeGreaterThanOrEqual(4.5);
      }
    }
  });
});

function cssProperty(block: string, property: string): string {
  return new RegExp(`${property}:\\s*([^;]+);`).exec(block)?.[1].trim().toLowerCase() || "";
}

function contrastRatio(first: string, second: string): number {
  const firstLuminance = relativeLuminance(first);
  const secondLuminance = relativeLuminance(second);
  return (Math.max(firstLuminance, secondLuminance) + 0.05) / (Math.min(firstLuminance, secondLuminance) + 0.05);
}

function relativeLuminance(color: string): number {
  const channels = color.slice(1).match(/.{2}/g)?.map((channel) => parseInt(channel, 16) / 255) || [];
  const [red = 0, green = 0, blue = 0] = channels.map((channel) => (
    channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4
  ));
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
}
