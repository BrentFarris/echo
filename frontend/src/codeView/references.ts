import { EditorState, Prec, StateEffect, StateField, type Extension } from "@codemirror/state";
import { Decoration, type DecorationSet, EditorView, WidgetType, keymap } from "@codemirror/view";
import { services } from "../../wailsjs/go/models";
import { codeIcons } from "./icons";
import { ensureCodeState } from "./state";
import type { CodeReferencePanelState, CodeViewCallbacks } from "./types";
import { clamp, fileName } from "./utils";

type OpenCodeFileForReference = (
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  options: { temporary: boolean; selectionPosition?: number },
) => Promise<unknown>;

type ReferenceGroup = {
  path: string;
  entries: Array<{ index: number; location: services.WorkspaceReferenceLocation }>;
};

const setReferencePanelEffect = StateEffect.define<number>();
const clearReferencePanelEffect = StateEffect.define<void>();
let referencePanelRenderSeq = 0;
let referenceFocusRequestSeq = 0;

export function referencesPanelExtension(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  openCodeFile: OpenCodeFileForReference,
): Extension {
  const field = StateField.define<DecorationSet>({
    create(state) {
      return referencePanelDecorations(state, workspaceID, path, callbacks, openCodeFile);
    },
    update(decorations, transaction) {
      decorations = decorations.map(transaction.changes);
      const panel = referencesPanelForPath(workspaceID, path);
      if (panel && transaction.docChanged) {
        panel.anchorPosition = transaction.changes.mapPos(panel.anchorPosition);
      }
      for (const effect of transaction.effects) {
        if (effect.is(clearReferencePanelEffect)) {
          decorations = Decoration.none;
        }
        if (effect.is(setReferencePanelEffect)) {
          decorations = referencePanelDecorations(
            transaction.state,
            workspaceID,
            path,
            callbacks,
            openCodeFile,
          );
        }
      }
      return decorations;
    },
    provide: (field) => EditorView.decorations.from(field),
  });
  return [
    field,
    Prec.highest(keymap.of([
      {
        key: "Escape",
        run(view) {
          if (!referencesPanelForPath(workspaceID, path)) {
            return false;
          }
          closeReferencesPanel(workspaceID, path, view);
          return true;
        },
      },
    ])),
  ];
}

export function openReferencesPanel(
  workspaceID: string,
  path: string,
  view: EditorView,
  locations: services.WorkspaceReferenceLocation[],
  options: { title?: string } = {},
) {
  if (!locations.length) {
    closeReferencesPanel(workspaceID, path, view);
    return;
  }
  const anchorPosition = view.state.selection.main.head;
  const selectedIndex = initialReferenceIndex(locations, path, anchorPosition);
  const expandedPaths = initialExpandedReferencePaths(locations, path, selectedIndex);
  ensureCodeState(workspaceID).referencesPanel = {
    path,
    title: options.title || "References",
    anchorPosition,
    locations,
    selectedIndex,
    expandedPaths,
    renderKey: nextReferencePanelRenderKey(),
  };
  view.dispatch({ effects: setReferencePanelEffect.of(anchorPosition) });
  queueSelectedReferenceFocus(workspaceID, path);
}

function closeReferencesPanel(workspaceID: string, path: string, view: EditorView) {
  const state = ensureCodeState(workspaceID);
  if (state.referencesPanel?.path !== path) {
    return;
  }
  state.referencesPanel = null;
  view.dispatch({ effects: clearReferencePanelEffect.of(undefined) });
}

function referencePanelDecorations(
  editorState: EditorState,
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  openCodeFile: OpenCodeFileForReference,
): DecorationSet {
  const panel = referencesPanelForPath(workspaceID, path);
  if (!panel) {
    return Decoration.none;
  }
  const anchorPosition = clamp(panel.anchorPosition, 0, editorState.doc.length);
  const line = editorState.doc.lineAt(anchorPosition);
  return Decoration.set([
    Decoration.widget({
      widget: new ReferencesPanelWidget(workspaceID, path, callbacks, openCodeFile, panel.renderKey),
      block: true,
      side: 1,
    }).range(line.to),
  ]);
}

class ReferencesPanelWidget extends WidgetType {
  constructor(
    private readonly workspaceID: string,
    private readonly path: string,
    private readonly callbacks: CodeViewCallbacks,
    private readonly openCodeFile: OpenCodeFileForReference,
    private readonly renderKey: number,
  ) {
    super();
  }

