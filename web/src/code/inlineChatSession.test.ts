import { afterEach, describe, expect, it, vi } from "vitest";
import {
  InlineChatSession, closeInlineChat, findInlineChat, inlineChatKey, openInlineChat, streamInlineChat,
  type InlineChatEvent, type InlineChatRequest, type InlineChatTransport,
} from "./inlineChatSession";

const file = { title: "draft", content: "before\r\n😀", selections: [{ startLine: 1, startColumn: 1, endLine: 1, endColumn: 7, text: "before" }] };

describe("inline conversation state", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("accumulates proposals across follow-ups without changing the original buffer", async () => {
    const requests: InlineChatRequest[] = [];
    const transport: InlineChatTransport = async (_workspace, request, _signal, emit) => {
      requests.push(request);
      emit({ type: "proposal", content: requests.length === 1 ? "after" : "after again", changes: [{ startLine: 1, endLine: 3, newLines: ["after"] }] });
      emit({ type: "delta", text: "Done" }); emit({ type: "done" });
    };
    const session = new InlineChatSession("workspace", "key", file, transport);
    const reference = { ref: { rootId: "root", path: "context.ts" }, kind: "file" as const, label: "context.ts", referencePath: "root/context.ts" };
    await session.send("first", [reference]);
    expect(session.status).toBe("idle");
    expect(session.hasProposal).toBe(true);
    await session.send("follow-up", []);
    expect(requests[1].proposal).toBe("after");
    expect(requests[1].history).toEqual([{ role: "user", content: "first" }, { role: "assistant", content: "Done" }]);
    expect(requests[1].references).toEqual([reference]);
    expect(session.proposal).toBe("after again");
    expect(session.file.content).toBe(file.content);
  });

  it("keeps complete previews on Stop and ignores all late events", async () => {
    let emit!: (event: InlineChatEvent) => void, finish!: () => void, signal!: AbortSignal;
    const session = new InlineChatSession("workspace", "key", file, async (_w, _r, abort, receive) => {
      signal = abort; emit = receive;
      await new Promise<void>(resolve => { finish = resolve; });
    });
    const pending = session.send("edit", []);
    emit({ type: "proposal", content: "valid" }); emit({ type: "delta", text: "partial" });
    session.stop();
    expect(signal.aborted).toBe(true);
    emit({ type: "proposal", content: "late" }); emit({ type: "delta", text: " late" });
    finish(); await pending;
    expect(session.status).toBe("stopped");
    expect(session.proposal).toBe("valid");
    expect(session.history[1].content).toBe("partial");
    expect(session.file.content).toBe(file.content);
  });

  it("reports stream failure and regenerates against a new baseline", async () => {
    const requests: InlineChatRequest[] = [];
    const session = new InlineChatSession("workspace", "key", file, async (_w, request) => {
      requests.push(request); throw new Error("provider unavailable");
    });
    await session.send("edit", []);
    expect(session.error).toBe("provider unavailable");
    session.proposal = "old proposal";
    session.regenerate({ ...file, content: "manual edit", selections: [] });
    await vi.waitFor(() => expect(session.status).toBe("error"));
    expect(requests[1]).toMatchObject({ message: "edit", file: { content: "manual edit" }, history: [] });
    expect(requests[1].proposal).toBeUndefined();
  });

  it("isolates files and workspaces while retaining sessions across remounts", () => {
    const key = inlineChatKey("workspace", "tab-1");
    const first = openInlineChat("workspace", key, file);
    expect(openInlineChat("workspace", inlineChatKey("workspace", "tab-1"), structuredClone(file))).toBe(first);
    expect(inlineChatKey("another-workspace", "tab-1")).not.toBe(key);
    const second = openInlineChat("workspace", inlineChatKey("workspace", "untitled-1"), file);
    closeInlineChat(key);
    expect(findInlineChat(key)).toBeUndefined();
    expect(findInlineChat(second.key)).toBe(second);
    closeInlineChat(second.key);
  });

  it("parses split NDJSON and UTF-8 chunks, including an empty-file proposal", async () => {
    const events: InlineChatEvent[] = [];
    const data = new TextEncoder().encode('{"type":"delta","text":"😀"}\n{"type":"proposal","content":"","changes":[]}\n{"type":"done"}\n');
    vi.stubGlobal("fetch", vi.fn(async () => new Response(new ReadableStream({ start(controller) {
      for (const byte of data) controller.enqueue(Uint8Array.of(byte)); controller.close();
    } }))));
    await streamInlineChat("workspace", { message: "edit", file, references: [], history: [] }, new AbortController().signal, event => events.push(event));
    expect(events).toEqual([{ type: "delta", text: "😀" }, { type: "proposal", content: "", changes: [] }, { type: "done" }]);
  });

  it.each([
    ['{"type":"delta","text":"partial"}\n', "before inline chat completed"],
    ['{"type":"error","text":"model failed"}\n', "model failed"],
    ['{"type":"surprise"}\n', "Invalid inline chat response"],
  ])("rejects incomplete or invalid responses", async (body, error) => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(body)));
    await expect(streamInlineChat("workspace", { message: "edit", file, references: [], history: [] }, new AbortController().signal, () => {})).rejects.toThrow(error);
  });
});
