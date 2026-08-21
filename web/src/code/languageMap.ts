const extensions: Record<string, string> = {
  c: "cpp", h: "cpp", cc: "cpp", cpp: "cpp", cxx: "cpp", hpp: "cpp",
  go: "go", py: "python", pyw: "python", lua: "lua",
  js: "javascript", jsx: "javascript", mjs: "javascript", cjs: "javascript",
  ts: "typescript", tsx: "typescript", mts: "typescript", cts: "typescript",
  json: "json", jsonc: "json", html: "html", htm: "html", css: "css", scss: "scss", less: "less",
  md: "markdown", markdown: "markdown", java: "java", cs: "csharp", rs: "rust",
  rb: "ruby", php: "php", sh: "shell", bash: "shell", zsh: "shell", ps1: "powershell",
  yaml: "yaml", yml: "yaml", sql: "sql", xml: "xml", toml: "ini", ini: "ini",
  asm: "echo-asm", s: "echo-asm", mips: "mips",
};

export function languageIdForPath(filePath: string): string {
  const name = filePath.split("/").pop() || filePath;
  if (/^dockerfile(?:\..+)?$/i.test(name)) return "dockerfile";
  if (/^makefile$/i.test(name)) return "makefile";
  const extension = name.includes(".") ? name.split(".").pop() || "" : "";
  return extensions[extension] || extensions[extension.toLowerCase()] || "plaintext";
}

export function configuredLanguageIdForPath(filePath: string, profiles: LSPProfile[] = []): string {
  for (const profile of profiles) {
    for (const selector of profile.selectors || []) {
      if (selectorMatchesPath(selector, filePath)) return selector.languageId;
    }
  }
  return languageIdForPath(filePath);
}

export function profileMatchesDocument(profile: LSPProfile, languageId: string, filePath: string): boolean {
  return (profile.selectors || []).some((selector) => selector.languageId === languageId && selectorMatchesPath(selector, filePath));
}

function selectorMatchesPath(selector: LSPProfile["selectors"][number], filePath: string): boolean {
  const name = filePath.replace(/\\/g, "/").split("/").pop() || filePath;
  const lowerName = name.toLowerCase();
  return (selector.filenames || []).some((candidate) => candidate.toLowerCase() === lowerName)
    || (selector.extensions || []).some((extension) => lowerName.endsWith(extension.toLowerCase()));
}
import type { LSPProfile } from "./lspTypes";
