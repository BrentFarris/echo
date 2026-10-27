import { describe, expect, it } from "vitest";
import { columnGuideRulers, columnGuideSettings, parseGuideColumns } from "./columnGuides";

describe("column guides", () => {
  it("defaults legacy settings to disabled guides at 80 and 90", () => {
    expect(columnGuideSettings({})).toEqual({ editorColumnGuidesEnabled: false, editorColumnGuides: [80, 90] });
    expect(columnGuideRulers({})).toEqual([]);
    expect(columnGuideRulers({ editorColumnGuidesEnabled: true })).toEqual([80, 90]);
  });

  it("parses whole columns, trims spaces, sorts, and deduplicates", () => {
    expect(parseGuideColumns(" 90, 80,90, 120 ")).toEqual([80, 90, 120]);
    expect(parseGuideColumns("1, 10000")).toEqual([1, 10000]);
    for (const text of ["", " ", "80,", ",80", "80,,90", "0", "-1", "2.5", "1e2", "80px", "10001", "80 90", "Infinity"]) {
      expect(parseGuideColumns(text), text).toBeNull();
    }
  });

  it("preserves configured guides while disabled and leaves input arrays untouched", () => {
    const columns = [90, 80, 90];
    const settings = { editorColumnGuidesEnabled: false, editorColumnGuides: columns };
    expect(columnGuideRulers(settings)).toEqual([]);
    expect(columnGuideSettings(settings).editorColumnGuides).toEqual([80, 90]);
    expect(columnGuideRulers({ ...settings, editorColumnGuidesEnabled: true })).toEqual([80, 90]);
    expect(columns).toEqual([90, 80, 90]);
  });

  it("safely defaults missing or malformed stored columns", () => {
    for (const columns of [null, [], "80,90", [80, "90"], [0], [2.5], [10001]]) {
      expect(columnGuideSettings({ editorColumnGuides: columns }).editorColumnGuides).toEqual([80, 90]);
    }
  });
});
