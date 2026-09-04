import type * as Monaco from "monaco-editor";
import { monaco } from "./language";
import { fromLSPRange, toLSPPosition, toLSPRange, type EchoLSPClient } from "./lspClient";
import type { LSPCodeAction, LSPCodeLens, LSPRange, LSPTextEdit, LSPWorkspaceEdit } from "./lspTypes";
import { builtInGoTestLensKeys, codeLensIdentity } from "./codeLensDedupe";

type LSPMarkup = string | { kind?: string; value?: string } | Array<string | { language?: string; value?: string }>;
type LSPCompletion = Record<string, any>;
type ProviderCompletion = Monaco.languages.CompletionItem & { lsp?: LSPCompletion; profileId?: string };
type ProviderCodeAction = Monaco.languages.CodeAction & { lsp?: LSPCodeAction; profileId?: string };
type ProviderCodeLens = Monaco.languages.CodeLens & { lsp?: LSPCodeLens; profileId?: string };

export function registerLSPProviders(client: EchoLSPClient): Monaco.IDisposable[] {
  const disposables: Monaco.IDisposable[] = [];
  const languageIds = new Set(client.effectiveProfiles().flatMap((profile) => profile.selectors.map((selector) => selector.languageId)));
  for (const languageId of languageIds) registerLanguage(client, languageId, disposables);
  return disposables;
}

