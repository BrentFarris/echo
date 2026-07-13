import { HighlightStyle, indentUnit, syntaxHighlighting } from "@codemirror/language";
import { acceptCompletion } from "@codemirror/autocomplete";
import { isolateHistory } from "@codemirror/commands";
import { languages as languageData } from "@codemirror/language-data";
import { highlightSelectionMatches } from "@codemirror/search";
import { countColumn, EditorSelection, EditorState, findColumn, Prec, RangeSetBuilder, type Extension, type SelectionRange } from "@codemirror/state";
import { crosshairCursor, Decoration, type DecorationSet, EditorView, GutterMarker, ViewPlugin, type ViewUpdate, gutter, keymap } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { tags } from "@lezer/highlight";
import { patchDirtyUI } from "./dom";
import type { CodeFileTab } from "./types";
import { inlineCodeChatExtension } from "./inlineChat";
import { lspCompletionExtension, lspDefinitionExtension, lspRenameExtension } from "./lsp";
import { referencesPanelExtension } from "./references";
import { activeCodeTab, ensureCodeState, findTab } from "./state";
import type { CodeViewCallbacks } from "./types";
import { clamp, codeTabName, editorDocumentLengthForFileContent, editorStateToFileContent, escapeAttribute, escapeHtml, formatBytes, isAudioFile } from "./utils";
import { debugEditorExtension, releaseDebugEditor } from "./debug";

export type EditorFeatureHooks = {
  openCodeFile: (
    workspaceID: string,
    path: string,
    callbacks: CodeViewCallbacks,
    options: { temporary: boolean; selectionPosition?: number },
  ) => Promise<unknown>;
  navigateCodeHistory: (
    workspaceID: string,
    callbacks: CodeViewCallbacks,
    direction: -1 | 1,
  ) => Promise<unknown>;
  saveActiveCodeFile: (workspaceID: string, callbacks: CodeViewCallbacks) => Promise<boolean>;
};

const tabSize = 4;
const maxRectangularSelectionOffset = 2000;
let mountedEditor: EditorView | null = null;
let mountedEditorWorkspaceID = "";
let mountedEditorPath = "";
let mountedEditorLineSeparator = "\n";
let mountedEditorWhitespaceIndicators = false;
let mountedEditorCallbacks: CodeViewCallbacks | null = null;
let mountedEditorHooks: EditorFeatureHooks | null = null;
let editorMountToken = 0;

class GitChangedLineMarker extends GutterMarker {
  toDOM() {
    const marker = document.createElement("div");
    marker.className = "cm-git-change-marker";
    marker.title = "Changed in Git";
    marker.setAttribute("aria-label", "Changed in Git");
    return marker;
  }
}

const gitChangedLineMarker = new GitChangedLineMarker();

const codeEditorTheme = EditorView.theme({
  "&": {
    height: "100%",
    backgroundColor: "var(--code-editor-bg)",
    color: "var(--code-editor-text)",
  },
  ".cm-scroller": {
    fontFamily: '"Cascadia Mono", "SFMono-Regular", Consolas, monospace',
    fontSize: "0.88rem",
    lineHeight: "1.55",
  },
  ".cm-content": {
    caretColor: "var(--code-editor-caret)",
  },
  "&.cm-focused .cm-cursor, .cm-dropCursor": {
    borderLeftColor: "var(--code-editor-caret)",
  },
  ".cm-gutters": {
    backgroundColor: "var(--code-editor-gutter-bg)",
    color: "var(--code-editor-gutter-text)",
    borderRight: "1px solid var(--code-editor-border)",
  },
  ".cm-activeLine": {
    backgroundColor: "var(--code-editor-active-line-bg)",
    boxShadow: "inset 2px 0 0 var(--code-editor-active-line)",
  },
  ".cm-activeLineGutter": {
    backgroundColor: "var(--code-editor-active-gutter)",
  },
  ".cm-selectionBackground, &.cm-focused .cm-selectionBackground, &.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground": {
    backgroundColor: "var(--code-editor-selection) !important",
  },
  "& ::selection, &::selection": {
    backgroundColor: "var(--code-editor-selection) !important",
    color: "var(--code-editor-text) !important",
  },
  ".cm-selectionMatch": {
    backgroundColor: "var(--code-editor-selection-match)",
  },
  ".cm-matchingBracket": {
    backgroundColor: "var(--code-editor-selection-match)",
    color: "var(--code-editor-text)",
  },
  ".cm-nonmatchingBracket": {
    color: "var(--code-editor-invalid)",
  },
  ".cm-leading-space-indicator": {
    backgroundImage: "radial-gradient(circle, var(--code-editor-whitespace-indicator) 1px, transparent 1.2px)",
    backgroundPosition: "center",
    backgroundRepeat: "no-repeat",
  },
  ".cm-leading-tab-indicator": {
    backgroundImage: "linear-gradient(90deg, transparent 18%, var(--code-editor-whitespace-indicator) 18% 64%, transparent 64%)",
    backgroundPosition: "center",
    backgroundRepeat: "no-repeat",
    backgroundSize: "100% 1px",
  },
  "&.cm-focused": {
    outline: "none",
  },
}, {
  dark: window.matchMedia("(prefers-color-scheme: dark)").matches,
});