  eq(other: WidgetType) {
    return (
      other instanceof ReferencesPanelWidget &&
      other.workspaceID === this.workspaceID &&
      other.path === this.path &&
      other.renderKey === this.renderKey
    );
  }

  toDOM(view: EditorView) {
    const panel = referencesPanelForPath(this.workspaceID, this.path);
    const root = document.createElement("section");
    root.className = "lsp-references-panel";
    root.dataset.lspReferencesPanel = "";
    root.dataset.lspWorkspaceId = this.workspaceID;
    root.dataset.lspPath = this.path;
    if (!panel) {
      return root;
    }
    root.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        closeReferencesPanel(this.workspaceID, this.path, view);
        return;
      }
      if (event.key === "ArrowDown") {
        event.preventDefault();
        event.stopPropagation();
        this.selectRelativeReference(1, view);
        return;
      }
      if (event.key === "ArrowUp") {
        event.preventDefault();
        event.stopPropagation();
        this.selectRelativeReference(-1, view);
      }
    });

    const header = document.createElement("header");
    header.className = "lsp-references-header";

    const title = document.createElement("div");
    title.className = "lsp-references-title";
    title.textContent = `${fileName(this.path)} - ${panel.title} (${panel.locations.length})`;

    const close = document.createElement("button");
    close.className = "icon-button lsp-references-close";
    close.type = "button";
    close.title = "Close references";
    close.setAttribute("aria-label", "Close references");
    close.innerHTML = codeIcons.close;
    close.addEventListener("mousedown", (event) => {
      if (event.button !== 0) {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      closeReferencesPanel(this.workspaceID, this.path, view);
    });
    close.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      closeReferencesPanel(this.workspaceID, this.path, view);
    });

    header.append(title, close);

    const body = document.createElement("div");
    body.className = "lsp-references-body";
    body.append(
      this.renderPreview(panel),
      this.renderList(panel, view),
    );

    root.append(header, body);
    return root;
  }

  ignoreEvent() {
    return true;
  }

  private renderPreview(panel: CodeReferencePanelState) {
    const location = selectedReference(panel);
    const preview = document.createElement("div");
    preview.className = "lsp-references-preview";
    preview.setAttribute("aria-live", "polite");

    if (!location) {
      const empty = document.createElement("div");
      empty.className = "lsp-references-empty";
      empty.textContent = "No references found.";
      preview.append(empty);
      return preview;
    }

    const meta = document.createElement("div");
    meta.className = "lsp-references-preview-meta";
    meta.textContent = `${location.path}:${location.range.start.line}`;
    preview.append(meta);

    const code = document.createElement("div");
    code.className = "lsp-references-preview-code";
    const lines = location.previewLines?.length
      ? location.previewLines
      : [services.WorkspaceReferencePreviewLine.createFrom({
        line: location.range.start.line,
        text: location.preview,
        highlightStart: Math.max(0, location.range.start.column - 1),
        highlightEnd: Math.max(0, location.range.end.column - 1),
      })];
    for (const line of lines) {
      code.append(renderPreviewLine(line, line.line === location.range.start.line));
    }
    preview.append(code);
    return preview;
  }

  private renderList(panel: CodeReferencePanelState, view: EditorView) {
    const sidebar = document.createElement("aside");
    sidebar.className = "lsp-references-sidebar";
    sidebar.setAttribute("role", "listbox");
    sidebar.setAttribute("aria-label", "References");

    for (const group of referenceGroups(panel.locations)) {
      const details = document.createElement("details");
      details.className = "lsp-references-group";
      details.open = panel.expandedPaths.some((expandedPath) => sameWorkspacePath(expandedPath, group.path));

      const summary = document.createElement("summary");
      summary.className = "lsp-references-group-summary";
      summary.addEventListener("mousedown", (event) => {
        if (event.button !== 0) {
          return;
        }
        event.preventDefault();
        event.stopPropagation();
        syncExpandedPath(panel, group.path, !details.open, view);
      });
      summary.addEventListener("click", (event) => {
        event.preventDefault();
        event.stopPropagation();
      });
      summary.addEventListener("keydown", (event) => {
        if (event.key !== "Enter" && event.key !== " ") {
          return;
        }
        event.preventDefault();
        event.stopPropagation();
        syncExpandedPath(panel, group.path, !details.open, view);
      });
      const chevron = document.createElement("span");
      chevron.className = "lsp-references-chevron";
      chevron.innerHTML = codeIcons.chevron;
      const label = document.createElement("span");
      label.className = "lsp-references-group-label";
      label.textContent = fileName(group.path);
      label.title = group.path;
      const count = document.createElement("span");
      count.className = "lsp-references-count";
      count.textContent = String(group.entries.length);
      summary.append(chevron, label, count);

      const entries = document.createElement("div");
      entries.className = "lsp-references-entries";
      for (const entry of group.entries) {
        entries.append(this.renderReferenceItem(panel, entry.index, entry.location, view));
      }

      details.append(summary, entries);
      sidebar.append(details);
    }
    return sidebar;
  }

  private renderReferenceItem(
    panel: CodeReferencePanelState,
    index: number,
    location: services.WorkspaceReferenceLocation,
    view: EditorView,
  ) {
    const item = document.createElement("button");
    item.className = `lsp-reference-item${index === panel.selectedIndex ? " is-active" : ""}`;
    item.type = "button";
    item.title = `${location.path}:${location.range.start.line}`;
    item.tabIndex = index === panel.selectedIndex ? 0 : -1;
    item.dataset.lspReferenceIndex = String(index);
    item.setAttribute("role", "option");
    item.setAttribute("aria-selected", String(index === panel.selectedIndex));
    item.addEventListener("mousedown", (event) => {
      if (event.button !== 0) {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      if (event.detail > 1) {
        this.openReference(index, view);
        return;
      }
      this.selectReference(index, view);
    });
    item.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      this.selectReference(index, view);
    });
    item.addEventListener("dblclick", (event) => {
      event.preventDefault();
      event.stopPropagation();
      this.openReference(index, view);
    });
    item.addEventListener("keydown", (event) => {
      if (event.key !== "Enter") {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      this.openReference(index, view);
    });

    const lineNumber = document.createElement("span");
    lineNumber.className = "lsp-reference-line-number";
    lineNumber.textContent = String(location.range.start.line);
    const code = document.createElement("span");
    code.className = "lsp-reference-line";
    const line = referenceTargetPreviewLine(location);
    appendHighlightedText(code, line.text, line.highlightStart, line.highlightEnd);
    item.append(lineNumber, code);
    return item;
  }

  private selectReference(index: number, view: EditorView) {
    const panel = referencesPanelForPath(this.workspaceID, this.path);
    if (!panel) {
      return;
    }
    panel.selectedIndex = clamp(index, 0, panel.locations.length - 1);
    const selected = selectedReference(panel);
    if (selected && !panel.expandedPaths.some((path) => sameWorkspacePath(path, selected.path))) {
      panel.expandedPaths = [...panel.expandedPaths, selected.path];
    }
    panel.renderKey = nextReferencePanelRenderKey();
    view.dispatch({ effects: setReferencePanelEffect.of(panel.renderKey) });
    queueSelectedReferenceFocus(this.workspaceID, this.path);
  }

  private selectRelativeReference(delta: number, view: EditorView) {
    const panel = referencesPanelForPath(this.workspaceID, this.path);
    if (!panel || panel.locations.length === 0) {
      return;
    }
    this.selectReference(wrapReferenceIndex(panel.selectedIndex + delta, panel.locations.length), view);
  }

  private openReference(index: number, view: EditorView) {
    const panel = referencesPanelForPath(this.workspaceID, this.path);
    const location = panel?.locations[index];
    if (!location) {
      return;
    }
    closeReferencesPanel(this.workspaceID, this.path, view);
    void this.openCodeFile(this.workspaceID, location.path, this.callbacks, {
      temporary: false,
      selectionPosition: location.range.start.offset,
    });
  }
}

