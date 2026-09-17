import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CancellationToken, editor } from "monaco-editor";

vi.mock("./language", () => ({ monaco: { editor: { registerCommand: vi.fn(() => ({ dispose() {} })), getModels: () => [], getModel: () => null, setModelMarkers: vi.fn() }, Uri: { parse: () => ({}) } } }));
vi.mock("./lspProviders", () => ({ registerLSPProviders: () => [] }));
import { EchoLSPClient } from "./lspClient";

class Socket {
  static readonly OPEN = 1;
  readyState = 1;
  send = vi.fn();
  close = vi.fn();
}
beforeEach(() => { vi.stubGlobal("WebSocket", Socket); vi.useFakeTimers(); });
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

function cancellation() {
  const listeners = new Set<() => void>();
  let cancelled = false;
  const token: CancellationToken = {
    get isCancellationRequested() { return cancelled; },
    onCancellationRequested: (callback) => { listeners.add(callback); return { dispose: () => { listeners.delete(callback); } }; },
  };
  return { token, cancel: () => { cancelled = true; for (const callback of [...listeners]) callback(); } };
}

function fixture(state: "starting" | "running" = "running", supported = true) {
  const onDocumentState = vi.fn();
  const client = new EchoLSPClient({ workspaceId: "w", initial: { config: {}, profiles: [], statuses: [{ workspaceId: "w", profileId: "go", name: "Go", state, capabilities: { documentSymbolProvider: supported } }] },
    prepareWorkspaceEdit: vi.fn(), applyWorkspaceEdit: vi.fn(), prepareURI: vi.fn(), diagnosticKey: (uri) => uri,
    isURIAllowed: () => true, onDocumentState, onDiagnosticsChange: vi.fn(), onMessage: vi.fn(),
  });
  (client as any).profiles = [{ id: "go", name: "Go", command: "gopls", selectors: [{ languageId: "go", extensions: [".go"] }] }];
  (client as any).activeURI = "file:///workspace/active.go";
  let disposed = false;
  let onDispose = () => {};
  const uri = "file:///workspace/background.go";
  const model = { uri: { scheme: "file", path: "/workspace/background.go", toString: () => uri },
    getLanguageId: () => "go", getVersionId: () => 3, getValue: () => "package main\nfunc unsaved() {}",
    isDisposed: () => disposed, onDidChangeContent: () => ({ dispose() {} }),
    onWillDispose: (callback: () => void) => { onDispose = callback; return { dispose() {} }; },
    dispose: () => { disposed = true; onDispose(); },
  } as unknown as editor.ITextModel;
  const receive = (message: unknown) => (client as any).receive(JSON.stringify(message));
  const messages = () => ((client as any).socket as Socket).send.mock.calls.map(([value]) => JSON.parse(value));
  const grant = () => receive({ type: "lsp_lease_granted", profileId: "go", uri });
  return { client, model, uri, receive, messages, grant, onDocumentState };
}

describe("LSP background outline preparation", () => {
  it("claims unopened models once with unsaved text without activating or taking over", async () => {
    const { client, model, messages, grant, onDocumentState } = fixture();
    const cancel = cancellation();
    const first = client.prepareDocumentSymbols(model, cancel.token);
    const second = client.prepareDocumentSymbols(model, cancel.token);
    expect(messages()).toEqual([expect.objectContaining({ type: "lsp_claim", takeOver: false, document: expect.objectContaining({ version: 3, text: expect.stringContaining("unsaved") }) })]);
    grant();
    await expect(first).resolves.toBe(true);
    await expect(second).resolves.toBe(true);
    expect(onDocumentState).not.toHaveBeenCalled();
    expect((client as any).activeURI).toBe("file:///workspace/active.go");
    model.dispose();
    expect(messages().at(-1)).toMatchObject({ type: "lsp_close" });
    expect((client as any).tracked.size).toBe(0);
    expect((client as any).documentWaiters.size).toBe(0);
    client.dispose();
  });

  it("waits for startup and lease acknowledgement before declaring the provider ready", async () => {
    const { client, model, messages, receive, grant } = fixture("starting");
    const ready = client.prepareDocumentSymbols(model, cancellation().token);
    expect(messages()).toEqual([]);
    receive({ type: "lsp_status", status: { workspaceId: "w", profileId: "go", name: "Go", state: "running", capabilities: { documentSymbolProvider: true } } });
    expect(messages()).toHaveLength(1);
    grant();
    await expect(ready).resolves.toBe(true);
    client.dispose();
  });

  it("reports denied leases and unsupported servers without takeover or editor status changes", async () => {
    const { client, model, uri, receive, messages, onDocumentState } = fixture();
    const ready = client.prepareDocumentSymbols(model, cancellation().token);
    receive({ type: "lsp_lease_denied", profileId: "go", uri });
    await expect(ready).rejects.toThrow("Another browser owns");
    expect(messages()).toHaveLength(1);
    expect(onDocumentState).not.toHaveBeenCalled();
    client.dispose();
    const unsupported = fixture("running", false);
    await expect(unsupported.client.prepareDocumentSymbols(unsupported.model, cancellation().token)).resolves.toBe(false);
    expect(unsupported.messages()).toEqual([]);
    unsupported.client.dispose();
  });

  it("removes cancelled waiters and never claims after their delayed startup", async () => {
    const { client, model, receive, messages } = fixture("starting");
    const cancel = cancellation();
    const ready = client.prepareDocumentSymbols(model, cancel.token);
    cancel.cancel();
    await expect(ready).rejects.toThrow("cancelled");
    receive({ type: "lsp_status", status: { workspaceId: "w", profileId: "go", state: "running", capabilities: { documentSymbolProvider: true } } });
    expect(messages()).toEqual([]);
    expect((client as any).documentWaiters.size).toBe(0);
    await expect(client.prepareDocumentSymbols(model, cancel.token)).rejects.toThrow("cancelled");
    client.dispose();
  });

  it("bounds startup waits and rejects when a model or client is disposed", async () => {
    const { client, model } = fixture("starting");
    const timeout = expect(client.prepareDocumentSymbols(model, cancellation().token, 20)).rejects.toThrow("did not become ready");
    await vi.advanceTimersByTimeAsync(20); await timeout;
    const disposed = client.prepareDocumentSymbols(model, cancellation().token);
    model.dispose();
    await expect(disposed).rejects.toThrow("no longer available");
    client.dispose();
    const next = fixture("starting");
    const closed = next.client.prepareDocumentSymbols(next.model, cancellation().token);
    next.client.dispose();
    await expect(closed).rejects.toThrow("no longer available");
  });
});
