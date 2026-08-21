import { describe, expect, it } from "vitest";
import { configuredLanguageIdForPath, languageIdForPath, profileMatchesDocument } from "./languageMap";

describe("languageIdForPath", () => {
  it.each([
    ["src/main.go", "go"],
    ["engine/render.cpp", "cpp"],
    ["script.py", "python"],
    ["init.lua", "lua"],
    ["Dockerfile.dev", "dockerfile"],
    ["Makefile", "makefile"],
    ["pipeline.yml", "yaml"],
    ["query.sql", "sql"],
    ["boot.asm", "echo-asm"],
    ["entry.S", "echo-asm"],
    ["cpu.mips", "mips"],
    ["README.unknown", "plaintext"],
  ])("maps %s to %s", (path, expected) => {
    expect(languageIdForPath(path)).toBe(expected);
  });
});

describe("configuredLanguageIdForPath", () => {
  const profiles = [{
    id: "custom", name: "Custom", command: "custom-ls",
    selectors: [{ languageId: "templating", extensions: [".tmpl"], filenames: ["Projectfile"] }],
  }];

  it("lets enabled profile selectors define future languages", () => {
    expect(configuredLanguageIdForPath("views/page.TMPL", profiles)).toBe("templating");
    expect(configuredLanguageIdForPath("nested/projectfile", profiles)).toBe("templating");
  });

  it("falls back to Echo's built-in language mapping", () => {
    expect(configuredLanguageIdForPath("main.go", profiles)).toBe("go");
  });

  it("requires both the language id and configured file match", () => {
    expect(profileMatchesDocument(profiles[0], "templating", "/workspace/page.tmpl")).toBe(true);
    expect(profileMatchesDocument(profiles[0], "templating", "/workspace/page.go")).toBe(false);
    expect(profileMatchesDocument(profiles[0], "go", "/workspace/page.tmpl")).toBe(false);
  });
});
