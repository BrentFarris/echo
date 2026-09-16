import { describe, expect, it } from "vitest";

import {
  MAX_NOTE_BYTES,
  createNoteRecord,
  normalizeIndex,
  noteBodyBytes,
  noteStorageKey,
  renderMarkdown,
  slugForTitle,
  validateNoteBody,
} from "../../../internal/plugins/builtin/notes/ui/notes/model.js";

describe("built-in Notes model", () => {
  it("creates stable note records and collision-safe virtual filenames", () => {
    const first = createNoteRecord([], "note-1", "2026-09-12T12:00:00.000Z");
    const second = createNoteRecord([first.metadata], "note-2", "2026-09-12T12:01:00.000Z");

    expect(first.metadata.slug).toBe("untitled-note");
    expect(first.content).toBe("# Untitled note\n\n");
    expect(second.metadata.slug).toBe("untitled-note-2");
    expect(slugForTitle("  Résumé / Ideas!  ", [first.metadata])).toBe("resume-ideas");
    expect(noteStorageKey("abc-123")).toBe("note.abc-123");
    expect(() => noteStorageKey("../escape")).toThrow(/identifier/i);
  });

  it("validates the versioned index without mutating corrupt data", () => {
    const note = createNoteRecord([], "note-1", "2026-09-12T12:00:00.000Z").metadata;
    expect(normalizeIndex({ version: 1, notes: [note] })).toMatchObject({ ok: true });
    expect(normalizeIndex({ version: 2, notes: [note] })).toMatchObject({ ok: false });
    expect(normalizeIndex({ version: 1, notes: [note, { ...note }] })).toMatchObject({ ok: false });
    expect(normalizeIndex({ version: 1, notes: [{ ...note, updatedAt: "not-a-date" }] })).toMatchObject({ ok: false });
  });

  it("preflights UTF-8 note bodies below the host storage limit", () => {
    const allowed = "a".repeat(MAX_NOTE_BYTES);
    const oversized = allowed + "b";
    expect(noteBodyBytes("é")).toBe(2);
    expect(validateNoteBody(allowed)).toMatchObject({ ok: true });
    expect(validateNoteBody(oversized)).toMatchObject({ ok: false });
    expect(validateNoteBody(null)).toMatchObject({ ok: false });
  });

  it("renders core Markdown without creating executable or navigable content", () => {
    const root = document.createElement("article");
    renderMarkdown(root, [
      "# Heading",
      "",
      "A **bold** and *quiet* [link](https://example.com).",
      "",
      "> quoted",
      "",
      "- one",
      "- two",
      "",
      "```js",
      "console.log('safe')",
      "```",
      "",
      "<script>window.notesOwned = true</script>",
    ].join("\n"));

    expect(root.querySelector("h1")?.textContent).toBe("Heading");
    expect(root.querySelector("strong")?.textContent).toBe("bold");
    expect(root.querySelector("em")?.textContent).toBe("quiet");
    expect(root.querySelectorAll("li")).toHaveLength(2);
    expect(root.querySelector("code")?.textContent).toContain("console.log");
    expect(root.querySelector("script")).toBeNull();
    expect(root.querySelector("a")).toBeNull();
    expect(root.textContent).toContain("<script>window.notesOwned = true</script>");
  });
});
