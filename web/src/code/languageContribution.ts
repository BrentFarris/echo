import type { editor, languages, IDisposable, Uri, IRange } from 'monaco-editor';
import type { Range, WorkspaceEdit } from 'vscode-languageserver-protocol';
import type { FileRef, FileSnapshot } from './types';
import { splitID } from '../instances';

export const workspaceModelAuthority = (workspace: string): string => splitID(workspace).join('-');
export const lspRange = (range: Range): IRange => ({ startLineNumber: range.start.line + 1, startColumn: range.start.character + 1, endLineNumber: range.end.line + 1, endColumn: range.end.character + 1 });
export type LanguageEditorHooks = {
 open(ref: FileRef): Promise<editor.ITextModel | null>;
 load(ref: FileRef): Promise<editor.ITextModel | null>;
 revision(uri: string): string | undefined;
 status(state: string, message: string): void;
};
export interface LanguageService {
 state: string; message: string; log: string;
 owns(model: editor.ITextModel): boolean;
 wireURI(uri: string): string;
 modelURI(uri: string): Uri;
 attach(model: editor.ITextModel, snapshot: FileSnapshot): void;
 saved(model: editor.ITextModel, snapshot: FileSnapshot): void;
 request(model: editor.ITextModel, method: string, params: any, signal?: AbortSignal): Promise<any>;
 ensureSource(uri: string): Promise<editor.ITextModel | null>;
 install(model?: editor.ITextModel | null): Promise<void>;
 restart(): void;
 reportFailure(message: string): void;
 versions(): Map<string, number>;
 prepareEdits(edit: WorkspaceEdit, versions: Map<string, number>, revisions: Record<string, string>): Promise<languages.IWorkspaceTextEdit[]>;
}
export type AcquiredLanguage = { service: LanguageService; dispose(): void };
export type LanguageContribution = {
 pluginID: string; name: string; languages: string[]; debugTypes: string[]; sourceSchemes: string[];
 selector: languages.LanguageSelector;
 acquire(botID: string, hooks: LanguageEditorHooks): AcquiredLanguage;
 hoverExpression?(line: string, column: number): string;
 launchTemplate?: string;
 debuggerStatus?(botID: string, directory?: string): Promise<{ state: string; message?: string; executable?: string; version?: string; managedVersion: string }>;
 installDebugger?(botID: string, directory?: string): Promise<void>;
};
const contributions = new Map<string, LanguageContribution>();
export function registerLanguageContribution(contribution: LanguageContribution): IDisposable {
 if (contributions.has(contribution.pluginID)) throw new Error('Duplicate language contribution');
 contributions.set(contribution.pluginID, contribution);
 return { dispose: () => { contributions.delete(contribution.pluginID); } };
}
export function languageContributions(): LanguageContribution[] { return [...contributions.values()]; }
export function debugContribution(type: string): LanguageContribution | undefined { return languageContributions().find(c => c.debugTypes.includes(type)); }
