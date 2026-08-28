export type TrajectoryTimelineEvent = {
  record: "event";
  sequence: number;
  timestamp: string;
  type: string;
  turnId?: string;
  step?: number;
  data?: Record<string, unknown>;
};

export type TrajectoryLane = "input" | "model" | "tools" | "system" | "research";

export type TrajectorySpanKind = "input" | "model" | "tool" | "system" | "job" | "status" | "report";

export type TrajectoryResearchAgent = {
  id: string;
  name: string;
};

export type TrajectoryTimelineSpan = {
  event: TrajectoryTimelineEvent;
  lane: TrajectoryLane;
  trackKey: string;
  kind: TrajectorySpanKind;
  start: number;
  end: number;
  pending: boolean;
};

export type TrajectoryTurnBoundary = {
  turnId: string;
  time: number;
};

export type TrajectoryTimelineModel = {
  start: number;
  end: number;
  spans: TrajectoryTimelineSpan[];
  turnBoundaries: TrajectoryTurnBoundary[];
  researchAgents: TrajectoryResearchAgent[];
  compressedIdleMs: number;
};

const overviewTypes = new Set([
  "legacy/import", "user/message", "request/start", "assistant/message",
  "tool/call", "tool/result", "transcript/delete", "transcript/edit",
  "transcript/rewind", "auxiliary/request", "auxiliary/result",
  "context/injection", "context/compression_queued", "context/compression_start",
  "context/compression_complete", "context/compression_skipped", "context/compression_error",
  "research/agent_created", "research/job_queued", "research/job_start", "research/job_end",
  "research/status", "research/request_start", "research/assistant_message",
  "research/tool_call", "research/tool_result", "research/report_delivered",
  "persistence/error",
]);

export function trajectoryLaneFor(type: string, data?: Record<string, unknown>): TrajectoryLane {
  if (type.startsWith("research/")) return "research";
  if (type.startsWith("context/compression") && data?.phase === "research" && typeof data.agentId === "string") return "research";
  if (type.startsWith("user/")) return "input";
  if (type.startsWith("assistant/") || type.startsWith("request/") || type.startsWith("auxiliary/")) return "model";
  if (type.startsWith("tool/")) return "tools";
  return "system";
}

function textValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function researchAgentId(event: TrajectoryTimelineEvent): string {
  if (event.type.startsWith("research/") || event.data?.phase === "research") {
    return textValue(event.data?.agentId);
  }
  return "";
}

function trackKeyFor(event: TrajectoryTimelineEvent, lane: TrajectoryLane): string {
  const agentId = researchAgentId(event);
  return lane === "research" && agentId ? `research:${agentId}` : lane;
}

function spanKindFor(event: TrajectoryTimelineEvent, lane: TrajectoryLane): TrajectorySpanKind {
  if (event.type === "research/job_end" || event.type === "research/job_start" || event.type === "research/job_queued") return "job";
  if (event.type === "research/status" || event.type === "research/agent_created") return "status";
  if (event.type === "research/report_delivered") return "report";
  if (event.type === "research/tool_result" || event.type === "research/tool_call" || lane === "tools") return "tool";
  if (event.type === "research/assistant_message" || event.type === "research/request_start" || lane === "model") return "model";
  if (lane === "input") return "input";
  return "system";
}

function parseTime(value: unknown): number | undefined {
  if (typeof value !== "string") return undefined;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : undefined;
}

function durationOf(event: TrajectoryTimelineEvent): number | undefined {
  const duration = event.data?.durationMs;
  return typeof duration === "number" && Number.isFinite(duration) ? Math.max(0, duration) : undefined;
}

function recordedAt(event: TrajectoryTimelineEvent): number | undefined {
  return parseTime(event.timestamp);
}

function eventStartedAt(event: TrajectoryTimelineEvent): number | undefined {
  return parseTime(event.data?.startedAt) ?? recordedAt(event);
}

function operationRange(
  event: TrajectoryTimelineEvent,
  startEvent?: TrajectoryTimelineEvent,
): { start: number; end: number } | null {
  const duration = durationOf(event);
  const completedAt = parseTime(event.data?.completedAt);
  const recorded = recordedAt(event);
  let start = startEvent === undefined
    ? parseTime(event.data?.startedAt)
    : eventStartedAt(startEvent);
  if (start === undefined && completedAt !== undefined && duration !== undefined) start = completedAt - duration;
  if (start === undefined) start = recorded;
  if (start === undefined) return null;

  let end = completedAt;
  if (end === undefined && duration !== undefined) end = start + duration;
  if (end === undefined) end = recorded ?? start;
  if (end < start) end = duration === undefined ? start : start + duration;
  return { start, end };
}

function instantRange(event: TrajectoryTimelineEvent): { start: number; end: number } | null {
  const start = eventStartedAt(event);
  return start === undefined ? null : { start, end: start };
}

function requestKey(event: TrajectoryTimelineEvent): string | null {
  if (!event.turnId) return null;
  return `${event.turnId}\0${event.step ?? 0}`;
}

