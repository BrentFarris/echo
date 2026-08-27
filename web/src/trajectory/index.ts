import { Virtualizer, elementScroll, observeElementOffset, observeElementRect } from "@tanstack/virtual-core";
import { get } from "../../js/api.js";
import { on as onSocket } from "../../js/ws.js";
import { escapeHTML, toast } from "../code/ui";
import {
  deriveTrajectoryTimeline,
  trajectoryLaneFor,
  type TrajectoryLane,
  type TrajectoryTimelineEvent,
} from "./timeline";

type TrajectoryEvent = TrajectoryTimelineEvent;

type TrajectoryPage = {
  header: { formatVersion: number; chatId: string; surface: string; createdAt: string };
  events: TrajectoryEvent[];
  incomplete?: boolean;
  warning?: string;
  hasMore: boolean;
  oldestSeq?: number;
  newestSeq?: number;
};

type SocketTrajectoryEvent = {
  type: "trajectory_event";
  workspaceId: string;
  surface: string;
  chatId: string;
  event: TrajectoryEvent;
};

type ProjectedRow = {
  key: string;
  event: TrajectoryEvent;
  lane: TrajectoryLane;
  title: string;
  summary: string;
  duration?: number;
  startedAt: string;
};

const semanticTypes = new Set([
  "legacy/import", "turn/start", "turn/end", "user/message", "request/start",
  "assistant/message", "tool/call", "tool/result", "transcript/delete",
  "transcript/edit", "transcript/rewind", "auxiliary/request", "auxiliary/result",
  "context/injection", "context/compression_queued", "context/compression_start",
  "context/compression_complete", "context/compression_skipped", "context/compression_error",
  "research/agent_created", "research/job_queued", "research/job_start", "research/job_end",
  "research/status", "research/request_start", "research/assistant_message",
  "research/tool_call", "research/tool_result", "research/report_delivered",
  "persistence/error",
]);

const viewScroll = new Map<string, number>();

export class TrajectoryView {
  private readonly abort = new AbortController();
  private readonly host: HTMLElement;
  private readonly scrollElement: HTMLElement;
  private readonly onViewChange: (view: "chat" | "trajectory") => void;
  private workspaceId = "";
  private chatId = "";
  private events: TrajectoryEvent[] = [];
  private rows: ProjectedRow[] = [];
  private selectedSequence = 0;
  private selectedInspectorTab = "summary";
  private includeChunks = false;
  private laneFilter = "all";
  private researchExpanded = true;
  private overviewTimer = 0;
  private searchTimer = 0;
  private loadSequence = 0;
  private page: TrajectoryPage | null = null;
  private virtualizer: Virtualizer<HTMLElement, HTMLElement>;
  private disposeVirtualizer: (() => void) | undefined;
  private socketUnsubscribers: Array<() => void> = [];

  constructor(host: HTMLElement, onViewChange: (view: "chat" | "trajectory") => void) {
    this.host = host;
    this.onViewChange = onViewChange;
    this.host.innerHTML = this.shell();
    this.scrollElement = this.host.querySelector<HTMLElement>("[data-trajectory-scroll]")!;
    this.virtualizer = new Virtualizer<HTMLElement, HTMLElement>({
      count: 0,
      getScrollElement: () => this.scrollElement,
      estimateSize: () => 58,
      getItemKey: (index) => this.rows[index]?.key || index,
      overscan: 12,
      observeElementRect,
      observeElementOffset,
      scrollToFn: elementScroll,
      onChange: () => this.renderRows(),
    });
    this.disposeVirtualizer = this.virtualizer._didMount();
    this.virtualizer._willUpdate();
    this.installEvents();
    this.socketUnsubscribers.push(onSocket("trajectory_event", (message) => this.onLiveEvent(message as SocketTrajectoryEvent)));
    this.socketUnsubscribers.push(onSocket("trajectory_error", (rawMessage) => {
      const message = rawMessage as Record<string, unknown>;
      if (message.workspaceId === this.workspaceId && message.chatId === this.chatId) {
        this.showWarning(String(message.error || "Trajectory logging is incomplete."));
      }
    }));
  }

  destroy(): void {
    this.saveScroll();
    this.abort.abort();
    this.socketUnsubscribers.forEach((unsubscribe) => unsubscribe());
    window.clearTimeout(this.searchTimer);
    window.clearTimeout(this.overviewTimer);
    this.disposeVirtualizer?.();
  }

  async setTarget(workspaceId: string, chatId: string): Promise<void> {
    if (workspaceId === this.workspaceId && chatId === this.chatId) {
      this.researchExpanded = true;
      this.renderOverview();
      return;
    }
    this.saveScroll();
    this.workspaceId = workspaceId;
    this.chatId = chatId;
    this.events = [];
    this.page = null;
    this.selectedSequence = 0;
    this.researchExpanded = true;
    window.clearTimeout(this.overviewTimer);
    this.host.querySelector<HTMLInputElement>("[data-trajectory-search]")!.value = "";
    if (!workspaceId || !chatId) {
      this.renderEmpty("Open a workspace chat to inspect its trajectory.");
      return;
    }
    await this.load();
  }

  refresh(): Promise<void> { return this.load(); }

  private targetKey(): string { return `${this.workspaceId}\0${this.chatId}`; }

