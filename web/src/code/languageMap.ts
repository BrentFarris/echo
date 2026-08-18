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
