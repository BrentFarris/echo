import { describe, expect, it } from "vitest";
import { isPreviewableName, previewKindForPath } from "./preview";

describe("previewKindForPath", () => {
  it.each([
    ["assets/logo.png", "image"],
    ["photos/sunset.JPG", "image"],
    ["docs/diagram.svg", "image"],
    ["renders/scene.webp", "image"],
    ["media/intro.mp4", "video"],
    ["clips/take-01.WEBM", "video"],
    ["archive/movie.m4v", "video"],
  ])("maps %s to %s", (path, expected) => {
    expect(previewKindForPath(path)).toBe(expected);
  });

  it.each([
    ["src/main.go", null],
    ["notes.md", null],
    ["movie.avi", null],
    ["photo.tiff", null],
    ["no-extension", null],
  ])("leaves %s as text-editable", (path) => {
    expect(previewKindForPath(path)).toBeNull();
  });
});

describe("isPreviewableName", () => {
  it("accepts image and video names only", () => {
    expect(isPreviewableName("a.gif")).toBe(true);
    expect(isPreviewableName("b.ogv")).toBe(true);
    expect(isPreviewableName("c.txt")).toBe(false);
  });
});