  private saveScroll(): void {
    if (this.workspaceId && this.chatId) viewScroll.set(this.targetKey(), this.scrollElement.scrollTop);
  }

  private shell(): string {
    return `
      <div class="trajectory-controls">
        <div class="chat-view-switcher" role="tablist" aria-label="Chat view">
          <button type="button" role="tab" aria-selected="false" data-trajectory-view="chat">Chat</button>
          <button type="button" role="tab" aria-selected="true" class="is-active" data-trajectory-view="trajectory">Trajectory</button>
        </div>
        <header class="trajectory-header">
          <div><p class="trajectory-eyebrow">Append-only session log</p><h2>Trajectory</h2></div>
          <div class="trajectory-header-actions">
            <button type="button" data-trajectory-refresh title="Refresh trajectory">Refresh</button>
            <button type="button" data-trajectory-export title="Export raw JSONL, including exact private research prompts, reasoning, tool data, and provider chunks">Export</button>
          </div>
        </header>
        <div class="trajectory-warning" data-trajectory-warning hidden></div>
        <div class="trajectory-notice" data-trajectory-notice hidden></div>
        <section class="trajectory-overview" aria-label="Trajectory timing overview">
          <div class="trajectory-overview-labels" data-trajectory-overview-labels></div>
          <div class="trajectory-overview-plot">
            <div class="trajectory-overview-tracks" data-trajectory-overview></div>
            <div class="trajectory-overview-meta" data-trajectory-overview-meta></div>
          </div>
        </section>
        <div class="trajectory-toolbar">
          <label class="trajectory-search"><span class="codicon codicon-search" aria-hidden="true"></span><input type="search" placeholder="Search the entire trajectory" data-trajectory-search></label>
          <div class="trajectory-filters" role="group" aria-label="Filter trajectory source">
            ${["all", "input", "model", "tools", "system", "research"].map((lane) => `<button type="button" data-trajectory-lane="${lane}" class="${lane === "all" ? "is-active" : ""}">${lane === "all" ? "All" : lane[0].toUpperCase() + lane.slice(1)}</button>`).join("")}
            <button type="button" data-trajectory-chunks aria-pressed="false">Raw chunks</button>
          </div>
        </div>
        <div class="trajectory-status" data-trajectory-status role="status">Loading trajectory…</div>
      </div>
      <div class="trajectory-scroll-region" data-trajectory-scroll>
        <div class="trajectory-workspace">
          <section class="trajectory-list" aria-label="Trajectory events">
            <button type="button" class="trajectory-load-older" data-trajectory-older hidden>Load earlier turns</button>
            <div class="trajectory-canvas" data-trajectory-canvas></div>
          </section>
          <aside class="trajectory-inspector" data-trajectory-inspector aria-label="Event details">
            <div class="trajectory-inspector-empty">Select an event to inspect its exact record.</div>
          </aside>
        </div>
      </div>
    `;
  }

  private installEvents(): void {
    const signal = this.abort.signal;
    this.host.addEventListener("click", (event) => {
      const target = event.target as Element;
      const view = target.closest<HTMLElement>("[data-trajectory-view]")?.dataset.trajectoryView;
      if (view === "chat" || view === "trajectory") this.onViewChange(view);
      const lane = target.closest<HTMLElement>("[data-trajectory-lane]")?.dataset.trajectoryLane;
      if (lane) {
        this.setLaneFilter(lane);
        this.project();
      }
      if (target.closest("[data-trajectory-chunks]")) {
        this.includeChunks = !this.includeChunks;
        const button = this.host.querySelector<HTMLElement>("[data-trajectory-chunks]")!;
        button.classList.toggle("is-active", this.includeChunks);
        button.setAttribute("aria-pressed", String(this.includeChunks));
        this.project();
      }
      if (target.closest("[data-trajectory-research-toggle]")) {
        this.researchExpanded = !this.researchExpanded;
        this.renderOverview();
      }
      const agentId = target.closest<HTMLElement>("[data-trajectory-agent]")?.dataset.trajectoryAgent;
      if (agentId) this.navigateToAgent(agentId);
      const row = target.closest<HTMLElement>("[data-trajectory-sequence]");
      if (row) {
        const sequence = Number(row.dataset.trajectorySequence);
        if (row.closest("[data-trajectory-overview]")) this.navigateToSequence(sequence);
        else this.select(sequence);
      }
      const tab = target.closest<HTMLElement>("[data-inspector-tab]")?.dataset.inspectorTab;
      if (tab) {
        this.selectedInspectorTab = tab;
        this.renderInspector();
      }
      if (target.closest("[data-trajectory-refresh]")) void this.load();
      if (target.closest("[data-trajectory-export]")) this.export();
      if (target.closest("[data-trajectory-older]")) void this.loadOlder();
    }, { signal });
    this.host.querySelector<HTMLInputElement>("[data-trajectory-search]")!.addEventListener("input", (event) => {
      window.clearTimeout(this.searchTimer);
      const query = (event.currentTarget as HTMLInputElement).value.trim();
      this.searchTimer = window.setTimeout(() => { void (query ? this.search(query) : this.load()); }, 220);
    }, { signal });
  }