function registerLanguage(client: EchoLSPClient, languageId: string, out: Monaco.IDisposable[]): void {
  const profileFor = (model: Monaco.editor.ITextModel) => client.profileForModel(model);
  const can = (model: Monaco.editor.ITextModel, capability: string) => {
    const profile = profileFor(model);
    return Boolean(profile && client.supports(profile.id, capability));
  };
  const documentPosition = (model: Monaco.editor.ITextModel, position: Monaco.Position) => ({
    textDocument: { uri: model.uri.toString() }, position: toLSPPosition(position),
  });

  out.push(monaco.languages.registerCompletionItemProvider(languageId, {
    triggerCharacters: triggerCharacters(client, languageId, "completionProvider.triggerCharacters"),
    async provideCompletionItems(model, position, context, token) {
      if (!can(model, "completionProvider")) return { suggestions: [] };
      const response = await client.requestForModel<LSPCompletion[] | { isIncomplete?: boolean; items?: LSPCompletion[] } | null>(model, "textDocument/completion", {
        ...documentPosition(model, position), context: { triggerKind: context.triggerKind + 1, triggerCharacter: context.triggerCharacter },
      }, token);
      const items = Array.isArray(response) ? response : response?.items || [];
      const profileId = profileFor(model)?.id;
      return {
        incomplete: !Array.isArray(response) && response?.isIncomplete === true,
        suggestions: items.map((item) => completionItem(model, position, item, profileId)),
      };
    },
    async resolveCompletionItem(item, token) {
      const local = item as ProviderCompletion;
      if (!local.lsp || !local.profileId || !client.supports(local.profileId, "completionProvider.resolveProvider")) return item;
      const resolved = await client.request<LSPCompletion>(local.profileId, "completionItem/resolve", local.lsp, token);
      return completionItem(undefined, undefined, resolved, local.profileId, item.range);
    },
  }));

  out.push(monaco.languages.registerHoverProvider(languageId, {
    async provideHover(model, position, token) {
      if (!can(model, "hoverProvider")) return null;
      const hover = await client.requestForModel<any>(model, "textDocument/hover", documentPosition(model, position), token);
      if (!hover) return null;
      return { range: hover.range ? fromLSPRange(hover.range) : undefined, contents: markdownContents(hover.contents) };
    },
  }));

  const signatureProfile = client.effectiveProfiles().find((profile) => profile.selectors.some((selector) => selector.languageId === languageId));
  out.push(monaco.languages.registerSignatureHelpProvider(languageId, {
    signatureHelpTriggerCharacters: signatureProfile ? client.capability<string[]>(signatureProfile.id, "signatureHelpProvider.triggerCharacters") || [] : [],
    signatureHelpRetriggerCharacters: signatureProfile ? client.capability<string[]>(signatureProfile.id, "signatureHelpProvider.retriggerCharacters") || [] : [],
    async provideSignatureHelp(model, position, token, context) {
      if (!can(model, "signatureHelpProvider")) return null;
      const value = await client.requestForModel<any>(model, "textDocument/signatureHelp", {
        ...documentPosition(model, position),
        context: {
          triggerKind: context.triggerKind, triggerCharacter: context.triggerCharacter,
          isRetrigger: context.isRetrigger, activeSignatureHelp: context.activeSignatureHelp,
        },
      }, token);
      if (!value) return null;
      return {
        value: {
          signatures: (value.signatures || []).map((signature: any) => ({
            label: String(signature.label || ""), documentation: markup(signature.documentation),
            parameters: (signature.parameters || []).map((parameter: any) => ({ label: parameter.label, documentation: markup(parameter.documentation) })),
            activeParameter: signature.activeParameter,
          })),
          activeSignature: value.activeSignature || 0, activeParameter: value.activeParameter || 0,
        },
        dispose() {},
      };
    },
  }));

  registerLocationProvider(out, languageId, "definitionProvider", "textDocument/definition", client, "definition", documentPosition);
  registerLocationProvider(out, languageId, "declarationProvider", "textDocument/declaration", client, "declaration", documentPosition);
  registerLocationProvider(out, languageId, "typeDefinitionProvider", "textDocument/typeDefinition", client, "typeDefinition", documentPosition);
  registerLocationProvider(out, languageId, "implementationProvider", "textDocument/implementation", client, "implementation", documentPosition);

  out.push(monaco.languages.registerReferenceProvider(languageId, {
    async provideReferences(model, position, context, token) {
      if (!can(model, "referencesProvider")) return [];
      const locations = await client.requestForModel<any[] | null>(model, "textDocument/references", {
        ...documentPosition(model, position), context: { includeDeclaration: context.includeDeclaration },
      }, token);
      return (await locationsFromLSP(locations, client)).filter((item): item is Monaco.languages.Location => "uri" in item && "range" in item);
    },
  }));

  out.push(monaco.languages.registerDocumentSymbolProvider(languageId, {
    displayName: "Language Server",
    async provideDocumentSymbols(model, token) {
      if (!can(model, "documentSymbolProvider")) return [];
      const symbols = await client.requestForModel<any[] | null>(model, "textDocument/documentSymbol", { textDocument: { uri: model.uri.toString() } }, token);
      return (symbols || []).map((symbol) => documentSymbol(symbol)).filter(Boolean) as Monaco.languages.DocumentSymbol[];
    },
  }));

  out.push(monaco.languages.registerRenameProvider(languageId, {
    async resolveRenameLocation(model, position, token) {
      const rejected = (reason: string) => ({ range: new monaco.Range(position.lineNumber, position.column, position.lineNumber, position.column), text: "", rejectReason: reason });
      if (!can(model, "renameProvider")) return rejected("The language server does not support rename.");
      const profile = profileFor(model)!;
      const prepare = client.capability<any>(profile.id, "renameProvider");
      if (prepare !== true && !prepare?.prepareProvider) {
        const word = model.getWordAtPosition(position);
        return word ? { range: new monaco.Range(position.lineNumber, word.startColumn, position.lineNumber, word.endColumn), text: word.word } : rejected("No symbol at this position.");
      }
      const result = await client.requestForModel<any>(model, "textDocument/prepareRename", documentPosition(model, position), token);
      if (!result) return rejected("The symbol cannot be renamed.");
      const range = result.range || result;
      return { range: fromLSPRange(range), text: result.placeholder || model.getValueInRange(fromLSPRange(range)) };
    },
    async provideRenameEdits(model, position, newName, token) {
      if (!can(model, "renameProvider")) return { edits: [], rejectReason: "The language server does not support rename." };
      const edit = await client.requestForModel<LSPWorkspaceEdit | null>(model, "textDocument/rename", {
        ...documentPosition(model, position), newName,
      }, token);
      if (!edit) return { edits: [] };
      return client.prepareWorkspaceEdit(edit);
    },
  }));

  out.push(monaco.languages.registerCodeActionProvider(languageId, {
    async provideCodeActions(model, range, context, token) {
      if (!can(model, "codeActionProvider")) return { actions: [], dispose() {} };
      const actions = await client.requestForModel<Array<LSPCodeAction | { title: string; command: string; arguments?: unknown[] }> | null>(model, "textDocument/codeAction", {
        textDocument: { uri: model.uri.toString() }, range: toLSPRange(range),
        context: {
          only: context.only ? [context.only] : undefined,
          diagnostics: context.markers.map((marker) => ({
            range: { start: { line: marker.startLineNumber - 1, character: marker.startColumn - 1 }, end: { line: marker.endLineNumber - 1, character: marker.endColumn - 1 } },
            severity: marker.severity, message: marker.message, source: marker.source, code: marker.code,
          })),
        },
      }, token);
      const profileId = profileFor(model)!.id;
      const prepared = await Promise.all((actions || []).map(async (action) => {
        const edit = "edit" in action && action.edit ? await client.prepareWorkspaceEdit(action.edit) : undefined;
        return codeAction(action, profileId, edit);
      }));
      return { actions: prepared, dispose() {} };
    },
    async resolveCodeAction(action, token) {
      const local = action as ProviderCodeAction;
      if (!local.lsp || !local.profileId || !client.supports(local.profileId, "codeActionProvider.resolveProvider")) return action;
      const resolved = await client.request<LSPCodeAction>(local.profileId, "codeAction/resolve", local.lsp, token);
      return codeAction(resolved, local.profileId, resolved.edit ? await client.prepareWorkspaceEdit(resolved.edit) : undefined);
    },
  }, { providedCodeActionKinds: ["quickfix", "refactor", "source"] }));

  out.push(monaco.languages.registerCodeLensProvider(languageId, {
    onDidChange: (listener) => client.onCodeLensRefresh(listener),
    async provideCodeLenses(model, token) {
      if (!can(model, "codeLensProvider")) return { lenses: [], dispose() {} };
      const profileId = profileFor(model)!.id;
      const lenses = await client.requestForModel<LSPCodeLens[] | null>(model, "textDocument/codeLens", {
        textDocument: { uri: model.uri.toString() },
      }, token);
      let composed = lenses || [];
      if (model.getLanguageId() === "go" && model.uri.path.toLowerCase().endsWith("_test.go")) {
        const builtIn = await builtInGoTestLensKeys(model);
        composed = composed.filter((item) => !item.command || !builtIn.has(codeLensIdentity({ range: item.range, title: item.command.title })));
      }
      return { lenses: composed.map((item) => codeLens(item, profileId)), dispose() {} };
    },
    async resolveCodeLens(_model, item, token) {
      const local = item as ProviderCodeLens;
      if (!local.lsp || !local.profileId || !client.supports(local.profileId, "codeLensProvider.resolveProvider")) return item;
      const resolved = await client.request<LSPCodeLens>(local.profileId, "codeLens/resolve", local.lsp, token);
      return codeLens(resolved, local.profileId);
    },
  }));

  out.push(monaco.languages.registerDocumentFormattingEditProvider(languageId, {
    displayName: "Language Server",
    async provideDocumentFormattingEdits(model, options, token) {
      if (!can(model, "documentFormattingProvider")) return [];
      return formattingEdits(await client.requestForModel<LSPTextEdit[] | null>(model, "textDocument/formatting", {
        textDocument: { uri: model.uri.toString() }, options,
      }, token));
    },
  }));

  out.push(monaco.languages.registerDocumentRangeFormattingEditProvider(languageId, {
    displayName: "Language Server",
    async provideDocumentRangeFormattingEdits(model, range, options, token) {
      if (!can(model, "documentRangeFormattingProvider")) return [];
      return formattingEdits(await client.requestForModel<LSPTextEdit[] | null>(model, "textDocument/rangeFormatting", {
        textDocument: { uri: model.uri.toString() }, range: toLSPRange(range), options,
      }, token));
    },
  }));
}

