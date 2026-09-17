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

function snapshot(workspaceId: string, turns: any[] = [], sequence = 0, surface = "chat"): void {
  emit("session_snapshot", { workspaceId, turns, sequence, surface });
}

function event(workspaceId: string, sequence: number, payload: any, surface = "chat"): void {
  emit("session_event", { workspaceId, sequence, event: payload, surface });
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

describe("final response code copying", () => {
  const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, "clipboard");
  const execCommandDescriptor = Object.getOwnPropertyDescriptor(document, "execCommand");
  let log: HTMLElement;
  let writeText: ReturnType<typeof vi.fn>;

  function storedResponse(content: string, sequence = 1, surface = "chat"): void {
    snapshot("workspace-copy", [{
      id: "turn-copy",
      userContent: "```text\nUser code\n```",
      status: "done",
      assistantTurns: [
        { number: 0, content: "```text\nIntermediate code\n```", hasToolCalls: true },
        { number: 1, content, hasToolCalls: false },
      ],
    }], sequence, surface);
  }

  beforeEach(() => {
    vi.useFakeTimers();
    document.body.replaceChildren();
    writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    log = document.createElement("div");
    document.body.appendChild(log);
    openWorkspaceSession(log, "workspace-copy");
  });

  afterEach(() => {
    closeWorkspaceSession(log);
    vi.runOnlyPendingTimers();
    vi.useRealTimers();
    if (clipboardDescriptor) Object.defineProperty(navigator, "clipboard", clipboardDescriptor);
    else Reflect.deleteProperty(navigator, "clipboard");
    if (execCommandDescriptor) Object.defineProperty(document, "execCommand", execCommandDescriptor);
    else Reflect.deleteProperty(document, "execCommand");
  });

  it.each(["chat", "code"])("copies individual stored blocks verbatim on the %s surface", async (surface) => {
    openWorkspaceSession(log, "workspace-copy", { surface });
    const content = [
      "Prose and `inline()`.", "",
      "```html", "  <div data-name=\"Echo\">\u03bb & \u4f60\u597d</div>", "", "\tkeepIndent();  ", "```", "",
      "```", "second block", "```", "",
      "    indented();", "      nested();",
    ].join("\n");
    storedResponse(content, 1, surface);

    const buttons = log.querySelectorAll<HTMLButtonElement>(".chat-code-copy");
    expect(buttons).toHaveLength(3);
    expect(log.querySelector(".chat-message-user .chat-code-copy")).toBeNull();
    expect(log.querySelector(".chat-progress-text .chat-code-copy")).toBeNull();
    const expected = [
      // Markdown expands a leading tab to four spaces before displaying it.
      '  <div data-name="Echo">\u03bb & \u4f60\u597d</div>\n\n    keepIndent();  \n',
      "second block\n",
      "indented();\n  nested();\n",
    ];
    for (const [index, button] of buttons.entries()) {
      button.click();
      await vi.advanceTimersByTimeAsync(0);
      expect(writeText).toHaveBeenNthCalledWith(index + 1, expected[index]);
    }
    expect(writeText).toHaveBeenCalledTimes(3);

    log.querySelector<HTMLButtonElement>(".chat-message-assistant [data-message-action='copy']")!.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(writeText).toHaveBeenLastCalledWith(content);
  });

  it.each([
    { surface: "chat", status: "done" }, { surface: "chat", status: "stopped" },
    { surface: "code", status: "done" }, { surface: "code", status: "stopped" },
  ])("copies the latest code after a $status response on $surface", async ({ surface, status }) => {
    openWorkspaceSession(log, "workspace-copy", { surface });
    snapshot("workspace-copy", [], 0, surface);
    event("workspace-copy", 1, { type: "turn_started", turnId: "turn-live", message: "Run it" }, surface);
    event("workspace-copy", 2, { type: "assistant_turn_start", turnId: "turn-live", turn: 0 }, surface);
    event("workspace-copy", 3, {
      type: "token", turnId: "turn-live", turn: 0, content: "```js\nfirst();",
    }, surface);
    vi.advanceTimersByTime(MARKDOWN_PATCH_DELAY_MS);
    expect(log.querySelector(".chat-code-copy")).toBeNull();
    event("workspace-copy", 4, {
      type: "token", turnId: "turn-live", turn: 0, content: "\nlast();",
    }, surface);
    let sequence = 5;
    if (status === "done") {
      const end = { type: "assistant_turn_end", turnId: "turn-live", turn: 0, hasToolCalls: false };
      event("workspace-copy", sequence++, end, surface);
      event("workspace-copy", sequence++, end, surface);
    }
    event("workspace-copy", sequence, { type: "turn_finished", turnId: "turn-live", status }, surface);

    expect(log.querySelectorAll(".chat-code-copy")).toHaveLength(1);
    log.querySelector<HTMLButtonElement>(".chat-code-copy")!.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(writeText).toHaveBeenCalledExactlyOnceWith("first();\nlast();\n");
  });

  it.each(["chat", "code"])("copies updated code after an edited response snapshot on %s", async (surface) => {
    openWorkspaceSession(log, "workspace-copy", { surface });
    storedResponse("```\nold code\n```", 1, surface);
    storedResponse("```\nupdated code\n```", 2, surface);
    expect(log.querySelectorAll(".chat-code-copy")).toHaveLength(1);
    log.querySelector<HTMLButtonElement>(".chat-code-copy")!.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(writeText).toHaveBeenCalledExactlyOnceWith("updated code\n");
  });

  it("shows copied feedback and resets 1.6 seconds after the latest copy", async () => {
    storedResponse("```\ncode\n```");
    const button = log.querySelector<HTMLButtonElement>(".chat-code-copy")!;
    button.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(button.getAttribute("aria-label")).toBe("Copied");
    expect(button.classList.contains("is-copied")).toBe(true);
    expect(document.querySelector(".code-toast")?.textContent).toContain("Code copied.");
    await vi.advanceTimersByTimeAsync(800);
    button.click();
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(1599);
    expect(button.title).toBe("Copied");
    await vi.advanceTimersByTimeAsync(1);
    expect(button.title).toBe("Copy code");
    expect(button.getAttribute("aria-label")).toBe("Copy code");
    expect(button.classList.contains("is-copied")).toBe(false);
  });

  it.each(["unavailable", "denied"])("uses the clipboard fallback when clipboard access is %s", async (mode) => {
    if (mode === "unavailable") {
      Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
    } else {
      writeText.mockRejectedValue(new Error("denied"));
    }
    const execCommand = vi.fn(() => {
      expect(document.querySelector("textarea")?.value).toBe("  copy me\n");
      return true;
    });
    Object.defineProperty(document, "execCommand", { configurable: true, value: execCommand });
    storedResponse("```\n  copy me\n```");
    const button = log.querySelector<HTMLButtonElement>(".chat-code-copy")!;
    button.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(execCommand).toHaveBeenCalledExactlyOnceWith("copy");
    expect(button.title).toBe("Copied");
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("reports clipboard failure without showing copied feedback", async () => {
    writeText.mockRejectedValue(new Error("denied"));
    Object.defineProperty(document, "execCommand", { configurable: true, value: vi.fn(() => false) });
    storedResponse("```\ncode\n```");
    const button = log.querySelector<HTMLButtonElement>(".chat-code-copy")!;
    button.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(button.title).toBe("Copy code");
    expect(button.classList.contains("is-copied")).toBe(false);
    expect(document.querySelector(".code-toast")?.textContent).toContain("Clipboard access was denied");
    expect(document.querySelector("textarea")).toBeNull();
  });
});