  private endpoint(suffix = ""): string {
    return `/api/workspaces/${encodeURIComponent(this.workspaceId)}/chats/${encodeURIComponent(this.chatId)}/trajectory${suffix}`;
  }

  private async load(): Promise<void> {
    const sequence = ++this.loadSequence;
    this.setStatus("Loading trajectory…");
    try {
      const page = await get(this.endpoint(), { query: { turnLimit: 20 } }) as TrajectoryPage;
      if (sequence !== this.loadSequence) return;
      this.page = page;
      this.events = page.events || [];
      this.showWarning(page.warning || (page.incomplete ? "The final log record was incomplete and has been ignored." : ""));
      this.project();
      requestAnimationFrame(() => { this.scrollElement.scrollTop = viewScroll.get(this.targetKey()) || 0; });
    } catch (error) {
      if (sequence !== this.loadSequence) return;
      this.renderEmpty(error instanceof Error ? error.message : String(error), true);
    }
  }

  private async loadOlder(): Promise<void> {
    if (!this.page?.hasMore || !this.page.oldestSeq) return;
    this.setStatus("Loading earlier turns…");
    try {
      const page = await get(this.endpoint(), { query: { beforeSeq: this.page.oldestSeq, turnLimit: 20 } }) as TrajectoryPage;
      const known = new Set(this.events.map((event) => event.sequence));
      this.events = [...page.events.filter((event) => !known.has(event.sequence)), ...this.events];
      this.page = { ...this.page, hasMore: page.hasMore, oldestSeq: page.oldestSeq };
      this.project();
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error), { sticky: true });
    }
  }

  private async search(query: string): Promise<void> {
    const sequence = ++this.loadSequence;
    this.setStatus(`Searching for “${query}”…`);
    try {
      const result = await get(this.endpoint("/search"), { query: { q: query, limit: 100 } }) as TrajectoryPage;
      if (sequence !== this.loadSequence) return;
      this.page = { ...result, hasMore: result.hasMore };
      this.events = result.events || [];
      this.project(true);
    } catch (error) {
      if (sequence === this.loadSequence) this.renderEmpty(error instanceof Error ? error.message : String(error), true);
    }
  }

  private export(): void {
    if (!this.workspaceId || !this.chatId) return;
    if (this.events.some((event) => event.type.startsWith("research/"))) {
      toast("Trajectory exports include exact private research prompts, reasoning, tool data, and provider chunks.", { sticky: true });
    }
    const anchor = document.createElement("a");
    anchor.href = this.endpoint("/export");
    anchor.download = `${this.chatId}-trajectory.jsonl`;
    document.body.append(anchor);
    anchor.click();
    anchor.remove();
  }

  private onLiveEvent(message: SocketTrajectoryEvent): void {
    if (message.workspaceId !== this.workspaceId || message.chatId !== this.chatId || message.surface !== "chat") return;
    if (this.events.some((event) => event.sequence === message.event.sequence)) return;
    const followTail = this.scrollElement.scrollHeight - this.scrollElement.scrollTop - this.scrollElement.clientHeight < 120;
    this.events.push(message.event);
    this.events.sort((left, right) => left.sequence - right.sequence);
    this.project();
    if (followTail) requestAnimationFrame(() => { this.scrollElement.scrollTop = this.scrollElement.scrollHeight; });
  }

  private project(searching = false): void {
    this.rows = this.events
      .filter((event) => event.type !== "research/chunk" || this.includeChunks)
      .filter((event) => searching || this.includeChunks || semanticTypes.has(event.type))
      .map((event) => this.rowFor(event))
      .filter((row) => this.laneFilter === "all" || row.lane === this.laneFilter);
    const older = this.host.querySelector<HTMLButtonElement>("[data-trajectory-older]")!;
    older.hidden = searching || !this.page?.hasMore;
    const canvas = this.host.querySelector<HTMLElement>("[data-trajectory-canvas]");
    const scrollMargin = canvas
      ? canvas.getBoundingClientRect().top - this.scrollElement.getBoundingClientRect().top + this.scrollElement.scrollTop
      : 0;
    this.virtualizer.setOptions({
      ...this.virtualizer.options,
      count: this.rows.length,
      getItemKey: (index) => this.rows[index]?.key || index,
      scrollMargin,
      scrollPaddingStart: 0,
    });
    this.virtualizer._willUpdate();
    this.renderOverview();
    this.showHistoricalResearchNotice(!searching && hasLegacyResearchGap(this.events));
    this.renderRows();
    this.setStatus(this.rows.length ? `${this.rows.length} event${this.rows.length === 1 ? "" : "s"}${searching ? " matched" : " loaded"}` : "No matching trajectory events.");
    if (this.selectedSequence && !this.events.some((event) => event.sequence === this.selectedSequence)) this.selectedSequence = 0;
    this.renderInspector();
  }

  private rowFor(event: TrajectoryEvent): ProjectedRow {
    const data = event.data || {};
    const lane = trajectoryLaneFor(event.type, data);
    let title = event.type;
    let summary = "";
    if (event.type === "user/message") { title = "User message"; summary = text(data.content); }
    else if (event.type === "request/start") { title = `Model request · step ${(event.step ?? 0) + 1}`; summary = text((data.request as Record<string, unknown>)?.model); }
    else if (event.type === "assistant/message") { title = `Assistant message · step ${(event.step ?? 0) + 1}`; summary = text(data.content) || "Tool calls only"; }
    else if (event.type === "tool/call") { title = `Tool · ${text(data.tool)}`; summary = text(data.arguments); }
    else if (event.type === "tool/result") { title = `Tool result · ${text(data.tool)}`; summary = data.success === false ? "Failed" : "Completed"; }
    else if (event.type === "turn/start") { title = "Turn started"; summary = text(data.origin); }
    else if (event.type === "turn/end") { title = `Turn ${text(data.status)}`; summary = text(data.error); }
    else if (event.type === "assistant/chunk") {
      const chunks = Array.isArray(data.streamEvents) ? data.streamEvents as Array<Record<string, unknown>> : [data];
      const first = (chunks[0]?.streamEvent || data.streamEvent) as Record<string, unknown> | undefined;
      title = `Raw ${text(first?.type) || "stream"} chunk${chunks.length === 1 ? "" : ` batch · ${chunks.length}`}`;
      summary = text(first?.content);
    }
    else if (event.type === "research/chunk") {
      const chunks = Array.isArray(data.streamEvents) ? data.streamEvents as Array<Record<string, unknown>> : [data];
      const first = (chunks[0]?.streamEvent || data.streamEvent) as Record<string, unknown> | undefined;
      title = `${researchName(data)} · Raw ${text(first?.type) || "stream"} chunk${chunks.length === 1 ? "" : ` batch · ${chunks.length}`}`;
      summary = text(first?.content);
    }
    else if (event.type === "legacy/import") { title = "Legacy transcript import"; summary = "Partial record reconstructed from the saved transcript."; }
    else if (event.type === "auxiliary/request") { title = "Auxiliary model request"; summary = text(data.operation); }
    else if (event.type === "auxiliary/result") { title = "Auxiliary model result"; summary = text(data.operation); }
    else if (event.type === "context/injection") { title = `Context · ${text(data.source)}`; summary = "Injected into the model request"; }
    else if (event.type === "context/compression_queued") { title = "Context compression queued"; summary = `${text(data.trigger) || "automatic"} · ${text(data.phase) || "boundary"}`; }
    else if (event.type === "context/compression_start") { title = `${lane === "research" ? `${researchName(data)} · ` : ""}Context compression started`; summary = `${text(data.model)} · ${text(data.thresholdPercent)}% threshold`; }
    else if (event.type === "context/compression_complete") { title = `${lane === "research" ? `${researchName(data)} · ` : ""}Context compressed`; summary = `${text(data.beforeTokens)} → ${text(data.afterTokens)} tokens · history recovery available`; }
    else if (event.type === "context/compression_skipped") { title = `${lane === "research" ? `${researchName(data)} · ` : ""}Context compression skipped`; summary = text(data.error) || "Nothing safe to compress"; }
    else if (event.type === "context/compression_error") { title = `${lane === "research" ? `${researchName(data)} · ` : ""}Context compression failed`; summary = text(data.error); }
    else if (event.type === "research/agent_created") { title = `Research agent · ${researchName(data)}`; summary = text(data.task); }
    else if (event.type === "research/job_queued") { title = `${researchName(data)} · Job ${jobNumber(data)} queued`; summary = text(data.prompt); }
    else if (event.type === "research/job_start") { title = `${researchName(data)} · Job ${jobNumber(data)} started`; summary = text(data.jobKind); }
    else if (event.type === "research/job_end") { title = `${researchName(data)} · Job ${jobNumber(data)} ${text(data.status)}`; summary = text(data.error) || `Research ${text(data.status)}`; }
    else if (event.type === "research/status") { title = `${researchName(data)} · ${text(data.status) || "status"}`; summary = text(data.phase) || text(data.error); }
    else if (event.type === "research/request_start") { title = `${researchName(data)} · Model request · round ${roundNumber(data)}`; summary = text(data.model) || text((data.request as Record<string, unknown>)?.model); }
    else if (event.type === "research/assistant_message") { title = `${researchName(data)} · Model response · round ${roundNumber(data)}`; summary = researchResponseSummary(data); }
    else if (event.type === "research/tool_call") { title = `${researchName(data)} · Tool · ${text(data.tool)}`; summary = text(data.arguments); }
    else if (event.type === "research/tool_result") { title = `${researchName(data)} · Tool result · ${text(data.tool)}`; summary = data.success === false ? "Failed" : "Completed"; }
    else if (event.type === "research/report_delivered") { title = `${researchName(data)} · Report delivered`; summary = `Report ${text(data.reportSequence)}`; }
    else if (event.type === "persistence/error") { title = "Persistence error"; summary = text(data.error); }
    else if (event.type.startsWith("transcript/")) { title = event.type.replace("transcript/", "Transcript "); summary = text(data.role); }
    const duration = numberValue(data.durationMs);
    let startedAt = text(data.startedAt) || event.timestamp;
    if (!data.startedAt && duration !== undefined && typeof data.completedAt === "string") {
      const completed = Date.parse(data.completedAt);
      if (Number.isFinite(completed)) startedAt = new Date(completed - duration).toISOString();
    }
    return { key: String(event.sequence), event, lane, title, summary: summary.slice(0, 260), duration, startedAt };
  }

  private renderRows(): void {
    const canvas = this.host.querySelector<HTMLElement>("[data-trajectory-canvas]");
    if (!canvas) return;
    const items = this.virtualizer.getVirtualItems();
    const margin = this.virtualizer.options.scrollMargin || 0;
    canvas.style.height = `${this.virtualizer.getTotalSize()}px`;
    canvas.innerHTML = items.map((virtual) => {
      const row = this.rows[virtual.index];
      if (!row) return "";
      return `<button type="button" class="trajectory-row lane-${row.lane}${row.event.sequence === this.selectedSequence ? " is-selected" : ""}" data-index="${virtual.index}" data-trajectory-sequence="${row.event.sequence}" style="transform:translateY(${virtual.start - margin}px)">
        <span class="trajectory-row-dot" aria-hidden="true"></span>
        <span class="trajectory-row-kind">${escapeHTML(row.lane)}</span>
        <span class="trajectory-row-main"><strong>${escapeHTML(row.title)}</strong><span>${escapeHTML(row.summary || `Sequence ${row.event.sequence}`)}</span></span>
        <span class="trajectory-row-meta"><time>${escapeHTML(formatClock(row.event.timestamp))}</time>${row.duration !== undefined ? `<small>${formatDuration(row.duration)}</small>` : ""}</span>
      </button>`;
    }).join("");
    canvas.querySelectorAll<HTMLElement>("[data-index]").forEach((element) => this.virtualizer.measureElement(element));
  }

  private renderOverview(): void {
    const overview = this.host.querySelector<HTMLElement>("[data-trajectory-overview]");
    const labels = this.host.querySelector<HTMLElement>("[data-trajectory-overview-labels]");
    const meta = this.host.querySelector<HTMLElement>("[data-trajectory-overview-meta]");
    window.clearTimeout(this.overviewTimer);
    this.overviewTimer = 0;
    if (!overview || !labels) return;
    const model = deriveTrajectoryTimeline(this.events, Date.now());
    if (!model) {
      overview.innerHTML = `<div class="trajectory-overview-empty">No timing records yet</div>`;
      labels.innerHTML = "";
      if (meta) meta.textContent = "";
      return;
    }
    const duration = Math.max(1, model.end - model.start);
    const boundaries = model.turnBoundaries.map((boundary) => {
      const left = ((boundary.time - model.start) / duration) * 100;
      return `<span class="trajectory-turn-boundary" style="left:${Math.min(100, Math.max(0, left))}%" title="${escapeHTML(boundary.turnId)}" aria-hidden="true"></span>`;
    }).join("");

    const tracks: Array<{ key: string; label: string; lane: TrajectoryLane | "group"; agentId?: string }> = [
      { key: "input", label: "Input", lane: "input" },
      { key: "model", label: "Model", lane: "model" },
      { key: "tools", label: "Tools", lane: "tools" },
      { key: "system", label: "System", lane: "system" },
    ];
    if (model.researchAgents.length) {
      if (this.researchExpanded) {
        tracks.push({ key: "research-group", label: `Research · ${model.researchAgents.length}`, lane: "group" });
        for (const agent of model.researchAgents) tracks.push({ key: `research:${agent.id}`, label: agent.name, lane: "research", agentId: agent.id });
      } else {
        tracks.push({ key: "research", label: `Research · ${model.researchAgents.length}`, lane: "research" });
      }
    }
    labels.innerHTML = tracks.map((track) => {
      if (track.lane === "group" || track.key === "research") {
        const symbol = this.researchExpanded ? "▾" : "▸";
        return `<button type="button" class="trajectory-research-toggle" data-trajectory-research-toggle aria-expanded="${this.researchExpanded}">${symbol} ${escapeHTML(track.label)}</button>`;
      }
      return `<span class="${track.agentId ? "is-research-agent" : ""}" title="${escapeHTML(track.label)}">${escapeHTML(track.label)}</span>`;
    }).join("");

    overview.innerHTML = tracks.map((track) => {
      if (track.lane === "group") return `<div class="trajectory-track is-group">${boundaries}</div>`;
      const trackSpans = model.spans.filter((span) => track.key === "research"
        ? span.lane === "research"
        : span.trackKey === track.key).sort((left, right) => Number(left.kind !== "job") - Number(right.kind !== "job"));
      return `<div class="trajectory-track lane-${track.lane}${track.agentId ? " is-research-agent" : ""}">${boundaries}${trackSpans.map((span) => {
      const row = this.rowFor(span.event);
      const left = ((span.start - model.start) / duration) * 100;
      const measuredWidth = ((span.end - span.start) / duration) * 100;
      const markerWidth = 0.55;
      const width = Math.max(markerWidth, measuredWidth);
      const positionedLeft = Math.min(Math.max(0, left), Math.max(0, 100 - width));
      const classes = [`kind-${span.kind}`, span.end === span.start ? "is-instant" : "", span.pending ? "is-pending" : "", span.event.sequence === this.selectedSequence ? "is-selected" : ""].filter(Boolean).join(" ");
      const timing = span.end > span.start ? ` · ${formatDuration(span.end - span.start)}` : "";
      return `<button type="button" class="${classes}" style="left:${positionedLeft}%;width:${Math.min(width, 100)}%" data-trajectory-sequence="${span.event.sequence}" title="${escapeHTML(row.title + timing)}" aria-label="${escapeHTML(row.title + timing)}" aria-pressed="${span.event.sequence === this.selectedSequence}"></button>`;
      }).join("")}</div>`;
    }).join("");
    if (meta) {
      const active = formatDuration(duration);
      const collapsed = model.compressedIdleMs > 0 ? ` · ${formatDuration(model.compressedIdleMs)} idle collapsed` : "";
      const turns = model.turnBoundaries.length ? ` · ${model.turnBoundaries.length} turn${model.turnBoundaries.length === 1 ? "" : "s"}` : "";
      meta.textContent = `Active time ${active}${collapsed}${turns}`;
    }
    if (model.spans.some((span) => span.pending)) {
      this.overviewTimer = window.setTimeout(() => { this.overviewTimer = 0; this.renderOverview(); }, 1000);
    }
  }

  private select(sequence: number): void {
    this.selectedSequence = sequence;
    this.host.querySelectorAll<HTMLElement>("[data-trajectory-overview] [data-trajectory-sequence]").forEach((element) => {
      const selected = Number(element.dataset.trajectorySequence) === sequence;
      element.classList.toggle("is-selected", selected);
      element.setAttribute("aria-pressed", String(selected));
    });
    this.renderRows();
    this.renderInspector();
  }

  private setLaneFilter(lane: string): void {
    this.laneFilter = lane;
    this.host.querySelectorAll<HTMLElement>("[data-trajectory-lane]").forEach((button) => {
      button.classList.toggle("is-active", button.dataset.trajectoryLane === lane);
    });
  }

  private navigateToSequence(sequence: number): void {
    this.select(sequence);
    let index = this.rows.findIndex((row) => row.event.sequence === sequence);
    if (index < 0 && this.laneFilter !== "all") {
      this.setLaneFilter("all");
      this.project();
      index = this.rows.findIndex((row) => row.event.sequence === sequence);
    }
    if (index < 0) return;
    this.virtualizer.scrollToIndex(index, { align: "start", behavior: "auto" });
  }

  private navigateToAgent(agentId: string): void {
    const event = this.events.find((candidate) => candidate.data?.agentId === agentId && candidate.type.startsWith("research/"));
    if (!event) return;
    if (!this.researchExpanded) this.researchExpanded = true;
    this.navigateToSequence(event.sequence);
  }

  private renderInspector(): void {
    const inspector = this.host.querySelector<HTMLElement>("[data-trajectory-inspector]");
    if (!inspector) return;
    const event = this.events.find((candidate) => candidate.sequence === this.selectedSequence);
    if (!event) { inspector.innerHTML = `<div class="trajectory-inspector-empty">Select an event to inspect its exact record.</div>`; return; }
    const row = this.rowFor(event);
    const tabs = ["summary", "payload", "result", "schema", "timing"];
    const content = inspectorValue(event, this.selectedInspectorTab, this.events);
    inspector.innerHTML = `
      <div class="trajectory-inspector-heading"><span class="trajectory-source lane-${row.lane}">${escapeHTML(row.lane)}</span><strong>${escapeHTML(row.title)}</strong><small>Sequence ${event.sequence}</small></div>
      <div class="trajectory-inspector-tabs" role="tablist">${tabs.map((tab) => `<button type="button" role="tab" aria-selected="${this.selectedInspectorTab === tab}" class="${this.selectedInspectorTab === tab ? "is-active" : ""}" data-inspector-tab="${tab}">${tab[0].toUpperCase() + tab.slice(1)}</button>`).join("")}</div>
      <div class="trajectory-inspector-content">${this.selectedInspectorTab === "summary" ? summaryHTML(event, row, this.events) : `<pre>${escapeHTML(JSON.stringify(content, null, 2) || "No data for this record.")}</pre>`}</div>
    `;
  }

  private renderEmpty(message: string, error = false): void {
    this.events = [];
    this.rows = [];
    this.virtualizer.setOptions({ ...this.virtualizer.options, count: 0 });
    this.virtualizer._willUpdate();
    const canvas = this.host.querySelector<HTMLElement>("[data-trajectory-canvas]");
    if (canvas) { canvas.style.height = "180px"; canvas.innerHTML = `<div class="trajectory-empty${error ? " is-error" : ""}">${escapeHTML(message)}</div>`; }
    this.setStatus(message);
    this.showHistoricalResearchNotice(false);
    this.renderOverview();
    this.renderInspector();
  }

  private setStatus(message: string): void {
    const status = this.host.querySelector<HTMLElement>("[data-trajectory-status]");
    if (status) status.textContent = message;
  }

  private showWarning(message: string): void {
    const warning = this.host.querySelector<HTMLElement>("[data-trajectory-warning]");
    if (!warning) return;
    warning.hidden = !message;
    warning.textContent = message;
  }

  private showHistoricalResearchNotice(show: boolean): void {
    const notice = this.host.querySelector<HTMLElement>("[data-trajectory-notice]");
    if (!notice) return;
    notice.hidden = !show;
    notice.textContent = show ? "This historical run used research agents before agent-level trajectory capture was available." : "";
  }
}

