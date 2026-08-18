import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const home = readFileSync(resolve(process.cwd(), "js/views/home.js"), "utf8");
const code = readFileSync(resolve(process.cwd(), "src/code/codeView.ts"), "utf8");
const settings = readFileSync(resolve(process.cwd(), "js/views/settings.js"), "utf8");

describe("terminal route mounting", () => {
  it("mounts and detaches the singleton dock on Chat", () => {
    expect(home).toContain('<div data-region="terminal"></div>');
    expect(home).toMatch(/mountTerminalDock\(terminalRegion, activeWorkspace\)/);
    expect(home).toMatch(/detachTerminalDock\(terminalRegion\)/);
  });

  it("mounts and detaches the same dock on Code and Git", () => {
    expect(code).toContain('<div data-region="terminal"></div>');
    expect(code).toMatch(/mountTerminalDock\(this\.root\.querySelector<HTMLElement>\("\[data-region=terminal\]"\), this\.workspace\)/);
    expect(code).toMatch(/detachTerminalDock\(this\.root\.querySelector<HTMLElement>\("\[data-region=terminal\]"\)\)/);
  });

  it("keeps full-page Settings terminal-free while showing its security warning", () => {
    expect(settings).not.toContain('data-region="terminal"');
    expect(settings).toContain("Every authenticated device can execute arbitrary commands");
  });
});