function referencesPanelForPath(workspaceID: string, path: string) {
  const panel = ensureCodeState(workspaceID).referencesPanel;
  if (!panel || panel.path !== path) {
    return null;
  }
  return panel;
}

function selectedReference(panel: CodeReferencePanelState) {
  return panel.locations[clamp(panel.selectedIndex, 0, panel.locations.length - 1)] ?? null;
}

function queueSelectedReferenceFocus(workspaceID: string, path: string) {
  const requestID = ++referenceFocusRequestSeq;
  window.requestAnimationFrame(() => {
    if (requestID !== referenceFocusRequestSeq) {
      return;
    }
    const panel = referencesPanelForPath(workspaceID, path);
    if (!panel) {
      return;
    }
    const root = Array.from(document.querySelectorAll<HTMLElement>("[data-lsp-references-panel]"))
      .find((candidate) => candidate.dataset.lspWorkspaceId === workspaceID && candidate.dataset.lspPath === path);
    const target = root?.querySelector<HTMLButtonElement>(`[data-lsp-reference-index="${panel.selectedIndex}"]`);
    if (!target) {
      return;
    }
    target.focus({ preventScroll: true });
    target.scrollIntoView({ block: "nearest" });
  });
}

function wrapReferenceIndex(index: number, count: number) {
  if (count <= 0) {
    return 0;
  }
  return ((index % count) + count) % count;
}

