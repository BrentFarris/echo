import { EditorState, Prec, StateEffect, StateField, type Text } from "@codemirror/state";
import { Decoration, type DecorationSet, EditorView, WidgetType, keymap } from "@codemirror/view";
import { ReadWorkspaceFile, SubmitInlineCodePrompt } from "../backend/services";
import { services } from "../../wailsjs/go/models";
import { renderMarkdown } from "../markdown";
import { patchInlineCodeChatResponse as patchInlineCodeChatResponseDom } from "./dom";
import { getMountedCodeEditor, mountedCodeEditorMatches, replaceMountedEditorContent, saveMountedEditorContent } from "./editor";
import { applySavedFile, ensureCodeState, findTab, workspaceFileChanged } from "./state";
import type { CodeFileTab, CodeViewCallbacks, InlineCodeChatState, InlineCodePromptEvent } from "./types";
import { clamp, editableWorkspaceFile, sleep } from "./utils";

export type InlineCodeChatHooks = {
  saveActiveCodeFile: (workspaceID: string, callbacks: CodeViewCallbacks) => Promise<void>;
};

const inlineSnippetContextLines = 40;
const inlineSnippetMaxBytes = 24 * 1024;
const inlineFocusSubstringMaxBytes = 4 * 1024;
const inlineReloadRetryDelays = [75, 175, 350, 650];
let inlineChatRenderSeq = 0;
let inlinePromptRequestSeq = 0;

const setInlineCodeChatEffect = StateEffect.define<number>();
const clearInlineCodeChatEffect = StateEffect.define<void>();

