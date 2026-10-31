import type { CancellationToken, editor, languages, IDisposable, Position } from 'monaco-editor';
import type * as LSP from 'vscode-languageserver-protocol';
import { monaco } from './language';
import { type GoLanguage, lspRange } from './goLanguage';

const providerSet = new WeakSet<object>();
export const isLSPDocumentSymbolProvider = (provider: object): boolean => providerSet.has(provider);
const position = (p: Position) => ({ line: p.lineNumber - 1, character: p.column - 1 });
const markdown = (value: any): { value: string; isTrusted: false; supportHtml: false } => ({ value: typeof value === 'string' ? value : value?.language ? '```' + value.language + '\n' + value.value + '\n```' : value?.value || '', isTrusted: false, supportHtml: false });
const kindNames = ['Text', 'Method', 'Function', 'Constructor', 'Field', 'Variable', 'Class', 'Interface', 'Module', 'Property', 'Unit', 'Value', 'Enum', 'Keyword', 'Snippet', 'Color', 'File', 'Reference', 'Folder', 'EnumMember', 'Constant', 'Struct', 'Event', 'Operator', 'TypeParameter'] as const;
function completion(item: LSP.CompletionItem, model: editor.ITextModel, p: Position): languages.CompletionItem {
  const word = model.getWordUntilPosition(p);
  const edit = item.textEdit;
  const range = edit ? 'range' in edit ? lspRange(edit.range) : { insert: lspRange(edit.insert), replace: lspRange(edit.replace) } : { startLineNumber: p.lineNumber, endLineNumber: p.lineNumber, startColumn: word.startColumn, endColumn: word.endColumn };
  return { label: item.label, kind: monaco.languages.CompletionItemKind[kindNames[(item.kind || 1) - 1] || 'Text'], range,
    insertText: edit?.newText ?? item.insertText ?? item.label, insertTextRules: item.insertTextFormat === 2 ? monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet : undefined,
    detail: item.detail, documentation: item.documentation ? markdown(item.documentation) : undefined, sortText: item.sortText, filterText: item.filterText, preselect: item.preselect,
    additionalTextEdits: item.additionalTextEdits?.map(e => ({ range: lspRange(e.range), text: e.newText })) };
}
function symbol(s: LSP.DocumentSymbol): languages.DocumentSymbol { return { name: s.name, detail: s.detail || '', kind: s.kind - 1, tags: s.tags || [], range: lspRange(s.range), selectionRange: lspRange(s.selectionRange), children: s.children?.map(symbol) }; }

