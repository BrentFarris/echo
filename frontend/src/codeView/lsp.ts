import { type Completion, type CompletionContext, type CompletionResult } from "@codemirror/autocomplete";
import { EditorState, Prec, type Extension } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { CompleteWorkspaceFile, FindWorkspaceFileDefinition } from "../../wailsjs/go/services/SystemService";
import { services } from "../../wailsjs/go/models";
import type { CodeViewCallbacks } from "./types";
import { clamp } from "./utils";

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
    const response = services.WorkspaceDefinitionResponse.createFrom(
      await FindWorkspaceFileDefinition(
        workspaceID,
        services.WorkspaceDefinitionRequest.createFrom({
          filePath: path,
          content: view.state.sliceDoc(0),
          position: view.state.selection.main.head,
        }),
      ),
    );
    if (!response.found || !response.targetPath) {
      callbacks.pushToast(response.message || "No definition found.", "info");
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
    try {
      const response = services.WorkspaceCompletionResponse.createFrom(
        await CompleteWorkspaceFile(
          workspaceID,
          services.WorkspaceCompletionRequest.createFrom({
            filePath: path,
            content: context.state.sliceDoc(0),
            position: context.pos,
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
      const fallbackFrom = dot ? context.pos : word?.from ?? context.pos;
      return {
        from: Math.min(fallbackFrom, ...items.map((item) => item.from)),
        to: context.pos,
        options: items.map((item) => lspCompletionOption(item)),
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
  const primaryFrom = clamp(item.from, 0, docLength);
  const primaryTo = clamp(item.to, primaryFrom, docLength);
  const primaryChange = {
    from: primaryFrom,
    to: primaryTo,
    insert: insertText,
  };
  const changes = [
    primaryChange,
    ...((item.additionalTextEdits ?? []).map((edit) => {
      const from = clamp(edit.from, 0, docLength);
      return {
        from,
        to: clamp(edit.to, from, docLength),
        insert: edit.newText,
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
    selection: { anchor: primaryFrom + selectionDelta + insertText.length },
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
