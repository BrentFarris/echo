import { api } from "../../js/api.js";
import { on as onSocket, onState as onSocketState, send as sendSocket } from "../../js/ws.js";
import {
  openWorkbenchPanel, registerWorkbenchPanel, subscribeTerminalWorkspace,
} from "../terminal";
import {
  listTerminalSessions, stopTerminal, syncTerminal, type TerminalEvent, type TerminalSnapshot,
} from "../terminal/terminalApi";
import { decodeTerminalBase64, terminalSequenceAction } from "../terminal/terminalUtils";
import { escapeHTML, toast } from "./ui";

const panelId = "test-output";

export class TestOutput {
  private readonly workspaceId: string;
  private readonly unregisterPanel: () => void;
  private readonly unsubscribe: Array<() => void> = [];
  private host: HTMLElement | null = null;
  private sessionId = "";
  private output = "";
  private decoder = new TextDecoder();
  private lastSequence = 0;
  private clearedThrough = 0;
  private taskStatus: TerminalSnapshot["taskStatus"] = undefined;
  private exitCode: number | undefined;
  private message = "";
  private syncPromise: Promise<void> | null = null;

  constructor(workspaceId: string, private readonly beforeRun: () => Promise<boolean>) {
    this.workspaceId = workspaceId;
    this.unregisterPanel = registerWorkbenchPanel(workspaceId, {
      id: panelId, label: "Test Output", icon: "beaker",
      mount: (host) => { this.host = host; this.render(); },
      unmount: () => { this.host = null; },
    });
    this.unsubscribe.push(onSocket("terminal_event", (raw: object) => this.receiveEvent(raw as TerminalEvent)));
    this.unsubscribe.push(onSocket("terminal_subscribed", (raw: object) => {
      if ((raw as { workspaceId?: string }).workspaceId === this.workspaceId) void this.restore();
    }));
    this.unsubscribe.push(onSocketState((state) => {
      if (state === "open") {
        sendSocket({ type: "terminal_subscribe", workspaceId: this.workspaceId });
      }
    }));
    void subscribeTerminalWorkspace(workspaceId).then(() => this.restore());
  }

  dispose(): void {
    this.unregisterPanel();
    for (const unsubscribe of this.unsubscribe) unsubscribe();
    this.host = null;
  }

  adopt(snapshot: TerminalSnapshot, open = true): void {
    const changed = snapshot.id !== this.sessionId;
    if (changed) {
      this.sessionId = snapshot.id;
      this.output = "";
      this.decoder = new TextDecoder();
      this.lastSequence = 0;
      this.clearedThrough = 0;
    } else if (snapshot.reset) {
      this.output = "";
      this.decoder = new TextDecoder();
      this.lastSequence = 0;
    }
    for (const chunk of [...(snapshot.output || [])].sort((left, right) => left.sequence - right.sequence)) {
      if (chunk.sequence <= this.lastSequence || chunk.sequence <= this.clearedThrough) continue;
      this.appendOutput(chunk.data);
      this.lastSequence = chunk.sequence;
    }
    this.lastSequence = Math.max(this.lastSequence, snapshot.lastSequence || 0);
    this.taskStatus = snapshot.taskStatus || (snapshot.status === "running" ? "running" : snapshot.exitCode === 0 ? "passed" : "failed");
    this.exitCode = snapshot.exitCode;
    this.message = snapshot.message || "";
    if (snapshot.status === "exited") this.output += this.decoder.decode();
    this.render();
    if (open) openWorkbenchPanel(this.workspaceId, panelId, true);
  }

  private receiveEvent(event: TerminalEvent): void {
    if (event.workspaceId !== this.workspaceId || event.kind !== "test") return;
    if (event.event === "started") {
      if (event.sessionId !== this.sessionId) {
        this.sessionId = event.sessionId;
        this.output = "";
        this.decoder = new TextDecoder();
        this.lastSequence = 0;
        this.clearedThrough = 0;
      }
      this.taskStatus = "running";
      this.exitCode = undefined;
      this.message = "";
      this.render();
      openWorkbenchPanel(this.workspaceId, panelId);
      return;
    }
    if (!this.sessionId || event.sessionId !== this.sessionId) return;
    if (event.event === "data") {
      const sequence = Number(event.sequence || 0);
      const action = terminalSequenceAction(this.lastSequence, sequence);
      if (action === "ignore") return;
      if (action === "resync") {
        void this.sync();
        return;
      }
      if (sequence > this.clearedThrough) this.appendOutput(event.data || "");
      this.lastSequence = sequence;
      this.render();
      return;
    }
    if (event.event === "exited") {
      this.output += this.decoder.decode();
      this.taskStatus = event.taskStatus || (event.exitCode === 0 ? "passed" : "failed");
      this.exitCode = event.exitCode;
      this.message = event.message || "";
      this.lastSequence = Math.max(this.lastSequence, Number(event.sequence || 0));
      this.render();
    }
  }

