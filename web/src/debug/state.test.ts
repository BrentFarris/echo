import { describe, expect, it } from "vitest";
import {
  activeDebugSession, applyDebugEvent, breakpointDecorationClass, commandSupported, debugKeyAction,
} from "./state";
import type { DebugSession, DebugSnapshot, SourceBreakpoint } from "./types";

const session = (patch: Partial<DebugSession> = {}): DebugSession => ({
  id: "session-1", workspaceId: "workspace-1", configuration: "Main", adapterProfileId: "delve",
  request: "launch", status: "running", revision: 1, stopGeneration: 0,
  startedAt: "2026-01-01T00:00:00Z", ...patch,
});
const snapshot = (sessions: DebugSession[] = []): DebugSnapshot => ({
  workspaceId: "workspace-1", sequence: 4, sessions, groups: [], state: { revision: 0 },
});

describe("debug event reducer", () => {
  it("applies exactly sequenced workspace events and requests a snapshot on gaps", () => {
    const initial = snapshot([session()]);
    expect(applyDebugEvent(initial, { type: "debug_event", workspaceId: "workspace-1", sequence: 6, event: "thread" })).toBeNull();
    expect(applyDebugEvent(initial, { type: "debug_event", workspaceId: "another", sequence: 99, event: "thread" })).toBe(initial);

    const stopped = session({ status: "stopped", revision: 2, stopGeneration: 1 });
    const next = applyDebugEvent(initial, {
      type: "debug_event", workspaceId: "workspace-1", sessionId: stopped.id,
      sequence: 5, event: "stopped", session: stopped,
    });
    expect(next?.sequence).toBe(5);
    expect(next?.sessions[0]).toEqual(stopped);
    expect(initial.sessions[0].status).toBe("running");
  });

  it("appends replayable output without mutating the prior snapshot", () => {
    const initial = snapshot([session({ output: [] })]);
    const next = applyDebugEvent(initial, {
      type: "debug_event", workspaceId: "workspace-1", sessionId: "session-1", sequence: 5,
      event: "output", output: { sequence: 3, category: "stdout", output: "hello", timestamp: "2026-01-01T00:00:01Z" },
    });
    expect(next?.sessions[0].lastOutputSequence).toBe(3);
    expect(next?.sessions[0].output?.[0].output).toBe("hello");
    expect(initial.sessions[0].output).toEqual([]);
  });
});

describe("debug capability and selection rules", () => {
  it("prefers the selected live session, then a stopped session", () => {
    const running = session({ id: "running" });
    const stopped = session({ id: "stopped", status: "stopped" });
    expect(activeDebugSession(snapshot([running, stopped]), "running")?.id).toBe("running");
    expect(activeDebugSession(snapshot([running, stopped]), "missing")?.id).toBe("stopped");
  });

  it("gates optional DAP commands on negotiated capabilities", () => {
    const current = session({ capabilities: { supportsStepBack: true, supportsReadMemoryRequest: false } });
    expect(commandSupported(current, "stepBack")).toBe(true);
    expect(commandSupported(current, "readMemory")).toBe(false);
    expect(commandSupported(current, "continue")).toBe(true);
  });
});

describe("debug editor state", () => {
  const breakpoint: SourceBreakpoint = {
    id: "bp-1", source: { rootId: "root", path: "main.go" }, line: 10, enabled: true,
  };

  it("distinguishes disabled, unverified, conditional, log, and relocated breakpoints", () => {
    expect(breakpointDecorationClass({ ...breakpoint, enabled: false }, [])).toBe("echo-debug-breakpoint-disabled");
    expect(breakpointDecorationClass(breakpoint, [session()])).toBe("echo-debug-breakpoint-unverified");
    expect(breakpointDecorationClass({ ...breakpoint, condition: "x > 1" }, [])).toBe("echo-debug-breakpoint-conditional");
    expect(breakpointDecorationClass({ ...breakpoint, logMessage: "x={x}" }, [])).toBe("echo-debug-logpoint");
    expect(breakpointDecorationClass(breakpoint, [session({ breakpoints: [{ stateId: "bp-1", kind: "source", verified: true, line: 12 }] })])).toBe("echo-debug-breakpoint-relocated");
  });

  it("uses the F8 keymap only in an unobstructed Echo Code context", () => {
    const clear = { codeActive: true, modalOpen: false, inputFocused: false };
    expect(debugKeyAction({ key: "F8", ctrlKey: false, metaKey: false, shiftKey: false, altKey: false }, clear)).toBe("toggle");
    expect(debugKeyAction({ key: "F8", ctrlKey: false, metaKey: false, shiftKey: true, altKey: false }, clear)).toBe("stop");
    expect(debugKeyAction({ key: "F8", ctrlKey: true, metaKey: false, shiftKey: true, altKey: false }, clear)).toBe("restart");
    expect(debugKeyAction({ key: "F9", ctrlKey: false, metaKey: false, shiftKey: false, altKey: false }, { ...clear, inputFocused: true })).toBeNull();
    expect(debugKeyAction({ key: "d", ctrlKey: true, metaKey: false, shiftKey: true, altKey: false }, clear)).toBeNull();
  });
});
