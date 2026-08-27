import * as monacoCore from "monaco-editor/editor/editor.api";
import type * as Monaco from "monaco-editor";
import EditorWorker from "monaco-editor/editor/editor.worker?worker";
import JSONWorker from "monaco-editor/language/json/json.worker?worker";
import CSSWorker from "monaco-editor/language/css/css.worker?worker";
import HTMLWorker from "monaco-editor/language/html/html.worker?worker";
import TypeScriptWorker from "monaco-editor/language/typescript/ts.worker?worker";
import "monaco-editor/basic-languages/monaco.contribution";
import "monaco-editor/language/json/monaco.contribution";
import "monaco-editor/language/css/monaco.contribution";
import "monaco-editor/language/html/monaco.contribution";
import "monaco-editor/language/typescript/monaco.contribution";
// The lean editor API exposes language-provider registries but does not load
// the editor contributions that invoke them. Import only Echo's LSP v1 UI.
import "monaco-editor/editor/browser/services/contribution.js";
import "monaco-editor/editor/contrib/codeAction/browser/codeActionContributions.js";
import "monaco-editor/editor/contrib/documentSymbols/browser/documentSymbols.js";
import "monaco-editor/editor/contrib/dnd/browser/dnd.js";
import "monaco-editor/editor/contrib/find/browser/findController.js";
import "monaco-editor/editor/contrib/format/browser/formatActions.js";
import "monaco-editor/editor/contrib/gotoSymbol/browser/goToCommands.js";
import "monaco-editor/editor/contrib/hover/browser/hoverContribution.js";
import "monaco-editor/editor/contrib/multicursor/browser/multicursor.js";
import "monaco-editor/editor/contrib/parameterHints/browser/parameterHints.js";
import "monaco-editor/editor/contrib/rename/browser/rename.js";
import "monaco-editor/editor/contrib/wordOperations/browser/wordOperations.js";
import "monaco-editor/editor/contrib/linesOperations/browser/linesOperations.js";
import "monaco-editor/editor/contrib/suggest/browser/suggestController.js";
import "monaco-editor/editor/standalone/browser/referenceSearch/standaloneReferenceSearch.js";
import "monaco-editor/editor/contrib/wordHighlighter/browser/wordHighlighter.js";
import { configuredLanguageIdForPath } from "./languageMap";
import type { LSPProfile } from "./lspTypes";

// Import the editor API rather than Monaco's aggregate entry point. The
// aggregate also eagerly pulls language registrations into this lazy route;
// contributions below remain explicit so the Code bundle has one copy of each.
const monaco = monacoCore as unknown as typeof Monaco;

declare global {
  interface Window {
    MonacoEnvironment?: {
      getWorker(_moduleId: string, label: string): Worker;
    };
  }
}

window.MonacoEnvironment = {
  getWorker(_moduleId: string, label: string): Worker {
    if (label === "json") return new JSONWorker();
    if (label === "css" || label === "scss" || label === "less") return new CSSWorker();
    if (label === "html" || label === "handlebars" || label === "razor") return new HTMLWorker();
    if (label === "typescript" || label === "javascript") return new TypeScriptWorker();
    return new EditorWorker();
  },
};

let assemblyRegistered = false;

function registerAssembly(): void {
  if (assemblyRegistered) return;
  assemblyRegistered = true;
  monaco.languages.register({ id: "echo-asm", extensions: [".asm", ".s", ".S"], aliases: ["Assembly", "ASM"] });
  monaco.languages.setMonarchTokensProvider("echo-asm", {
    ignoreCase: true,
    tokenizer: {
      root: [
        [/^[\t ]*[A-Za-z_.$][\w.$]*:/, "type.identifier"],
        [/[;#].*$/, "comment"],
        [/\/\/.*$/, "comment"],
        [/\.(?:text|data|bss|section|global|globl|extern|align|byte|word|long|quad|ascii|asciz)\b/, "keyword.directive"],
        [/\b(?:mov|lea|push|pop|call|ret|jmp|je|jne|jg|jge|jl|jle|cmp|test|add|sub|mul|imul|div|idiv|and|or|xor|not|shl|shr|nop|ldr|str|bl|bx|bne|beq)\b/, "keyword"],
        [/%?(?:r(?:ax|bx|cx|dx|si|di|sp|bp|[0-9]+)|e(?:ax|bx|cx|dx|si|di|sp|bp)|[abcd][lh]|xmm\d+|sp|lr|pc)\b/, "variable.predefined"],
        [/0x[0-9a-f]+|\b\d+\b/, "number"],
        [/"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'/, "string"],
        [/[A-Za-z_.$][\w.$]*/, "identifier"],
      ],
    },
  });
}

export function configuredLanguageForPath(filePath: string, profiles: LSPProfile[] = []): string {
  return configuredLanguageIdForPath(filePath, profiles);
}

export function languageForPath(filePath: string, profiles: LSPProfile[] = []): string {
  const language = configuredLanguageForPath(filePath, profiles);
  const known = monaco.languages.getLanguages().some((candidate) => candidate.id === language);
  if (!known && language !== "plaintext") {
    const selector = profiles.flatMap((profile) => profile.selectors || []).find((candidate) => candidate.languageId === language);
    monaco.languages.register({ id: language, extensions: selector?.extensions, filenames: selector?.filenames });
  }
  if (language === "echo-asm") registerAssembly();
  return language;
}

export { monaco };
