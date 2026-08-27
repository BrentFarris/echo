import type { LSPDiagnosticSeverity } from "./lspClient";
import { refKey, type FileRef } from "./types";

export function updateExplorerDiagnostic(
  diagnostics: Map<string, LSPDiagnosticSeverity>,
  uri: string,
  severity: LSPDiagnosticSeverity | null,
  refForURI: (uri: string) => FileRef | null,
): boolean {
  const ref = refForURI(uri);
  if (!ref) return false;
  const key = refKey(ref);
  if (severity) diagnostics.set(key, severity);
  else diagnostics.delete(key);
  return true;
}

export function explorerDiagnosticPresentation(severity: LSPDiagnosticSeverity | undefined): {
  className: string;
  description: string;
} {
  if (severity === "error") return { className: "has-diagnostic-error", description: "has errors" };
  if (severity === "warning") return { className: "has-diagnostic-warning", description: "has warnings" };
  return { className: "", description: "" };
}
