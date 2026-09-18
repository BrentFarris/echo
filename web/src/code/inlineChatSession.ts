import type { EditorContextSelection } from "../chatSurface";
import type { ChatReference, ComposerSegment } from "../chatMentions";
import type { FileRef } from "./types";

export type InlineChatFile = { title: string; ref?: FileRef; content: string; selections: EditorContextSelection[] };
export type InlineChatChange = { startLine: number; endLine: number; newLines: string[] };
export type InlineChatMessage = { role: "user" | "assistant"; content: string };
export type InlineChatReference = Omit<ChatReference, "workspaceId">;
export type InlineChatRequest = {
  message: string; model?: string; file: InlineChatFile; proposal?: string;
  references: InlineChatReference[]; history: InlineChatMessage[];
};
export type InlineChatEvent =
  | { type: "delta"; text: string }
  | { type: "proposal"; content: string; changes?: InlineChatChange[] }
  | { type: "done" }
  | { type: "error"; text: string };

export async function streamInlineChat(workspaceId: string, request: InlineChatRequest, signal: AbortSignal, receive: (event: InlineChatEvent) => void): Promise<void> {
  const response = await fetch(`/api/workspaces/${encodeURIComponent(workspaceId)}/inline-chat`, {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(request), signal,
  });
  if (!response.ok) {
    if (response.status === 401) window.dispatchEvent(new CustomEvent("echo:unauthorized"));
    const payload = await response.json().catch(() => null);
    throw new Error(payload?.error || `Inline chat failed (HTTP ${response.status}).`);
  }
  if (!response.body) throw new Error("Inline chat did not return a response stream.");
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let pending = "", completed = false;
  const consume = (line: string) => {
    if (!line.trim() || signal.aborted) return;
    if (completed) throw new Error("Unexpected data after inline chat completed.");
    const event = JSON.parse(line) as InlineChatEvent;
    if (event.type === "error") throw new Error(event.text);
    if (event.type === "done") completed = true;
    else if (event.type === "delta" && typeof event.text === "string") { /* validated */ }
    else if (event.type === "proposal" && typeof event.content === "string" && (!event.changes || Array.isArray(event.changes))) { /* validated */ }
    else throw new Error("Invalid inline chat response.");
    receive(event);
  };
  try {
    while (true) {
      const { value, done } = await reader.read();
      pending += done ? decoder.decode() : decoder.decode(value, { stream: true });
      let newline: number;
      while ((newline = pending.indexOf("\n")) >= 0) {
        consume(pending.slice(0, newline));
        pending = pending.slice(newline + 1);
      }
      if (pending.length > 4 * 1024 * 1024) throw new Error("Inline response exceeded its size limit.");
      if (done) { consume(pending); break; }
    }
    if (!completed && !signal.aborted) throw new Error("Connection closed before inline chat completed. You can send a follow-up to continue.");
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

export type InlineChatTransport = typeof streamInlineChat;

/** In-memory only; survives view remounts without sharing regular chat state. */
export class InlineChatSession {
  file: InlineChatFile;
  proposal: string | undefined;
  changes: InlineChatChange[] = [];
  history: InlineChatMessage[] = [];
  references: InlineChatReference[] = [];
  draft: ComposerSegment[] = [];
  model = "";
  status: "idle" | "running" | "stopped" | "error" = "idle";
  error = "";
  diskConflict = false;
  lastPrompt = "";
  private generation = 0;
  private abort: AbortController | null = null;
  private listeners = new Set<() => void>();

  constructor(readonly workspaceId: string, readonly key: string, file: InlineChatFile, private transport: InlineChatTransport = streamInlineChat) {
    this.file = structuredClone(file);
  }

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  }
  notify(): void { this.listeners.forEach(listener => listener()); }
  get hasProposal(): boolean { return this.proposal !== undefined && this.proposal !== this.file.content; }

  async send(message: string, references: InlineChatReference[]): Promise<void> {
    if (this.status === "running" || !message.trim()) return;
    const generation = ++this.generation;
    const abort = new AbortController();
    this.abort = abort;
    this.lastPrompt = message;
    const unique = new Map(this.references.concat(references).map(reference => [`${reference.ref.rootId}\0${reference.ref.path}`, reference]));
    this.references = [...unique.values()];
    const request: InlineChatRequest = {
      message, model: this.model || undefined, file: structuredClone(this.file), proposal: this.proposal,
      references: structuredClone(this.references), history: structuredClone(this.history),
    };
    this.history.push({ role: "user", content: message });
    const answer: InlineChatMessage = { role: "assistant", content: "" };
    this.history.push(answer);
    this.status = "running";
    this.error = "";
    this.notify();
    try {
      await this.transport(this.workspaceId, request, abort.signal, event => {
        if (generation !== this.generation || abort.signal.aborted) return;
        if (event.type === "delta") answer.content += event.text;
        if (event.type === "proposal") {
          this.proposal = event.content;
          this.changes = event.changes || [];
        }
        this.notify();
      });
      if (generation === this.generation) this.status = "idle";
    } catch (error) {
      if (generation !== this.generation) return;
      this.status = abort.signal.aborted ? "stopped" : "error";
      this.error = abort.signal.aborted ? "" : error instanceof Error ? error.message : String(error);
    } finally {
      if (generation === this.generation) { this.abort = null; this.notify(); }
    }
  }

  stop(): void {
    ++this.generation;
    this.abort?.abort();
    this.abort = null;
    if (this.status === "running") this.status = "stopped";
    this.notify();
  }

  regenerate(file: InlineChatFile): void {
    this.stop();
    this.file = structuredClone(file);
    this.proposal = undefined;
    this.changes = [];
    this.history = [];
    this.error = "";
    this.diskConflict = false;
    this.status = "idle";
    this.notify();
    if (this.lastPrompt) void this.send(this.lastPrompt, this.references);
  }
}

const sessions = new Map<string, InlineChatSession>();
export function inlineChatKey(workspaceId: string, id: string): string {
  // Open-file IDs survive Code remounts, Save As, and workspace renames.
  return `${workspaceId}\0${id}`;
}
export function findInlineChat(key: string): InlineChatSession | undefined { return sessions.get(key); }
export function openInlineChat(workspaceId: string, key: string, file: InlineChatFile): InlineChatSession {
  const existing = sessions.get(key);
  if (existing) return existing;
  const session = new InlineChatSession(workspaceId, key, file);
  sessions.set(key, session);
  return session;
}
export function closeInlineChat(key: string): void {
  const session = sessions.get(key);
  sessions.delete(key);
  session?.stop();
}
