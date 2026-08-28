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

  it("pairs compression timing and exposes the generated summary only in the inspector result", async () => {
    const compressionEvents: TrajectoryTimelineEvent[] = [
      {
        record: "event", sequence: 1, timestamp: "2026-08-19T12:00:00.000Z",
        type: "context/compression_start", turnId: "turn-1",
        data: { compressionId: "compression-1", trigger: "automatic", model: "test-model", startedAt: "2026-08-19T12:00:00.000Z" },
      },
      {
        record: "event", sequence: 2, timestamp: "2026-08-19T12:00:00.250Z",
        type: "context/compression_complete", turnId: "turn-1",
        data: {
          compressionId: "compression-1", beforeTokens: 7000, afterTokens: 2800,
          summary: "## Goal\nKeep this inspector-only summary", durationMs: 250,
          completedAt: "2026-08-19T12:00:00.250Z", recoveryAvailable: true,
        },
      },
    ];
    const timeline = deriveTrajectoryTimeline(compressionEvents);
    expect(timeline?.spans).toHaveLength(1);
    expect(timeline?.spans[0]).toMatchObject({ lane: "system", start: 0, end: 250, pending: false });

    api.get.mockResolvedValue({
      header: { formatVersion: 1, chatId: "chat-1", surface: "chat", createdAt: "2026-08-19T12:00:00Z" },
      events: compressionEvents, hasMore: false, oldestSeq: 1, newestSeq: 2,
    });
    await view.setTarget("workspace-1", "chat-1");

    const compressionBlock = host.querySelector<HTMLElement>('[data-trajectory-overview] [data-trajectory-sequence="2"]');
    expect(compressionBlock).not.toBeNull();
    compressionBlock?.click();
    host.querySelector<HTMLButtonElement>('[data-inspector-tab="result"]')?.click();
    expect(host.querySelector("[data-trajectory-inspector]")?.textContent).toContain("Keep this inspector-only summary");
  });

  it("projects overlapping research jobs into attributed agent tracks", () => {
    const researchEvents: TrajectoryTimelineEvent[] = [
      {
        record: "event", sequence: 1, timestamp: "2026-08-19T12:00:00.000Z", type: "research/job_start", turnId: "turn-1",
        data: { agentId: "agent-1", agentName: "Docs", jobId: "agent-1-job-1", jobNumber: 1, startedAt: "2026-08-19T12:00:00.000Z" },
      },
      {
        record: "event", sequence: 2, timestamp: "2026-08-19T12:00:00.100Z", type: "research/job_start", turnId: "turn-1",
        data: { agentId: "agent-2", agentName: "Code", jobId: "agent-2-job-1", jobNumber: 1, startedAt: "2026-08-19T12:00:00.100Z" },
      },
      {
        record: "event", sequence: 3, timestamp: "2026-08-19T12:00:00.150Z", type: "research/request_start", turnId: "turn-1",
        data: { agentId: "agent-1", agentName: "Docs", jobId: "agent-1-job-1", jobNumber: 1, round: 0, startedAt: "2026-08-19T12:00:00.150Z" },
      },
      {
        record: "event", sequence: 4, timestamp: "2026-08-19T12:00:00.450Z", type: "research/assistant_message", turnId: "turn-1",
        data: { agentId: "agent-1", agentName: "Docs", jobId: "agent-1-job-1", jobNumber: 1, round: 0, completedAt: "2026-08-19T12:00:00.450Z", durationMs: 300 },
      },
      {
        record: "event", sequence: 5, timestamp: "2026-08-19T12:00:00.800Z", type: "research/job_end", turnId: "turn-1",
        data: { agentId: "agent-2", agentName: "Code", jobId: "agent-2-job-1", jobNumber: 1, status: "completed", completedAt: "2026-08-19T12:00:00.800Z", durationMs: 700 },
      },
      {
        record: "event", sequence: 6, timestamp: "2026-08-19T12:00:01.000Z", type: "research/job_end", turnId: "turn-1",
        data: { agentId: "agent-1", agentName: "Docs", jobId: "agent-1-job-1", jobNumber: 1, status: "completed", completedAt: "2026-08-19T12:00:01.000Z", durationMs: 1000 },
      },
    ];

    const timeline = deriveTrajectoryTimeline(researchEvents, Date.parse("2026-08-19T12:00:02.000Z"));
    expect(timeline?.researchAgents).toEqual([{ id: "agent-1", name: "Docs" }, { id: "agent-2", name: "Code" }]);
    expect(timeline?.spans.filter((span) => span.kind === "job")).toHaveLength(2);
    expect(timeline?.spans.find((span) => span.event.sequence === 6)).toMatchObject({ trackKey: "research:agent-1", lane: "research", kind: "job", start: 0, end: 1000 });
    expect(timeline?.spans.find((span) => span.event.sequence === 5)).toMatchObject({ trackKey: "research:agent-2", lane: "research", kind: "job", start: 100, end: 800 });
    expect(timeline?.spans.find((span) => span.event.sequence === 4)).toMatchObject({ trackKey: "research:agent-1", kind: "model", start: 150, end: 450 });
  });

  it("extends a queued research job to the current time until it starts", () => {
    const queuedAt = "2026-08-19T12:00:00.000Z";
    const now = Date.parse("2026-08-19T12:00:02.000Z");
    const timeline = deriveTrajectoryTimeline([{
      record: "event", sequence: 1, timestamp: queuedAt, type: "research/job_queued", turnId: "turn-1",
      data: { agentId: "agent-1", agentName: "Docs", jobId: "agent-1-job-1", jobNumber: 1, queuedAt },
    }], now);
    expect(timeline?.spans[0]).toMatchObject({ kind: "job", pending: true, start: 0, end: 2000 });
  });

  it("renders expandable research swimlanes, filters child events, and inspects exact child requests", async () => {
    const researchEvents: TrajectoryTimelineEvent[] = [
      {
        record: "event", sequence: 1, timestamp: "2026-08-19T12:00:00.000Z", type: "tool/call", turnId: "turn-1", step: 0,
        data: { callId: "spawn", tool: "research_agents_spawn", arguments: "{\"agents\":[{\"name\":\"Docs\",\"task\":\"Inspect docs\"}]}", startedAt: "2026-08-19T12:00:00.000Z" },
      },
      {
        record: "event", sequence: 2, timestamp: "2026-08-19T12:00:00.010Z", type: "tool/result", turnId: "turn-1", step: 0,
        data: { callId: "spawn", tool: "research_agents_spawn", result: { output: [{ id: "agent-1", name: "Docs" }] }, completedAt: "2026-08-19T12:00:00.010Z", durationMs: 10 },
      },
      {
        record: "event", sequence: 3, timestamp: "2026-08-19T12:00:00.011Z", type: "research/agent_created", turnId: "turn-1",
        data: { agentId: "agent-1", agentName: "Docs", jobId: "agent-1-job-1", jobNumber: 1, task: "Inspect docs" },
      },
      {
        record: "event", sequence: 4, timestamp: "2026-08-19T12:00:00.020Z", type: "research/request_start", turnId: "turn-1",
        data: { agentId: "agent-1", agentName: "Docs", jobId: "agent-1-job-1", jobNumber: 1, round: 0, request: { model: "research-model", messages: [{ role: "user", content: "Inspect docs" }], tools: [{ function: { name: "web_fetch" } }] }, startedAt: "2026-08-19T12:00:00.020Z" },
      },
      {
        record: "event", sequence: 5, timestamp: "2026-08-19T12:00:00.030Z", type: "research/chunk", turnId: "turn-1",
        data: { agentId: "agent-1", agentName: "Docs", jobId: "agent-1-job-1", jobNumber: 1, round: 0, streamEvents: [{ streamEvent: { type: "token", content: "Found" } }] },
      },
      {
        record: "event", sequence: 6, timestamp: "2026-08-19T12:00:00.120Z", type: "research/assistant_message", turnId: "turn-1",
        data: { agentId: "agent-1", agentName: "Docs", jobId: "agent-1-job-1", jobNumber: 1, round: 0, content: "Found evidence", finishReason: "stop", usage: { total_tokens: 18 }, completedAt: "2026-08-19T12:00:00.120Z", durationMs: 100 },
      },
    ];
    api.get.mockResolvedValue({
      header: { formatVersion: 1, chatId: "chat-1", surface: "chat", createdAt: "2026-08-19T12:00:00Z" },
      events: researchEvents, hasMore: false, oldestSeq: 1, newestSeq: 6,
    });
    await view.setTarget("workspace-1", "chat-1");

    expect(host.querySelector("[data-trajectory-overview-labels]")?.textContent).toContain("Docs");
    const toggle = host.querySelector<HTMLButtonElement>("[data-trajectory-research-toggle]")!;
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    toggle.click();
    expect(host.querySelector("[data-trajectory-research-toggle]")?.getAttribute("aria-expanded")).toBe("false");
    await view.setTarget("workspace-1", "chat-1");
    expect(host.querySelector("[data-trajectory-research-toggle]")?.getAttribute("aria-expanded")).toBe("true");

    host.querySelector<HTMLElement>('[data-trajectory-overview] [data-trajectory-sequence="2"]')?.click();
    const agentChip = host.querySelector<HTMLButtonElement>('[data-trajectory-agent="agent-1"]');
    expect(agentChip?.textContent).toContain("Docs");
    agentChip?.click();
    expect(host.querySelector("[data-trajectory-inspector]")?.textContent).toContain("Research agent · Docs");

    host.querySelector<HTMLButtonElement>('[data-trajectory-lane="research"]')?.click();
    expect(host.querySelector("[data-trajectory-status]")?.textContent).toContain("3 events loaded");
    host.querySelector<HTMLButtonElement>("[data-trajectory-chunks]")?.click();
    expect(host.querySelector("[data-trajectory-status]")?.textContent).toContain("4 events loaded");

    host.querySelector<HTMLElement>('[data-trajectory-overview] [data-trajectory-sequence="6"]')?.click();
    host.querySelector<HTMLButtonElement>('[data-inspector-tab="payload"]')?.click();
    expect(host.querySelector("[data-trajectory-inspector]")?.textContent).toContain("research-model");
    host.querySelector<HTMLButtonElement>('[data-inspector-tab="result"]')?.click();
    expect(host.querySelector("[data-trajectory-inspector]")?.textContent).toContain("Found evidence");
  });

  it("marks historical research orchestration without child trajectory events", async () => {
    api.get.mockResolvedValue({
      header: { formatVersion: 1, chatId: "chat-1", surface: "chat", createdAt: "2026-08-19T12:00:00Z" },
      events: [{
        record: "event", sequence: 1, timestamp: "2026-08-19T12:00:00.000Z", type: "tool/call", turnId: "turn-1", step: 0,
        data: { callId: "spawn", tool: "research_agents_spawn", arguments: "{}" },
      }], hasMore: false, oldestSeq: 1, newestSeq: 1,
    });
    await view.setTarget("workspace-1", "chat-1");
    expect(host.querySelector<HTMLElement>("[data-trajectory-notice]")?.hidden).toBe(false);
    expect(host.querySelector("[data-trajectory-notice]")?.textContent).toContain("historical run");
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
