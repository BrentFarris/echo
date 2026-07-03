import { type Completion, type CompletionContext, type CompletionResult } from "@codemirror/autocomplete";
import { EditorState, Prec, StateEffect, StateField, Transaction, type Extension } from "@codemirror/state";
import { EditorView, keymap, showTooltip, type Tooltip } from "@codemirror/view";
import { CompleteWorkspaceFile, FindWorkspaceFileDefinition, FindWorkspaceFileImplementations, FindWorkspaceFileReferences, PrepareWorkspaceSymbolRename, RenameWorkspaceSymbol } from "../backend/services";
import { services } from "../../wailsjs/go/models";
import { patchDirtyUI } from "./dom";
import { applySavedFile, ensureCodeState, findTab } from "./state";
import type { CodeViewCallbacks } from "./types";
import { openReferencesPanel } from "./references";
import {
  clamp,
  editableWorkspaceFile,
  editorPositionToFileContentOffset,
  editorStateToFileContent,
  fileContentOffsetToEditorPosition,
  normalizeEditorLineBreaks,
} from "./utils";

type OpenCodeFileForDefinition = (
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  options: { temporary: boolean; selectionPosition?: number },
) => Promise<unknown>;

const lspCompletionValidFor = /^[A-Za-z0-9_]*$/;
const lspCompletionTriggerWord = /[A-Za-z_][A-Za-z0-9_]*$/;
const lspCompletionTriggerDot = /\.$/;
const lspCompletionTriggerCppMember = /(?:\.|->|::)$/;
const lspSourceExtensions = [
  ".go",
  ".c",
  ".cc",
  ".cpp",
  ".cxx",
  ".c++",
  ".h",
  ".hh",
  ".hpp",
  ".hxx",
  ".ipp",
  ".inl",
  ".ixx",
  ".cppm",
];
const lspCompletionErrors = new Map<string, string>();
const setRenameTooltipEffect = StateEffect.define<RenameTooltipState>();
const clearRenameTooltipEffect = StateEffect.define<void>();

type RenameTooltipState = {
  from: number;
  to: number;
  originalName: string;
  selectionAnchor: number;
  selectionHead: number;
  requestPosition: number;
};

export function lspDefinitionExtension(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  openCodeFile: OpenCodeFileForDefinition,
): Extension {
  if (!isLspSourcePath(path)) {
    return [];
  }
  return Prec.highest(
    keymap.of([
      {
        key: "F12",
        run: (view) => {
          void goToLspDefinition(workspaceID, path, view, callbacks, openCodeFile);
          return true;
        },
      },
      {
        key: "Shift-F12",
        run: (view) => {
          void showLspReferences(workspaceID, path, view, callbacks);
          return true;
        },
      },
      {
        key: "Ctrl-F12",
        run: (view) => {
          void goToLspImplementation(workspaceID, path, view, callbacks, openCodeFile);
          return true;
        },
      },
    ]),
  );
}

export function lspRenameExtension(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
): Extension {
  if (!isLspSourcePath(path)) {
    return [];
  }
  const field = StateField.define<RenameTooltipState | null>({
    create() {
      return null;
    },
    update(value, transaction) {
      if (transaction.docChanged) {
        value = null;
      }
      for (const effect of transaction.effects) {
        if (effect.is(setRenameTooltipEffect)) {
          value = effect.value;
        } else if (effect.is(clearRenameTooltipEffect)) {
          value = null;
        }
      }
      return value;
    },
    provide: (renameField) => showTooltip.from(renameField, (value) => (
      value ? renameTooltip(workspaceID, path, callbacks, value) : null
    )),
  });
  return [
    field,
    Prec.highest(keymap.of([{
      key: "F2",
      run(view) {
        void startLspRename(workspaceID, path, view, callbacks);
        return true;
      },
    }])),
  ];
}

