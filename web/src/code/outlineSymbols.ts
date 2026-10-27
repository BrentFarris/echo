import type { IRange, languages } from "monaco-editor";
import type { OutlineSymbol } from "./outlineTypes";

function validRange(value: IRange | undefined): value is IRange {
  return !!value && [value.startLineNumber, value.startColumn, value.endLineNumber, value.endColumn]
    .every((part) => Number.isInteger(part) && part > 0)
    && (value.endLineNumber > value.startLineNumber
      || value.endLineNumber === value.startLineNumber && value.endColumn >= value.startColumn);
}

function compare(left: OutlineSymbol, right: OutlineSymbol): number {
  return left.range.startLineNumber - right.range.startLineNumber || left.range.startColumn - right.range.startColumn
    || right.range.endLineNumber - left.range.endLineNumber || right.range.endColumn - left.range.endColumn
    || left.name.localeCompare(right.name);
}

/** Copy provider data: the rendered snapshot must not share mutable provider objects. */
export function normalizeOutlineSymbols(input: readonly languages.DocumentSymbol[]): OutlineSymbol[] {
  const unique = new Map<string, OutlineSymbol>();
  const containers = new Map<OutlineSymbol, string>();
  for (const item of input) {
    if (!item || !validRange(item.range)) continue;
    const range = { ...item.range };
    const key = JSON.stringify([item.name, item.kind, range.startLineNumber, range.startColumn, range.endLineNumber, range.endColumn]);
    const previous = unique.get(key);
    const children = normalizeOutlineSymbols(item.children || []);
    if (previous) {
      previous.children = normalizeOutlineSymbols([...previous.children, ...children] as languages.DocumentSymbol[]);
      if (!previous.detail) previous.detail = item.detail || "";
      continue;
    }
    const symbol: OutlineSymbol = {
      name: String(item.name || ""), detail: String(item.detail || ""), kind: item.kind,
      range, selectionRange: { ...(validRange(item.selectionRange) ? item.selectionRange : range) }, children,
    };
    unique.set(key, symbol);
    if (item.containerName) containers.set(symbol, item.containerName);
  }
  const symbols = [...unique.values()].sort(compare);
  // Flat SymbolInformation may name a container. Attach only to an unambiguous enclosing declaration.
  const nested = new Set<OutlineSymbol>();
  for (const [symbol, name] of [...containers].sort(([left], [right]) => compare(right, left))) {
    const parents = symbols.filter((parent) => parent !== symbol && parent.name === name
      && compare(parent, symbol) < 0
      && (parent.range.endLineNumber > symbol.range.endLineNumber
        || parent.range.endLineNumber === symbol.range.endLineNumber && parent.range.endColumn >= symbol.range.endColumn));
    if (parents.length === 1) {
      parents[0].children = normalizeOutlineSymbols([...parents[0].children, symbol] as languages.DocumentSymbol[]);
      nested.add(symbol);
    }
  }
  return symbols.filter((symbol) => !nested.has(symbol));
}

const icons = ["file", "module", "namespace", "package", "class", "method", "property", "field", "constructor", "enum", "interface", "function", "variable", "constant", "string", "numeric", "boolean", "array", "object", "key", "null", "enum-member", "struct", "event", "operator", "type-parameter"];
export function outlineSymbolIcon(kind: number): string {
  return `symbol-${icons[kind] || "misc"}`;
}