function text(value: unknown): string {
  if (typeof value === "string") return value;
  if (value === null || value === undefined) return "";
  try { return JSON.stringify(value); } catch { return String(value); }
}

function researchName(data: Record<string, unknown>): string {
  return text(data.agentName) || text(data.agentId) || "Researcher";
}

function jobNumber(data: Record<string, unknown>): string {
  return text(data.jobNumber) || "?";
}

function roundNumber(data: Record<string, unknown>): string {
  const round = numberValue(data.round);
  return round === undefined ? "?" : String(round + 1);
}

function researchResponseSummary(data: Record<string, unknown>): string {
  if (data.streamError) return text(data.streamError);
  const usage = data.usage as Record<string, unknown> | undefined;
  const tokens = numberValue(usage?.total_tokens) ?? numberValue(usage?.totalTokens);
  const finish = text(data.finishReason) || "completed";
  return tokens === undefined ? finish : `${finish} · ${tokens} tokens`;
}

function isResearchOrchestration(event: TrajectoryEvent): boolean {
  return (event.type === "tool/call" || event.type === "tool/result")
    && text(event.data?.tool).startsWith("research_agent");
}

function hasLegacyResearchGap(events: readonly TrajectoryEvent[]): boolean {
  return events.some(isResearchOrchestration) && !events.some((event) => event.type.startsWith("research/"));
}

