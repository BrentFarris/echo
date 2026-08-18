import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MARKDOWN_PATCH_DELAY_MS } from "./markdown";

const socket = vi.hoisted(() => {
  const handlers = new Map<string, Set<(message: any) => void>>();
  return {
    handlers,
    on: vi.fn((type: string, handler: (message: any) => void) => {
      if (!handlers.has(type)) handlers.set(type, new Set());
      handlers.get(type)!.add(handler);
      return () => handlers.get(type)?.delete(handler);
    }),
    onState: vi.fn((handler: (state: string) => void) => {
      handler("closed");
      return () => undefined;
    }),
    send: vi.fn(() => true),
  };
});

vi.mock("../js/ws.js", () => ({
  on: socket.on,
  onState: socket.onState,
  send: socket.send,
}));

import { closeWorkspaceSession, openWorkspaceSession } from "../js/chat.js";

function emit(type: string, message: any): void {
  for (const handler of socket.handlers.get(type) ?? []) handler(message);
}

function snapshot(workspaceId: string, turns: any[] = [], sequence = 0): void {
  emit("session_snapshot", { workspaceId, turns, sequence });
}

function event(workspaceId: string, sequence: number, payload: any): void {
  emit("session_event", { workspaceId, sequence, event: payload });
}

describe("chat Markdown integration", () => {
  let log: HTMLElement;

  beforeEach(() => {
    vi.useFakeTimers();
    document.body.textContent = "";
    socket.send.mockClear();
    log = document.createElement("div");
    document.body.appendChild(log);
    openWorkspaceSession(log, "workspace-one");
  });

  afterEach(() => {
    closeWorkspaceSession(log);
    vi.runOnlyPendingTimers();
    vi.useRealTimers();
  });

  it("renders stored user and assistant Markdown from a snapshot", () => {
    snapshot("workspace-one", [{
      id: "turn-stored",
      userContent: "**User prompt**",
      status: "done",
      assistantTurns: [{ number: 0, content: "# Assistant reply", hasToolCalls: false }],
    }], 4);

    expect(log.querySelector(".chat-message-user strong")?.textContent).toBe("User prompt");
    expect(log.querySelector(".chat-message-assistant h1")?.textContent).toBe("Assistant reply");
  });

  it("formats Markdown while tokens stream across syntax boundaries", () => {
    snapshot("workspace-one");
    event("workspace-one", 1, { type: "turn_started", turnId: "turn-live", message: "- **Question**" });
    event("workspace-one", 2, { type: "assistant_turn_start", turnId: "turn-live", turn: 0 });
    event("workspace-one", 3, { type: "token", turnId: "turn-live", turn: 0, content: "**bo" });
    vi.advanceTimersByTime(MARKDOWN_PATCH_DELAY_MS);
    expect(log.querySelector(".chat-progress-text strong")).toBeNull();
    expect(log.querySelector(".chat-progress-text")?.textContent?.trim()).toBe("**bo");

    event("workspace-one", 4, { type: "token", turnId: "turn-live", turn: 0, content: "ld**" });
    vi.advanceTimersByTime(MARKDOWN_PATCH_DELAY_MS);
    expect(log.querySelector(".chat-progress-text strong")?.textContent).toBe("bold");
    expect(log.querySelector(".chat-message-user li strong")?.textContent).toBe("Question");

    event("workspace-one", 5, {
      type: "assistant_turn_end", turnId: "turn-live", turn: 0, hasToolCalls: false,
    });
    expect(log.querySelector(".chat-final-content strong")?.textContent).toBe("bold");
    expect(log.querySelector(".chat-progress-text")).toBeNull();
  });

  it("renders partial fences safely and flushes stopped responses", () => {
    snapshot("workspace-one");
    event("workspace-one", 1, { type: "turn_started", turnId: "turn-stop", message: "Run it" });
    event("workspace-one", 2, { type: "assistant_turn_start", turnId: "turn-stop", turn: 0 });
    event("workspace-one", 3, {
      type: "token", turnId: "turn-stop", turn: 0, content: "```js\nconst value = 1;",
    });
    vi.advanceTimersByTime(MARKDOWN_PATCH_DELAY_MS);
    expect(log.querySelector(".chat-progress-text pre code")?.textContent).toContain("const value = 1;");

    event("workspace-one", 4, {
      type: "token", turnId: "turn-stop", turn: 0, content: "\nconst pending = true;",
    });
    event("workspace-one", 5, {
      type: "turn_finished", turnId: "turn-stop", status: "stopped", error: "",
    });
    expect(log.querySelector(".chat-final-content pre code")?.textContent).toContain("const pending = true;");
    expect(log.querySelector(".chat-stream-status.is-stopped")?.textContent).toBe("Response stopped.");
  });

  it("cancels queued rendering when the workspace changes", () => {
    snapshot("workspace-one");
    event("workspace-one", 1, { type: "turn_started", turnId: "turn-stale", message: "Old" });
    event("workspace-one", 2, { type: "assistant_turn_start", turnId: "turn-stale", turn: 0 });
    event("workspace-one", 3, { type: "token", turnId: "turn-stale", turn: 0, content: "# Stale" });
    const staleProgress = log.querySelector(".chat-progress-text") as HTMLElement;

    const nextLog = document.createElement("div");
    document.body.appendChild(nextLog);
    openWorkspaceSession(nextLog, "workspace-two");
    vi.advanceTimersByTime(MARKDOWN_PATCH_DELAY_MS);

    expect(staleProgress.childElementCount).toBe(0);
    expect(nextLog.querySelector("h1")).toBeNull();
    closeWorkspaceSession(nextLog);
  });
});