export function inlineCodeChatExtension(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  hooks: InlineCodeChatHooks,
) {
  const field = StateField.define<DecorationSet>({
    create(state) {
      return inlineCodeChatDecorations(state, workspaceID, path, callbacks, hooks);
    },
    update(decorations, transaction) {
      decorations = decorations.map(transaction.changes);
      const chat = inlineChatForPath(workspaceID, path);
      if (chat && transaction.docChanged) {
        chat.anchorPosition = transaction.changes.mapPos(chat.anchorPosition);
      }
      for (const effect of transaction.effects) {
        if (effect.is(clearInlineCodeChatEffect)) {
          decorations = Decoration.none;
        }
        if (effect.is(setInlineCodeChatEffect)) {
          decorations = inlineCodeChatDecorations(
            transaction.state,
            workspaceID,
            path,
            callbacks,
            hooks,
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
        key: "Ctrl-i",
        preventDefault: true,
        run(view) {
          openInlineCodeChat(workspaceID, path, view);
          return true;
        },
      },
      {
        key: "Mod-i",
        preventDefault: true,
        run(view) {
          openInlineCodeChat(workspaceID, path, view);
          return true;
        },
      },
    ])),
  ];
}

function inlineCodeChatDecorations(
  editorState: EditorState,
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  hooks: InlineCodeChatHooks,
): DecorationSet {
  const chat = inlineChatForPath(workspaceID, path);
  if (!chat) {
    return Decoration.none;
  }
  const anchorPosition = clamp(chat.anchorPosition, 0, editorState.doc.length);
  const line = editorState.doc.lineAt(anchorPosition);
  return Decoration.set([
    Decoration.widget({
      widget: new InlineCodeChatWidget(workspaceID, path, callbacks, hooks, chat.renderKey),
      block: true,
      side: 1,
    }).range(line.to),
  ]);
}

class InlineCodeChatWidget extends WidgetType {
  constructor(
    private readonly workspaceID: string,
    private readonly path: string,
    private readonly callbacks: CodeViewCallbacks,
    private readonly hooks: InlineCodeChatHooks,
    private readonly renderKey: number,
  ) {
    super();
  }

  eq(other: WidgetType) {
    return (
      other instanceof InlineCodeChatWidget &&
      other.workspaceID === this.workspaceID &&
      other.path === this.path &&
      other.renderKey === this.renderKey
    );
  }

  toDOM() {
    const chat = inlineChatForPath(this.workspaceID, this.path);
    const root = document.createElement("div");
    root.className = "inline-code-chat";
    root.dataset.inlineCodeChat = "";
    root.dataset.inlineWorkspaceId = this.workspaceID;
    root.dataset.inlinePath = this.path;
    if (!chat) {
      return root;
    }
    root.dataset.inlineRequestId = chat.requestID;

    const form = document.createElement("form");
    form.className = "inline-code-chat-form";

    const textarea = document.createElement("textarea");
    textarea.className = "inline-code-chat-input";
    textarea.rows = Math.max(2, Math.min(7, chat.draft.split("\n").length));
    textarea.placeholder = "Ask about this code...";
    textarea.value = chat.draft;
    textarea.disabled = chat.submitting;
    textarea.setAttribute("aria-label", "Inline code prompt");

    const submit = document.createElement("button");
    submit.className = "primary-button inline-code-chat-submit";
    submit.type = "submit";
    submit.disabled = chat.submitting || !chat.draft.trim();
    submit.textContent = chat.submitting ? "Sending" : "Send";

    const canSubmitDraft = () => {
      const latest = inlineChatForPath(this.workspaceID, this.path);
      return Boolean(latest && !latest.submitting && latest.draft.trim());
    };
    const syncSubmitState = () => {
      const latest = inlineChatForPath(this.workspaceID, this.path);
      submit.disabled = !latest || latest.submitting || !latest.draft.trim();
    };

    textarea.addEventListener("input", () => {
      const latest = inlineChatForPath(this.workspaceID, this.path);
      if (latest) {
        latest.draft = textarea.value;
      }
      syncSubmitState();
    });
    textarea.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" || event.shiftKey || event.isComposing) {
        return;
      }
      if (!canSubmitDraft()) {
        return;
      }
      event.preventDefault();
      form.requestSubmit(submit);
    });

    const actions = document.createElement("div");
    actions.className = "inline-code-chat-actions";

    const close = document.createElement("button");
    close.className = "secondary-button inline-code-chat-close";
    close.type = "button";
    close.textContent = "Close";
    close.addEventListener("click", () => {
      closeInlineCodeChat(this.workspaceID, this.path);
    });

    actions.append(submit, close);
    form.append(textarea, actions);
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      void submitInlineCodeChat(this.workspaceID, this.path, this.callbacks, this.hooks);
    });

    root.append(form);
    if (chat.error) {
      const error = document.createElement("div");
      error.className = "inline-code-chat-error";
      error.textContent = chat.error;
      root.append(error);
    }
    if (chat.response) {
      const response = document.createElement("div");
      response.className = "inline-code-chat-response markdown-body";
      response.dataset.inlineCodeResponse = "";
      response.innerHTML = renderMarkdown(chat.response);
      root.append(response);
    }
    return root;
  }

  ignoreEvent() {
    return true;
  }
}

function inlineChatForPath(workspaceID: string, path: string) {
  const chat = ensureCodeState(workspaceID).inlineChat;
  if (!chat || chat.path !== path) {
    return null;
  }
  return chat;
}

function openInlineCodeChat(
  workspaceID: string,
  path: string,
  view: EditorView,
) {
  const selection = view.state.selection.main;
  const line = view.state.doc.lineAt(selection.head);
  const previous = inlineChatForPath(workspaceID, path);
  ensureCodeState(workspaceID).inlineChat = {
    path,
    anchorPosition: selection.head,
    selectedText: selectedEditorText(view),
    draft: previous?.draft ?? "",
    submitting: false,
    response: "",
    error: "",
    requestID: previous?.requestID ?? "",
    renderKey: nextInlineChatRenderKey(),
  };
  view.dispatch({ effects: setInlineCodeChatEffect.of(line.to) });
  focusInlineCodeChatInput();
}