const codeHighlightStyle = HighlightStyle.define([
  { tag: tags.comment, color: "var(--code-token-comment)" },
  { tag: [tags.keyword, tags.controlKeyword, tags.definitionKeyword, tags.moduleKeyword], color: "var(--code-token-keyword)", fontWeight: "600" },
  { tag: [tags.atom, tags.bool, tags.null], color: "var(--code-token-atom)" },
  { tag: [tags.string, tags.special(tags.string), tags.character], color: "var(--code-token-string)" },
  { tag: [tags.number, tags.integer, tags.float], color: "var(--code-token-number)" },
  { tag: [tags.regexp, tags.escape], color: "var(--code-token-special)" },
  { tag: tags.variableName, color: "var(--code-token-variable)" },
  { tag: [tags.definition(tags.variableName), tags.function(tags.variableName)], color: "var(--code-token-function)" },
  { tag: [tags.typeName, tags.className, tags.namespace], color: "var(--code-token-type)" },
  { tag: [tags.propertyName, tags.attributeName], color: "var(--code-token-property)" },
  { tag: [tags.operator, tags.operatorKeyword, tags.punctuation], color: "var(--code-token-punctuation)" },
  { tag: tags.meta, color: "var(--code-token-meta)" },
  { tag: tags.invalid, color: "var(--code-editor-invalid)" },
]);

function tabIndentionExtensions(): Extension[] {
  return [
    EditorState.tabSize.of(tabSize),
    indentUnit.of("\t"),
    Prec.highest(
      keymap.of([
        {
          key: "Tab",
          run: (view) => {
            if (acceptCompletion(view)) {
              return true;
            }
            view.dispatch(view.state.replaceSelection("\t"));
            return true;
          },
        },
      ]),
    ),
  ];
}

export function destroyCodeEditor() {
  teardownMountedEditor(true);
}

function teardownMountedEditor(saveContent: boolean) {
  if (saveContent) {
    saveMountedEditorContent();
  }
  if (mountedEditor) {
    releaseDebugEditor(mountedEditor);
    mountedEditor.destroy();
  }
  mountedEditor = null;
  mountedEditorWorkspaceID = "";
  mountedEditorPath = "";
  mountedEditorLineSeparator = "\n";
  mountedEditorWhitespaceIndicators = false;
  mountedEditorCallbacks = null;
  mountedEditorHooks = null;
  editorMountToken++;
}

export function getMountedCodeEditor() {
  return { view: mountedEditor, workspaceID: mountedEditorWorkspaceID, path: mountedEditorPath };
}

export function selectedMountedCodeEditorText(workspaceID: string): string {
  if (!mountedEditor || mountedEditorWorkspaceID !== workspaceID || !mountedEditor.hasFocus) {
    return "";
  }
  const selection = mountedEditor.state.selection.main;
  if (selection.empty) {
    return "";
  }
  return mountedEditor.state.sliceDoc(selection.from, selection.to);
}

export function mountedCodeEditorMatches(workspaceID: string, path: string) {
  return Boolean(mountedEditor && mountedEditorWorkspaceID === workspaceID && mountedEditorPath === path);
}

export function focusMountedCodeEditor(workspaceID: string, path: string) {
  if (!mountedCodeEditorMatches(workspaceID, path) || !mountedEditor) {
    return false;
  }
  mountedEditor.focus();
  return true;
}

