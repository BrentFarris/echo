import type { CancellationToken, IDisposable, editor, languages } from "monaco-editor";
import { CancellationTokenSource } from "monaco-editor/base/common/cancellation.js";
import { StandaloneServices } from "monaco-editor/editor/standalone/browser/standaloneServices.js";
import { ILanguageFeaturesService } from "monaco-editor/editor/common/services/languageFeatures.js";
import { ILanguageService } from "monaco-editor/editor/common/languages/language.js";
import type { EchoLSPClient } from "./lspClient";
import { isLSPDocumentSymbolProvider } from "./lspProviders";
import { normalizeOutlineSymbols } from "./outlineSymbols";
import type { OutlineResult } from "./outlineTypes";

type SymbolRegistry = {
  ordered(model: editor.ITextModel): languages.DocumentSymbolProvider[];
  onDidChange(callback: () => void): IDisposable;
};
const builtInLanguages = new Set(["typescript", "javascript", "json", "css", "scss", "less", "html", "handlebars", "razor"]);

function cancellable<T>(promise: PromiseLike<T>, token: CancellationToken): Promise<T> {
  if (token.isCancellationRequested) return Promise.reject(new Error("Outline loading cancelled"));
  return new Promise((resolve, reject) => {
    const cancel = token.onCancellationRequested(() => reject(new Error("Outline loading cancelled")));
    Promise.resolve(promise).then(resolve, reject).finally(() => cancel.dispose());
  });
}

async function waitForBuiltInProvider(registry: SymbolRegistry, model: editor.ITextModel, token: CancellationToken): Promise<void> {
  if (!builtInLanguages.has(model.getLanguageId()) || registry.ordered(model).some((provider) => !isLSPDocumentSymbolProvider(provider))) return;
  let listener: IDisposable | undefined;
  let timer: number | undefined;
  try {
    await cancellable(new Promise<void>((resolve, reject) => {
      timer = window.setTimeout(() => reject(new Error("The outline provider did not become ready in time.")), 15000);
      listener = registry.onDidChange(() => {
        if (registry.ordered(model).some((provider) => !isLSPDocumentSymbolProvider(provider))) resolve();
      });
    }), token);
  } finally { listener?.dispose(); window.clearTimeout(timer); }
}

/** Isolate Monaco's internal registry access here; ordinary UI code only consumes OutlineResult. */
export async function resolveOutlineSymbols(model: editor.ITextModel, lsp: EchoLSPClient | null, signal: AbortSignal): Promise<OutlineResult> {
  signal.throwIfAborted();
  const source = new CancellationTokenSource();
  const cancel = () => source.cancel();
  signal.addEventListener("abort", cancel, { once: true });
  if (signal.aborted) cancel();
  let timedOut = false;
  let timer: number | undefined;
  try {
    const registry = StandaloneServices.get(ILanguageFeaturesService).documentSymbolProvider as SymbolRegistry;
    StandaloneServices.get(ILanguageService).requestRichLanguageFeatures(model.getLanguageId());
    const preparation = await Promise.allSettled([
      cancellable(lsp?.prepareDocumentSymbols(model, source.token) || Promise.resolve(false), source.token),
      waitForBuiltInProvider(registry, model, source.token),
    ]);
    if (source.token.isCancellationRequested) throw new Error(timedOut ? "Outline loading timed out." : "Outline loading cancelled");
    const lspReady = preparation[0].status === "fulfilled" && preparation[0].value;
    const providers = registry.ordered(model).filter((provider) => !isLSPDocumentSymbolProvider(provider) || lspReady);
    if (!providers.length) {
      const failed = preparation.find((result) => result.status === "rejected");
      if (failed?.status === "rejected") throw failed.reason;
      return { status: "unsupported", symbols: [], message: "No outline provider is available for this file." };
    }
    timer = window.setTimeout(() => { timedOut = true; cancel(); }, 15000);
    const responses = await Promise.allSettled(providers.map((provider) => cancellable(
      Promise.resolve().then(() => provider.provideDocumentSymbols(model, source.token)), source.token,
    )));
    if (source.token.isCancellationRequested) throw new Error(timedOut ? "Outline loading timed out." : "Outline loading cancelled");
    const succeeded = responses.filter((result) => result.status === "fulfilled");
    if (!succeeded.length) throw (responses[0] as PromiseRejectedResult).reason;
    const symbols = normalizeOutlineSymbols(succeeded.flatMap((result) => result.value || []));
    return { status: symbols.length ? "ready" : "empty", symbols, message: symbols.length ? undefined : "No symbols found." };
  } finally {
    window.clearTimeout(timer);
    signal.removeEventListener("abort", cancel);
    source.cancel();
    source.dispose();
  }
}