function registerLocationProvider(
  out: Monaco.IDisposable[], languageId: string, capability: string, method: string, client: EchoLSPClient,
  kind: "definition" | "declaration" | "typeDefinition" | "implementation",
  params: (model: Monaco.editor.ITextModel, position: Monaco.Position) => unknown,
): void {
  const provider = async (model: Monaco.editor.ITextModel, position: Monaco.Position, token: Monaco.CancellationToken) => {
    const profile = client.profileForModel(model);
    if (!profile || !client.supports(profile.id, capability)) return [];
    return locationsFromLSP(await client.requestForModel<any>(model, method, params(model, position), token), client);
  };
  if (kind === "definition") out.push(monaco.languages.registerDefinitionProvider(languageId, { provideDefinition: provider }));
  if (kind === "declaration") out.push(monaco.languages.registerDeclarationProvider(languageId, { provideDeclaration: provider }));
  if (kind === "typeDefinition") out.push(monaco.languages.registerTypeDefinitionProvider(languageId, { provideTypeDefinition: provider }));
  if (kind === "implementation") out.push(monaco.languages.registerImplementationProvider(languageId, { provideImplementation: provider }));
}

function completionItem(model: Monaco.editor.ITextModel | undefined, position: Monaco.Position | undefined, item: LSPCompletion, profileId?: string, fallbackRange?: Monaco.IRange | Monaco.languages.CompletionItemRanges): ProviderCompletion {
  const textEdit = item.textEdit && item.textEdit.newText !== undefined ? item.textEdit : undefined;
  const insertReplace = textEdit?.insert && textEdit?.replace ? { insert: fromLSPRange(textEdit.insert), replace: fromLSPRange(textEdit.replace) } : undefined;
  const word = model && position ? model.getWordUntilPosition(position) : undefined;
  const range = insertReplace || (textEdit?.range ? fromLSPRange(textEdit.range) : fallbackRange || (position && new monaco.Range(position.lineNumber, word?.startColumn || position.column, position.lineNumber, position.column))!);
  return {
    label: item.label || "", kind: completionKind(item.kind), detail: item.detail,
    documentation: markup(item.documentation), sortText: item.sortText, filterText: item.filterText,
    preselect: item.preselect, insertText: textEdit?.newText ?? item.insertText ?? item.label ?? "", range,
    insertTextRules: item.insertTextFormat === 2 ? monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet : undefined,
    commitCharacters: item.commitCharacters,
    additionalTextEdits: (item.additionalTextEdits || []).map((edit: LSPTextEdit) => ({ range: fromLSPRange(edit.range), text: edit.newText })),
    command: item.command ? { id: "echo.lsp.executeCommand", title: item.command.title || item.command.command, arguments: [profileId, item.command.command, item.command.arguments] } : undefined,
    tags: item.tags?.includes(1) || item.deprecated ? [monaco.languages.CompletionItemTag.Deprecated] : undefined,
    lsp: item, profileId,
  };
}