export async function mountActiveCodeEditor(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
  hooks: EditorFeatureHooks,
) {
  mountedEditorCallbacks = callbacks;
  mountedEditorHooks = hooks;
  const mount = document.querySelector<HTMLElement>("[data-code-editor-mount]");
  const tab = activeCodeTab(workspaceID);
  if (!mount || !tab) {
    destroyCodeEditor();
    return;
  }

  // Render media tabs as dedicated viewers instead of CodeMirror
  if (tab.isMedia && tab.mediaDataUrl) {
    destroyCodeEditor();
    if (tab.mediaMimeType?.startsWith("audio/")) {
      mount.innerHTML = renderAudioViewer(tab);
      bindAudioViewerEvents(mount);
    } else if (tab.mediaMimeType?.startsWith("video/")) {
      mount.innerHTML = renderVideoViewer(tab);
    } else if (tab.mediaMimeType?.startsWith("audio/")) {
      mount.innerHTML = renderAudioViewer(tab);
    } else {
      mount.innerHTML = renderImageViewer(tab);
      bindImageViewerEvents(mount, workspaceID, tab.path, callbacks);
    }
    return;
  }

  const whitespaceIndicators = callbacks.leadingWhitespaceIndicatorsEnabled();
  if (
    mountedCodeEditorMatches(workspaceID, tab.path) &&
    mountedEditor &&
    mountedEditorLineSeparator === tab.lineSeparator &&
    mountedEditorWhitespaceIndicators === whitespaceIndicators
  ) {
    if (mountedEditor.dom.parentElement !== mount) {
      mount.innerHTML = "";
      mount.appendChild(mountedEditor.dom);
    }
    updateTabEditorState(workspaceID, tab.path, mountedEditor);
    return;
  }

  destroyCodeEditor();
  mountedEditorCallbacks = callbacks;
  mountedEditorHooks = hooks;
  const token = ++editorMountToken;
  const gitChangedLineGutter = gitChangedLineGutterExtension(workspaceID, tab.path, callbacks);
  const extensions = [
    ...(gitChangedLineGutter ? [gitChangedLineGutter] : []),
    basicSetup,
    highlightSelectionMatches({
      minSelectionLength: 1,
      maxMatches: 2000,
      wholeWords: false,
    }),
    ...tabIndentionExtensions(),
    EditorState.lineSeparator.of(tab.lineSeparator),
    EditorState.allowMultipleSelections.of(true),
    EditorView.lineWrapping,
    codeEditorTheme,
    syntaxHighlighting(codeHighlightStyle),
    codeNavigationHistoryKeymap(workspaceID, callbacks, hooks),
    rectangularAltSelectionExtension(),
    crosshairCursor(),
    EditorView.updateListener.of((update) => {
      if (update.selectionSet || update.docChanged) {
        updateTabEditorState(workspaceID, tab.path, update.view);
      }
      if (!update.docChanged) {
        return;
      }
      updateTabContent(
        workspaceID,
        tab.path,
        editorStateToFileContent(update.state),
      );
    }),
  ];
  if (!tab.untitled && !tab.external) {
    extensions.push(
      debugEditorExtension(workspaceID, tab.path),
      lspDefinitionExtension(workspaceID, tab.path, callbacks, hooks.openCodeFile),
      lspRenameExtension(workspaceID, tab.path, callbacks),
      referencesPanelExtension(workspaceID, tab.path, callbacks, hooks.openCodeFile),
      inlineCodeChatExtension(workspaceID, tab.path, callbacks, { saveActiveCodeFile: hooks.saveActiveCodeFile }),
    );
  }
  if (whitespaceIndicators) {
    extensions.push(leadingWhitespaceIndicatorExtension());
  }
  const language = await languageExtensionForPath(tab.path);
  if (token !== editorMountToken) {
    return;
  }
  if (language) {
    extensions.push(language);
  }
  if (!tab.untitled && !tab.external) {
    extensions.push(lspCompletionExtension(workspaceID, tab.path, callbacks));
  }
  const docLength = editorDocumentLengthForFileContent(tab.content, tab.lineSeparator);
  const selectionAnchor = clamp(tab.selectionAnchor, 0, docLength);
  const selectionHead = clamp(tab.selectionHead, 0, docLength);
  mountedEditor = new EditorView({
    state: EditorState.create({
      doc: tab.content,
      selection: { anchor: selectionAnchor, head: selectionHead },
      extensions,
    }),
    parent: mount,
  });
  mountedEditorWorkspaceID = workspaceID;
  mountedEditorPath = tab.path;
  mountedEditorLineSeparator = tab.lineSeparator;
  mountedEditorWhitespaceIndicators = whitespaceIndicators;
  const initialScrollTop = tab.scrollTop;
  const initialScrollLeft = tab.scrollLeft;
  restoreMountedEditorScroll(workspaceID, tab.path, initialScrollTop, initialScrollLeft);
  if (tab.pendingRevealPosition !== undefined) {
    const position = clamp(tab.pendingRevealPosition, 0, mountedEditor.state.doc.length);
    const y = tab.pendingRevealScroll ?? "center";
    tab.pendingRevealPosition = undefined;
    tab.pendingRevealScroll = undefined;
    mountedEditor.dispatch({
      selection: { anchor: position },
      effects: EditorView.scrollIntoView(position, { y }),
    });
  } else {
    window.requestAnimationFrame(() => {
      restoreMountedEditorScroll(workspaceID, tab.path, initialScrollTop, initialScrollLeft);
    });
  }
  if (shouldFocusMountedEditor(workspaceID)) {
    mountedEditor.focus();
  }
}