async function startLspRename(
  workspaceID: string,
  path: string,
  view: EditorView,
  callbacks: CodeViewCallbacks,
) {
  const content = editorStateToFileContent(view.state);
  const selection = view.state.selection.main;
  const requestPosition = editorPositionToFileContentOffset(view.state, selection.head);
  const lineSeparator = view.state.lineBreak;
  try {
    const response = services.WorkspacePrepareRenameResponse.createFrom(
      await PrepareWorkspaceSymbolRename(
        workspaceID,
        services.WorkspaceDefinitionRequest.createFrom({
          filePath: path,
          content,
          position: requestPosition,
        }),
      ),
    );
    if (!view.dom.isConnected || editorStateToFileContent(view.state) !== content) {
      return;
    }
    if (!response.available) {
      callbacks.pushToast(response.message || "The selected symbol cannot be renamed.", "info");
      return;
    }
    const from = fileContentOffsetToEditorPosition(content, lineSeparator, response.from);
    const to = fileContentOffsetToEditorPosition(content, lineSeparator, response.to);
    if (to <= from || from < 0 || to > view.state.doc.length) {
      callbacks.pushToast("The selected symbol cannot be renamed.", "info");
      return;
    }
    view.dispatch({
      effects: setRenameTooltipEffect.of({
        from,
        to,
        originalName: view.state.sliceDoc(from, to) || response.placeholder || "",
        selectionAnchor: selection.anchor,
        selectionHead: selection.head,
        requestPosition,
      }),
    });
  } catch (error) {
    callbacks.pushToast(callbacks.errorMessage(error), "error");
  }
}

function renameTooltip(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  rename: RenameTooltipState,
): Tooltip {
  return {
    pos: rename.from,
    end: rename.to,
    above: true,
    strictSide: false,
    create(view) {
      const wrapper = document.createElement("div");
      wrapper.className = "code-symbol-rename";
      const input = document.createElement("input");
      input.className = "code-symbol-rename-input";
      input.type = "text";
      input.value = rename.originalName;
      input.size = Math.max(18, Math.min(60, rename.originalName.length + 2));
      input.setAttribute("aria-label", "New symbol name");
      input.autocomplete = "off";
      input.spellcheck = false;
      wrapper.append(input);

      let submitting = false;
      const close = (focusEditor: boolean) => {
        if (!view.dom.isConnected) {
          return;
        }
        view.dispatch({ effects: clearRenameTooltipEffect.of() });
        if (focusEditor) {
          view.focus();
        }
      };
      const submit = async () => {
        if (submitting) {
          return;
        }
        const newName = input.value;
        if (newName === rename.originalName) {
          close(true);
          return;
        }
        submitting = true;
        input.disabled = true;
        try {
          const state = ensureCodeState(workspaceID);
          const openFiles = state.tabs
            .filter((tab) => !tab.untitled && !tab.external)
            .map((tab) => services.WorkspaceRenameFileContent.createFrom({
              filePath: tab.path,
              content: sameWorkspacePath(tab.path, path)
                ? editorStateToFileContent(view.state)
                : tab.content,
              modifiedAt: tab.modifiedAt,
            }));
          const response = services.WorkspaceRenameResponse.createFrom(
            await RenameWorkspaceSymbol(
              workspaceID,
              services.WorkspaceRenameRequest.createFrom({
                filePath: path,
                content: editorStateToFileContent(view.state),
                position: rename.requestPosition,
                newName,
                openFiles,
              }),
            ),
          );
          if (!response.applied) {
            callbacks.pushToast(response.message || "The selected symbol cannot be renamed.", "info");
            close(true);
            return;
          }
          applyWorkspaceRenameResponse(workspaceID, path, view, rename, newName, response);
        } catch (error) {
          submitting = false;
          input.disabled = false;
          callbacks.pushToast(callbacks.errorMessage(error), "error");
          window.requestAnimationFrame(() => input.focus());
        }
      };
      input.addEventListener("keydown", (event) => {
        event.stopPropagation();
        if (event.key === "Escape") {
          event.preventDefault();
          close(true);
        } else if (event.key === "Enter") {
          event.preventDefault();
          void submit();
        }
      });
      input.addEventListener("blur", () => {
        if (!submitting) {
          close(false);
        }
      });
      window.requestAnimationFrame(() => {
        if (!input.isConnected) {
          return;
        }
        input.focus();
        selectRenameInputRange(input, rename);
      });
      return { dom: wrapper };
    },
  };
}