function closeInlineCodeChat(workspaceID: string, path: string) {
  const state = ensureCodeState(workspaceID);
  if (state.inlineChat?.path !== path) {
    return;
  }
  state.inlineChat = null;
  const mounted = getMountedCodeEditor();
  if (mounted.view && mounted.workspaceID === workspaceID && mounted.path === path) {
    mounted.view.dispatch({ effects: clearInlineCodeChatEffect.of(undefined) });
    mounted.view.focus();
  }
}

export function applyInlineCodePromptEvent(event: InlineCodePromptEvent) {
  const chat = inlineChatForPath(event.workspaceId, event.filePath);
  if (!chat || !event.requestId || chat.requestID !== event.requestId) {
    return;
  }
  if (event.type === "token") {
    chat.response = `${chat.response ?? ""}${event.content ?? ""}`;
    chat.error = "";
    patchInlineCodeChatResponse(event.workspaceId, event.filePath, chat);
    return;
  }
  if (event.type === "error") {
    chat.submitting = false;
    chat.error = event.error ?? "Inline code prompt failed.";
    refreshInlineCodeChatWidget(event.workspaceId, event.filePath);
  }
}

async function submitInlineCodeChat(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  hooks: InlineCodeChatHooks,
) {
  const state = ensureCodeState(workspaceID);
  const chat = inlineChatForPath(workspaceID, path);
  if (!chat || chat.submitting) {
    return;
  }
  const prompt = chat.draft.trim();
  if (!prompt) {
    return;
  }
  const mounted = getMountedCodeEditor();
  const view = mounted.workspaceID === workspaceID && mounted.path === path ? mounted.view : null;
  if (!view) {
    callbacks.pushToast("Open the file before sending the inline prompt.", "error");
    return;
  }
  const requestID = nextInlinePromptRequestID();
  const request = buildInlineCodePromptRequest(view, path, chat, prompt, requestID);
  const tab = findTab(workspaceID, path);
  if (tab?.dirty) {
    const confirmed = window.confirm("Save this file before sending the inline prompt?");
    if (!confirmed) {
      return;
    }
    await hooks.saveActiveCodeFile(workspaceID, callbacks);
    const savedTab = findTab(workspaceID, path);
    if (savedTab?.dirty) {
      return;
    }
  }

  chat.submitting = true;
  chat.response = "";
  chat.error = "";
  chat.requestID = requestID;
  refreshInlineCodeChatWidget(workspaceID, path);

  try {
    const response = services.InlineCodePromptResponse.createFrom(
      await SubmitInlineCodePrompt(
        workspaceID,
        services.InlineCodePromptRequest.createFrom(request),
      ),
    );
    const latest = state.inlineChat;
    if (!latest || latest.path !== path || latest.requestID !== requestID) {
      return;
    }
    const reloaded = await reloadInlineCodePromptTabs(
      workspaceID,
      path,
      response.affectedPaths ?? [],
      (response.toolCalls ?? []).length > 0,
      callbacks,
    );
    const content = (response.content ?? "").trim();
    if (!content) {
      state.inlineChat = null;
      if (reloaded) {
        callbacks.render();
      } else {
        refreshInlineCodeChatWidget(workspaceID, path);
      }
      return;
    }
    latest.submitting = false;
    latest.response = content;
    latest.error = "";
    latest.renderKey = nextInlineChatRenderKey();
    if (reloaded) {
      callbacks.render();
    } else {
      refreshInlineCodeChatWidget(workspaceID, path);
    }
  } catch (error) {
    const latest = inlineChatForPath(workspaceID, path);
    if (!latest || latest.requestID !== requestID) {
      return;
    }
    latest.submitting = false;
    latest.error = callbacks.errorMessage(error);
    latest.renderKey = nextInlineChatRenderKey();
    refreshInlineCodeChatWidget(workspaceID, path);
  }
}

