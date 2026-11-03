import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { editor, languages } from "monaco-editor";

const state = vi.hoisted(() => ({
  providers: [] as Array<languages.DocumentSymbolProvider & { lsp?: boolean }>,
  listeners: new Set<() => void>(), rich: vi.fn(),
}));
vi.mock("monaco-editor/editor/common/services/languageFeatures.js", () => ({ ILanguageFeaturesService: "features" }));
vi.mock("monaco-editor/editor/common/languages/language.js", () => ({ ILanguageService: "language" }));
vi.mock("monaco-editor/editor/standalone/browser/standaloneServices.js", () => ({ StandaloneServices: {
  get: (id: string) => id === "features" ? { documentSymbolProvider: {
    ordered: () => state.providers,
    onDidChange: (listener: () => void) => { state.listeners.add(listener); return { dispose: () => state.listeners.delete(listener) }; },
  } } : { requestRichLanguageFeatures: state.rich },
} }));
vi.mock("./lspProviders", () => ({ isLSPDocumentSymbolProvider: (provider: { lsp?: boolean }) => provider.lsp === true }));
import { resolveOutlineSymbols } from "./outlineProviders";

beforeEach(() => { vi.useFakeTimers(); state.providers = []; state.listeners.clear(); state.rich.mockClear(); });
afterEach(() => { vi.useRealTimers(); });
const model = (language = "typescript") => ({ getLanguageId: () => language }) as editor.ITextModel;
const symbol: languages.DocumentSymbol = { name: "Example", kind: 11, detail: "()", tags: [],
  range: { startLineNumber: 1, startColumn: 1, endLineNumber: 2, endColumn: 2 },
  selectionRange: { startLineNumber: 1, startColumn: 10, endLineNumber: 1, endColumn: 17 },
};

describe("Outline provider adapter", () => {
  it("activates built-in language features and waits for their lazy registration", async () => {
    const resolve = resolveOutlineSymbols(model(), null, new AbortController().signal);
    expect(state.rich).toHaveBeenCalledWith("typescript");
    const provider = { provideDocumentSymbols: vi.fn(async () => [symbol]) };
    state.providers.push(provider);
    for (const listener of state.listeners) listener();
    const result = await resolve;
    expect(result.status).toBe("ready");
    expect(result.symbols[0].selectionRange.startColumn).toBe(10);
    expect(state.listeners.size).toBe(0);
  });

  it("distinguishes unsupported documents from supported empty documents", async () => {
    await expect(resolveOutlineSymbols(model("plaintext"), null, new AbortController().signal)).resolves.toMatchObject({ status: "unsupported" });
    state.providers = [{ provideDocumentSymbols: () => [] }];
    await expect(resolveOutlineSymbols(model(), null, new AbortController().signal)).resolves.toMatchObject({ status: "empty" });
  });




  it("cancels providers that never resolve and detaches registration listeners", async () => {
    const abort = new AbortController();
    const pending = resolveOutlineSymbols(model(), null, abort.signal);
    abort.abort();
    await expect(pending).rejects.toThrow("cancelled");
    expect(state.listeners.size).toBe(0);
    let token!: Parameters<languages.DocumentSymbolProvider["provideDocumentSymbols"]>[1];
    state.providers = [{ provideDocumentSymbols: (_model, value) => { token = value; return new Promise(() => {}); } }];
    const timeout = expect(resolveOutlineSymbols(model(), null, new AbortController().signal)).rejects.toThrow("timed out");
    await vi.advanceTimersByTimeAsync(15001);
    await timeout;
    expect(token.isCancellationRequested).toBe(true);
  });
});
