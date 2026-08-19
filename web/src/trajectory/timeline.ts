export type TrajectoryTimelineEvent = {
  record: "event";
  sequence: number;
  timestamp: string;
  type: string;
  turnId?: string;
  step?: number;
  data?: Record<string, unknown>;
};

export type TrajectoryLane = "input" | "model" | "tools" | "system";

export type TrajectoryTimelineSpan = {
  event: TrajectoryTimelineEvent;
  lane: TrajectoryLane;
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
  compressedIdleMs: number;
};

const overviewTypes = new Set([
  "legacy/import", "user/message", "request/start", "assistant/message",
  "tool/call", "tool/result", "transcript/delete", "transcript/edit",
  "transcript/rewind", "auxiliary/request", "auxiliary/result",
  "context/injection", "persistence/error",
]);

export function trajectoryLaneFor(type: string): TrajectoryLane {
  if (type.startsWith("user/")) return "input";
  if (type.startsWith("assistant/") || type.startsWith("request/") || type.startsWith("auxiliary/")) return "model";
  if (type.startsWith("tool/")) return "tools";
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

/**
 * Build the compact Duration projection used by the trajectory overview.
 * Completed request/result pairs become one operation and wall-clock gaps with
 * no recorded activity are removed from the horizontal domain.
 */
export function deriveTrajectoryTimeline(
  events: readonly TrajectoryTimelineEvent[],
): TrajectoryTimelineModel | null {
  const requestStarts = new Map<string, TrajectoryTimelineEvent>();
  const completedRequests = new Set<string>();
  const toolCalls = new Map<string, TrajectoryTimelineEvent>();
  const completedTools = new Set<string>();

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
    }
  }

  const rawSpans: TrajectoryTimelineSpan[] = [];
  for (const event of events) {
    if (!overviewTypes.has(event.type)) continue;
    const key = event.type.startsWith("tool/") ? toolKey(event) : requestKey(event);
    if (event.type === "request/start" && key !== null && completedRequests.has(key)) continue;
    if (event.type === "tool/call" && key !== null && completedTools.has(key)) continue;

    let range: { start: number; end: number } | null;
    if (event.type === "assistant/message") {
      range = operationRange(event, key === null ? undefined : requestStarts.get(key));
    } else if (event.type === "tool/result") {
      range = operationRange(event, key === null ? undefined : toolCalls.get(key));
    } else if (durationOf(event) !== undefined || parseTime(event.data?.completedAt) !== undefined) {
      range = operationRange(event);
    } else {
      range = instantRange(event);
    }
    if (range === null) continue;
    rawSpans.push({
      event,
      lane: trajectoryLaneFor(event.type),
      ...range,
      pending: event.type === "request/start" || event.type === "tool/call",
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

  return {
    start: 0,
    end: Math.max(...spans.map((span) => span.end), 1),
    spans,
    turnBoundaries,
    compressedIdleMs,
  };
}