function numberValue(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function formatClock(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3 });
}

function formatDuration(milliseconds: number): string {
  if (milliseconds < 1000) return `${Math.max(0, milliseconds)} ms`;
  if (milliseconds < 60_000) return `${(milliseconds / 1000).toFixed(milliseconds < 10_000 ? 2 : 1)} s`;
  return `${Math.floor(milliseconds / 60_000)}m ${Math.round((milliseconds % 60_000) / 1000)}s`;
}

function matchingOperationEvent(event: TrajectoryEvent, events: readonly TrajectoryEvent[], type: string): TrajectoryEvent | undefined {
  if (event.type.startsWith("context/compression")) {
    const compressionId = event.data?.compressionId;
    if (typeof compressionId !== "string" || !compressionId) return undefined;
    return events.find((candidate) => candidate.type === type && candidate.data?.compressionId === compressionId);
  }
  if (event.type.startsWith("tool/")) {
    const callId = event.data?.callId;
    const callOrder = event.data?.callOrder;
    if (typeof callId === "string" && callId) {
      return events.find((candidate) => candidate.type === type
        && candidate.turnId === event.turnId && candidate.data?.callId === callId);
    }
    if (typeof callOrder !== "number" && typeof callOrder !== "string") return undefined;
    return events.find((candidate) => candidate.type === type
      && candidate.turnId === event.turnId && candidate.step === event.step
      && candidate.data?.callOrder === callOrder);
  }
  if (event.type === "research/request_start" || event.type === "research/assistant_message") {
    return events.find((candidate) => candidate.type === type && candidate.turnId === event.turnId
      && candidate.data?.agentId === event.data?.agentId && candidate.data?.jobId === event.data?.jobId
      && candidate.data?.round === event.data?.round);
  }
  if (event.type === "research/tool_call" || event.type === "research/tool_result") {
    return events.find((candidate) => candidate.type === type && candidate.turnId === event.turnId
      && candidate.data?.agentId === event.data?.agentId && candidate.data?.jobId === event.data?.jobId
      && candidate.data?.round === event.data?.round && candidate.data?.callId === event.data?.callId);
  }
  if (event.type === "research/job_start" || event.type === "research/job_end" || event.type === "research/job_queued") {
    return events.find((candidate) => candidate.type === type && candidate.turnId === event.turnId
      && candidate.data?.agentId === event.data?.agentId && candidate.data?.jobId === event.data?.jobId);
  }
  return events.find((candidate) => candidate.type === type
    && candidate.turnId === event.turnId
    && (candidate.step ?? 0) === (event.step ?? 0));
}

