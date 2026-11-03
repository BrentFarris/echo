import type { DebugSource } from "./types";

export function debugSourceKey(sessionId: string, source: DebugSource): string {
  return JSON.stringify([sessionId, source.echoSourceId || source.path || source.name || "", source.sourceReference || 0]);
}
