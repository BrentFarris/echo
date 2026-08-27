import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const socket = vi.hoisted(() => {
  const handlers = new Map<string, Set<(message: any) => void>>();
  return {
    handlers,
    on: vi.fn((type: string, handler: (message: any) => void) => {
      if (!handlers.has(type)) handlers.set(type, new Set());
      handlers.get(type)!.add(handler);
      return () => handlers.get(type)?.delete(handler);
    }),
    onState: vi.fn(() => () => undefined),
    send: vi.fn(() => true),
  };
});

vi.mock("../js/ws.js", () => ({ on: socket.on, onState: socket.onState, send: socket.send }));

import {
  activateChatTab, canClearChat, canCompressChat, clearChat, compressChat, closeChatTab, closeWorkspaceSession,
  createChatTab, getChatWorkspaceState, onChatWorkspaceChange, openWorkspaceSession,
  sendMessage, stopStream,
} from "../js/chat.js";

function emit(type: string, message: any) {
  for (const handler of socket.handlers.get(type) ?? []) handler(message);
}

describe("multi-chat WebSocket protocol", () => {
  let log: HTMLElement;

  beforeEach(() => {
    socket.send.mockClear();
    log = document.createElement("div");
    document.body.append(log);
    openWorkspaceSession(log, "workspace-tabs");
    socket.send.mockClear();
  });

  afterEach(() => {
    closeWorkspaceSession(log);
    document.body.innerHTML = "";
  });

  it("routes commands to the active or explicit chat", () => {
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 7,
      activeChatId: "chat-two",
      tabs: [
        { chatId: "chat-one", preview: "First", busy: false },
        { chatId: "chat-two", preview: "Second", busy: false },
      ],
      turns: [{
        id: "stored", userContent: "Question", status: "done",
        assistantTurns: [{ number: 0, content: "Answer", hasToolCalls: false }],
      }],
    });

    expect(sendMessage(log, "next question", "model-a", "general")).toBe(true);
    expect(socket.send).toHaveBeenLastCalledWith(expect.objectContaining({
      type: "chat_send", workspaceId: "workspace-tabs", chatId: "chat-two",
      message: "next question", model: "model-a", agentModeId: "general",
    }));
    expect(canClearChat(log)).toBe(true);
    expect(canCompressChat(log)).toBe(true);
    expect(compressChat(log, "model-a")).toBe(true);
    expect(socket.send).toHaveBeenLastCalledWith({
      type: "chat_compress", workspaceId: "workspace-tabs", chatId: "chat-two", model: "model-a",
    });
    expect(clearChat(log)).toBe(true);
    expect(socket.send).toHaveBeenLastCalledWith({
      type: "chat_clear", workspaceId: "workspace-tabs", chatId: "chat-two",
    });

    expect(createChatTab()).toBe(true);
    expect(activateChatTab("chat-one")).toBe(true);
    expect(closeChatTab("chat-one", true)).toBe(true);
    expect(socket.send).toHaveBeenLastCalledWith({
      type: "chat_tab_close", workspaceId: "workspace-tabs", chatId: "chat-one", stopIfBusy: true,
    });
  });

  it("tracks an inactive stream without replacing the active transcript", () => {
    const states: any[] = [];
    const unsubscribe = onChatWorkspaceChange((state: any) => states.push(state));
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 2,
      activeChatId: "chat-one",
      tabs: [
        { chatId: "chat-one", preview: "Visible", busy: false },
        { chatId: "chat-two", preview: "New chat", busy: false },
      ],
      turns: [],
    });
    const emptyText = log.textContent;

    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-two", sequence: 3,
      event: { type: "turn_started", turnId: "inactive-turn", message: "  background\n task  " },
    });
    expect(log.textContent).toBe(emptyText);
    expect(getChatWorkspaceState()?.tabs[1]).toMatchObject({
      chatId: "chat-two", preview: "background task", busy: true,
    });

    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-two", sequence: 4,
      event: { type: "turn_finished", turnId: "inactive-turn", status: "done" },
    });
    expect(states.at(-1)?.tabs[1].busy).toBe(false);
    unsubscribe();
  });

  it("does not rebuild tab state for streamed content events", () => {
    const listener = vi.fn();
    const unsubscribe = onChatWorkspaceChange(listener);
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 1,
      activeChatId: "chat-one",
      tabs: [
        { chatId: "chat-one", preview: "Running", busy: false },
        { chatId: "chat-two", preview: "Older chat", busy: false },
      ],
      turns: [],
    });
    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: 2,
      event: { type: "turn_started", turnId: "live", message: "Running" },
    });
    const notificationsAfterStart = listener.mock.calls.length;

    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: 3,
      event: { type: "assistant_turn_start", turnId: "live", turn: 0 },
    });
    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: 4,
      event: { type: "token", turnId: "live", turn: 0, content: "Still streaming" },
    });
    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: 5,
      event: { type: "reasoning", turnId: "live", turn: 0, content: "Working" },
    });

    expect(listener).toHaveBeenCalledTimes(notificationsAfterStart);

    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: 6,
      event: { type: "turn_finished", turnId: "live", status: "done" },
    });
    expect(listener).toHaveBeenCalledTimes(notificationsAfterStart + 1);
    unsubscribe();
  });

  it("preserves manual transcript scrolling while messages stream", async () => {
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 1,
      activeChatId: "chat-one", tabs: [{ chatId: "chat-one", preview: "Main", busy: false }],
      turns: [{
        id: "stored", userContent: "Earlier question", status: "done",
        assistantTurns: [{ number: 0, content: "Earlier answer", hasToolCalls: false }],
      }],
    });
    log.scrollTop = 37;

    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: 2,
      event: { type: "turn_started", turnId: "live", message: "New question" },
    });
    expect(log.scrollTop).toBe(37);

    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: 3,
      event: { type: "assistant_turn_start", turnId: "live", turn: 0 },
    });
    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: 4,
      event: { type: "token", turnId: "live", turn: 0, content: "Streaming answer" },
    });
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    expect(log.scrollTop).toBe(37);
  });

  it("rewinds selected and later messages before rendering a rerun", () => {
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 10,
      activeChatId: "chat-one", tabs: [{ chatId: "chat-one", preview: "Second", busy: false }],
      turns: [
        {
          id: "turn-first", userContent: "First prompt", status: "done",
          assistantTurns: [{ number: 0, content: "First answer", hasToolCalls: false }],
        },
        {
          id: "turn-second", userContent: "Second prompt", status: "done",
          assistantTurns: [{ number: 0, content: "Second answer", hasToolCalls: false }],
        },
      ],
    });

    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: 11,
      event: {
        type: "turn_rerun_started", fromTurnId: "turn-first", turnId: "turn-replacement",
        message: "First prompt", images: [], videos: [],
      },
    });

    expect(log.querySelector("[data-turn-id='turn-first']")).toBeNull();
    expect(log.querySelector("[data-turn-id='turn-second']")).toBeNull();
    expect(log.querySelectorAll("[data-turn-id='turn-replacement']")).toHaveLength(2);
    expect(log.querySelector(".chat-message-user[data-turn-id='turn-replacement']")?.textContent).toContain("First prompt");
    expect(getChatWorkspaceState()?.tabs[0]).toMatchObject({ preview: "First prompt", busy: true });
  });

  it("rewinds selected and later messages before rendering a user edit", () => {
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 20,
      activeChatId: "chat-one", tabs: [{ chatId: "chat-one", preview: "Old", busy: false }],
      turns: [{
        id: "turn-old", userContent: "Old prompt", status: "done",
        assistantTurns: [{ number: 0, content: "Old answer", hasToolCalls: false }],
      }],
    });
    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: 21,
      event: {
        type: "turn_edit_started", fromTurnId: "turn-old", turnId: "turn-edited",
        message: "Edited prompt", images: [], videos: [],
      },
    });

    expect(log.querySelector("[data-turn-id='turn-old']")).toBeNull();
    expect(log.querySelector(".chat-message-user[data-turn-id='turn-edited']")?.textContent).toContain("Edited prompt");
    expect(getChatWorkspaceState()?.tabs[0]).toMatchObject({ preview: "Edited prompt", busy: true });
  });

  it("stops only the active stream", () => {
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 0,
      activeChatId: "chat-one",
      tabs: [{ chatId: "chat-one", preview: "Running", busy: true }],
      turns: [],
      activeTurn: { id: "turn-one", userContent: "Run", status: "streaming", assistantTurns: [] },
    });
    stopStream();
    expect(socket.send).toHaveBeenLastCalledWith({
      type: "chat_stop", workspaceId: "workspace-tabs", chatId: "chat-one",
    });
  });

  it("sends attachment-only media and renders it from snapshots and live events", () => {
    const storedImage = {
      id: "image-one", name: "diagram.png", mediaType: "image/png", bytes: 12,
      dataUrl: "data:image/png;base64,c3RvcmVk",
    };
    const storedVideo = {
      id: "video-one", name: "demo.webm", mediaType: "video/webm", bytes: 34,
      dataUrl: "data:video/webm;base64,c3RvcmVk",
    };
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 7,
      activeChatId: "chat-one", tabs: [{ chatId: "chat-one", preview: "Media", busy: false }],
      turns: [{
        id: "stored-media", userContent: "Review these.", status: "done",
        images: [storedImage], videos: [storedVideo], assistantTurns: [],
      }],
    });

    const restoredImage = log.querySelector<HTMLImageElement>(".chat-message-media img")!;
    const restoredVideo = log.querySelector<HTMLVideoElement>(".chat-message-media video")!;
    expect(restoredImage.alt).toBe("diagram.png");
    expect(restoredImage.src).toBe(storedImage.dataUrl);
    expect(restoredVideo.controls).toBe(true);
    expect(restoredVideo.preload).toBe("metadata");
    expect(restoredVideo.src).toBe(storedVideo.dataUrl);

    expect(sendMessage(log, "", undefined, "general", { images: [storedImage], videos: [storedVideo] })).toBe(true);
    expect(socket.send).toHaveBeenLastCalledWith(expect.objectContaining({
      type: "chat_send", workspaceId: "workspace-tabs", chatId: "chat-one", message: "",
      images: [storedImage], videos: [storedVideo],
    }));

    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: 8,
      event: {
        type: "turn_started", turnId: "live-media", message: "Please review the attached image(s).",
        images: [{ ...storedImage, id: "live-image" }], videos: [],
      },
    });
    expect(log.querySelectorAll(".chat-message-media img")).toHaveLength(2);
    expect(log.textContent).toContain("Please review the attached image(s).");
  });

  it("restores, collapses, and activates completed file changes", () => {
    const activateFile = vi.fn();
    closeWorkspaceSession(log);
    openWorkspaceSession(log, "workspace-tabs", { onActivateFile: activateFile });
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 1,
      activeChatId: "chat-one", tabs: [{ chatId: "chat-one", preview: "Changed files", busy: false }],
      turns: [{
        id: "changed", userContent: "Make changes", status: "done",
        assistantTurns: [{ number: 0, content: "Done.", hasToolCalls: false }],
        fileChanges: [
          { path: "echo/src/new.ts", operation: "created", ref: { rootId: "root", path: "src/new.ts" } },
          { path: "echo/src/new.ts", operation: "edited", ref: { rootId: "root", path: "src/new.ts" } },
          { path: "echo/src/old.ts", operation: "edited", ref: { rootId: "root", path: "src/old.ts" } },
          { path: "echo/src/old.ts", operation: "deleted" },
          { path: "echo/src/restored.ts", operation: "deleted" },
          { path: "echo/src/restored.ts", operation: "created", ref: { rootId: "root", path: "src/restored.ts" } },
        ],
      }],
    });

    const summary = log.querySelector<HTMLElement>(".chat-file-changes")!;
    expect(summary.querySelector(".chat-file-changes-header")?.textContent).toContain("3 files");
    const rows = [...summary.querySelectorAll<HTMLElement>(".chat-file-change-row")];
    expect(rows.map((row) => row.querySelector("code")?.textContent)).toEqual([
      "echo/src/new.ts", "echo/src/old.ts", "echo/src/restored.ts",
    ]);
    expect(rows.map((row) => row.querySelector(".chat-file-change-operation")?.textContent)).toEqual([
      "Created", "Deleted", "Edited",
    ]);
    expect(rows[1].tagName).toBe("DIV");
    expect(rows[1].getAttribute("aria-disabled")).toBe("true");

    (rows[0] as HTMLButtonElement).click();
    (rows[2] as HTMLButtonElement).click();
    expect(activateFile).toHaveBeenNthCalledWith(1, { rootId: "root", path: "src/new.ts" });
    expect(activateFile).toHaveBeenNthCalledWith(2, { rootId: "root", path: "src/restored.ts" });
  });

  it("shows live file changes after a stopped run", () => {
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 1,
      activeChatId: "chat-one", tabs: [{ chatId: "chat-one", preview: "Stopped", busy: false }], turns: [],
    });
    const events = [
      { type: "turn_started", turnId: "stopped", message: "Start" },
      { type: "assistant_turn_start", turnId: "stopped", turn: 0 },
      { type: "assistant_turn_end", turnId: "stopped", turn: 0, hasToolCalls: true },
      { type: "tool_call", turnId: "stopped", turn: 0, callId: "call-edit", callOrder: 0, tool: "filesystem_edit_text", arguments: "{}" },
      {
        type: "tool_result", turnId: "stopped", turn: 0, callId: "call-edit", callOrder: 0,
        tool: "filesystem_edit_text", success: true, content: "{}",
        fileChanges: [{ path: "echo/main.go", operation: "edited", ref: { rootId: "root", path: "main.go" } }],
      },
      { type: "assistant_turn_start", turnId: "stopped", turn: 1 },
      { type: "token", turnId: "stopped", turn: 1, content: "Partial response" },
      { type: "turn_finished", turnId: "stopped", status: "stopped" },
    ];
    events.forEach((event, index) => emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: index + 2, event,
    }));

    expect(log.querySelector(".chat-stream-status")?.textContent).toContain("stopped");
    expect(log.querySelector(".chat-file-change-path")?.textContent).toBe("echo/main.go");
    expect(log.querySelector(".chat-file-change-operation")?.textContent).toBe("Edited");
  });

  it("restores and updates metrics-only context compression activity", () => {
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 1,
      activeChatId: "chat-one", tabs: [{ chatId: "chat-one", preview: "Compress", busy: true }],
      turns: [{
        id: "compressed", userContent: "Continue", status: "done",
        assistantTurns: [{ number: 0, content: "Working", hasToolCalls: false }],
        compressions: [{
          id: "compression-1", trigger: "manual", phase: "idle", status: "running",
          thresholdPercent: 70, usageSource: "estimated", startedAt: "2026-08-19T12:00:00Z",
        }],
      }],
    });

    const item = log.querySelector<HTMLElement>(".chat-compression-item")!;
    expect(item.textContent).toContain("Compressing context");
    expect(item.textContent).not.toContain("secret summary");

    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: 2,
      event: {
        type: "context_compression_completed", turnId: "compressed",
        compression: {
          id: "compression-1", trigger: "manual", phase: "idle", status: "completed",
          beforeTokens: 7000, afterTokens: 2800, reclaimedTokens: 4200, durationMs: 1250,
          usageSource: "provider", recoveryAvailable: true, summary: "secret summary",
        },
      },
    });

    expect(item.classList).toContain("is-completed");
    expect(item.textContent).toContain("7,000 → 2,800 tokens");
    expect(item.textContent).toContain("60% reclaimed");
    expect(item.textContent).toContain("Compacted raw history remains searchable");
    expect(item.textContent).not.toContain("secret summary");
    expect(getChatWorkspaceState()?.tabs[0].busy).toBe(false);
  });

  it("keeps rendering tool execution after a queued manual compression checkpoint", () => {
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 1,
      activeChatId: "chat-one", tabs: [{ chatId: "chat-one", preview: "Queued", busy: false }], turns: [],
    });
    const events = [
      { type: "turn_started", turnId: "queued-compression", message: "Keep working" },
      { type: "assistant_turn_start", turnId: "queued-compression", turn: 0 },
      { type: "assistant_turn_end", turnId: "queued-compression", turn: 0, hasToolCalls: true },
      { type: "tool_call", turnId: "queued-compression", turn: 0, callId: "call-1", callOrder: 0, tool: "read_file", arguments: "{}" },
      {
        type: "context_compression_queued", turnId: "queued-compression",
        compression: { id: "compression-queued", trigger: "manual", phase: "mid_turn", status: "queued", afterAssistantNumber: 0 },
      },
      {
        type: "context_compression_started", turnId: "queued-compression",
        compression: { id: "compression-queued", trigger: "manual", phase: "mid_turn", status: "running", afterAssistantNumber: 0 },
      },
      {
        type: "context_compression_completed", turnId: "queued-compression",
        compression: {
          id: "compression-queued", trigger: "manual", phase: "mid_turn", status: "completed", afterAssistantNumber: 0,
          beforeTokens: 6000, afterTokens: 2400, durationMs: 500, recoveryAvailable: true,
        },
      },
      { type: "tool_result", turnId: "queued-compression", turn: 0, callId: "call-1", callOrder: 0, tool: "read_file", success: true, content: "ok" },
      { type: "assistant_turn_start", turnId: "queued-compression", turn: 1 },
      { type: "token", turnId: "queued-compression", turn: 1, content: "Finished after compression." },
      { type: "assistant_turn_end", turnId: "queued-compression", turn: 1, hasToolCalls: false },
      { type: "turn_finished", turnId: "queued-compression", status: "done" },
    ];
    events.forEach((event, index) => emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-one", sequence: index + 2, event,
    }));

    expect(log.querySelector(".chat-compression-item")?.textContent).toContain("Context compressed");
    expect(log.querySelector(".chat-tool-item")?.classList).toContain("is-success");
    expect(log.querySelector(".chat-final-content")?.textContent).toContain("Finished after compression.");
  });

  it("isolates the code surface and sends editor context", () => {
    closeWorkspaceSession(log);
    openWorkspaceSession(log, "workspace-tabs", { surface: "code" });
    expect(socket.send).toHaveBeenLastCalledWith({
      type: "session_subscribe", workspaceId: "workspace-tabs", surface: "code",
    });
    socket.send.mockClear();

    emit("session_snapshot", {
      type: "session_snapshot", surface: "chat", workspaceId: "workspace-tabs", sequence: 9,
      activeChatId: "chat-one", tabs: [{ chatId: "chat-one", preview: "Main", busy: false }], turns: [],
    });
    expect(getChatWorkspaceState()?.hasSnapshot).toBe(false);

    emit("session_snapshot", {
      type: "session_snapshot", surface: "code", workspaceId: "workspace-tabs", sequence: 3,
      activeChatId: "code-chat-one", tabs: [{ chatId: "code-chat-one", preview: "Code", busy: false }], turns: [],
    });
    const editorContext = {
      tabs: [{ kind: "file", title: "main.go", active: true, ref: { rootId: "root", path: "main.go" } }],
    };
    expect(sendMessage(log, "review this", "model-a", "general", { editorContext })).toBe(true);
    expect(socket.send).toHaveBeenLastCalledWith(expect.objectContaining({
      type: "chat_send", surface: "code", workspaceId: "workspace-tabs", chatId: "code-chat-one",
      message: "review this", editorContext,
    }));
    emit("session_snapshot", {
      type: "session_snapshot", surface: "code", workspaceId: "workspace-tabs", sequence: 4,
      activeChatId: "code-chat-one", tabs: [{ chatId: "code-chat-one", preview: "Code", busy: false }],
      turns: [{ id: "stored", userContent: "review this", status: "done", assistantTurns: [] }],
    });
    clearChat(log);
    expect(socket.send).toHaveBeenLastCalledWith({
      type: "chat_clear", surface: "code", workspaceId: "workspace-tabs", chatId: "code-chat-one",
    });
  });

  it("renders and activates persisted and live Code Chat prompt resources", () => {
    closeWorkspaceSession(log);
    const activateResource = vi.fn();
    openWorkspaceSession(log, "workspace-tabs", { surface: "code", onActivateResource: activateResource });
    const references = [{
      ref: { rootId: "root", path: "docs" }, kind: "directory",
      referencePath: "echo/docs", label: "docs",
    }];
    const editorContext = {
      tabs: [
        {
          kind: "diff", title: "main.go (Index)", active: true,
          ref: { rootId: "root", path: "main.go" }, reference: "echo/main.go",
          diff: { repositoryId: "repo", repository: "echo", scope: "staged", path: "main.go" },
          selections: [{ side: "original", startLine: 3, startColumn: 2, endLine: 4, endColumn: 5 }],
        },
        { kind: "untitled", title: "Untitled-1", dirty: true },
      ],
      truncated: true,
    };
    emit("session_snapshot", {
      type: "session_snapshot", surface: "code", workspaceId: "workspace-tabs", sequence: 1,
      activeChatId: "code-chat", tabs: [{ chatId: "code-chat", preview: "Review", busy: false }],
      turns: [{ id: "stored-resources", userContent: "Review it", status: "done", assistantTurns: [], references, editorContext }],
    });

    const stored = log.querySelector<HTMLDetailsElement>(".chat-prompt-resources")!;
    expect(stored.querySelector("summary")?.textContent).toContain("2 tabs · 1 selection · 1 mention");
    expect(stored.querySelector("summary")?.textContent).toContain("Truncated");
    stored.open = true;
    expect(stored.textContent).toContain("Mentioned");
    expect(stored.textContent).toContain("Editor context");
    expect(stored.textContent).toContain("Lines 3:2–4:5 · original");
    expect(stored.textContent).not.toContain("selected source text");

    const selection = stored.querySelector<HTMLButtonElement>(".chat-prompt-resource-row.is-selection")!;
    selection.click();
    expect(activateResource).toHaveBeenCalledWith(expect.objectContaining({
      kind: "diff", label: "main.go (Index)", ref: { rootId: "root", path: "main.go" },
      diff: expect.objectContaining({ repositoryId: "repo", scope: "staged", path: "main.go" }),
      selection: { side: "original", startLine: 3, startColumn: 2, endLine: 4, endColumn: 5 },
    }));

    emit("session_snapshot", {
      type: "session_snapshot", surface: "code", workspaceId: "workspace-tabs", sequence: 2,
      activeChatId: "code-chat", tabs: [{ chatId: "code-chat", preview: "New", busy: false }], turns: [],
    });
    emit("session_event", {
      type: "session_event", surface: "code", workspaceId: "workspace-tabs", chatId: "code-chat", sequence: 3,
      event: { type: "turn_started", turnId: "live-resources", message: "Live", references, editorContext },
    });
    expect(log.querySelector(".chat-prompt-resources summary")?.textContent).toContain("2 tabs · 1 selection · 1 mention");
  });
});