function gitChangedLineGutterExtension(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
): Extension | null {
  const changedLines = new Set(
    callbacks.gitChangedLineNumbers(workspaceID, path).filter((line) => Number.isInteger(line) && line > 0),
  );
  if (!changedLines.size) {
    return null;
  }
  return gutter({
    class: "cm-git-change-gutter",
    initialSpacer: () => gitChangedLineMarker,
    lineMarker: (view, line) => {
      const lineNumber = view.state.doc.lineAt(line.from).number;
      return changedLines.has(lineNumber) ? gitChangedLineMarker : null;
    },
  });
}

function shouldFocusMountedEditor(workspaceID: string) {
  const state = ensureCodeState(workspaceID);
  return !state.searchFocused && !state.textSearchOpen && !state.textSearchFocusedField && !state.quickOpen.open;
}

function codeNavigationHistoryKeymap(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
  hooks: EditorFeatureHooks,
) {
  return Prec.highest(
    keymap.of([
      {
        key: "Alt-ArrowLeft",
        run: () => {
          void hooks.navigateCodeHistory(workspaceID, callbacks, -1);
          return true;
        },
      },
      {
        key: "Alt-Left",
        run: () => {
          void hooks.navigateCodeHistory(workspaceID, callbacks, -1);
          return true;
        },
      },
      {
        key: "Alt-ArrowRight",
        run: () => {
          void hooks.navigateCodeHistory(workspaceID, callbacks, 1);
          return true;
        },
      },
      {
        key: "Alt-Right",
        run: () => {
          void hooks.navigateCodeHistory(workspaceID, callbacks, 1);
          return true;
        },
      },
    ]),
  );
}

function rectangularAltSelectionExtension() {
  return Prec.highest(EditorView.mouseSelectionStyle.of((view, event) => {
    if (!event.altKey || event.button !== 0) {
      return null;
    }
    const start = event.shiftKey
      ? rectangularSelectionPositionFromOffset(view, view.state.selection.main.head)
      : rectangularSelectionPositionAtCoords(view, event);
    if (!start) {
      return null;
    }
    let startSelection = view.state.selection;
    return {
      update(update) {
        if (!update.docChanged) {
          return;
        }
        const lineStart = update.changes.mapPos(update.startState.doc.line(start.line).from);
        const line = update.state.doc.lineAt(lineStart);
        start.line = line.number;
        start.off = Math.min(start.off, line.length);
        startSelection = startSelection.map(update.changes);
      },
      get(event, _extend, multiple) {
        const current = rectangularSelectionPositionAtCoords(view, event);
        if (!current) {
          return startSelection;
        }
        const ranges = rectangularSelectionRanges(view.state, start, current);
        if (!ranges.length) {
          return startSelection;
        }
        if (multiple) {
          return EditorSelection.create(ranges.concat(startSelection.ranges), Math.max(0, ranges.length - 1));
        }
        return EditorSelection.create(ranges, ranges.length - 1);
      },
    };
  }));
}

type RectangularSelectionPosition = {
  line: number;
  col: number;
  off: number;
};

function rectangularSelectionPositionFromOffset(
  view: EditorView,
  offset: number,
): RectangularSelectionPosition {
  const line = view.state.doc.lineAt(clamp(offset, 0, view.state.doc.length));
  const off = clamp(offset - line.from, 0, line.length);
  return {
    line: line.number,
    col: off > maxRectangularSelectionOffset
      ? -1
      : countColumn(line.text, view.state.tabSize, off),
    off,
  };
}