function inspectorValue(event: TrajectoryEvent, tab: string, events: readonly TrajectoryEvent[]): unknown {
  const data = event.data || {};
  const researchRequest = event.type === "research/request_start" || event.type === "research/assistant_message";
  const researchTool = event.type === "research/tool_call" || event.type === "research/tool_result";
  const requestEvent = event.type === "assistant/message" || event.type === "research/assistant_message"
    ? matchingOperationEvent(event, events, researchRequest ? "research/request_start" : "request/start")
    : event.type === "request/start" || event.type === "research/request_start" ? event : undefined;
  const assistantEvent = event.type === "request/start" || event.type === "research/request_start"
    ? matchingOperationEvent(event, events, researchRequest ? "research/assistant_message" : "assistant/message")
    : event.type === "assistant/message" || event.type === "research/assistant_message" ? event : undefined;
  const callEvent = event.type === "tool/result" || event.type === "research/tool_result"
    ? matchingOperationEvent(event, events, researchTool ? "research/tool_call" : "tool/call")
    : event.type === "tool/call" || event.type === "research/tool_call" ? event : undefined;
  const resultEvent = event.type === "tool/call" || event.type === "research/tool_call"
    ? matchingOperationEvent(event, events, researchTool ? "research/tool_result" : "tool/result")
    : event.type === "tool/result" || event.type === "research/tool_result" ? event : undefined;
  const jobStartEvent = event.type === "research/job_end"
    ? matchingOperationEvent(event, events, "research/job_start") || matchingOperationEvent(event, events, "research/job_queued")
    : event.type === "research/job_start" || event.type === "research/job_queued" ? event : undefined;
  const jobEndEvent = event.type === "research/job_start" || event.type === "research/job_queued"
    ? matchingOperationEvent(event, events, "research/job_end")
    : event.type === "research/job_end" ? event : undefined;
  const compressionStartEvent = ["context/compression_complete", "context/compression_skipped", "context/compression_error"].includes(event.type)
    ? matchingOperationEvent(event, events, "context/compression_start")
    : event.type === "context/compression_start" ? event : undefined;
  const compressionResultEvent = event.type === "context/compression_start"
    ? matchingOperationEvent(event, events, "context/compression_complete")
      || matchingOperationEvent(event, events, "context/compression_skipped")
      || matchingOperationEvent(event, events, "context/compression_error")
    : ["context/compression_complete", "context/compression_skipped", "context/compression_error"].includes(event.type) ? event : undefined;
  if (tab === "payload") {
    if (requestEvent) return requestEvent.data?.request;
    if (callEvent) return { arguments: callEvent.data?.arguments, planQuestions: callEvent.data?.planQuestions };
    if (jobStartEvent) return jobStartEvent.data;
    if (compressionStartEvent) return compressionStartEvent.data;
    if (event.type === "assistant/chunk") return data.streamEvents || data.streamEvent;
    return data;
  }
  if (tab === "result") {
    if (resultEvent) return resultEvent.data?.result;
    if (jobEndEvent) return jobEndEvent.data;
    if (compressionResultEvent) return compressionResultEvent.data;
    if (assistantEvent) {
      const assistant = assistantEvent.data || {};
      return { content: assistant.content, reasoning: assistant.reasoning, toolCalls: assistant.toolCalls, finishReason: assistant.finishReason, usage: assistant.usage };
    }
    return null;
  }
  if (tab === "schema") return requestEvent ? (requestEvent.data?.request as Record<string, unknown>)?.tools : null;
  if (tab === "timing") {
    const timing = { ...(requestEvent?.data || callEvent?.data || jobStartEvent?.data || compressionStartEvent?.data || {}), ...(assistantEvent?.data || resultEvent?.data || jobEndEvent?.data || compressionResultEvent?.data || data) };
    return Object.fromEntries(Object.entries(timing).filter(([key]) => /(?:At|Ms|usage|duration|ttft)/i.test(key)));
  }
  return event;
}