function selectRenameInputRange(input: HTMLInputElement, rename: RenameTooltipState) {
  const selectionFrom = Math.max(rename.from, Math.min(rename.selectionAnchor, rename.selectionHead));
  const selectionTo = Math.min(rename.to, Math.max(rename.selectionAnchor, rename.selectionHead));
  if (selectionTo > selectionFrom) {
    input.setSelectionRange(
      selectionFrom - rename.from,
      selectionTo - rename.from,
      rename.selectionAnchor <= rename.selectionHead ? "forward" : "backward",
    );
    return;
  }
  const caret = clamp(rename.selectionHead - rename.from, 0, input.value.length);
  input.setSelectionRange(caret, caret);
}

function applyWorkspaceRenameResponse(
  workspaceID: string,
  path: string,
  view: EditorView,
  rename: RenameTooltipState,
  newName: string,
  response: services.WorkspaceRenameResponse,
) {
  let activeContent = "";
  for (const rawFile of response.files ?? []) {
    const file = services.WorkspaceFile.createFrom(rawFile);
    applySavedFile(workspaceID, file);
    const tab = findTab(workspaceID, file.path);
    if (tab) {
      patchDirtyUI(workspaceID, tab);
    }
    if (sameWorkspacePath(file.path, path)) {
      activeContent = editableWorkspaceFile(file).content;
    }
  }
  if (!activeContent || !view.dom.isConnected) {
    view.dispatch({ effects: clearRenameTooltipEffect.of() });
    return;
  }
  const scrollTop = view.scrollDOM.scrollTop;
  const scrollLeft = view.scrollDOM.scrollLeft;
  const caret = clamp(rename.from + newName.length, 0, activeContent.length);
  view.dispatch({
    changes: { from: 0, to: view.state.doc.length, insert: activeContent },
    selection: { anchor: caret },
    effects: clearRenameTooltipEffect.of(),
    annotations: Transaction.addToHistory.of(false),
    userEvent: "input.rename",
  });
  restoreEditorScrollAfterRename(workspaceID, path, view, scrollTop, scrollLeft);
  window.requestAnimationFrame(() => {
    restoreEditorScrollAfterRename(workspaceID, path, view, scrollTop, scrollLeft);
  });
}

function restoreEditorScrollAfterRename(
  workspaceID: string,
  path: string,
  view: EditorView,
  scrollTop: number,
  scrollLeft: number,
) {
  if (!view.dom.isConnected) {
    return;
  }
  view.scrollDOM.scrollTop = scrollTop;
  view.scrollDOM.scrollLeft = scrollLeft;
  const tab = findTab(workspaceID, path);
  if (tab) {
    tab.scrollTop = view.scrollDOM.scrollTop;
    tab.scrollLeft = view.scrollDOM.scrollLeft;
  }
}

async function goToLspDefinition(
  workspaceID: string,
  path: string,
  view: EditorView,
  callbacks: CodeViewCallbacks,
  openCodeFile: OpenCodeFileForDefinition,
) {
  try {
    const content = editorStateToFileContent(view.state);
    const editorPosition = lspDefinitionEditorPosition(view.state, view.state.selection.main.head);
    const requestPosition = editorPositionToFileContentOffset(view.state, editorPosition);
    const response = services.WorkspaceDefinitionResponse.createFrom(
      await FindWorkspaceFileDefinition(
        workspaceID,
        services.WorkspaceDefinitionRequest.createFrom({
          filePath: path,
          content,
          position: requestPosition,
        }),
      ),
    );
    if (!response.found || !response.targetPath) {
      callbacks.pushToast(response.message || "No definition found.", "info");
      return;
    }
    if (isDefinitionAtRequest(response, path, requestPosition)) {
      openLspReferencesResponse(
        workspaceID,
        path,
        view,
        callbacks,
        await findLspReferences(workspaceID, path, content, requestPosition),
      );
      return;
    }
    await openCodeFile(workspaceID, response.targetPath, callbacks, {
      temporary: false,
      selectionPosition: response.position,
    });
  } catch (error) {
    callbacks.pushToast(callbacks.errorMessage(error), "error");
  }
}

