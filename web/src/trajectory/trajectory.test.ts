import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const socket = vi.hoisted(() => {
  const handlers = new Map<string, (message: object) => void>();
  return {
    handlers,
    on: vi.fn((type: string, handler: (message: object) => void) => {
      handlers.set(type, handler);
      return () => handlers.delete(type);
    }),
  };
});

const api = vi.hoisted(() => ({ get: vi.fn() }));

vi.mock("../../js/api.js", () => ({ get: api.get }));
vi.mock("../../js/ws.js", () => ({ on: socket.on }));
vi.mock("../code/ui", () => ({
  escapeHTML: (value: unknown) => String(value ?? "").replace(/[&<>"']/g, ""),
  toast: vi.fn(),
}));

import { TrajectoryView } from "./index";
import { deriveTrajectoryTimeline, type TrajectoryTimelineEvent } from "./timeline";

const events = [
  {
    record: "event", sequence: 1, timestamp: "2026-08-19T12:00:00.000Z",
    type: "user/message", turnId: "turn-1", data: { content: "Inspect this repository" },
  },
  {
    record: "event", sequence: 2, timestamp: "2026-08-19T12:00:00.010Z",
    type: "request/start", turnId: "turn-1", step: 0,
    data: { request: { model: "test-model", messages: [{ role: "user", content: "Inspect this repository" }], tools: [{ function: { name: "read_file" } }] } },
  },
  {
    record: "event", sequence: 3, timestamp: "2026-08-19T12:00:00.020Z",
    type: "assistant/chunk", turnId: "turn-1", step: 0,
    data: { streamEvent: { type: "token", content: "Done", raw: { choices: [] } } },
  },
  {
    record: "event", sequence: 4, timestamp: "2026-08-19T12:00:00.030Z",
    type: "assistant/message", turnId: "turn-1", step: 0,
    data: { content: "Done", reasoning: "Checked it", durationMs: 20, ttftMs: 10 },
  },
];

describe("TrajectoryView", () => {
  let host: HTMLElement;
  let view: TrajectoryView;
  let scrollRegion: HTMLElement;
  let scrollTo: ReturnType<typeof vi.fn>;
  const onViewChange = vi.fn();

  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
    api.get.mockResolvedValue({
      header: { formatVersion: 1, chatId: "chat-1", surface: "chat", createdAt: "2026-08-19T12:00:00Z" },
      events, hasMore: false, oldestSeq: 1, newestSeq: 4,
    });
    view = new TrajectoryView(host, onViewChange);
    scrollRegion = host.querySelector<HTMLElement>("[data-trajectory-scroll]")!;
    Object.defineProperty(scrollRegion, "clientHeight", { configurable: true, value: 600 });
    scrollTo = vi.fn();
    Object.defineProperty(scrollRegion, "scrollTo", { configurable: true, value: scrollTo });
  });

  afterEach(() => {
    view.destroy();
    host.remove();
    api.get.mockReset();
    onViewChange.mockReset();
  });

  it("loads semantic events, exposes the timing overview, and inspects exact request payloads", async () => {
    await view.setTarget("workspace-1", "chat-1");

    expect(api.get).toHaveBeenCalledWith(
      "/api/workspaces/workspace-1/chats/chat-1/trajectory",
      { query: { turnLimit: 20 } },
    );
    const controls = host.querySelector(".trajectory-controls");
    expect(controls?.querySelector("[data-trajectory-overview]")).not.toBeNull();
    expect(controls?.querySelector("[data-trajectory-search]")).not.toBeNull();
    expect(scrollRegion.querySelector(".trajectory-workspace")).not.toBeNull();
    expect(scrollRegion.contains(controls)).toBe(false);
    expect(host.querySelector("[data-trajectory-status]")?.textContent).toContain("3 events loaded");
    expect(host.querySelectorAll("[data-trajectory-overview] [data-trajectory-sequence]")).toHaveLength(2);
    expect(host.querySelector('[data-trajectory-overview] [data-trajectory-sequence="2"]')).toBeNull();

    host.querySelector<HTMLElement>('[data-trajectory-overview] [data-trajectory-sequence="4"]')?.click();
    host.querySelector<HTMLButtonElement>('[data-inspector-tab="payload"]')?.click();
    expect(host.querySelector("[data-trajectory-inspector]")?.textContent).toContain("test-model");
    expect(host.querySelector("[data-trajectory-inspector]")?.textContent).toContain("Inspect this repository");
  });

  it("collapses idle time and renders completed model and tool lifecycles once", () => {
    const timelineEvents: TrajectoryTimelineEvent[] = [
      {
        record: "event", sequence: 1, timestamp: "2026-08-19T12:00:00.000Z",
        type: "turn/start", turnId: "turn-1", data: { startedAt: "2026-08-19T12:00:00.000Z" },
      },
      {
        record: "event", sequence: 2, timestamp: "2026-08-19T12:00:00.000Z",
        type: "user/message", turnId: "turn-1", data: { content: "First" },
      },
      {
        record: "event", sequence: 3, timestamp: "2026-08-19T12:00:00.010Z",
        type: "request/start", turnId: "turn-1", step: 0, data: { startedAt: "2026-08-19T12:00:00.010Z" },
      },
      {
        record: "event", sequence: 4, timestamp: "2026-08-19T12:00:01.010Z",
        type: "assistant/message", turnId: "turn-1", step: 0,
        data: { completedAt: "2026-08-19T12:00:01.010Z", durationMs: 1000 },
      },
      {
        record: "event", sequence: 5, timestamp: "2026-08-19T13:00:00.000Z",
        type: "turn/start", turnId: "turn-2", data: { startedAt: "2026-08-19T13:00:00.000Z" },
      },
      {
        record: "event", sequence: 6, timestamp: "2026-08-19T13:00:00.000Z",
        type: "user/message", turnId: "turn-2", data: { content: "Second" },
      },
      {
        record: "event", sequence: 7, timestamp: "2026-08-19T13:00:00.010Z",
        type: "request/start", turnId: "turn-2", step: 0, data: { startedAt: "2026-08-19T13:00:00.010Z" },
      },
      {
        record: "event", sequence: 8, timestamp: "2026-08-19T13:00:00.510Z",
        type: "assistant/message", turnId: "turn-2", step: 0,
        data: { completedAt: "2026-08-19T13:00:00.510Z", durationMs: 500 },
      },
      {
        record: "event", sequence: 9, timestamp: "2026-08-19T13:00:00.510Z",
        type: "tool/call", turnId: "turn-2", step: 0,
        data: { callId: "call-1", startedAt: "2026-08-19T13:00:00.510Z", tool: "read_file" },
      },
      {
        record: "event", sequence: 10, timestamp: "2026-08-19T13:00:00.760Z",
        type: "tool/result", turnId: "turn-2", step: 0,
        data: { callId: "call-1", completedAt: "2026-08-19T13:00:00.760Z", durationMs: 250, tool: "read_file" },
      },
      {
        record: "event", sequence: 11, timestamp: "2026-08-19T13:00:00.760Z",
        type: "turn/end", turnId: "turn-2",
        data: { completedAt: "2026-08-19T13:00:00.760Z", durationMs: 760 },
      },
    ];

    const model = deriveTrajectoryTimeline(timelineEvents);
    expect(model).not.toBeNull();
    expect(model?.spans.map((span) => span.event.sequence)).toEqual([2, 4, 6, 8, 10]);
    expect(model?.spans.some((span) => span.event.type === "turn/end")).toBe(false);
    expect(model?.spans.find((span) => span.event.sequence === 10)).toMatchObject({
      lane: "tools", start: 1500, end: 1750,
    });
    expect(model?.turnBoundaries).toEqual([
      { turnId: "turn-1", time: 0 },
      { turnId: "turn-2", time: 1000 },
    ]);
    expect(model?.end).toBe(1750);
    expect(model?.compressedIdleMs).toBe(3_599_010);
  });

  it("scrolls from a timeline block to its ledger row and clears a conflicting lane filter", async () => {
    await view.setTarget("workspace-1", "chat-1");
    host.querySelector<HTMLButtonElement>('[data-trajectory-lane="input"]')?.click();
    expect(host.querySelector("[data-trajectory-status]")?.textContent).toContain("1 event loaded");

    const timelineBlock = host.querySelector<HTMLButtonElement>('[data-trajectory-overview] [data-trajectory-sequence="4"]');
    timelineBlock?.click();

    expect(host.querySelector('[data-trajectory-lane="all"]')?.classList).toContain("is-active");
    expect(host.querySelector("[data-trajectory-status]")?.textContent).toContain("3 events loaded");
    expect(scrollTo).toHaveBeenCalled();
    expect(host.scrollTop).toBe(0);
    expect(host.querySelector('[data-trajectory-overview] [data-trajectory-sequence="4"]')?.classList).toContain("is-selected");
    expect(host.querySelector("[data-trajectory-inspector]")?.textContent).toContain("Assistant message");
  });

  it("exposes both sides of a paired tool operation from its single timeline block", async () => {
    api.get.mockResolvedValue({
      header: { formatVersion: 1, chatId: "chat-1", surface: "chat", createdAt: "2026-08-19T12:00:00Z" },
      events: [
        {
          record: "event", sequence: 1, timestamp: "2026-08-19T12:00:00.000Z",
          type: "tool/call", turnId: "turn-1", step: 0,
          data: { callId: "call-1", callOrder: 0, tool: "read_file", arguments: { path: "workspace.json" }, startedAt: "2026-08-19T12:00:00.000Z" },
        },
        {
          record: "event", sequence: 2, timestamp: "2026-08-19T12:00:00.025Z",
          type: "tool/result", turnId: "turn-1", step: 0,
          data: { callId: "call-1", callOrder: 0, tool: "read_file", result: { content: "ok" }, completedAt: "2026-08-19T12:00:00.025Z", durationMs: 25 },
        },
      ],
      hasMore: false, oldestSeq: 1, newestSeq: 2,
    });
    await view.setTarget("workspace-1", "chat-1");

    expect(host.querySelectorAll("[data-trajectory-overview] [data-trajectory-sequence]")).toHaveLength(1);
    host.querySelector<HTMLElement>('[data-trajectory-overview] [data-trajectory-sequence="2"]')?.click();
    host.querySelector<HTMLButtonElement>('[data-inspector-tab="payload"]')?.click();
    expect(host.querySelector("[data-trajectory-inspector]")?.textContent).toContain("workspace.json");
    host.querySelector<HTMLButtonElement>('[data-inspector-tab="result"]')?.click();
    expect(host.querySelector("[data-trajectory-inspector]")?.textContent).toContain("ok");
  });

  it("can reveal raw chunks and follows live events for the selected chat", async () => {
    await view.setTarget("workspace-1", "chat-1");
    host.querySelector<HTMLButtonElement>("[data-trajectory-chunks]")?.click();
    expect(host.querySelector("[data-trajectory-status]")?.textContent).toContain("4 events loaded");

    socket.handlers.get("trajectory_event")?.({
      type: "trajectory_event", workspaceId: "workspace-1", surface: "chat", chatId: "chat-1",
      event: {
        record: "event", sequence: 5, timestamp: "2026-08-19T12:00:00.050Z",
        type: "turn/end", turnId: "turn-1", data: { status: "done", durationMs: 50 },
      },
    });
    expect(host.querySelector("[data-trajectory-status]")?.textContent).toContain("5 events loaded");

    host.querySelector<HTMLButtonElement>('[data-trajectory-view="chat"]')?.click();
    expect(onViewChange).toHaveBeenCalledWith("chat");
  });
});