function codeAction(action: any, profileId: string, preparedEdit?: Monaco.languages.WorkspaceEdit): ProviderCodeAction {
  const command = action.command && typeof action.command === "object" ? action.command : (typeof action.command === "string" ? action : undefined);
  return {
    title: action.title || command?.title || command?.command || "Language server action",
    kind: action.kind, diagnostics: [], isPreferred: action.isPreferred,
    disabled: action.disabled?.reason,
    edit: preparedEdit,
    command: command ? { id: "echo.lsp.executeCommand", title: command.title || command.command, arguments: [profileId, command.command, command.arguments] } : undefined,
    lsp: action, profileId,
  };
}

function codeLens(item: LSPCodeLens, profileId: string): ProviderCodeLens {
  return {
    range: fromLSPRange(item.range),
    command: item.command ? {
      id: "echo.lsp.executeCommand", title: item.command.title || item.command.command,
      arguments: [profileId, item.command.command, item.command.arguments],
    } : undefined,
    lsp: item,
    profileId,
  };
}

async function locationsFromLSP(value: any, client: EchoLSPClient): Promise<Array<Monaco.languages.Location | Monaco.languages.LocationLink>> {
  if (!value) return [];
  const items = Array.isArray(value) ? value : [value];
  const allowed = await Promise.all(items.map(async (item) => {
    const uri = String(item.targetUri || item.uri || "");
    return { item, uri, ready: Boolean(uri && client.isURIAllowed(uri) && await client.prepareURI(uri)) };
  }));
  return allowed.flatMap(({ item, uri, ready }) => {
    if (!ready) return [];
    if (item.targetUri) return [{
      uri: monaco.Uri.parse(uri), range: fromLSPRange(item.targetRange || item.targetSelectionRange),
      targetSelectionRange: item.targetSelectionRange ? fromLSPRange(item.targetSelectionRange) : undefined,
      originSelectionRange: item.originSelectionRange ? fromLSPRange(item.originSelectionRange) : undefined,
    }];
    return [{ uri: monaco.Uri.parse(uri), range: fromLSPRange(item.range) }];
  });
}