async function showLspReferences(
  workspaceID: string,
  path: string,
  view: EditorView,
  callbacks: CodeViewCallbacks,
) {
  try {
    const content = editorStateToFileContent(view.state);
    const editorPosition = lspDefinitionEditorPosition(view.state, view.state.selection.main.head);
    const requestPosition = editorPositionToFileContentOffset(view.state, editorPosition);
    openLspReferencesResponse(
      workspaceID,
      path,
      view,
      callbacks,
      await findLspReferences(workspaceID, path, content, requestPosition),
    );
  } catch (error) {
    callbacks.pushToast(callbacks.errorMessage(error), "error");
  }
}

async function goToLspImplementation(
  workspaceID: string,
  path: string,
  view: EditorView,
  callbacks: CodeViewCallbacks,
  openCodeFile: OpenCodeFileForDefinition,
) {
  try {
    const content = editorStateToFileContent(view.state);
    const editorPosition = lspDefinitionEditorPosition(view.state, view.state.selection.main.head);
    const requestPosition = editorPositionToFileContentOffset(view.state, editorPosition);
    const implementations = await findLspImplementations(workspaceID, path, content, requestPosition);
    const locations = implementations.locations ?? [];
    if (!implementations.found || !locations.length) {
      openReferencesPanel(workspaceID, path, view, []);
      callbacks.pushToast(implementations.message || "No implementations found.", "info");
      return;
    }
    if (locations.length === 1) {
      const location = locations[0];
      await openCodeFile(workspaceID, location.path, callbacks, {
        temporary: false,
        selectionPosition: location.range.start.offset,
      });
      return;
    }
    openReferencesPanel(workspaceID, path, view, locations, { title: "Implementations" });
  } catch (error) {
    callbacks.pushToast(callbacks.errorMessage(error), "error");
  }
}

async function findLspReferences(
  workspaceID: string,
  path: string,
  content: string,
  position: number,
) {
  return services.WorkspaceReferenceResponse.createFrom(
    await FindWorkspaceFileReferences(
      workspaceID,
      services.WorkspaceReferenceRequest.createFrom({
        filePath: path,
        content,
        position,
        includeDeclaration: true,
        maxResults: 200,
      }),
    ),
  );
}

async function findLspImplementations(
  workspaceID: string,
  path: string,
  content: string,
  position: number,
) {
  return services.WorkspaceReferenceResponse.createFrom(
    await FindWorkspaceFileImplementations(
      workspaceID,
      services.WorkspaceReferenceRequest.createFrom({
        filePath: path,
        content,
        position,
        maxResults: 200,
      }),
    ),
  );
}

function openLspReferencesResponse(
  workspaceID: string,
  path: string,
  view: EditorView,
  callbacks: CodeViewCallbacks,
  references: services.WorkspaceReferenceResponse,
) {
  if (!references.found || !references.locations?.length) {
    openReferencesPanel(workspaceID, path, view, []);
    callbacks.pushToast(references.message || "No references found.", "info");
    return;
  }
  openReferencesPanel(workspaceID, path, view, references.locations);
}

function isDefinitionAtRequest(
  response: services.WorkspaceDefinitionResponse,
  path: string,
  requestPosition: number,
) {
  const targetPath = response.targetPath;
  if (!targetPath) {
    return false;
  }
  return (
    sameWorkspacePath(targetPath, response.sourcePath || path) &&
    response.position === requestPosition
  );
}

export function lspCompletionExtension(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
): Extension {
  if (!isLspSourcePath(path)) {
    return [];
  }
  const source = lspCompletionSource(workspaceID, path, callbacks);
  return EditorState.languageData.of(() => [{ autocomplete: source }]);
}

