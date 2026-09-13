import type { DebugSource } from "./types";

export function debugSourceKey(sessionId: string, source: DebugSource): string {
  return JSON.stringify([sessionId, source.echoSourceId || source.path || source.name || "", source.sourceReference || 0]);
}

// Expand a hover to a field/index chain without including calls or assignments.
// Strings, comments, and more complex expressions can still be selected explicitly.
export function debugHoverExpression(line: string, column: number): string {
  const chain = /[\p{L}_$][\p{L}\p{N}_$]*(?:(?:\s*(?:\.|->)\s*[\p{L}_$][\p{L}\p{N}_$]*)|(?:\s*\[\s*(?:\d+|[\p{L}_$][\p{L}\p{N}_$]*)\s*\]))*/gu;
  const offset = column - 1;
  for (const match of line.matchAll(chain)) {
    if (offset >= match.index && offset < match.index + match[0].length) return match[0].trim();
  }
  return "";
}
