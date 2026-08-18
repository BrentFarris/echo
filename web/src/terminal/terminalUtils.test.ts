import { describe, expect, it } from "vitest";
import {
  clampTerminalHeight, decodeTerminalBase64, parseTerminalPreferences,
  splitTerminalInput, terminalSequenceAction,
} from "./terminalUtils";

describe("terminal input", () => {
  it("chunks by UTF-8 bytes without splitting a code point", () => {
    const chunks = splitTerminalInput("ab😀cdé", 5);
    expect(chunks).toEqual(["ab", "😀c", "dé"]);
    const encoder = new TextEncoder();
    expect(chunks.every((chunk) => encoder.encode(chunk).byteLength <= 5)).toBe(true);
    expect(chunks.join("")).toBe("ab😀cdé");
  });

  it("decodes raw terminal bytes from base64", () => {
    const encoded = window.btoa("\x1b[32mOK\x1b[0m");
    expect([...decodeTerminalBase64(encoded)]).toEqual([...new TextEncoder().encode("\x1b[32mOK\x1b[0m")]);
  });
});

describe("terminal event sequencing", () => {
  it("deduplicates old events and resyncs gaps", () => {
    expect(terminalSequenceAction(7, 7)).toBe("ignore");
    expect(terminalSequenceAction(7, 6)).toBe("ignore");
    expect(terminalSequenceAction(7, 8)).toBe("apply");
    expect(terminalSequenceAction(7, 9)).toBe("resync");
  });
});

describe("terminal preferences", () => {
  it("restores per-workspace values and clamps heights", () => {
    const restored = parseTerminalPreferences(JSON.stringify({
      alpha: { open: true, maximized: false, height: 9999 },
      beta: { open: false, maximized: true, height: 10 },
    }), 1000);
    expect(restored.alpha).toEqual({ open: true, maximized: false, height: 700 });
    expect(restored.beta).toEqual({ open: true, maximized: true, height: 160 });
    expect(clampTerminalHeight(280, 1000)).toBe(280);
  });

  it("ignores corrupt storage", () => {
    expect(parseTerminalPreferences("not-json", 800)).toEqual({});
    expect(parseTerminalPreferences("[]", 800)).toEqual({});
  });
});