function referenceGroups(locations: services.WorkspaceReferenceLocation[]): ReferenceGroup[] {
  const groups: ReferenceGroup[] = [];
  const indexByPath = new Map<string, number>();
  locations.forEach((location, index) => {
    const normalized = normalizedWorkspacePath(location.path);
    let groupIndex = indexByPath.get(normalized);
    if (groupIndex === undefined) {
      groupIndex = groups.length;
      indexByPath.set(normalized, groupIndex);
      groups.push({ path: location.path, entries: [] });
    }
    groups[groupIndex].entries.push({ index, location });
  });
  return groups;
}

function initialReferenceIndex(
  locations: services.WorkspaceReferenceLocation[],
  path: string,
  anchorPosition: number,
) {
  const exactIndex = locations.findIndex((location) => (
    sameWorkspacePath(location.path, path) &&
    location.range.start.offset <= anchorPosition &&
    location.range.end.offset >= anchorPosition
  ));
  return exactIndex >= 0 ? exactIndex : 0;
}

function initialExpandedReferencePaths(
  locations: services.WorkspaceReferenceLocation[],
  path: string,
  selectedIndex: number,
) {
  const expanded = new Set<string>();
  const current = locations.find((location) => sameWorkspacePath(location.path, path));
  if (current) {
    expanded.add(current.path);
  }
  const selected = locations[selectedIndex];
  if (selected) {
    expanded.add(selected.path);
  }
  return [...expanded];
}

function syncExpandedPath(
  panel: CodeReferencePanelState,
  path: string,
  open: boolean,
  view: EditorView,
) {
  const normalized = normalizedWorkspacePath(path);
  const next = panel.expandedPaths.filter((expandedPath) => normalizedWorkspacePath(expandedPath) !== normalized);
  if (open) {
    next.push(path);
  }
  panel.expandedPaths = next;
  panel.renderKey = nextReferencePanelRenderKey();
  view.dispatch({ effects: setReferencePanelEffect.of(panel.renderKey) });
}

function renderPreviewLine(line: services.WorkspaceReferencePreviewLine, selected: boolean) {
  const row = document.createElement("div");
  row.className = `lsp-references-preview-line${selected ? " is-selected" : ""}`;
  const lineNumber = document.createElement("span");
  lineNumber.className = "lsp-references-preview-line-number";
  lineNumber.textContent = String(line.line);
  const code = document.createElement("code");
  appendHighlightedText(code, line.text, line.highlightStart, line.highlightEnd);
  row.append(lineNumber, code);
  return row;
}

function referenceTargetPreviewLine(location: services.WorkspaceReferenceLocation) {
  const line = location.previewLines?.find((candidate) => candidate.line === location.range.start.line);
  if (line) {
    return line;
  }
  return services.WorkspaceReferencePreviewLine.createFrom({
    line: location.range.start.line,
    text: location.preview,
    highlightStart: Math.max(0, location.range.start.column - 1),
    highlightEnd: Math.max(0, location.range.end.column - 1),
  });
}

function appendHighlightedText(container: HTMLElement, text: string, highlightStart: number, highlightEnd: number) {
  const start = clamp(highlightStart, 0, text.length);
  const end = clamp(highlightEnd, start, text.length);
  if (highlightStart < 0 || end <= start) {
    container.textContent = text;
    return;
  }
  container.append(document.createTextNode(text.slice(0, start)));
  const highlight = document.createElement("mark");
  highlight.className = "lsp-reference-highlight";
  highlight.textContent = text.slice(start, end);
  container.append(highlight, document.createTextNode(text.slice(end)));
}

function sameWorkspacePath(left: string, right: string) {
  return normalizedWorkspacePath(left) === normalizedWorkspacePath(right);
}

function normalizedWorkspacePath(path: string) {
  return path.replaceAll("\\", "/").toLowerCase();
}

function nextReferencePanelRenderKey() {
  referencePanelRenderSeq += 1;
  return referencePanelRenderSeq;
}