function lspCompletionSource(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  return async (context: CompletionContext): Promise<CompletionResult | null> => {
    const word = context.matchBefore(lspCompletionTriggerWord);
    const member = context.matchBefore(lspCompletionMemberTriggerForPath(path));
    if (!context.explicit && !word && !member) {
      return null;
    }

    const triggerCharacter = member ? member.text.slice(-1) : "";
    const triggerKind = context.explicit ? 1 : triggerCharacter ? 2 : 1;
    const content = editorStateToFileContent(context.state);
    try {
      const response = services.WorkspaceCompletionResponse.createFrom(
        await CompleteWorkspaceFile(
          workspaceID,
          services.WorkspaceCompletionRequest.createFrom({
            filePath: path,
            content,
            position: editorPositionToFileContentOffset(context.state, context.pos),
            triggerKind,
            triggerCharacter,
          }),
        ),
      );
      if (context.aborted) {
        return null;
      }
      clearLspCompletionError(workspaceID, path);
      const items: services.WorkspaceCompletionItem[] = response.items ?? [];
      if (!items.length) {
        return null;
      }
      const editorItems = items.map((item) => lspCompletionItemToEditorOffsets(item, content, context.state.lineBreak));
      const fallbackFrom = member ? context.pos : word?.from ?? context.pos;
      return {
        from: Math.min(fallbackFrom, ...editorItems.map((item) => item.from)),
        to: context.pos,
        options: editorItems.map((item) => lspCompletionOption(item)),
        validFor: response.isIncomplete ? undefined : lspCompletionValidFor,
      };
    } catch (error) {
      if (!context.aborted) {
        noteLspCompletionError(workspaceID, path, callbacks.errorMessage(error), context.explicit, callbacks);
      }
      return null;
    }
  };
}

function lspDefinitionEditorPosition(state: EditorState, position: number) {
  const cursor = clamp(position, 0, state.doc.length);
  const line = state.doc.lineAt(cursor);
  const offset = cursor - line.from;
  const identifier = identifierBoundsAt(line.text, offset);
  return identifier ? line.from + identifier.from : cursor;
}

function identifierBoundsAt(line: string, offset: number): { from: number; to: number } | null {
  let probe = clamp(offset, 0, line.length);
  if (probe < line.length && line.charAt(probe) === "." && isIdentifierCharacter(line.charAt(probe + 1))) {
    probe++;
  } else if (!isIdentifierCharacter(line.charAt(probe)) && probe > 0 && isIdentifierCharacter(line.charAt(probe - 1))) {
    probe--;
  }
  if (!isIdentifierCharacter(line.charAt(probe))) {
    return null;
  }

  let from = probe;
  while (from > 0 && isIdentifierCharacter(line.charAt(from - 1))) {
    from--;
  }
  let to = probe;
  while (to < line.length && isIdentifierCharacter(line.charAt(to))) {
    to++;
  }
  return { from, to };
}

function isIdentifierCharacter(character: string) {
  return /^[\p{L}\p{N}_]$/u.test(character);
}

function sameWorkspacePath(left: string, right: string) {
  return left.replaceAll("\\", "/").toLowerCase() === right.replaceAll("\\", "/").toLowerCase();
}

function lspCompletionItemToEditorOffsets(
  item: services.WorkspaceCompletionItem,
  content: string,
  lineSeparator: string,
): services.WorkspaceCompletionItem {
  return services.WorkspaceCompletionItem.createFrom({
    ...item,
    from: fileContentOffsetToEditorPosition(content, lineSeparator, item.from),
    to: fileContentOffsetToEditorPosition(content, lineSeparator, item.to),
    additionalTextEdits: (item.additionalTextEdits ?? []).map((edit) => ({
      ...edit,
      from: fileContentOffsetToEditorPosition(content, lineSeparator, edit.from),
      to: fileContentOffsetToEditorPosition(content, lineSeparator, edit.to),
    })),
  });
}

function lspCompletionOption(item: services.WorkspaceCompletionItem): Completion {
  const insertText = item.insertText || item.label;
  return {
    label: item.label,
    detail: item.detail || undefined,
    info: item.documentation || undefined,
    type: lspCompletionType(item.kind),
    apply: (view) => {
      applyLspCompletion(view, item, insertText);
    },
  };
}