function summaryHTML(event: TrajectoryEvent, row: ProjectedRow, events: readonly TrajectoryEvent[]): string {
  const data = event.data || {};
  const hierarchy = event.turnId ? [`Turn ${event.turnId}`] : ["Session"];
  if (data.agentId) hierarchy.push(`Agent ${researchName(data)} (${text(data.agentId)})`);
  if (data.jobNumber !== undefined) hierarchy.push(`Job ${text(data.jobNumber)}`);
  if (data.round !== undefined) hierarchy.push(`Round ${roundNumber(data)}`);
  else if (event.step !== undefined) hierarchy.push(`Step ${event.step + 1}`);
  const entries: Array<[string, unknown]> = [
    ["Hierarchy", hierarchy.join(" · ")],
    ["Type", event.type], ["Source", row.lane], ["Recorded", event.timestamp],
  ];
  if (data.status !== undefined) entries.push(["Status", data.status]);
  if (data.model !== undefined) entries.push(["Model", data.model]);
  if (data.durationMs !== undefined) entries.push(["Duration", formatDuration(Number(data.durationMs))]);
  if (data.ttftMs !== undefined && data.ttftMs !== null) entries.push(["Time to first token", formatDuration(Number(data.ttftMs))]);
  const related = relatedResearchAgents(event, events);
  const relatedHTML = related.length ? `<div class="trajectory-related-agents"><strong>Related agents</strong>${related.map((agent) => `<button type="button" data-trajectory-agent="${escapeHTML(agent.id)}">${escapeHTML(agent.name)}</button>`).join("")}</div>` : "";
  return `<dl>${entries.map(([key, value]) => `<div><dt>${escapeHTML(key)}</dt><dd>${escapeHTML(text(value))}</dd></div>`).join("")}</dl>${row.summary ? `<p>${escapeHTML(row.summary)}</p>` : ""}${relatedHTML}`;
}

function relatedResearchAgents(event: TrajectoryEvent, events: readonly TrajectoryEvent[]): Array<{ id: string; name: string }> {
  if (!isResearchOrchestration(event)) return [];
  const counterpart = event.type === "tool/call"
    ? matchingOperationEvent(event, events, "tool/result")
    : matchingOperationEvent(event, events, "tool/call");
  const encoded = JSON.stringify([event.data, counterpart?.data]);
  const agents = new Map<string, string>();
  for (const candidate of events) {
    const id = text(candidate.data?.agentId);
    if (id && encoded.includes(id) && !agents.has(id)) agents.set(id, text(candidate.data?.agentName) || id);
  }
  return [...agents].map(([id, name]) => ({ id, name }));
}
