import { linter, type Diagnostic as LintDiagnostic } from "@codemirror/lint";
import { Prec, type Extension } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { EventsOn } from "../backend/runtime";

// ─── Types (mirror Go WorkspaceDiagnostic) ────────────────────

interface LSPDiagnosticRange {
  start: LSPDiagnosticPosition;
  end: LSPDiagnosticPosition;
}

interface LSPDiagnosticPosition {
  line: number;
  character: number;
}

interface LSPDiagnostic {
  range: LSPDiagnosticRange;
  severity: number;
  code?: unknown;
  codeDescription?: string;
  source?: string;
  message: string;
  relatedInformation?: unknown[];
  tags?: number[];
}

interface LSPDiagnosticsPayload {
  workspaceId: string;
  filePath: string;
  diagnostics: LSPDiagnostic[];
}

// ─── State ─────────────────────────────────────────────────────

/** Per-file diagnostic map keyed by composite key. */
const fileDiagnostics = new Map<string, LSPDiagnostic[]>();

/** Track which keys have an active editor view. */
const activeEditors = new Set<string>();

function diagnosticsKey(workspaceID: string, filePath: string): string {
  return `${workspaceID}\u0000${filePath}`;
}

// ─── LSP → CodeMirror mapping ──────────────────────────────────

/**
 * Map LSP severity to CodeMirror severity.
 * LSP: 1 = error, 2 = warning, 3 = info, 4 = hint
 */
function lspSeverityToLintSeverity(severity: number): "error" | "warning" | "info" | "hint" {
  switch (severity) {
    case 1: return "error";
    case 2: return "warning";
    case 3: return "info";
    case 4: return "hint";
    default: return "info";
  }
}

function lspDiagnosticToLintDiagnostic(
  view: EditorView,
  d: LSPDiagnostic,
): LintDiagnostic | null {
  const startLine = d.range.start.line + 1; // LSP lines are 0-based, CodeMirror is 1-based
  const endLine = d.range.end.line + 1;

  const startLineObj = view.state.doc.line(startLine);
  if (startLineObj.from > view.state.doc.length) {
    return null;
  }
  const from = startLineObj.from + d.range.start.character;

  const endLineObj = view.state.doc.line(endLine);
  const to = endLineObj.from + d.range.end.character;

  // Build tooltip content
  let severityLabel: string;
  switch (d.severity) {
    case 1: severityLabel = "Error"; break;
    case 2: severityLabel = "Warning"; break;
    case 3: severityLabel = "Info"; break;
    case 4: severityLabel = "Hint"; break;
    default: severityLabel = "Diagnostic";
  }

  const parts: string[] = [];
  if (d.source) {
    parts.push(`[${severityLabel}] ${d.source}`);
  } else {
    parts.push(`[${severityLabel}]`);
  }
  parts.push(d.message);
  if (d.codeDescription) {
    parts.push(d.codeDescription);
  }

  return {
    from: Math.min(from, view.state.doc.length),
    to: Math.min(to, view.state.doc.length),
    severity: lspSeverityToLintSeverity(d.severity),
    message: parts.join("\n"),
  };
}

// ─── Event subscription ────────────────────────────────────────

let eventUnsubscribe: (() => void) | null = null;

function ensureEventSubscription() {
  if (eventUnsubscribe) {
    return;
  }
  eventUnsubscribe = EventsOn("echo:lsp:diagnostics", (raw) => {
    let payload: LSPDiagnosticsPayload;
    try {
      payload = typeof raw === "string" ? JSON.parse(raw) : raw;
    } catch {
      return;
    }

    const key = diagnosticsKey(payload.workspaceId, payload.filePath);
    fileDiagnostics.set(key, payload.diagnostics ?? []);

    // If there's an active editor for this file, trigger a re-render
    // by dispatching a no-op transaction so the linter re-runs.
    if (activeEditors.has(key)) {
      const views = diagnosticViews.get(key);
      if (views) {
        for (const view of views) {
          if (view.dom.isConnected) {
            view.dispatch({});
          }
        }
      }
    }
  });
}

function stopEventSubscription() {
  if (eventUnsubscribe) {
    eventUnsubscribe();
    eventUnsubscribe = null;
  }
}

// ─── CodeMirror extension ──────────────────────────────────────

/** Track all active EditorViews per diagnostics key. */
const diagnosticViews = new Map<string, Set<EditorView>>();

function trackView(key: string, view: EditorView) {
  let views = diagnosticViews.get(key);
  if (!views) {
    views = new Set();
    diagnosticViews.set(key, views);
  }
  views.add(view);
}

function untrackView(key: string, view: EditorView) {
  const views = diagnosticViews.get(key);
  if (views) {
    views.delete(view);
    if (views.size === 0) {
      diagnosticViews.delete(key);
    }
  }
}

export function lspDiagnosticsExtension(
  workspaceID: string,
  path: string,
): Extension | null {
  const key = diagnosticsKey(workspaceID, path);

  // Dynamic linter source that reads from our global state map.
  const lintExtension = linter((view) => {
    const diagnostics = fileDiagnostics.get(key) ?? [];
    if (!diagnostics.length) {
      return [];
    }
    const results: LintDiagnostic[] = [];
    for (const d of diagnostics) {
      const mapped = lspDiagnosticToLintDiagnostic(view, d);
      if (mapped) {
        results.push(mapped);
      }
    }
    return results;
  });

  // Track view lifecycle so we know which editors need updating.
  // We use a weak reference pattern: track on first update, clean up when DOM disconnects.
  let tracked = false;
  const trackExtension = EditorView.updateListener.of((update) => {
    if (!tracked) {
      tracked = true;
      ensureEventSubscription();
      activeEditors.add(key);
      trackView(key, update.view);
    }
  });

  // Cleanup when the view is destroyed — check DOM connectivity on each update.
  const cleanupExtension = EditorView.updateListener.of((update) => {
    if (!update.view.dom.isConnected) {
      untrackView(key, update.view);
      // Only remove from active editors if no views remain for this key.
      const remaining = diagnosticViews.get(key);
      if (!remaining || remaining.size === 0) {
        activeEditors.delete(key);
      }
    }
  });

  return Prec.high([lintExtension, trackExtension, cleanupExtension]);
}

export function destroyLSPDiagnostics() {
  // Clean up when the workspace is torn down.
  const keys = [...activeEditors];
  for (const key of keys) {
    diagnosticViews.delete(key);
  }
  activeEditors.clear();
  fileDiagnostics.clear();
  stopEventSubscription();
}
