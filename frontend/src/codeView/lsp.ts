import { type Completion, type CompletionContext, type CompletionResult } from "@codemirror/autocomplete";
import { EditorState, Prec, type Extension } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { CompleteWorkspaceFile, FindWorkspaceFileDefinition, FindWorkspaceFileReferences } from "../backend/services";
import { services } from "../../wailsjs/go/models";
import type { CodeViewCallbacks } from "./types";
import { openReferencesPanel } from "./references";
import {
  clamp,
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
) => Promise<void>;

const lspCompletionValidFor = /^[A-Za-z0-9_]*$/;
const lspCompletionTriggerWord = /[A-Za-z_][A-Za-z0-9_]*$/;
const lspCompletionTriggerDot = /\.$/;
const lspCompletionErrors = new Map<string, string>();

export function lspDefinitionExtension(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  openCodeFile: OpenCodeFileForDefinition,
): Extension {
  if (!isGoSourcePath(path)) {
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
    ]),
  );
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
  if (!isGoSourcePath(path)) {
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
    const dot = context.matchBefore(lspCompletionTriggerDot);
    if (!context.explicit && !word && !dot) {
      return null;
    }

    const triggerCharacter = dot ? "." : "";
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
      const fallbackFrom = dot ? context.pos : word?.from ?? context.pos;
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
  const primaryTo = clamp(item.to, primaryFrom, docLength);
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

function isGoSourcePath(path: string) {
  return path.toLowerCase().endsWith(".go");
}
