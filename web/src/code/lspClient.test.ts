import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./language", () => ({
  monaco: {
    Range: class Range {
      constructor(
        public startLineNumber: number, public startColumn: number,
        public endLineNumber: number, public endColumn: number,
      ) {}
    },
    MarkerSeverity: { Error: 8, Warning: 4, Info: 2, Hint: 1 },
    Uri: { parse: vi.fn((value: string) => ({ scheme: "file", path: value, toString: () => value })) },
    editor: {
      registerCommand: vi.fn(() => ({ dispose() {} })),
      getModel: vi.fn(() => null),
      getModels: vi.fn(() => []),
      setModelMarkers: vi.fn(),
    },
  },
}));

import { EchoLSPClient, fromLSPRange, toLSPPosition, toLSPRange } from "./lspClient";

class FakeWebSocket {
  static readonly OPEN = 1;
  readyState = FakeWebSocket.OPEN;
  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;
  send = vi.fn();
  close = vi.fn(() => { this.readyState = 3; });
}

beforeEach(() => {
  vi.stubGlobal("WebSocket", FakeWebSocket);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("LSP Monaco conversions", () => {
  it("converts Monaco's one-based positions to LSP UTF-16 coordinates", () => {
    expect(toLSPPosition({ lineNumber: 4, column: 9 } as never)).toEqual({ line: 3, character: 8 });
  });

  it("round-trips ranges without an off-by-one error", () => {
    const range = { startLineNumber: 2, startColumn: 3, endLineNumber: 5, endColumn: 8 };
    const lsp = toLSPRange(range);
    expect(lsp).toEqual({ start: { line: 1, character: 2 }, end: { line: 4, character: 7 } });
    expect(fromLSPRange(lsp)).toMatchObject(range);
  });
});

describe("LSP diagnostic decorations", () => {
  const uri = "file:///workspace/main.go";

  function createClient(onDiagnosticsChange = vi.fn(), diagnosticKey = (candidate: string) => candidate): EchoLSPClient {
    return new EchoLSPClient({
      workspaceId: "workspace",
      initial: {
        config: {},
        profiles: [],
        statuses: [],
      },
      prepareWorkspaceEdit: vi.fn(), applyWorkspaceEdit: vi.fn(async () => true),
      isURIAllowed: () => true, diagnosticKey, prepareURI: async () => true,
      onDocumentState: vi.fn(), onDiagnosticsChange, onMessage: vi.fn(),
    });
  }

  function publish(client: EchoLSPClient, profileId: string, diagnostics: Array<{ severity?: number }>, diagnosticURI = uri): void {
    (client as any).handleNotification(profileId, "textDocument/publishDiagnostics", { uri: diagnosticURI, diagnostics });
  }

  it("aggregates profiles and decorates only errors and warnings", () => {
    const onDiagnosticsChange = vi.fn();
    const client = createClient(onDiagnosticsChange);

    publish(client, "gopls", [{ severity: 3 }, { severity: 4 }, {}]);
    expect(onDiagnosticsChange).not.toHaveBeenCalled();

    publish(client, "gopls", [{ severity: 2 }, { severity: 3 }]);
    publish(client, "secondary", [{ severity: 1 }, { severity: 2 }]);
    publish(client, "secondary", []);
    publish(client, "gopls", [{ severity: 4 }]);

    expect(onDiagnosticsChange.mock.calls).toEqual([
      [uri, "warning"],
      [uri, "error"],
      [uri, "warning"],
      [uri, null],
    ]);
    client.dispose();
  });

  it("treats normalized URI aliases as the same workspace file", () => {
    const onDiagnosticsChange = vi.fn();
    const serverURI = "file:///C:/workspace/main.go";
    const browserURI = "file:///c%3A/workspace/main.go";
    const client = createClient(onDiagnosticsChange, () => "root:main.go");

    publish(client, "gopls", [{ severity: 2 }], serverURI);
    publish(client, "gopls", [{ severity: 1 }], browserURI);
    publish(client, "gopls", [], serverURI);

    expect(onDiagnosticsChange.mock.calls).toEqual([
      [serverURI, "warning"],
      [browserURI, "error"],
      [serverURI, null],
    ]);
    client.dispose();
  });

  it("clears stale decorations across document and server lifecycles", () => {
    const onDiagnosticsChange = vi.fn();
    const client = createClient(onDiagnosticsChange);
    let disposeModel = () => {};
    const model = {
      uri: { scheme: "file", path: "/workspace/main.go", toString: () => uri },
      getLanguageId: () => "go",
      onDidChangeContent: () => ({ dispose() {} }),
      onWillDispose: (callback: () => void) => {
        disposeModel = callback;
        return { dispose() {} };
      },
    };
    (client as any).profiles = [{ id: "gopls", name: "gopls", command: "gopls", selectors: [{ languageId: "go", extensions: [".go"] }] }];
    client.trackModel(model as never);

    publish(client, "gopls", [{ severity: 2 }]);
    onDiagnosticsChange.mockClear();
    disposeModel();
    expect(onDiagnosticsChange).not.toHaveBeenCalled();
    publish(client, "gopls", []);
    expect(onDiagnosticsChange).toHaveBeenLastCalledWith(uri, null);

    publish(client, "gopls", [{ severity: 1 }]);
    onDiagnosticsChange.mockClear();
    (client as any).handleStatus({ workspaceId: "workspace", profileId: "gopls", name: "gopls", state: "failed", capabilities: {} });
    expect(onDiagnosticsChange).toHaveBeenLastCalledWith(uri, null);

    publish(client, "gopls", [{ severity: 2 }]);
    onDiagnosticsChange.mockClear();
    (client as any).receive(JSON.stringify({ type: "lsp_configuration", config: {}, profiles: [], statuses: [] }));
    expect(onDiagnosticsChange).toHaveBeenLastCalledWith(uri, null);

    publish(client, "gopls", [{ severity: 1 }]);
    onDiagnosticsChange.mockClear();
    client.dispose();
    expect(onDiagnosticsChange).toHaveBeenLastCalledWith(uri, null);
  });
});

describe("LSP CodeLens refresh", () => {
  it("invalidates providers and acknowledges workspace refresh requests", async () => {
    const client = new EchoLSPClient({
      workspaceId: "workspace", initial: { config: {}, profiles: [], statuses: [] },
      prepareWorkspaceEdit: vi.fn(), applyWorkspaceEdit: vi.fn(async () => true),
      isURIAllowed: () => true, diagnosticKey: (candidate) => candidate, prepareURI: async () => true,
      onDocumentState: vi.fn(), onDiagnosticsChange: vi.fn(), onMessage: vi.fn(),
    });
    const listener = vi.fn();
    client.onCodeLensRefresh(listener);
    const send = vi.spyOn(client as any, "send");

    await (client as any).handleServerRequest({ id: "refresh-1", profileId: "gopls", method: "workspace/codeLens/refresh", params: {} });

    expect(listener).toHaveBeenCalledOnce();
    expect(send).toHaveBeenCalledWith({ type: "lsp_server_response", id: "refresh-1", profileId: "gopls", result: null });
    client.dispose();
  });
});

describe("LSP save actions", () => {
  it("organizes imports before formatting when format on save is enabled", async () => {
    const workspaceEdit = { changes: { "file:///workspace/main.go": [{
      range: { start: { line: 1, character: 0 }, end: { line: 1, character: 0 } }, newText: "import \"fmt\"\n",
    }] } };
    const applyWorkspaceEdit = vi.fn(async () => true);
    const onMessage = vi.fn();
    const client = new EchoLSPClient({
      workspaceId: "workspace",
      initial: {
        config: { formatOnSave: true, formatOnSaveTimeoutMs: 3000 }, profiles: [],
        statuses: [{
          workspaceId: "workspace", profileId: "gopls", name: "gopls", state: "running",
          capabilities: { codeActionProvider: true, documentFormattingProvider: true },
        }],
      },
      prepareWorkspaceEdit: vi.fn(), applyWorkspaceEdit,
      isURIAllowed: () => true, diagnosticKey: (candidate) => candidate, prepareURI: async () => true,
      onDocumentState: vi.fn(), onDiagnosticsChange: vi.fn(), onMessage,
    });
    const uri = { scheme: "file", path: "/workspace/main.go", toString: () => "file:///workspace/main.go" };
    const model = {
      uri,
      getFullModelRange: () => ({ startLineNumber: 1, startColumn: 1, endLineNumber: 4, endColumn: 2 }),
      getOptions: () => ({ tabSize: 4, insertSpaces: false }),
      pushEditOperations: vi.fn(),
    };
    const profile = { id: "gopls", name: "gopls", command: "gopls", selectors: [{ languageId: "go", extensions: [".go"] }] };
    (client as any).profiles = [profile];
    (client as any).tracked.set(uri.toString(), {
      model, profileId: "gopls", leased: true, change: { dispose() {} }, dispose: { dispose() {} },
    });
    const request = vi.spyOn(client, "requestForModel").mockImplementation(async (_model, method, params) => {
      if (method === "textDocument/codeAction") return [{ title: "Organize Imports", kind: "source.organizeImports", edit: workspaceEdit }] as never;
      if (method === "textDocument/formatting") return [{
        range: { start: { line: 0, character: 0 }, end: { line: 0, character: 0 } }, newText: "// formatted\n",
      }] as never;
      throw new Error(`unexpected request: ${method} ${JSON.stringify(params)}`);
    });

    await client.formatBeforeSave(model as never);

    expect(request.mock.calls.map((call) => call[1])).toEqual(["textDocument/codeAction", "textDocument/formatting"]);
    expect(request.mock.calls[0][2]).toMatchObject({
      range: { start: { line: 0, character: 0 }, end: { line: 3, character: 1 } },
      context: { diagnostics: [], only: ["source.organizeImports"], triggerKind: 2 },
    });
    expect(applyWorkspaceEdit).toHaveBeenCalledWith(workspaceEdit);
    expect(model.pushEditOperations).toHaveBeenCalledOnce();
    expect(onMessage).not.toHaveBeenCalled();
    client.dispose();
  });

  it("still formats when import organization fails", async () => {
    const onMessage = vi.fn();
    const client = new EchoLSPClient({
      workspaceId: "workspace",
      initial: {
        config: { formatOnSave: true, formatOnSaveTimeoutMs: 3000 }, profiles: [],
        statuses: [{
          workspaceId: "workspace", profileId: "gopls", name: "gopls", state: "running",
          capabilities: { codeActionProvider: true, documentFormattingProvider: true },
        }],
      },
      prepareWorkspaceEdit: vi.fn(), applyWorkspaceEdit: vi.fn(async () => true),
      isURIAllowed: () => true, diagnosticKey: (candidate) => candidate, prepareURI: async () => true,
      onDocumentState: vi.fn(), onDiagnosticsChange: vi.fn(), onMessage,
    });
    const uri = { scheme: "file", path: "/workspace/main.go", toString: () => "file:///workspace/main.go" };
    const model = {
      uri,
      getFullModelRange: () => ({ startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1 }),
      getOptions: () => ({ tabSize: 4, insertSpaces: false }),
      pushEditOperations: vi.fn(),
    };
    (client as any).profiles = [{ id: "gopls", name: "gopls", command: "gopls", selectors: [{ languageId: "go", extensions: [".go"] }] }];
    (client as any).tracked.set(uri.toString(), {
      model, profileId: "gopls", leased: true, change: { dispose() {} }, dispose: { dispose() {} },
    });
    const request = vi.spyOn(client, "requestForModel").mockImplementation(async (_model, method) => {
      if (method === "textDocument/codeAction") throw new Error("organize failed");
      if (method === "textDocument/formatting") return [] as never;
      throw new Error(`unexpected request: ${method}`);
    });

    await client.formatBeforeSave(model as never);

    expect(request.mock.calls.map((call) => call[1])).toEqual(["textDocument/codeAction", "textDocument/formatting"]);
    expect(onMessage).toHaveBeenCalledWith(expect.stringContaining("import organization: organize failed"));
    client.dispose();
  });
});