function rectangularSelectionPositionAtCoords(
  view: EditorView,
  event: MouseEvent,
): RectangularSelectionPosition | null {
  const offset = view.posAtCoords({ x: event.clientX, y: event.clientY }, false);
  const line = view.state.doc.lineAt(offset);
  const off = offset - line.from;
  return {
    line: line.number,
    col: off > maxRectangularSelectionOffset
      ? -1
      : off === line.length
        ? rectangularSelectionAbsoluteColumn(view, event.clientX)
        : countColumn(line.text, view.state.tabSize, off),
    off,
  };
}

function rectangularSelectionAbsoluteColumn(view: EditorView, x: number) {
  const reference = view.coordsAtPos(view.viewport.from);
  return reference ? Math.round(Math.abs((reference.left - x) / view.defaultCharacterWidth)) : -1;
}

function rectangularSelectionRanges(
  state: EditorState,
  anchor: RectangularSelectionPosition,
  head: RectangularSelectionPosition,
) {
  const startLine = Math.min(anchor.line, head.line);
  const endLine = Math.max(anchor.line, head.line);
  const ranges: SelectionRange[] = [];
  if (
    anchor.off > maxRectangularSelectionOffset ||
    head.off > maxRectangularSelectionOffset ||
    anchor.col < 0 ||
    head.col < 0
  ) {
    const startOff = Math.min(anchor.off, head.off);
    const endOff = Math.max(anchor.off, head.off);
    for (let lineNumber = startLine; lineNumber <= endLine; lineNumber++) {
      const line = state.doc.line(lineNumber);
      const start = Math.min(line.from + startOff, line.to);
      const end = Math.min(line.from + endOff, line.to);
      ranges.push(start === end ? EditorSelection.cursor(start) : EditorSelection.range(start, end));
    }
    return ranges;
  }

  const startCol = Math.min(anchor.col, head.col);
  const endCol = Math.max(anchor.col, head.col);
  for (let lineNumber = startLine; lineNumber <= endLine; lineNumber++) {
    const line = state.doc.line(lineNumber);
    const start = findColumn(line.text, startCol, state.tabSize, true);
    if (start < 0) {
      ranges.push(EditorSelection.cursor(line.to));
      continue;
    }
    const end = findColumn(line.text, endCol, state.tabSize);
    ranges.push(start === end
      ? EditorSelection.cursor(line.from + start)
      : EditorSelection.range(line.from + start, line.from + end));
  }
  return ranges;
}

function leadingWhitespaceIndicatorExtension() {
  return ViewPlugin.fromClass(
    class {
      decorations: DecorationSet;

      constructor(view: EditorView) {
        this.decorations = buildLeadingWhitespaceDecorations(view);
      }

      update(update: ViewUpdate) {
        if (update.docChanged || update.viewportChanged) {
          this.decorations = buildLeadingWhitespaceDecorations(update.view);
        }
      }
    },
    {
      decorations: (plugin) => plugin.decorations,
    },
  );
}

function buildLeadingWhitespaceDecorations(view: EditorView) {
  const builder = new RangeSetBuilder<Decoration>();
  const doc = view.state.doc;
  for (const { from, to } of view.visibleRanges) {
    let line = doc.lineAt(from);
    for (;;) {
      addLeadingWhitespaceDecorations(builder, line.from, line.text);
      if (line.to >= to || line.number >= doc.lines) {
        break;
      }
      line = doc.line(line.number + 1);
    }
  }
  return builder.finish();
}

function addLeadingWhitespaceDecorations(
  builder: RangeSetBuilder<Decoration>,
  lineFrom: number,
  text: string,
) {
  for (let index = 0; index < text.length; index++) {
    const char = text[index];
    if (char !== " " && char !== "\t") {
      return;
    }
    const className =
      char === "\t" ? "cm-leading-tab-indicator" : "cm-leading-space-indicator";
    builder.add(lineFrom + index, lineFrom + index + 1, Decoration.mark({ class: className }));
  }
}