function documentSymbol(symbol: any): Monaco.languages.DocumentSymbol | null {
  const range: LSPRange | undefined = symbol.range || symbol.location?.range;
  if (!range) return null;
  return {
    name: String(symbol.name || ""), detail: String(symbol.detail || ""), kind: symbolKind(symbol.kind),
    tags: symbol.tags?.includes(1) || symbol.deprecated ? [monaco.languages.SymbolTag.Deprecated] : [],
    containerName: symbol.containerName, range: fromLSPRange(range), selectionRange: fromLSPRange(symbol.selectionRange || range),
    children: (symbol.children || []).map(documentSymbol).filter(Boolean) as Monaco.languages.DocumentSymbol[],
  };
}

function formattingEdits(edits: LSPTextEdit[] | null): Monaco.languages.TextEdit[] {
  return (edits || []).map((edit) => ({ range: fromLSPRange(edit.range), text: edit.newText }));
}

function triggerCharacters(client: EchoLSPClient, languageId: string, capability: string): string[] {
  const profile = client.effectiveProfiles().find((candidate) => candidate.selectors.some((selector) => selector.languageId === languageId));
  return profile ? client.capability<string[]>(profile.id, capability) || [] : [];
}

function markdownContents(value: LSPMarkup | undefined): Monaco.IMarkdownString[] {
  if (Array.isArray(value)) return value.map((item) => ({ value: typeof item === "string" ? item : item.language ? `\`\`\`${item.language}\n${item.value || ""}\n\`\`\`` : item.value || "" }));
  return value === undefined || value === null ? [] : [{ value: typeof value === "string" ? value : value.value || "" }];
}

function markup(value: LSPMarkup | undefined): string | Monaco.IMarkdownString | undefined {
  const contents = markdownContents(value);
  return contents.length ? contents[0] : undefined;
}

function completionKind(kind: number | undefined): Monaco.languages.CompletionItemKind {
  const map = [18, 0, 1, 2, 3, 4, 5, 7, 8, 9, 12, 13, 14, 15, 17, 28, 19, 20, 23, 16, 14, 6, 23, 10, 11, 24];
  return (map[(kind || 1) - 1] ?? monaco.languages.CompletionItemKind.Text) as Monaco.languages.CompletionItemKind;
}

function symbolKind(kind: number | undefined): Monaco.languages.SymbolKind {
  return Math.max(0, Math.min(25, (kind || 1) - 1)) as Monaco.languages.SymbolKind;
}
