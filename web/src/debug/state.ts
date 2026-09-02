import type { DebugEvent, DebugSession, DebugSnapshot, SourceBreakpoint } from "./types";

export function applyDebugEvent(snapshot: DebugSnapshot, event: DebugEvent): DebugSnapshot | null {
  if (event.workspaceId !== snapshot.workspaceId) return snapshot;
  if (event.sequence !== snapshot.sequence + 1) return null;
  const next: DebugSnapshot = {
    ...snapshot,
    sequence: event.sequence,
    sessions: [...(snapshot.sessions || [])],
    groups: [...(snapshot.groups || [])],
    state: event.state ? { ...event.state } : snapshot.state,
  };
  if (event.session) {
    const index = next.sessions.findIndex((session) => session.id === event.session!.id);
    if (index >= 0) next.sessions[index] = event.session;
    else next.sessions.push(event.session);
  } else if (event.output && event.sessionId) {
    const index = next.sessions.findIndex((session) => session.id === event.sessionId);
    if (index >= 0) {
      const session = next.sessions[index];
      next.sessions[index] = {
        ...session,
        lastOutputSequence: event.output.sequence,
        output: [...(session.output || []), event.output],
      };
    }
  }
  return next;
}

export function activeDebugSession(snapshot: DebugSnapshot, selectedId?: string): DebugSession | undefined {
  const sessions = snapshot.sessions || [];
  const selectable = sessions.filter((session) => session.status !== "terminated" && session.status !== "failed");
  return selectable.find((session) => session.id === selectedId)
    || selectable.find((session) => session.status === "stopped")
    || selectable.at(-1)
    || sessions.at(-1);
}

export function capability(session: DebugSession | undefined, name: string, defaultValue = false): boolean {
  const value = session?.capabilities?.[name];
  return typeof value === "boolean" ? value : defaultValue;
}

const commandCapabilities: Record<string, string> = {
  stepBack: "supportsStepBack",
  reverseContinue: "supportsStepBack",
  restartFrame: "supportsRestartFrame",
  terminateThreads: "supportsTerminateThreadsRequest",
  goto: "supportsGotoTargetsRequest",
  setVariable: "supportsSetVariable",
  setExpression: "supportsSetExpression",
  completions: "supportsCompletionsRequest",
  modules: "supportsModulesRequest",
  loadedSources: "supportsLoadedSourcesRequest",
  readMemory: "supportsReadMemoryRequest",
  writeMemory: "supportsWriteMemoryRequest",
  disassemble: "supportsDisassembleRequest",
  exceptionInfo: "supportsExceptionInfoRequest",
  inlineValues: "supportsInlineValues",
  cancel: "supportsCancelRequest",
  dataBreakpointInfo: "supportsDataBreakpoints",
  setInstructionBreakpoints: "supportsInstructionBreakpoints",
};

export function commandSupported(session: DebugSession | undefined, command: string): boolean {
  const required = commandCapabilities[command];
  return !required || capability(session, required);
}

export function breakpointDecorationClass(breakpoint: SourceBreakpoint, sessions: DebugSession[]): string {
  if (!breakpoint.enabled) return "echo-debug-breakpoint-disabled";
  const statuses = sessions.flatMap((session) => session.breakpoints || []).filter((status) => status.stateId === breakpoint.id);
  if (statuses.some((status) => status.verified && status.line && status.line !== breakpoint.line)) return "echo-debug-breakpoint-relocated";
  if (statuses.length > 0 && statuses.every((status) => !status.verified)) return "echo-debug-breakpoint-unverified";
  if (breakpoint.logMessage) return "echo-debug-logpoint";
  if (breakpoint.condition || breakpoint.hitCondition) return "echo-debug-breakpoint-conditional";
  if (sessions.some((session) => ["starting", "configuring", "running", "stopped"].includes(session.status))) {
    return "echo-debug-breakpoint-unverified";
  }
  return "echo-debug-breakpoint";
}

export type DebugKeyContext = {
  codeActive: boolean;
  modalOpen: boolean;
  inputFocused: boolean;
};

export function debugKeyAction(event: Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey" | "shiftKey" | "altKey">, context: DebugKeyContext): string | null {
  if (!context.codeActive || context.modalOpen || context.inputFocused || event.altKey) return null;
  const modifier = event.ctrlKey || event.metaKey;
  if (modifier && event.shiftKey && event.key === "F8") return "restart";
  if (!modifier && event.shiftKey && event.key === "F8") return "stop";
  if (!modifier && !event.shiftKey && event.key === "F8") return "toggle";
  if (!modifier && !event.shiftKey && event.key === "F9") return "breakpoint";
  if (!modifier && !event.shiftKey && event.key === "F10") return "next";
  if (!modifier && !event.shiftKey && event.key === "F11") return "stepIn";
  if (!modifier && event.shiftKey && event.key === "F11") return "stepOut";
  return null;
}