async function languageExtensionForPath(path: string) {
  const fileName = path.split("/").pop() ?? path;
  const extension = fileName.includes(".")
    ? fileName.split(".").pop()?.toLowerCase() ?? ""
    : "";
  const match = languageData.find((language) => {
    if (language.filename?.test(fileName)) {
      return true;
    }
    return extension !== "" && language.extensions.includes(extension);
  });
  if (!match) {
    return null;
  }
  try {
    return await match.load();
  } catch {
    return null;
  }
}

function updateTabContent(workspaceID: string, path: string, content: string) {
  const tab = findTab(workspaceID, path);
  if (!tab) {
    return;
  }
  tab.content = content;
  tab.bytes = new TextEncoder().encode(content).length;
  tab.dirty = tab.content !== tab.savedContent;
  if (tab.temporary && tab.dirty) {
    tab.temporary = false;
  }
  const docLength = editorDocumentLengthForFileContent(tab.content, tab.lineSeparator);
  tab.selectionAnchor = clamp(tab.selectionAnchor, 0, docLength);
  tab.selectionHead = clamp(tab.selectionHead, 0, docLength);
  patchDirtyUI(workspaceID, tab);
}

function updateTabEditorState(workspaceID: string, path: string, view: EditorView) {
  const tab = findTab(workspaceID, path);
  if (!tab) {
    return;
  }
  const selection = view.state.selection.main;
  tab.selectionAnchor = selection.anchor;
  tab.selectionHead = selection.head;
  tab.scrollTop = view.scrollDOM.scrollTop;
  tab.scrollLeft = view.scrollDOM.scrollLeft;
}

type MountedEditorContentChangeOptions = {
  userEvent?: string;
};

export function applyMountedEditorUserContentChange(
  workspaceID: string,
  path: string,
  content: string,
  options: MountedEditorContentChangeOptions = {},
) {
  if (!mountedEditor || mountedEditorWorkspaceID !== workspaceID || mountedEditorPath !== path) {
    return false;
  }
  const current = mountedEditor.state.sliceDoc(0);
  const change = minimalContentChange(current, content);
  if (!change) {
    return true;
  }
  const scrollTop = mountedEditor.scrollDOM.scrollTop;
  const scrollLeft = mountedEditor.scrollDOM.scrollLeft;
  mountedEditor.dispatch({
    changes: change,
    annotations: isolateHistory.of("full"),
    userEvent: options.userEvent ?? "input",
  });
  restoreMountedEditorScroll(workspaceID, path, scrollTop, scrollLeft);
  window.requestAnimationFrame(() => {
    restoreMountedEditorScroll(workspaceID, path, scrollTop, scrollLeft);
  });
  return true;
}

export function resetMountedEditorDocument(
  workspaceID: string,
  path: string,
  _content: string,
  _lineSeparator: string,
) {
  if (!mountedEditor || mountedEditorWorkspaceID !== workspaceID || mountedEditorPath !== path) {
    return false;
  }
  const callbacks = mountedEditorCallbacks;
  const hooks = mountedEditorHooks;
  const mount = mountedEditor.dom.parentElement;
  teardownMountedEditor(false);
  if (!callbacks || !hooks || !mount?.isConnected) {
    return true;
  }
  void mountActiveCodeEditor(workspaceID, callbacks, hooks);
  return true;
}

function minimalContentChange(current: string, next: string) {
  if (current === next) {
    return null;
  }
  let prefix = 0;
  const maxPrefix = Math.min(current.length, next.length);
  while (prefix < maxPrefix && current.charCodeAt(prefix) === next.charCodeAt(prefix)) {
    prefix++;
  }
  let suffix = 0;
  const maxSuffix = Math.min(current.length - prefix, next.length - prefix);
  while (
    suffix < maxSuffix &&
    current.charCodeAt(current.length - 1 - suffix) === next.charCodeAt(next.length - 1 - suffix)
  ) {
    suffix++;
  }
  return {
    from: prefix,
    to: current.length - suffix,
    insert: next.slice(prefix, next.length - suffix),
  };
}

function restoreMountedEditorScroll(
  workspaceID: string,
  path: string,
  scrollTop: number,
  scrollLeft: number,
) {
  if (!mountedEditor || mountedEditorWorkspaceID !== workspaceID || mountedEditorPath !== path) {
    return;
  }
  mountedEditor.scrollDOM.scrollTop = scrollTop;
  mountedEditor.scrollDOM.scrollLeft = scrollLeft;
  updateTabEditorState(workspaceID, path, mountedEditor);
}

