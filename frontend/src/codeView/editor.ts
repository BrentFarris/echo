import { HighlightStyle, indentUnit, syntaxHighlighting } from "@codemirror/language";
import { acceptCompletion } from "@codemirror/autocomplete";
import { languages as languageData } from "@codemirror/language-data";
import { countColumn, EditorSelection, EditorState, findColumn, Prec, RangeSetBuilder, Transaction, type Extension, type SelectionRange } from "@codemirror/state";
import { crosshairCursor, Decoration, type DecorationSet, EditorView, ViewPlugin, type ViewUpdate, keymap } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { tags } from "@lezer/highlight";
import { patchDirtyUI } from "./dom";
import { inlineCodeChatExtension } from "./inlineChat";
import { lspCompletionExtension, lspDefinitionExtension } from "./lsp";
import { referencesPanelExtension } from "./references";
import { activeCodeTab, ensureCodeState, findTab } from "./state";
import type { CodeViewCallbacks } from "./types";
import { clamp, editorDocumentLengthForFileContent, editorStateToFileContent } from "./utils";

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
let editorMountToken = 0;

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
  saveMountedEditorContent();
  if (mountedEditor) {
    mountedEditor.destroy();
  }
  mountedEditor = null;
  mountedEditorWorkspaceID = "";
  mountedEditorPath = "";
  editorMountToken++;
}

export function getMountedCodeEditor() {
  return { view: mountedEditor, workspaceID: mountedEditorWorkspaceID, path: mountedEditorPath };
}

export function mountedCodeEditorMatches(workspaceID: string, path: string) {
  return Boolean(mountedEditor && mountedEditorWorkspaceID === workspaceID && mountedEditorPath === path);
}

export async function mountActiveCodeEditor(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
  hooks: EditorFeatureHooks,
) {
  const mount = document.querySelector<HTMLElement>("[data-code-editor-mount]");
  const tab = activeCodeTab(workspaceID);
  destroyCodeEditor();
  if (!mount || !tab) {
    return;
  }

  const token = ++editorMountToken;
  const extensions = [
    basicSetup,
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
  if (!tab.untitled) {
    extensions.push(
      lspDefinitionExtension(workspaceID, tab.path, callbacks, hooks.openCodeFile),
      referencesPanelExtension(workspaceID, tab.path, callbacks, hooks.openCodeFile),
      inlineCodeChatExtension(workspaceID, tab.path, callbacks, { saveActiveCodeFile: hooks.saveActiveCodeFile }),
    );
  }
  if (callbacks.leadingWhitespaceIndicatorsEnabled()) {
    extensions.push(leadingWhitespaceIndicatorExtension());
  }
  const language = await languageExtensionForPath(tab.path);
  if (token !== editorMountToken) {
    return;
  }
  if (language) {
    extensions.push(language);
  }
  if (!tab.untitled) {
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

export function replaceMountedEditorContent(workspaceID: string, path: string, content: string) {
  if (!mountedEditor || mountedEditorWorkspaceID !== workspaceID || mountedEditorPath !== path) {
    return;
  }
  const scrollTop = mountedEditor.scrollDOM.scrollTop;
  const scrollLeft = mountedEditor.scrollDOM.scrollLeft;
  const selection = mountedEditor.state.selection.main;
  const nextDocLength = editorDocumentLengthForFileContent(content, mountedEditor.state.lineBreak);
  mountedEditor.dispatch({
    changes: { from: 0, to: mountedEditor.state.doc.length, insert: content },
    selection: {
      anchor: clamp(selection.anchor, 0, nextDocLength),
      head: clamp(selection.head, 0, nextDocLength),
    },
    annotations: Transaction.addToHistory.of(false),
  });
  restoreMountedEditorScroll(workspaceID, path, scrollTop, scrollLeft);
  window.requestAnimationFrame(() => {
    restoreMountedEditorScroll(workspaceID, path, scrollTop, scrollLeft);
  });
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