export function installLSPProviders(service: GoLanguage): IDisposable[] {
  const selector = [{ language: 'go', scheme: 'file' }, { scheme: 'file', pattern: '**/go.mod' }, { scheme: 'file', pattern: '**/go.work' }];
  const request = async (model: editor.ITextModel, method: string, params: any, token: CancellationToken): Promise<any> => {
    if (!service.owns(model)) return null;
    const abort = new AbortController(); if (token.isCancellationRequested) abort.abort();
    const listener = token.onCancellationRequested(() => abort.abort());
    try { return await service.request(model, method, params, abort.signal); }
    catch (error) { if (!abort.signal.aborted) service.reportFailure((error as Error).message); return null; }
    finally { listener.dispose(); }
  };
  const doc = (model: editor.ITextModel) => ({ uri: service.wireURI(model.uri.toString()) });
  const locations = async (value: LSP.Location | Array<LSP.Location | LSP.LocationLink> | null, token: CancellationToken) => {
    const results: languages.Location[] = [];
    const items = !value ? [] : Array.isArray(value) ? value : [value];
    if (items.length > 500) service.reportFailure('Showing the first 500 locations; narrow the query');
    for (const item of items.slice(0, 500)) {
      if (token.isCancellationRequested) return [];
      const uri = 'targetUri' in item ? item.targetUri : item.uri; const range = 'targetUri' in item ? item.targetSelectionRange : item.range;
      try { await service.ensureSource(uri); results.push({ uri: service.modelURI(uri), range: lspRange(range) }); }
      catch (error) { service.reportFailure((error as Error).message); break; }
    }
    return results;
  };
  const symbols: languages.DocumentSymbolProvider = { provideDocumentSymbols: async (model, token) => {
    if (!service.owns(model)) return null;
    const result = await request(model, 'textDocument/documentSymbol', { textDocument: doc(model) }, token) as LSP.DocumentSymbol[] | null;
    return result?.map(symbol) || [];
  } }; providerSet.add(symbols);
  const completionData = new WeakMap<languages.CompletionItem, { item: LSP.CompletionItem; model: editor.ITextModel; position: Position }>();
  return [
    monaco.languages.registerDocumentSymbolProvider(selector, symbols),
    monaco.languages.registerCompletionItemProvider(selector, {
      triggerCharacters: ['.'],
      provideCompletionItems: async (model, p, context, token) => {
        if (!service.owns(model)) return null;
        const result = await request(model, 'textDocument/completion', { textDocument: doc(model), position: position(p), context: { triggerKind: context.triggerKind + 1, triggerCharacter: context.triggerCharacter } }, token) as LSP.CompletionList | LSP.CompletionItem[] | null;
        if (!result) return null;
        return { incomplete: !Array.isArray(result) && result.isIncomplete, suggestions: (Array.isArray(result) ? result : result.items).map(item => { const converted = completion(item, model, p); completionData.set(converted, { item, model, position: p }); return converted; }) };
      },
      resolveCompletionItem: async (item, token) => {
        const saved = completionData.get(item); if (!saved) return item;
        const resolved = await request(saved.model, 'completionItem/resolve', saved.item, token);
        return resolved ? completion(resolved, saved.model, saved.position) : item;
      },
    }),
    monaco.languages.registerHoverProvider(selector, { provideHover: async (model, p, token) => {
      if (!service.owns(model)) return null;
      const result = await request(model, 'textDocument/hover', { textDocument: doc(model), position: position(p) }, token) as LSP.Hover | null;
      return result ? { range: result.range ? lspRange(result.range) : undefined, contents: (Array.isArray(result.contents) ? result.contents : [result.contents]).map(markdown) } : null;
    } }),
    monaco.languages.registerSignatureHelpProvider(selector, { signatureHelpTriggerCharacters: ['(', ','], signatureHelpRetriggerCharacters: [')'], provideSignatureHelp: async (model, p, token) => {
      if (!service.owns(model)) return null;
      const result = await request(model, 'textDocument/signatureHelp', { textDocument: doc(model), position: position(p) }, token) as LSP.SignatureHelp | null;
      return result ? { value: { signatures: result.signatures.map(s => ({ label: s.label, documentation: s.documentation ? markdown(s.documentation) : undefined, parameters: (s.parameters || []).map(p => ({ label: p.label, documentation: p.documentation ? markdown(p.documentation) : undefined })) })), activeSignature: result.activeSignature || 0, activeParameter: result.activeParameter || 0 }, dispose() {} } : null;
    } }),
    monaco.languages.registerDefinitionProvider(selector, { provideDefinition: async (model, p, token) => service.owns(model) ? locations(await request(model, 'textDocument/definition', { textDocument: doc(model), position: position(p) }, token), token) : null }),
    monaco.languages.registerReferenceProvider(selector, { provideReferences: async (model, p, context, token) => service.owns(model) ? locations(await request(model, 'textDocument/references', { textDocument: doc(model), position: position(p), context }, token), token) : null }),
    monaco.languages.registerDocumentFormattingEditProvider(selector, { provideDocumentFormattingEdits: async (model, options, token) => {
      if (!service.owns(model)) return null;
      const baseline = service.versions(); const result = await request(model, 'textDocument/formatting', { textDocument: doc(model), options }, token);
      if (!result) return null;
      try { return (await service.prepareEdits(result.edit, baseline, result.revisions)).map(e => e.textEdit); }
      catch (error) { service.reportFailure((error as Error).message); return null; }
    } }),
    monaco.languages.registerRenameProvider(selector, {
      resolveRenameLocation: async (model, p, token) => {
        if (!service.owns(model)) return null;
        const result = await request(model, 'textDocument/prepareRename', { textDocument: doc(model), position: position(p) }, token);
        if (!result) return { range: model.getFullModelRange(), text: '', rejectReason: 'This symbol cannot be renamed' };
        const range = lspRange(result.range || result); return { range, text: result.placeholder || model.getValueInRange(range) };
      },
      provideRenameEdits: async (model, p, newName, token) => {
        if (!service.owns(model)) return null;
        const baseline = service.versions();
        const result = await request(model, 'textDocument/rename', { textDocument: doc(model), position: position(p), newName }, token);
        try { return result ? { edits: await service.prepareEdits(result.edit, baseline, result.revisions) } : null; }
        catch (error) { return { edits: [], rejectReason: (error as Error).message }; }
      },
    }),
  ];
}
