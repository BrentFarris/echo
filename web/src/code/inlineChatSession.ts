import type { Generation } from '../types';
import { route, localID, splitID } from '../instances';
export type EditorContextSelection = { startLine: number; startColumn: number; endLine: number; endColumn: number; text: string };
import type { ChatReference, ComposerSegment } from "./chatMentions";
import type { FileRef } from "./types";

export type InlineChatFile = { title: string; ref?: FileRef; content: string; selections: EditorContextSelection[] };
export type InlineChatChange = { startLine: number; endLine: number; newLines: string[] };
export type InlineChatMessage = { role: "user" | "assistant"; content: string };
export type InlineChatReference = Omit<ChatReference, "workspaceId">;
export type InlineChatRequest = {
  message: string; generation: Generation; file: InlineChatFile; proposal?: string;
  references: InlineChatReference[]; history: InlineChatMessage[];
};
export type InlineChatEvent =
  | { type: "delta"; text: string }
  | { type: "reset"; text: string }
  | { type: "proposal"; content: string; changes?: InlineChatChange[] }
  | { type: "status"; text: string }
  | { type: "done" }
  | { type: "error"; text: string };

/** Validate the preview as one complete patch before exposing any candidate. */
function validProposal(event: { content?: unknown; changes?: unknown }, original: string): boolean {
  if (typeof event.content !== 'string' || event.content.length > 256 * 1024) return false;
  const before = original.replaceAll('\r\n', '\n').split('\n');
  const changes = event.changes ?? [];
  if (!Array.isArray(changes)) return false;
  let after: string[] = [];
  let cursor = 0;
  for (const change of changes) {
    if (!change || !Number.isInteger(change.startLine) || !Number.isInteger(change.endLine) || change.startLine < cursor + 1 || change.endLine < change.startLine || change.endLine > before.length + 1 || !Array.isArray(change.newLines) || !change.newLines.every((line: unknown) => typeof line === 'string' && !line.includes('\n'))) return false;
    after = after.concat(before.slice(cursor, change.startLine - 1), change.newLines);
    cursor = change.endLine - 1;
  }
  after = after.concat(before.slice(cursor));
  return after.join('\n') === event.content.replaceAll('\r\n', '\n');
}

export async function streamInlineChat(workspaceId: string, request: InlineChatRequest, signal: AbortSignal, receive: (event: InlineChatEvent) => void): Promise<void> {
  if (request.generation.endpointId && splitID(request.generation.endpointId)[0] !== splitID(workspaceId)[0]) throw new Error("The model connection belongs to a different machine.");
  const target = route(`/api/v1/plugins/monaco-editor/bots/${encodeURIComponent(workspaceId)}/inline-chat`);
  const response = await target.client.fetch(target.path, {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ...request, generation: { ...request.generation, endpointId: localID(request.generation.endpointId) } }), signal,
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
    if (line.length > 4 * 1024 * 1024) throw new Error("Inline response exceeded its size limit.");
    const event = JSON.parse(line) as InlineChatEvent;
    if (!event || typeof event !== 'object') throw new Error("Invalid inline chat response.");
    if (event.type === "error") throw new Error(typeof event.text === "string" ? event.text : "Invalid inline chat error.");
    if (event.type === "done") completed = true;
    else if ((event.type === "delta" || event.type === "status" || event.type === "reset") && typeof event.text === "string") { /* validated */ }
    else if (event.type === "proposal" && validProposal(event, request.file.content)) { /* validated */ }
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
  generation: Generation = { endpointId: "", model: "", thinking: "", temperature: .6 };
  activity = "";
  status: "idle" | "running" | "stopped" | "error" = "idle";
  error = "";
  diskConflict = false;
  diskRevision: string | undefined;
  lastPrompt = "";
  private sequence = 0;
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
    const generation = ++this.sequence;
    const abort = new AbortController();
    this.abort = abort;
    this.lastPrompt = message;
    const unique = new Map(this.references.concat(references).map(reference => [`${reference.ref.rootId}\0${reference.ref.path}`, reference]));
    this.references = [...unique.values()];
    const request: InlineChatRequest = {
      message, generation: { ...this.generation }, file: structuredClone(this.file), proposal: this.proposal,
      references: structuredClone(this.references), history: structuredClone(this.history),
    };
    this.history.push({ role: "user", content: message });
    const answer: InlineChatMessage = { role: "assistant", content: "" };
    this.history.push(answer);
    this.status = "running";
    this.activity = "Working…";
    this.error = "";
    this.notify();
    try {
      await this.transport(this.workspaceId, request, abort.signal, event => {
        if (generation !== this.sequence || abort.signal.aborted) return;
        if (event.type === "delta") answer.content += event.text;
        if (event.type === "reset") answer.content = event.text;
        if (event.type === "status") this.activity = event.text;
        if (event.type === "proposal") {
          this.proposal = event.content;
          this.changes = event.changes || [];
        }
        this.notify();
      });
      if (generation === this.sequence) this.status = "idle";
    } catch (error) {
      if (generation !== this.sequence) return;
      this.status = abort.signal.aborted ? "stopped" : "error";
      this.error = abort.signal.aborted ? "" : error instanceof Error ? error.message : String(error);
    } finally {
      if (generation === this.sequence) { this.abort = null; this.notify(); }
    }
  }

  stop(): void {
    ++this.sequence;
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
