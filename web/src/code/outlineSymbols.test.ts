import { describe, expect, it } from "vitest";
import type { languages } from "monaco-editor";
import { normalizeOutlineSymbols } from "./outlineSymbols";

const symbol = (name: string, start: number, end = start, children: languages.DocumentSymbol[] = []): languages.DocumentSymbol => ({
  name, detail: `${name}()`, kind: 11, tags: [], children,
  range: { startLineNumber: start, startColumn: 1, endLineNumber: end, endColumn: 20 },
  selectionRange: { startLineNumber: start, startColumn: 5, endLineNumber: start, endColumn: 5 + name.length },
});

describe("Outline symbol normalization", () => {
  it("orders declarations, preserves navigation ranges, and copies nested data", () => {
    const container = symbol("Container", 1, 15, [symbol("later", 10), symbol("first", 3)]);
    const result = normalizeOutlineSymbols([symbol("last", 20), container]);
    expect(result.map((item) => item.name)).toEqual(["Container", "last"]);
    expect(result[0].children.map((item) => item.name)).toEqual(["first", "later"]);
    expect(result[0].children[0].selectionRange.startColumn).toBe(5);
    container.children![1].name = "mutated";
    container.range = { ...container.range, startLineNumber: 99 };
    expect(result[0].range.startLineNumber).toBe(1);
    expect(result[0].children[0].name).toBe("first");
  });

  it("merges duplicate providers without discarding unique child symbols or overloads", () => {
    const result = normalizeOutlineSymbols([symbol("Box", 1, 20, [symbol("a", 4)]), symbol("Box", 1, 20, [symbol("b", 8)]), symbol("overload", 25), symbol("overload", 30)]);
    expect(result.map((item) => item.name)).toEqual(["Box", "overload", "overload"]);
    expect(result[0].children.map((item) => item.name)).toEqual(["a", "b"]);
  });

  it("nests flat symbols only when their named container encloses them", () => {
    const method = { ...symbol("method", 4), containerName: "Box" };
    const result = normalizeOutlineSymbols([method, symbol("Box", 1, 15), { ...symbol("outside", 25), containerName: "Box" }]);
    expect(result.map((item) => item.name)).toEqual(["Box", "outside"]);
    expect(result[0].children[0].name).toBe("method");
  });

  it("ignores invalid ranges and falls back to the declaration range", () => {
    const invalid = symbol("bad", 0);
    const fallback = { ...symbol("valid", 3), selectionRange: undefined } as unknown as languages.DocumentSymbol;
    const result = normalizeOutlineSymbols([invalid, fallback]);
    expect(result).toHaveLength(1);
    expect(result[0].selectionRange).toEqual(result[0].range);
  });

  it("retains grandchildren when a provider reports several flat nesting levels", () => {
    const result = normalizeOutlineSymbols([
      symbol("Outer", 1, 30), { ...symbol("Inner", 3, 20), containerName: "Outer" },
      { ...symbol("method", 5), containerName: "Inner" },
    ]);
    expect(result).toHaveLength(1);
    expect(result[0].children[0].children[0].name).toBe("method");
  });
});
