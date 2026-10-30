import { describe, expect, it } from "vitest";
import { diffBlock, replacementEdit, revertBlockText } from "./diffHunks";

describe("diff block edits", () => {
  it("maps Monaco's empty sides to insertion boundaries", () => {
    expect(diffBlock({ originalStartLineNumber: 0, originalEndLineNumber: 0, modifiedStartLineNumber: 1, modifiedEndLineNumber: 2, charChanges: undefined }))
      .toEqual({ original: { start: 1, end: 1 }, modified: { start: 1, end: 3 } });
    expect(diffBlock({ originalStartLineNumber: 4, originalEndLineNumber: 5, modifiedStartLineNumber: 3, modifiedEndLineNumber: 0, charChanges: undefined }))
      .toEqual({ original: { start: 4, end: 6 }, modified: { start: 4, end: 4 } });
  });
  it.each([
    ["first\nlast\n", "FIRST\nLAST\n", { original: { start: 1, end: 2 }, modified: { start: 1, end: 2 } }, "first\nLAST\n"],
    ["one\n", "new\none\n", { original: { start: 1, end: 1 }, modified: { start: 1, end: 2 } }, "one\n"],
    ["a\nb", "a", { original: { start: 2, end: 3 }, modified: { start: 2, end: 2 } }, "a\nb"],
    ["one", "one\n", { original: { start: 2, end: 2 }, modified: { start: 2, end: 3 } }, "one"],
  ] as const)("reverts one block including EOF (%s)", (original, modified, block, expected) => {
    const after = revertBlockText(original, modified, block, "\n");
    expect(after).toBe(expected);
    const edit = replacementEdit(modified, after);
    expect(modified.slice(0, edit.start) + edit.text + modified.slice(edit.end)).toBe(expected);
  });
  it("uses the working editor's CRLF convention", () => {
    expect(revertBlockText("a\nb\n", "A\r\nB\r\n", { original: { start: 1, end: 2 }, modified: { start: 1, end: 2 } }, "\r\n")).toBe("a\r\nB\r\n");
  });
  it.each([
    ["Hello 😀!", "Hello 😃!", { start: 6, end: 8, text: "😃" }],
    ["𐀀!", "𐐀!", { start: 0, end: 2, text: "𐐀" }],
    ["a\r\nb", "a\nb", { start: 1, end: 3, text: "\n" }],
    ["a\nb", "a\r\nb", { start: 1, end: 2, text: "\r\n" }],
  ])("keeps editor replacement boundaries intact (%s)", (before, after, expected) => {
    expect(replacementEdit(before, after)).toEqual(expected);
  });
  it("restores large deleted blocks without exceeding JavaScript argument limits", () => {
    const original = "line\n".repeat(150_000);
    expect(revertBlockText(original, "", { original: { start: 1, end: 150_001 }, modified: { start: 1, end: 1 } }, "\n")).toBe(original);
  });
});
