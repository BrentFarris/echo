export const terminalInputChunkSize = 48 * 1024;
const encoder = new TextEncoder();

export type TerminalPreferenceValue = { open: boolean; maximized: boolean; height: number };
export type TerminalSequenceAction = "apply" | "ignore" | "resync";

export function splitTerminalInput(value: string, limit = terminalInputChunkSize): string[] {
  const chunks: string[] = [];
  let chunk = "";
  let bytes = 0;
  for (const character of value) {
    const characterBytes = encoder.encode(character).byteLength;
    if (chunk && bytes + characterBytes > limit) {
      chunks.push(chunk);
      chunk = "";
      bytes = 0;
    }
    chunk += character;
    bytes += characterBytes;
  }
  if (chunk) chunks.push(chunk);
  return chunks;
}

export function clampTerminalHeight(value: number, viewportHeight = window.innerHeight): number {
  const minimum = 160;
  const maximum = Math.max(minimum, Math.floor(viewportHeight * 0.7));
  return Math.min(maximum, Math.max(minimum, Math.round(value)));
}

export function decodeTerminalBase64(value: string): Uint8Array {
  if (!value) return new Uint8Array();
  const binary = window.atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
  return bytes;
}

export function terminalSequenceAction(lastSequence: number, incomingSequence: number): TerminalSequenceAction {
  if (incomingSequence <= lastSequence) return "ignore";
  if (incomingSequence !== lastSequence + 1) return "resync";
  return "apply";
}

export function parseTerminalPreferences(
  raw: string | null,
  viewportHeight = window.innerHeight,
): Record<string, TerminalPreferenceValue> {
  let parsed: unknown;
  try { parsed = JSON.parse(raw || "{}"); } catch { return {}; }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
  const result: Record<string, TerminalPreferenceValue> = {};
  for (const [workspaceId, value] of Object.entries(parsed)) {
    if (!workspaceId || !value || typeof value !== "object" || Array.isArray(value)) continue;
    const candidate = value as { open?: unknown; maximized?: unknown; height?: unknown };
    const maximized = candidate.maximized === true;
    const numericHeight = typeof candidate.height === "number" ? candidate.height : 280;
    result[workspaceId] = {
      open: candidate.open === true || maximized,
      maximized,
      height: clampTerminalHeight(Number.isFinite(numericHeight) ? numericHeight : 280, viewportHeight),
    };
  }
  return result;
}
