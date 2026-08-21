import { describe, expect, it, vi } from "vitest";

vi.mock("./language", () => ({
  monaco: {
    Range: class Range {
      constructor(
        public startLineNumber: number, public startColumn: number,
        public endLineNumber: number, public endColumn: number,
      ) {}
    },
  },
}));

import { fromLSPRange, toLSPPosition, toLSPRange } from "./lspClient";

describe("LSP Monaco conversions", () => {
  it("converts Monaco's one-based positions to LSP UTF-16 coordinates", () => {
    expect(toLSPPosition({ lineNumber: 4, column: 9 } as never)).toEqual({ line: 3, character: 8 });
  });

  it("round-trips ranges without an off-by-one error", () => {
    const range = { startLineNumber: 2, startColumn: 3, endLineNumber: 5, endColumn: 8 };
    const lsp = toLSPRange(range);
    expect(lsp).toEqual({ start: { line: 1, character: 2 }, end: { line: 4, character: 7 } });
    expect(fromLSPRange(lsp)).toMatchObject(range);
  });
});
