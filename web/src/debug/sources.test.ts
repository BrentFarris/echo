import { expect, it } from "vitest";
import { debugHoverExpression, debugSourceKey } from "./sources";

it.each([
  ["return ptr->items[1];", 14, "ptr->items[1]"],
  ["sample.value + 1", 9, "sample.value"],
  ["items[index]", 8, "items[index]"],
  ["变量.字段", 4, "变量.字段"],
  ["native(value)", 3, "native"],
  ["native(value)", 9, "value"],
  ["x = 42;", 7, ""],
])("extracts a side-effect-free hover chain from %s", (line, column, expected) => {
  expect(debugHoverExpression(line, column)).toBe(expected);
});

it("separates same-named sources across paths, references, and sessions", () => {
  const source = { name: "native.c", path: "/one/native.c" };
  const keys = [
    debugSourceKey("one", source), debugSourceKey("two", source),
    debugSourceKey("one", { ...source, path: "/two/native.c" }),
    debugSourceKey("one", { ...source, sourceReference: 1 }),
    debugSourceKey("one", { ...source, echoSourceId: "adapter-source" }),
  ];
  expect(new Set(keys).size).toBe(keys.length);
  expect(debugSourceKey("one", source)).toBe(keys[0]);
});