export function saveMountedEditorContent() {
  if (!mountedEditor || !mountedEditorWorkspaceID || !mountedEditorPath) {
    return;
  }
  updateTabEditorState(mountedEditorWorkspaceID, mountedEditorPath, mountedEditor);
  updateTabContent(
    mountedEditorWorkspaceID,
    mountedEditorPath,
    editorStateToFileContent(mountedEditor.state),
  );
}

// ─── Image Viewer ──────────────────────────────────────────────

const zoomInSVG = `<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="11" cy="11" r="7"/><path d="m21 21-4.35-4.35"/><path d="M8 11h6"/><path d="M11 8v6"/></svg>`;
const zoomOutSVG = `<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="11" cy="11" r="7"/><path d="m21 21-4.35-4.35"/><path d="M8 11h6"/></svg>`;
const zoomFitSVG = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 3h6v6"/><path d="M9 21H3v-6"/><path d="M21 3l-7 7"/><path d="M3 21l7-7"/></svg>`;

function renderImageViewer(tab: CodeFileTab): string {
  const zoomPercent = Math.round((tab.zoomLevel ?? 1) * 100);
  return `
    <div class="code-image-viewer" data-code-image-viewer>
      <div class="code-image-toolbar" data-code-image-toolbar>
        <button class="icon-button code-image-zoom-out" type="button" title="Zoom out (Ctrl+-)" aria-label="Zoom out" data-code-action="image-zoom-out">
          ${zoomOutSVG}
        </button>
        <span class="code-image-zoom-level">${escapeHtml(String(zoomPercent))}%</span>
        <button class="icon-button code-image-zoom-in" type="button" title="Zoom in (Ctrl++)" aria-label="Zoom in" data-code-action="image-zoom-in">
          ${zoomInSVG}
        </button>
        <button class="icon-button code-image-zoom-fit" type="button" title="Fit to view (0)" aria-label="Reset zoom" data-code-action="image-zoom-fit">
          ${zoomFitSVG}
        </button>
      </div>
      <div class="code-image-canvas" data-code-image-canvas>
        ${tab.mediaError
          ? `<div class="code-image-error">${escapeHtml(tab.mediaError)}</div>`
          : `<img
              src="${escapeAttribute(tab.mediaDataUrl ?? "")}"
              alt="${escapeAttribute(codeTabName(tab))}"
              title="${escapeAttribute(tab.path)}"
              draggable="false"
              data-code-image
              style="transform: scale(${tab.zoomLevel ?? 1})"
            />`
        }
      </div>
    </div>
  `;
}

function bindImageViewerEvents(
  mount: HTMLElement,
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  const toolbar = mount.querySelector<HTMLElement>("[data-code-image-toolbar]");
  if (!toolbar) return;

  toolbar.querySelectorAll<HTMLElement>("[data-code-action]").forEach((el) => {
    el.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      const action = el.dataset.codeAction ?? "";
      handleImageZoomAction(workspaceID, path, action, callbacks);
    });
  });

  // Mouse wheel zoom on canvas (Ctrl/Cmd + scroll)
  const canvas = mount.querySelector<HTMLElement>("[data-code-image-canvas]");
  if (canvas) {
    canvas.addEventListener("wheel", (event: WheelEvent) => {
      if (!event.ctrlKey && !event.metaKey) return;
      event.preventDefault();
      event.stopPropagation();
      const delta = event.deltaY > 0 ? -0.1 : 0.1;
      applyImageZoom(workspaceID, path, delta, callbacks);
    }, { passive: false });
  }
}

function handleImageZoomAction(
  workspaceID: string,
  path: string,
  action: string,
  callbacks: CodeViewCallbacks,
) {
  if (action === "image-zoom-in") {
    applyImageZoom(workspaceID, path, 0.25, callbacks);
  } else if (action === "image-zoom-out") {
    applyImageZoom(workspaceID, path, -0.25, callbacks);
  } else if (action === "image-zoom-fit") {
    resetImageZoom(workspaceID, path, callbacks);
  }
}

function applyImageZoom(
  workspaceID: string,
  path: string,
  delta: number,
  callbacks: CodeViewCallbacks,
) {
  const tab = findTab(workspaceID, path);
  if (!tab || !tab.isMedia) return;
  const current = tab.zoomLevel ?? 1;
  tab.zoomLevel = clamp(current + delta, 0.1, 5);
  patchImageZoomUI(tab);
  callbacks.render();
}