function buildInlineCodePromptRequest(
  view: EditorView,
  path: string,
  chat: InlineCodeChatState,
  prompt: string,
  requestID: string,
) {
  const doc = view.state.doc;
  const anchorPosition = clamp(chat.anchorPosition, 0, doc.length);
  const anchorLineInfo = doc.lineAt(anchorPosition);
  const anchorLine = anchorLineInfo.number;
  let startLine = clamp(anchorLine - inlineSnippetContextLines, 1, doc.lines);
  let endLine = clamp(anchorLine + inlineSnippetContextLines, 1, doc.lines);
  let contextSubstring = substringFromDocLines(doc, startLine, endLine);
  while (
    new TextEncoder().encode(contextSubstring).length > inlineSnippetMaxBytes &&
    (startLine < anchorLine || endLine > anchorLine)
  ) {
    if (endLine > anchorLine) {
      endLine--;
      contextSubstring = substringFromDocLines(doc, startLine, endLine);
    }
    if (
      new TextEncoder().encode(contextSubstring).length <= inlineSnippetMaxBytes ||
      (startLine >= anchorLine && endLine <= anchorLine)
    ) {
      break;
    }
    if (startLine < anchorLine) {
      startLine++;
      contextSubstring = substringFromDocLines(doc, startLine, endLine);
    }
  }
  if (new TextEncoder().encode(contextSubstring).length > inlineSnippetMaxBytes) {
    contextSubstring = contextSubstring.slice(0, inlineSnippetMaxBytes);
  }
  return {
    requestId: requestID,
    filePath: path,
    prompt,
    cursorToken: tokenAroundLineOffset(anchorLineInfo.text, anchorPosition - anchorLineInfo.from),
    cursorLineText: anchorLineInfo.text,
    focusSubstring: substringAroundDocPosition(doc, anchorPosition, inlineFocusSubstringMaxBytes),
    contextSubstring,
    selectedText: chat.selectedText,
  };
}

function tokenAroundLineOffset(lineText: string, offset: number) {
  let probe = clamp(offset, 0, lineText.length);
  if (!isIdentifierCharacter(lineText.charAt(probe)) && probe > 0 && isIdentifierCharacter(lineText.charAt(probe - 1))) {
    probe--;
  }
  if (!isIdentifierCharacter(lineText.charAt(probe))) {
    return "";
  }

  let start = probe;
  while (start > 0 && isIdentifierCharacter(lineText.charAt(start - 1))) {
    start--;
  }
  let end = probe;
  while (end < lineText.length && isIdentifierCharacter(lineText.charAt(end))) {
    end++;
  }
  return lineText.slice(start, end);
}

function isIdentifierCharacter(character: string) {
  return /^[\p{L}\p{N}_$]$/u.test(character);
}

function substringAroundDocPosition(
  doc: Text,
  position: number,
  maxBytes: number,
) {
  let start = clamp(position - Math.floor(maxBytes / 2), 0, doc.length);
  let end = clamp(position + Math.ceil(maxBytes / 2), 0, doc.length);
  let substring = doc.sliceString(start, end);
  const encoder = new TextEncoder();
  while (encoder.encode(substring).length > maxBytes && (start < position || end > position)) {
    if (end > position) {
      end--;
      substring = doc.sliceString(start, end);
    }
    if (encoder.encode(substring).length <= maxBytes || (start >= position && end <= position)) {
      break;
    }
    if (start < position) {
      start++;
      substring = doc.sliceString(start, end);
    }
  }
  return substring;
}

function substringFromDocLines(
  doc: Text,
  startLine: number,
  endLine: number,
) {
  const lines: string[] = [];
  for (let lineNumber = startLine; lineNumber <= endLine; lineNumber++) {
    lines.push(doc.line(lineNumber).text);
  }
  return lines.join("\n");
}

function selectedEditorText(view: EditorView) {
  const selection = view.state.selection.main;
  if (selection.empty) {
    return "";
  }
  return view.state.doc.sliceString(selection.from, selection.to);
}