function toolKey(event: TrajectoryTimelineEvent): string | null {
  const callId = event.data?.callId;
  if (typeof callId === "string" && callId) return `${event.turnId ?? ""}\0${callId}`;
  const order = event.data?.callOrder;
  if (!event.turnId || (typeof order !== "number" && typeof order !== "string")) return null;
  return `${event.turnId}\0${event.step ?? 0}\0${String(order)}`;
}

function compressionKey(event: TrajectoryTimelineEvent): string | null {
  const compressionId = event.data?.compressionId;
  return typeof compressionId === "string" && compressionId ? compressionId : null;
}

function researchJobKey(event: TrajectoryTimelineEvent): string | null {
  const agentId = researchAgentId(event);
  const jobId = textValue(event.data?.jobId);
  return agentId && jobId ? `${event.turnId ?? ""}\0${agentId}\0${jobId}` : null;
}

function researchRequestKey(event: TrajectoryTimelineEvent): string | null {
  const job = researchJobKey(event);
  const round = event.data?.round;
  return job !== null && (typeof round === "number" || typeof round === "string") ? `${job}\0${String(round)}` : null;
}

function researchToolKey(event: TrajectoryTimelineEvent): string | null {
  const request = researchRequestKey(event);
  const callId = textValue(event.data?.callId);
  return request !== null && callId ? `${request}\0${callId}` : null;
}

function pendingRange(event: TrajectoryTimelineEvent, now: number): { start: number; end: number } | null {
  const start = eventStartedAt(event);
  return start === undefined ? null : { start, end: Math.max(start, now) };
}

/**
 * Build the compact Duration projection used by the trajectory overview.
 * Completed request/result pairs become one operation and wall-clock gaps with
 * no recorded activity are removed from the horizontal domain.
 */