function applyLspCompletion(
  view: EditorView,
  item: services.WorkspaceCompletionItem,
  insertText: string,
) {
  const docLength = view.state.doc.length;
  const primaryInsert = normalizeEditorLineBreaks(insertText, view.state.lineBreak);
  const primaryFrom = clamp(item.from, 0, docLength);
  const primaryTo = lspCompletionPrimaryTo(view.state, item, primaryFrom);
  const primaryChange = {
    from: primaryFrom,
    to: primaryTo,
    insert: primaryInsert,
  };
  const changes = [
    primaryChange,
    ...((item.additionalTextEdits ?? []).map((edit) => {
      const from = clamp(edit.from, 0, docLength);
      return {
        from,
        to: clamp(edit.to, from, docLength),
        insert: normalizeEditorLineBreaks(edit.newText, view.state.lineBreak),
      };
    })),
  ]
    .filter((change, index, all) => {
      if (index === 0) {
        return true;
      }
      return !rangesOverlap(change.from, change.to, all[0].from, all[0].to);
    })
    .sort((left, right) => left.from - right.from || left.to - right.to);

  const selectionDelta = changes
    .filter((change) => change !== primaryChange && change.from <= primaryFrom)
    .reduce((total, change) => total + change.insert.length - (change.to - change.from), 0);
  view.dispatch({
    changes,
    selection: { anchor: primaryFrom + selectionDelta + primaryInsert.length },
    userEvent: "input.complete",
  });
}

function lspCompletionPrimaryTo(
  state: EditorState,
  item: services.WorkspaceCompletionItem,
  primaryFrom: number,
) {
  const docLength = state.doc.length;
  const selection = state.selection.main;
  const selectionTo = selection.empty ? selection.head : selection.to;
  const itemTo = clamp(item.to, primaryFrom, docLength);
  const line = state.doc.lineAt(primaryFrom);
  if (selectionTo >= primaryFrom && selectionTo <= line.to) {
    return Math.max(itemTo, selectionTo);
  }
  return itemTo;
}

function rangesOverlap(leftFrom: number, leftTo: number, rightFrom: number, rightTo: number) {
  return leftFrom < rightTo && rightFrom < leftTo;
}

function lspCompletionType(kind?: number): string | undefined {
  switch (kind) {
    case 2:
      return "method";
    case 3:
    case 4:
      return "function";
    case 5:
    case 10:
      return "property";
    case 6:
      return "variable";
    case 7:
    case 22:
      return "class";
    case 8:
      return "interface";
    case 9:
      return "namespace";
    case 13:
    case 20:
      return "enum";
    case 14:
      return "keyword";
    case 21:
      return "constant";
    case 23:
      return "event";
    case 25:
      return "type";
    default:
      return undefined;
  }
}

function noteLspCompletionError(
  workspaceID: string,
  path: string,
  message: string,
  explicit: boolean,
  callbacks: CodeViewCallbacks,
) {
  const key = lspCompletionErrorKey(workspaceID, path);
  if (!explicit && lspCompletionErrors.get(key) === message) {
    return;
  }
  lspCompletionErrors.set(key, message);
  callbacks.pushToast(`Autocomplete unavailable: ${message}`, "error");
}

function clearLspCompletionError(workspaceID: string, path: string) {
  lspCompletionErrors.delete(lspCompletionErrorKey(workspaceID, path));
}

function lspCompletionErrorKey(workspaceID: string, path: string) {
  return `${workspaceID}\u0000${path}`;
}

function lspCompletionMemberTriggerForPath(path: string) {
  return isCppSourcePath(path) ? lspCompletionTriggerCppMember : lspCompletionTriggerDot;
}

function isLspSourcePath(path: string) {
  const lower = path.toLowerCase();
  return lspSourceExtensions.some((extension) => lower.endsWith(extension));
}

function isCppSourcePath(path: string) {
  const lower = path.toLowerCase();
  return lspSourceExtensions.slice(1).some((extension) => lower.endsWith(extension));
}