async function reloadInlineCodePromptTabs(
  workspaceID: string,
  promptedPath: string,
  paths: string[],
  usedTools: boolean,
  callbacks: CodeViewCallbacks,
) {
  saveMountedEditorContent();
  let reloaded = false;
  const affectedPaths = new Set(paths.map((path) => path.trim()).filter(Boolean));
  for (const path of uniqueInlineReloadPaths(promptedPath, paths)) {
    const tab = findTab(workspaceID, path);
    if (!tab) {
      continue;
    }
    if (tab.dirty) {
      callbacks.pushToast(`${path} changed on disk; unsaved editor content was left open.`, "info");
      continue;
    }
    try {
      const waitForChange =
        affectedPaths.has(path) ||
        (affectedPaths.size === 0 && usedTools && path === promptedPath);
      const file = await readWorkspaceFileUntilChanged(workspaceID, tab, waitForChange);
      if (!workspaceFileChanged(tab, file)) {
        continue;
      }
      applySavedFile(workspaceID, file);
      const reloadedTab = findTab(workspaceID, path);
      replaceMountedEditorContent(workspaceID, path, reloadedTab?.content ?? editableWorkspaceFile(file).content);
      reloaded = true;
    } catch (error) {
      callbacks.pushToast(callbacks.errorMessage(error), "error");
    }
  }
  return reloaded;
}

async function readWorkspaceFileUntilChanged(
  workspaceID: string,
  tab: CodeFileTab,
  waitForChange: boolean,
) {
  let file = services.WorkspaceFile.createFrom(await ReadWorkspaceFile(workspaceID, tab.path));
  if (!waitForChange || workspaceFileChanged(tab, file)) {
    return file;
  }
  for (const delay of inlineReloadRetryDelays) {
    await sleep(delay);
    file = services.WorkspaceFile.createFrom(await ReadWorkspaceFile(workspaceID, tab.path));
    if (workspaceFileChanged(tab, file)) {
      return file;
    }
  }
  return file;
}

function uniqueInlineReloadPaths(promptedPath: string, affectedPaths: string[]) {
  const paths = new Set<string>();
  const add = (path: string) => {
    const trimmed = path.trim();
    if (trimmed) {
      paths.add(trimmed);
    }
  };
  add(promptedPath);
  affectedPaths.forEach(add);
  return paths;
}

function refreshInlineCodeChatWidget(workspaceID: string, path: string) {
  if (!mountedCodeEditorMatches(workspaceID, path)) {
    return;
  }
  const chat = inlineChatForPath(workspaceID, path);
  if (!chat) {
    getMountedCodeEditor().view?.dispatch({ effects: clearInlineCodeChatEffect.of(undefined) });
    return;
  }
  chat.renderKey = nextInlineChatRenderKey();
  const line = getMountedCodeEditor().view!.state.doc.lineAt(clamp(chat.anchorPosition, 0, getMountedCodeEditor().view!.state.doc.length));
  getMountedCodeEditor().view!.dispatch({ effects: setInlineCodeChatEffect.of(line.to) });
  focusInlineCodeChatInput();
}

function patchInlineCodeChatResponse(
  workspaceID: string,
  path: string,
  chat: InlineCodeChatState,
) {
  patchInlineCodeChatResponseDom(
    workspaceID,
    path,
    chat.requestID,
    renderMarkdown(chat.response),
    () => refreshInlineCodeChatWidget(workspaceID, path),
  );
}

function focusInlineCodeChatInput() {
  window.setTimeout(() => {
    const input = document.querySelector<HTMLTextAreaElement>(".inline-code-chat-input");
    if (!input || input.disabled) {
      return;
    }
    input.focus();
    input.setSelectionRange(input.value.length, input.value.length);
  }, 0);
}

function nextInlineChatRenderKey() {
  inlineChatRenderSeq++;
  return inlineChatRenderSeq;
}

function nextInlinePromptRequestID() {
  inlinePromptRequestSeq++;
  return `inline-${Date.now()}-${inlinePromptRequestSeq}`;
}