export function deriveTrajectoryTimeline(
  events: readonly TrajectoryTimelineEvent[],
  now = Date.now(),
): TrajectoryTimelineModel | null {
  const requestStarts = new Map<string, TrajectoryTimelineEvent>();
  const completedRequests = new Set<string>();
  const toolCalls = new Map<string, TrajectoryTimelineEvent>();
  const completedTools = new Set<string>();
  const compressionStarts = new Map<string, TrajectoryTimelineEvent>();
  const completedCompressions = new Set<string>();
  const researchJobQueued = new Map<string, TrajectoryTimelineEvent>();
  const researchJobStarts = new Map<string, TrajectoryTimelineEvent>();
  const startedResearchJobs = new Set<string>();
  const completedResearchJobs = new Set<string>();
  const researchRequestStarts = new Map<string, TrajectoryTimelineEvent>();
  const completedResearchRequests = new Set<string>();
  const researchToolCalls = new Map<string, TrajectoryTimelineEvent>();
  const completedResearchTools = new Set<string>();

  for (const event of events) {
    if (event.type === "request/start") {
      const key = requestKey(event);
      if (key !== null) requestStarts.set(key, event);
    } else if (event.type === "assistant/message") {
      const key = requestKey(event);
      if (key !== null) completedRequests.add(key);
    } else if (event.type === "tool/call") {
      const key = toolKey(event);
      if (key !== null) toolCalls.set(key, event);
    } else if (event.type === "tool/result") {
      const key = toolKey(event);
      if (key !== null) completedTools.add(key);
    } else if (event.type === "context/compression_start") {
      const key = compressionKey(event);
      if (key !== null) compressionStarts.set(key, event);
    } else if (["context/compression_complete", "context/compression_skipped", "context/compression_error"].includes(event.type)) {
      const key = compressionKey(event);
      if (key !== null) completedCompressions.add(key);
    } else if (event.type === "research/job_queued") {
      const key = researchJobKey(event);
      if (key !== null) researchJobQueued.set(key, event);
    } else if (event.type === "research/job_start") {
      const key = researchJobKey(event);
      if (key !== null) {
        researchJobStarts.set(key, event);
        startedResearchJobs.add(key);
      }
    } else if (event.type === "research/job_end") {
      const key = researchJobKey(event);
      if (key !== null) completedResearchJobs.add(key);
    } else if (event.type === "research/request_start") {
      const key = researchRequestKey(event);
      if (key !== null) researchRequestStarts.set(key, event);
    } else if (event.type === "research/assistant_message") {
      const key = researchRequestKey(event);
      if (key !== null) completedResearchRequests.add(key);
    } else if (event.type === "research/tool_call") {
      const key = researchToolKey(event);
      if (key !== null) researchToolCalls.set(key, event);
    } else if (event.type === "research/tool_result") {
      const key = researchToolKey(event);
      if (key !== null) completedResearchTools.add(key);
    }
  }

  const rawSpans: TrajectoryTimelineSpan[] = [];
  for (const event of events) {
    if (!overviewTypes.has(event.type)) continue;
    const key = event.type.startsWith("tool/")
      ? toolKey(event)
      : event.type.startsWith("context/compression") ? compressionKey(event) : requestKey(event);
    if (event.type === "request/start" && key !== null && completedRequests.has(key)) continue;
    if (event.type === "tool/call" && key !== null && completedTools.has(key)) continue;
    if (event.type === "context/compression_start" && key !== null && completedCompressions.has(key)) continue;
    const researchJob = researchJobKey(event);
    const researchRequest = researchRequestKey(event);
    const researchTool = researchToolKey(event);
    if (event.type === "research/job_queued" && researchJob !== null
      && (startedResearchJobs.has(researchJob) || completedResearchJobs.has(researchJob))) continue;
    if (event.type === "research/job_start" && researchJob !== null && completedResearchJobs.has(researchJob)) continue;
    if (event.type === "research/request_start" && researchRequest !== null && completedResearchRequests.has(researchRequest)) continue;
    if (event.type === "research/tool_call" && researchTool !== null && completedResearchTools.has(researchTool)) continue;

    let range: { start: number; end: number } | null;
    if (event.type === "assistant/message") {
      range = operationRange(event, key === null ? undefined : requestStarts.get(key));
    } else if (event.type === "tool/result") {
      range = operationRange(event, key === null ? undefined : toolCalls.get(key));
    } else if (["context/compression_complete", "context/compression_skipped", "context/compression_error"].includes(event.type)) {
      range = operationRange(event, key === null ? undefined : compressionStarts.get(key));
    } else if (event.type === "research/job_end") {
      range = operationRange(event, researchJob === null ? undefined : researchJobStarts.get(researchJob) || researchJobQueued.get(researchJob));
    } else if (event.type === "research/assistant_message") {
      range = operationRange(event, researchRequest === null ? undefined : researchRequestStarts.get(researchRequest));
    } else if (event.type === "research/tool_result") {
      range = operationRange(event, researchTool === null ? undefined : researchToolCalls.get(researchTool));
    } else if (event.type === "research/job_queued" || event.type === "research/job_start" || event.type === "research/request_start" || event.type === "research/tool_call") {
      range = pendingRange(event, now);
    } else if (event.type === "request/start" || event.type === "tool/call" || event.type === "context/compression_start") {
      range = pendingRange(event, now);
    } else if (durationOf(event) !== undefined || parseTime(event.data?.completedAt) !== undefined) {
      range = operationRange(event);
    } else {
      range = instantRange(event);
    }
    if (range === null) continue;
    const lane = trajectoryLaneFor(event.type, event.data);
    rawSpans.push({
      event,
      lane,
      trackKey: trackKeyFor(event, lane),
      kind: spanKindFor(event, lane),
      ...range,
      pending: event.type === "request/start" || event.type === "tool/call" || event.type === "context/compression_start"
        || event.type === "research/job_queued" || event.type === "research/job_start" || event.type === "research/request_start" || event.type === "research/tool_call",
    });
  }
  if (!rawSpans.length) return null;

  const ordered = [...rawSpans].sort((left, right) =>
    left.start - right.start || left.end - right.end || left.event.sequence - right.event.sequence);
  const removedBefore = new Map<TrajectoryTimelineSpan, number>();
  let compressedIdleMs = 0;
  let coveredUntil: number | undefined;
  for (const span of ordered) {
    if (coveredUntil !== undefined && span.start > coveredUntil) {
      compressedIdleMs += span.start - coveredUntil;
    }
    removedBefore.set(span, compressedIdleMs);
    coveredUntil = coveredUntil === undefined ? span.end : Math.max(coveredUntil, span.end);
  }

  const projected = rawSpans.map((span) => {
    const removed = removedBefore.get(span) ?? 0;
    return { ...span, start: span.start - removed, end: span.end - removed };
  });
  const domainStart = Math.min(...projected.map((span) => span.start));
  const spans = projected.map((span) => ({
    ...span,
    start: span.start - domainStart,
    end: span.end - domainStart,
  }));

  const firstSpanByTurn = new Map<string, number>();
  for (const span of spans) {
    if (!span.event.turnId) continue;
    const previous = firstSpanByTurn.get(span.event.turnId);
    if (previous === undefined || span.start < previous) firstSpanByTurn.set(span.event.turnId, span.start);
  }
  const turnBoundaries = [...firstSpanByTurn.entries()]
    .map(([turnId, time]) => ({ turnId, time }))
    .sort((left, right) => left.time - right.time);

  const researchAgents: TrajectoryResearchAgent[] = [];
  const knownAgents = new Set<string>();
  for (const event of events) {
    const id = researchAgentId(event);
    if (!id || knownAgents.has(id)) continue;
    knownAgents.add(id);
    researchAgents.push({ id, name: textValue(event.data?.agentName) || id });
  }

  return {
    start: 0,
    end: Math.max(...spans.map((span) => span.end), 1),
    spans,
    turnBoundaries,
    researchAgents,
    compressedIdleMs,
  };
}