function resetImageZoom(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  const tab = findTab(workspaceID, path);
  if (!tab || !tab.isMedia) return;
  tab.zoomLevel = 1;
  patchImageZoomUI(tab);
  callbacks.render();
}

function patchImageZoomUI(tab: CodeFileTab) {
  const zoomLevelEl = document.querySelector<HTMLElement>(".code-image-zoom-level");
  if (zoomLevelEl) {
    zoomLevelEl.textContent = `${Math.round((tab.zoomLevel ?? 1) * 100)}%`;
  }
  const img = document.querySelector<HTMLImageElement>("[data-code-image]");
  if (img) {
    img.style.transform = `scale(${tab.zoomLevel ?? 1})`;
  }
}

// ─── Audio Viewer ──────────────────────────────────────────────

function renderAudioViewer(tab: CodeFileTab): string {
  return `
    <div class="code-audio-viewer" data-code-audio-viewer>
      <div class="code-audio-player">
        <audio src="${escapeAttribute(tab.mediaDataUrl ?? "")}" preload="metadata" data-code-audio></audio>
        <button class="code-audio-play" type="button" aria-label="Play audio" data-code-audio-play>
          <span data-code-audio-play-icon>▶</span>
        </button>
        <input
          class="code-audio-seek"
          type="range"
          min="0"
          max="1000"
          value="0"
          step="1"
          aria-label="Audio position"
          data-code-audio-seek
        />
        <span class="code-audio-time" data-code-audio-time>0:00 / 0:00</span>
      </div>
    </div>
  `;
}

function bindAudioViewerEvents(mount: HTMLElement) {
  const audio = mount.querySelector<HTMLAudioElement>("[data-code-audio]");
  const play = mount.querySelector<HTMLButtonElement>("[data-code-audio-play]");
  const playIcon = mount.querySelector<HTMLElement>("[data-code-audio-play-icon]");
  const seek = mount.querySelector<HTMLInputElement>("[data-code-audio-seek]");
  const time = mount.querySelector<HTMLElement>("[data-code-audio-time]");
  if (!audio || !play || !playIcon || !seek || !time) {
    return;
  }

  const update = () => {
    const duration = Number.isFinite(audio.duration) ? audio.duration : 0;
    const current = Number.isFinite(audio.currentTime) ? audio.currentTime : 0;
    seek.value = duration > 0 ? String(Math.round((current / duration) * 1000)) : "0";
    time.textContent = `${formatAudioTime(current)} / ${formatAudioTime(duration)}`;
  };
  const updatePlaybackState = () => {
    const paused = audio.paused;
    playIcon.textContent = paused ? "▶" : "❚❚";
    play.setAttribute("aria-label", paused ? "Play audio" : "Pause audio");
    play.classList.toggle("is-playing", !paused);
  };

  play.addEventListener("click", () => {
    if (audio.paused) {
      void audio.play().catch(() => {
        time.textContent = "Unable to play audio";
      });
    } else {
      audio.pause();
    }
  });
  seek.addEventListener("input", () => {
    if (Number.isFinite(audio.duration) && audio.duration > 0) {
      audio.currentTime = (Number(seek.value) / 1000) * audio.duration;
      update();
    }
  });
  audio.addEventListener("loadedmetadata", update);
  audio.addEventListener("durationchange", update);
  audio.addEventListener("timeupdate", update);
  audio.addEventListener("play", updatePlaybackState);
  audio.addEventListener("pause", updatePlaybackState);
  audio.addEventListener("ended", updatePlaybackState);
  update();
  updatePlaybackState();
}

function formatAudioTime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) {
    return "0:00";
  }
  const wholeSeconds = Math.floor(seconds);
  const minutes = Math.floor(wholeSeconds / 60);
  const remainder = String(wholeSeconds % 60).padStart(2, "0");
  return `${minutes}:${remainder}`;
}

function renderVideoViewer(tab: CodeFileTab): string {
  return `
    <div class="code-video-viewer" data-code-video-viewer>
      <div class="code-video-container" data-code-video-container>
        ${tab.mediaError
          ? `<div class="code-video-error">${escapeHtml(tab.mediaError)}</div>`
          : `<video
              src="${escapeAttribute(tab.mediaDataUrl ?? "")}"
              controls
              autoplay
              playsinline
              preload="metadata"
              data-code-video
            ></video>`
        }
      </div>
    </div>
  `;
}
