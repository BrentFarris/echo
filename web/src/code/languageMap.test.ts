import { describe, expect, it } from "vitest";
import { languageIdForPath } from "./languageMap";

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