  private appendOutput(data: string): void {
    this.output += this.decoder.decode(decodeTerminalBase64(data), { stream: true });
  }

  private async restore(): Promise<void> {
    try {
      if (this.sessionId) {
        await this.sync();
        return;
      }
      const sessions = await listTerminalSessions(this.workspaceId);
      const snapshot = sessions.find((candidate) => candidate.kind === "test");
      if (snapshot) this.adopt(snapshot, false);
    } catch (error) {
      toast(`Could not restore test output: ${errorMessage(error)}`);
    }
  }

  private async sync(): Promise<void> {
    if (!this.sessionId) return;
    if (this.syncPromise) return this.syncPromise;
    this.syncPromise = syncTerminal(this.workspaceId, this.sessionId, this.lastSequence)
      .then((snapshot) => this.adopt(snapshot, false))
      .catch((error) => toast(`Could not synchronize test output: ${errorMessage(error)}`))
      .finally(() => { this.syncPromise = null; });
    return this.syncPromise;
  }

  private render(): void {
    if (!this.host) return;
    const output = this.host.querySelector<HTMLElement>("[data-test-output-text]");
    const follow = !output || output.scrollHeight - output.scrollTop - output.clientHeight < 32;
    const status = this.taskStatus || "idle";
    const statusLabel = status === "running" ? "Running" : status === "passed" ? "Passed" : status === "failed" ? "Failed" : status === "stopped" ? "Stopped" : "Ready";
    this.host.innerHTML = `<section class="test-output go-test-output">
      <header><span class="test-status go-test-status is-${escapeHTML(status)}"><span></span>${statusLabel}</span><div>
        <button type="button" data-test-output-action="clear">Clear</button>
        <button type="button" data-test-output-action="rerun" ${this.sessionId && status !== "running" ? "" : "disabled"}>Rerun</button>
        <button type="button" data-test-output-action="stop" ${status === "running" ? "" : "disabled"}>Stop</button>
      </div></header>
      <pre tabindex="0" data-test-output-text>${escapeHTML(stripANSI(this.output))}</pre>
      ${this.message ? `<footer>${escapeHTML(this.message)}</footer>` : ""}
      ${this.exitCode !== undefined && status !== "running" ? `<span class="test-exit go-test-exit">Exit code ${this.exitCode}</span>` : ""}
    </section>`;
    this.host.onclick = (event) => {
      const button = (event.target as Element).closest<HTMLButtonElement>("[data-test-output-action]");
      if (!button || button.disabled) return;
      if (button.dataset.testOutputAction === "clear") {
        this.output = "";
        this.clearedThrough = this.lastSequence;
        this.render();
      } else if (button.dataset.testOutputAction === "stop") {
        void this.stop();
      } else if (button.dataset.testOutputAction === "rerun") {
        void this.rerun();
      }
    };
    const next = this.host.querySelector<HTMLElement>("[data-test-output-text]");
    if (next && follow) next.scrollTop = next.scrollHeight;
  }

  private async stop(): Promise<void> {
    if (!this.sessionId) return;
    this.taskStatus = "stopped";
    this.render();
    try {
      await stopTerminal(this.workspaceId, this.sessionId);
    } catch (error) {
      toast(errorMessage(error), { sticky: true });
      await this.sync();
    }
  }

  private async rerun(): Promise<void> {
    if (!this.sessionId) return;
    if (!(await this.beforeRun())) return;
    try {
      const result = await api(`/api/workspaces/${encodeURIComponent(this.workspaceId)}/testing/runs/${encodeURIComponent(this.sessionId)}/rerun`, { method: "POST", body: {} }) as { session: TerminalSnapshot };
      this.adopt(result.session);
    } catch (error) {
      toast(errorMessage(error), { sticky: true });
    }
  }
}

// Keep the historical export for extensions that imported the Go-only name.
export { TestOutput as GoTestOutput };

function stripANSI(value: string): string {
  return value.replace(/[\u001B\u009B][[\]()#;?]*(?:(?:(?:;[-a-zA-Z\d\/#&.:=?%@~_]+)*|[a-zA-Z\d]+(?:;[-a-zA-Z\d\/#&.:=?%@~_]*)*)?\u0007|(?:(?:\d{1,4}(?:[;:]\d{0,4})*)?[\dA-PR-TZcf-nq-uy=><~]))/g, "");
}

function errorMessage(error: unknown): string { return error instanceof Error ? error.message : String(error); }
