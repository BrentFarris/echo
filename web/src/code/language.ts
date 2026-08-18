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
import { languageIdForPath } from "./languageMap";

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

export function languageForPath(filePath: string): string {
  const language = languageIdForPath(filePath);
  if (language === "echo-asm") registerAssembly();
  return language;
}

export { monaco };
