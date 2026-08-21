// chat.js — workspace-scoped shared chat state and rendering.

import * as ws from "./ws.js";
import {
  cancelMarkdownPatch,
  flushMarkdownPatch,
  patchMarkdownElement,
  queueMarkdownPatch,
} from "../src/markdown.ts";
import { copyText, toast } from "../src/code/ui.ts";
import { attachVideoVolumeControl } from "../src/mediaVolume.ts";
import { icons } from "./icons.js";
import { del, post } from "./api.js";
import { refreshPluginCatalog } from "../src/plugins/catalog.ts";

let binding = null;
let activeStream = null;
const streamingListeners = new Set();
const workspaceListeners = new Set();
const commandErrorListeners = new Set();
const askUserQuestionsToolName = "ask_user_questions";

function setStreaming(streaming) {
  for (const cb of streamingListeners) {
    try { cb(streaming); } catch (err) { console.error("streaming state handler error:", err); }
  }
}

export function onStreamingChange(cb) {
  streamingListeners.add(cb);
  try { cb(activeStream != null); } catch (err) { console.error("streaming state handler error:", err); }
  return () => streamingListeners.delete(cb);
}

export function isStreaming() { return activeStream != null; }

function publicWorkspaceState() {
  if (!binding) return null;
  return {
    workspaceId: binding.workspaceId,
    surface: binding.surface,
    activeChatId: binding.activeChatId,
    hasSnapshot: binding.hasSnapshot,
    tabs: binding.tabs.map((tab) => ({ ...tab })),
  };
}

function emitWorkspaceState() {
  const state = publicWorkspaceState();
  for (const cb of workspaceListeners) {
    try { cb(state); } catch (err) { console.error("chat workspace state handler error:", err); }
  }
}

export function getChatWorkspaceState() { return publicWorkspaceState(); }

export function onChatWorkspaceChange(cb) {
  workspaceListeners.add(cb);
  try { cb(publicWorkspaceState()); } catch (err) { console.error("chat workspace state handler error:", err); }
  return () => workspaceListeners.delete(cb);
}

export function onChatCommandError(cb) {
  commandErrorListeners.add(cb);
  return () => commandErrorListeners.delete(cb);
}

function activeBindingChatBusy() {
  return Boolean(binding?.tabs.find((tab) => tab.chatId === binding.activeChatId)?.busy);
}

export function canClearChat(log) {
  return Boolean(
    binding?.log === log
    && binding.workspaceId
    && binding.hasSnapshot
    && binding.turns.size > 0
    && !activeBindingChatBusy()
    && activeStream == null,
  );
}

export function clearChat(log) {
  if (!canClearChat(log)) return false;
  return ws.send({
    type: "chat_clear", workspaceId: binding.workspaceId, chatId: binding.activeChatId,
    ...(binding.surface === "code" ? { surface: "code" } : {}),
  });
}

export function createChatTab() {
  if (!binding?.workspaceId) return false;
  return ws.send({ type: "chat_tab_create", workspaceId: binding.workspaceId });
}

export function activateChatTab(chatId) {
  if (!binding?.workspaceId || !chatId) return false;
  return ws.send({ type: "chat_tab_activate", workspaceId: binding.workspaceId, chatId });
}

export function closeChatTab(chatId, stopIfBusy = false) {
  if (!binding?.workspaceId || !chatId) return false;
  return ws.send({
    type: "chat_tab_close", workspaceId: binding.workspaceId, chatId,
    ...(stopIfBusy ? { stopIfBusy: true } : {}),
  });
}

export function openWorkspaceSession(log, workspaceId, options = {}) {
  cancelBindingMarkdownPatches();
  activeStream = null;
  setStreaming(false);
  binding = {
    log, workspaceId: workspaceId || "", surface: options.surface === "code" ? "code" : "chat",
    onActivateFile: typeof options.onActivateFile === "function" ? options.onActivateFile : null,
    sequence: 0, hasSnapshot: false,
    activeChatId: "", tabs: [], turns: new Map(),
  };
  emitWorkspaceState();
  renderEmpty(log, workspaceId ? "Loading conversation…" : "Select a workspace to start chatting.");
  if (workspaceId) ws.send({
    type: "session_subscribe", workspaceId,
    ...(binding.surface === "code" ? { surface: "code" } : {}),
  });
}

export function closeWorkspaceSession(log) {
  if (binding?.log === log) cancelBindingMarkdownPatches();
  if (binding?.log === log) binding = null;
  activeStream = null;
  setStreaming(false);
  emitWorkspaceState();
}

ws.onState((state) => {
  if (state === "open" && binding?.workspaceId) {
    ws.send({
      type: "session_subscribe", workspaceId: binding.workspaceId,
      ...(binding.surface === "code" ? { surface: "code" } : {}),
    });
  }
});

ws.on("session_snapshot", (snapshot) => {
  const surface = snapshot.surface || "chat";
  if (!binding || snapshot.workspaceId !== binding.workspaceId || surface !== binding.surface) return;
  cancelBindingMarkdownPatches();
  binding.sequence = Number(snapshot.sequence) || 0;
  binding.hasSnapshot = true;
  binding.activeChatId = snapshot.activeChatId || snapshot.chatId || binding.activeChatId || "legacy-active";
  const incomingTabs = Array.isArray(snapshot.tabs) && snapshot.tabs.length
    ? snapshot.tabs
    : [{ chatId: binding.activeChatId, preview: "New chat", busy: Boolean(snapshot.activeTurn) }];
  binding.tabs = incomingTabs.map((tab) => ({
    chatId: tab.chatId,
    preview: tab.preview || "New chat",
    busy: Boolean(tab.busy),
    revision: Number(tab.revision) || 0,
  }));
  binding.turns.clear();
  activeStream = null;
  binding.log.textContent = "";
  for (const turn of snapshot.turns || []) renderStoredTurn(turn, false);
  if (snapshot.activeTurn) renderStoredTurn(snapshot.activeTurn, true);
  if (!binding.log.childElementCount) renderEmpty(binding.log, "Ask Echo to inspect, plan, or build in this workspace.");
  setStreaming(activeStream != null);
  emitWorkspaceState();
});

ws.on("session_event", (message) => {
  const surface = message.surface || "chat";
  if (!binding || message.workspaceId !== binding.workspaceId || surface !== binding.surface) return;
  const sequence = Number(message.sequence) || 0;
  if (sequence <= binding.sequence) return;
  if (!binding.hasSnapshot) {
    ws.send({
      type: "session_subscribe", workspaceId: binding.workspaceId,
      ...(binding.surface === "code" ? { surface: "code" } : {}),
    });
    return;
  }
  if (binding.sequence && sequence !== binding.sequence + 1) {
    ws.send({
      type: "session_subscribe", workspaceId: binding.workspaceId,
      ...(binding.surface === "code" ? { surface: "code" } : {}),
    });
    return;
  }
  binding.sequence = sequence;
  const event = message.event || {};
  const chatId = message.chatId || binding.activeChatId;
  const tab = binding.tabs.find((candidate) => candidate.chatId === chatId);
  let tabStateChanged = false;
  if (tab && (event.type === "turn_started" || event.type === "turn_rerun_started" || event.type === "turn_edit_started")) {
    tab.preview = normalizePreview(event.message) || "New chat";
    tab.busy = true;
    tabStateChanged = true;
  } else if (tab && event.type === "context_compression_started" && event.compression?.phase === "idle") {
    tab.busy = true;
    tabStateChanged = true;
  } else if (tab && ["context_compression_completed", "context_compression_skipped", "context_compression_failed"].includes(event.type) && event.compression?.phase === "idle") {
    tab.busy = false;
    tabStateChanged = true;
  } else if (tab && event.type === "turn_finished") {
    tab.busy = false;
    tabStateChanged = true;
  }
  // Workspace listeners rebuild the tab strip. Rebuilding it for every streamed
  // token can replace a tab button between pointerdown and click, which makes
  // tab activation and close controls appear unresponsive while a reply runs.
  // Only notify listeners when the public tab state actually changed.
  if (tabStateChanged) emitWorkspaceState();
  if (chatId === binding.activeChatId) {
    applyEvent(event);
  }
});

ws.on("command_error", (message) => {
  const surface = message.surface || "chat";
  if (!binding || (message.workspaceId && message.workspaceId !== binding.workspaceId) || surface !== binding.surface) return;
  let handled = false;
  for (const cb of commandErrorListeners) {
    try { handled = cb(message) === true || handled; } catch (err) { console.error("chat command error handler error:", err); }
  }
  if (handled) return;
  const status = document.createElement("div");
  status.className = "chat-stream-status is-error";
  status.textContent = message.error || "The chat command failed.";
  binding.log.querySelector(".chat-empty")?.remove();
  binding.log.appendChild(status);
});

function normalizePreview(value) { return String(value || "").trim().replace(/\s+/g, " "); }

function applyEvent(event) {
  switch (event.type) {
    case "turn_started": {
      binding.log.querySelector(".chat-empty")?.remove();
      const stream = createTurnView(event.turnId, event.message || "", event.images || [], event.videos || []);
      binding.turns.set(event.turnId, stream);
      activeStream = stream;
      setStreaming(true);
      break;
    }
    case "turn_rerun_started":
    case "turn_edit_started": {
      rewindTurnViews(event.fromTurnId);
      binding.log.querySelector(".chat-empty")?.remove();
      const stream = createTurnView(event.turnId, event.message || "", event.images || [], event.videos || []);
      binding.turns.set(event.turnId, stream);
      activeStream = stream;
      setStreaming(true);
      break;
    }
    case "assistant_turn_start":
      startTurn(findStream(event.turnId), event.turn);
      break;
    case "token":
      appendTurnText(findStream(event.turnId), event.turn, event.content || "");
      break;
    case "reasoning":
      appendReasoning(findStream(event.turnId), event.turn, event.content || "");
      break;
    case "assistant_turn_end":
      endTurn(findStream(event.turnId), event.turn, Boolean(event.hasToolCalls));
      break;
    case "tool_call":
      appendToolCall(findStream(event.turnId), event, event.turn);
      break;
    case "tool_result":
      appendToolMedia(findStream(event.turnId), event.images, event.videos);
      completeToolCall(findStream(event.turnId), event, event.turn);
      break;
    case "research_agent_status":
      updateResearchAgentStatus(findStream(event.turnId), event.researchAgent || {});
      break;
    case "research_agents_clear":
      clearResearchAgentStatuses(findStream(event.turnId));
      break;
    case "research_reasoning":
      appendResearchReasoning(findStream(event.turnId), event);
      break;
    case "plan_questions_resolved":
      resolvePlanQuestionItem(findStream(event.turnId), event, event.turn);
      break;
    case "context_compression_queued":
    case "context_compression_started":
    case "context_compression_completed":
    case "context_compression_skipped":
    case "context_compression_failed":
      upsertCompressionActivity(findStream(event.turnId), event.compression || {});
      break;
    case "turn_finished": {
      const stream = findStream(event.turnId);
      finishStream(stream, event.status || "error", event.error || "");
      break;
    }
  }
}

function findStream(turnId) { return binding?.turns.get(turnId) || null; }

export function sendMessage(log, text, model, agentModeId, options = {}) {
  text = text.trim();
  const images = Array.isArray(options.images) ? options.images : [];
  const videos = Array.isArray(options.videos) ? options.videos : [];
  if ((!text && images.length === 0 && videos.length === 0) || activeStream || activeBindingChatBusy() || !binding?.workspaceId || !binding.activeChatId || binding.log !== log) return false;
  const requestId = globalThis.crypto?.randomUUID?.() || `request-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return ws.send({
    type: "chat_send", workspaceId: binding.workspaceId, chatId: binding.activeChatId, requestId,
    message: text, ...(model ? { model } : {}), ...(agentModeId ? { agentModeId } : {}),
    ...(images.length ? { images } : {}), ...(videos.length ? { videos } : {}),
    ...(binding.surface === "code" ? { surface: "code" } : {}),
    ...(options.editorContext ? { editorContext: options.editorContext } : {}),
  });
}

export function stopStream() {
  if (binding?.workspaceId && activeStream) {
    ws.send({
      type: "chat_stop", workspaceId: binding.workspaceId, chatId: binding.activeChatId,
      ...(binding.surface === "code" ? { surface: "code" } : {}),
    });
  }
}

function renderStoredTurn(turn, active) {
  const stream = createTurnView(turn.id, turn.userContent || "", turn.images || [], turn.videos || [], {
    userDeleted: Boolean(turn.userDeleted),
    assistantDeleted: Boolean(turn.assistantDeleted),
  });
  binding.turns.set(turn.id, stream);
  const compressions = turn.assistantDeleted ? [] : (turn.compressions || []);
  for (const activity of compressions.filter((item) => item.phase !== "idle" && item.afterAssistantNumber == null)) {
    upsertCompressionActivity(stream, activity);
  }
  for (const assistant of turn.assistantDeleted ? [] : (turn.assistantTurns || [])) {
    startTurn(stream, assistant.number);
    appendReasoning(stream, assistant.number, assistant.reasoning || "");
    appendTurnText(stream, assistant.number, assistant.content || "");
    endTurn(stream, assistant.number, Boolean(assistant.hasToolCalls));
    for (const tool of assistant.tools || []) {
      const data = {
        turn: assistant.number, callId: tool.callId, callOrder: tool.callOrder,
        tool: tool.name, arguments: tool.arguments || "", status: tool.status,
        planQuestions: tool.planQuestions, answers: tool.answers, skipped: Boolean(tool.skipped),
      };
      appendToolCall(stream, data, assistant.number);
      if (tool.status === "answered") {
        resolvePlanQuestionItem(stream, data, assistant.number);
      } else if (tool.status !== "running" && tool.status !== "awaiting_input") {
        completeToolCall(stream, { ...data, success: tool.success, content: tool.result || "" }, assistant.number);
      }
    }
    appendToolMedia(stream, assistant.images, assistant.videos);
    for (const activity of compressions.filter((item) => item.phase !== "idle" && item.afterAssistantNumber === assistant.number)) {
      upsertCompressionActivity(stream, activity);
    }
  }
  for (const reasoning of turn.assistantDeleted ? [] : (turn.researchReasoning || [])) {
    appendResearchReasoning(stream, {
      agentId: reasoning.agentId, agentName: reasoning.agentName,
      content: reasoning.reasoning || "", replace: true, truncated: Boolean(reasoning.truncated),
    }, true);
  }
  for (const tool of turn.assistantDeleted ? [] : (turn.researchTools || [])) {
    const data = {
      callId: tool.callId, callOrder: tool.callOrder, tool: tool.name,
      arguments: tool.arguments || "", status: tool.status, agentId: tool.agentId,
      agentName: tool.agentName, research: true,
    };
    appendToolCall(stream, data);
    if (tool.status !== "running") {
      completeToolCall(stream, { ...data, success: tool.success, content: tool.result || "" });
    }
  }
  for (const activity of compressions.filter((item) => item.phase === "idle" || (
    item.afterAssistantNumber != null
    && !(turn.assistantTurns || []).some((assistant) => assistant.number === item.afterAssistantNumber)
  ))) {
    upsertCompressionActivity(stream, activity);
  }
  if (!turn.assistantDeleted) recordFileChanges(stream, turn.fileChanges);
  if (active || turn.status === "streaming") {
    for (const agent of turn.researchAgents || []) updateResearchAgentStatus(stream, agent);
    activeStream = stream;
    return;
  }
  finishStream(stream, turn.status || "done", turn.error || "");
}

function renderEmpty(log, text) {
  for (const element of log.querySelectorAll(".chat-progress-text")) cancelMarkdownPatch(element);
  log.innerHTML = "";
  const empty = document.createElement("div");
  empty.className = "empty-state chat-empty";
  empty.textContent = text;
  log.appendChild(empty);
}

function createMessageEl(role, text, images = [], videos = [], options = {}) {
  const el = document.createElement("div");
  el.className = `chat-message chat-message-${role}`;
  el.dataset.role = role;
  el.dataset.copyText = text;
  el.appendChild(createMessageActions(el, role));
  const body = document.createElement("div");
  body.className = "chat-message-body";
  el.appendChild(body);
  if (role === "assistant") {
    const timeline = document.createElement("div");
    timeline.className = "chat-timeline";
    timeline.hidden = true;
    body.appendChild(timeline);
    const content = document.createElement("div");
    content.className = "chat-message-content chat-final-content markdown-body";
    patchMarkdownElement(content, text);
    content.hidden = text === "";
    body.appendChild(content);
  } else {
    appendUserMedia(body, images, videos, options.turnId || "");
    const content = document.createElement("div");
    content.className = "chat-message-content markdown-body";
    patchMarkdownElement(content, text);
    body.appendChild(content);
  }
  return el;
}

export function canCompressChat(log) {
  return Boolean(
    binding?.log === log
    && binding.workspaceId
    && binding.surface === "chat"
    && binding.hasSnapshot
    && binding.turns.size > 0,
  );
}

export function compressChat(log, model) {
  if (!canCompressChat(log)) return false;
  return ws.send({
    type: "chat_compress", workspaceId: binding.workspaceId, chatId: binding.activeChatId,
    ...(model ? { model } : {}),
  });
}

function createMessageActions(message, role) {
  const actions = document.createElement("div");
  actions.className = "chat-message-actions";
  actions.setAttribute("role", "toolbar");
  actions.setAttribute("aria-label", "Message actions");

  const copy = messageActionButton({
    action: "copy",
    label: role === "assistant" ? "Copy final response" : "Copy message",
    icon: icons.copy,
    className: "chat-message-copy",
  });
  let resetCopyState = 0;
  copy.addEventListener("click", async () => {
    const text = message.dataset.copyText || "";
    if (!text) {
      toast(role === "assistant" ? "There is no final response to copy yet." : "Message has no text to copy.", { sticky: true });
      return;
    }
    try {
      await copyText(text);
      copy.innerHTML = icons.check;
      copy.classList.add("is-copied");
      copy.title = "Copied";
      copy.setAttribute("aria-label", "Copied");
      window.clearTimeout(resetCopyState);
      resetCopyState = window.setTimeout(() => {
        copy.innerHTML = icons.copy;
        copy.classList.remove("is-copied");
        copy.title = role === "assistant" ? "Copy final response" : "Copy message";
        copy.setAttribute("aria-label", role === "assistant" ? "Copy final response" : "Copy message");
      }, 1600);
      toast(role === "assistant" ? "Final response copied." : "Message copied.");
    } catch (error) {
      toast(error instanceof Error ? error.message : "Could not copy the message.", { sticky: true });
    }
  });

  const remove = messageActionButton({
    action: "delete",
    label: role === "assistant" ? "Delete response" : "Delete message",
    icon: icons.trash,
    className: "chat-message-delete",
  });
  remove.addEventListener("click", () => {
    const turnId = message.dataset.turnId || "";
    if (!binding?.workspaceId || !binding.activeChatId || !turnId) return;
    if (activeStream) {
      toast("Wait for the current response to finish before deleting messages.", { sticky: true });
      return;
    }
    const prompt = role === "assistant"
      ? "Delete this AI response? Its content, reasoning, tool calls, and future AI context will be removed."
      : "Delete this user message? Its text, attachments, and future AI context will be removed.";
    if (!window.confirm(prompt)) return;
    const sent = ws.send({
      type: "chat_message_delete",
      workspaceId: binding.workspaceId,
      chatId: binding.activeChatId,
      turnId,
      role,
      ...(binding.surface === "code" ? { surface: "code" } : {}),
    });
    if (!sent) toast("Could not send the delete request.", { sticky: true });
  });

  const rerun = messageActionButton({
    action: "rerun",
    label: role === "assistant" ? "Rerun response" : "Rerun from message",
    icon: icons.refresh,
  });
  rerun.addEventListener("click", () => {
    const turnId = message.dataset.turnId || "";
    if (!binding?.workspaceId || !binding.activeChatId || !turnId) return;
    if (activeStream) {
      toast("Wait for the current response to finish before rerunning messages.", { sticky: true });
      return;
    }
    const prompt = role === "assistant"
      ? "Rerun this response? Its preceding user message will be rerun, and this response and all later messages will be removed."
      : "Rerun from this message? Its response and all later messages will be removed.";
    if (!window.confirm(prompt)) return;
    const sent = ws.send({
      type: "chat_message_rerun",
      workspaceId: binding.workspaceId,
      chatId: binding.activeChatId,
      turnId,
      ...(binding.surface === "code" ? { surface: "code" } : {}),
    });
    if (!sent) toast("Could not send the rerun request.", { sticky: true });
  });

  const edit = messageActionButton({
    action: "edit",
    label: role === "assistant" ? "Edit response" : "Edit message",
    icon: icons.edit,
  });
  edit.addEventListener("click", () => beginMessageEdit(message, role));

  actions.append(
    copy,
    edit,
    rerun,
    remove,
  );
  return actions;
}

function messageActionButton({ action, label, icon, className = "", disabled = false }) {
  const button = document.createElement("button");
  button.type = "button";
  button.className = `icon-button chat-message-action chat-message-${action} ${className}`.trim();
  button.dataset.messageAction = action;
  button.title = label;
  button.setAttribute("aria-label", label);
  button.disabled = disabled;
  button.innerHTML = icon;
  return button;
}

function createTurnView(turnId, userText, images = [], videos = [], options = {}) {
  const user = createMessageEl("user", userText, images, videos, { turnId });
  user.dataset.turnId = turnId;
  if (!options.userDeleted) binding.log.appendChild(user);
  const el = createMessageEl("assistant", "");
  el.classList.add("is-streaming");
  el.dataset.turnId = turnId;
  if (options.userDeleted) {
    const rerun = el.querySelector("[data-message-action='rerun']");
    if (rerun) {
      rerun.disabled = true;
      rerun.title = "Cannot rerun without the deleted user message";
      rerun.setAttribute("aria-label", rerun.title);
    }
  }
  if (!options.assistantDeleted) binding.log.appendChild(el);
  return {
    id: turnId, el, user, timeline: el.querySelector(".chat-timeline"),
    content: el.querySelector(".chat-final-content"), done: false,
    currentTurn: null, finalTurn: null, turns: new Map(), tools: new Map(),
    mediaZone: null, mediaSeen: new Set(),
    compressions: new Map(),
    fileChanges: [],
    researchAgents: new Map(), researchReasoning: new Map(), researchStatusContainer: null,
  };
}

function rewindTurnViews(fromTurnId) {
  let removing = false;
  for (const [turnId, stream] of binding.turns) {
    if (turnId === fromTurnId) removing = true;
    if (!removing) continue;
    for (const element of stream.el.querySelectorAll(".chat-progress-text")) cancelMarkdownPatch(element);
    stream.user.remove();
    stream.el.remove();
    binding.turns.delete(turnId);
  }
}

function beginMessageEdit(message, role) {
  if (activeStream) {
    toast("Wait for the current response to finish before editing messages.", { sticky: true });
    return;
  }
  if (message.querySelector(".chat-edit-form")) return;
  const content = message.querySelector(".chat-message-content");
  const original = message.dataset.copyText || "";
  if (!content || !original.trim()) {
    toast("Message has no editable text.", { sticky: true });
    return;
  }
  message.classList.add("is-editing");

  const form = document.createElement("form");
  form.className = "chat-edit-form";
  const textarea = document.createElement("textarea");
  textarea.className = "chat-edit-textarea";
  textarea.rows = Math.min(10, Math.max(3, original.split("\n").length));
  textarea.value = original;
  textarea.setAttribute("aria-label", role === "assistant" ? "Edit assistant response" : "Edit user message");
  const help = document.createElement("p");
  help.className = "chat-edit-help";
  help.textContent = role === "assistant"
    ? "Saving updates conversation history without generating a new response."
    : "Saving resubmits this message and removes its current response and all later messages.";
  const controls = document.createElement("div");
  controls.className = "chat-edit-actions";
  const cancel = document.createElement("button");
  cancel.type = "button";
  cancel.className = "secondary-button compact-button";
  cancel.textContent = "Cancel";
  const save = document.createElement("button");
  save.type = "submit";
  save.className = "primary-button compact-button";
  save.textContent = role === "user" ? "Save & Resubmit" : "Save";
  controls.append(cancel, save);
  form.append(textarea, help, controls);
  content.hidden = true;
  content.parentElement.insertBefore(form, content);

  const close = () => {
    form.remove();
    content.hidden = false;
    message.classList.remove("is-editing");
  };
  cancel.addEventListener("click", close);
  textarea.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      event.preventDefault();
      close();
    } else if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      form.requestSubmit();
    }
  });
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    const value = textarea.value.trim();
    const turnId = message.dataset.turnId || "";
    if (!value) {
      toast("Message cannot be empty.", { sticky: true });
      textarea.focus();
      return;
    }
    if (!binding?.workspaceId || !binding.activeChatId || !turnId) return;
    const sent = ws.send({
      type: "chat_message_edit",
      workspaceId: binding.workspaceId,
      chatId: binding.activeChatId,
      turnId,
      role,
      message: value,
      ...(binding.surface === "code" ? { surface: "code" } : {}),
    });
    if (!sent) toast("Could not send the edit request.", { sticky: true });
    else message.classList.remove("is-editing");
  });
  textarea.focus();
  textarea.setSelectionRange(textarea.value.length, textarea.value.length);
}

// --- Collapsible chat media zones -----------------------------------------
// Every message that carries attachments renders one `.chat-message-media`
// zone containing either a collapsed chip ("N images + M videos - size") or an
// expanded header bar plus the figures. Media defaults to collapsed; expand
// state is runtime-only per turn (not persisted), matching the Wails app.

let userMediaCounter = 0;
const nextUserMediaId = () => `user-media-${++userMediaCounter}`;

function mediaSummaryLabel(images, videos) {
  const parts = [];
  if (images.length) parts.push(`${images.length} image${images.length === 1 ? "" : "s"}`);
  if (videos.length) parts.push(`${videos.length} video${videos.length === 1 ? "" : "s"}`);
  const totalBytes = [...images, ...videos].reduce((total, item) => total + (Number(item.bytes) || 0), 0);
  return `${parts.join(" + ")} - ${formatMediaBytes(totalBytes)}`;
}

// createMediaZone builds the collapsible container for a set of attachments.
// kind is "user" or "assistant"; ownerKey identifies the owning message/turn
// for expand-state tracking. The canonical attachment lists live on the zone
// itself (zone.mediaImages / zone.mediaVideos) so collapsing never loses the
// payloads — toggling only swaps which view is rendered.
function createMediaZone(kind, ownerKey, images, videos) {
  const zone = document.createElement("div");
  zone.className = "chat-message-media";
  zone.dataset.mediaZone = kind;
  zone.mediaImages = images.slice();
  zone.mediaVideos = videos.slice();
  renderMediaZone(zone, ownerKey);
  return zone;
}

function renderMediaZone(zone, ownerKey) {
  const images = zone.mediaImages || [];
  const videos = zone.mediaVideos || [];
  const attachments = [...images, ...videos];
  const expanded = isMediaExpanded(ownerKey);
  zone.classList.toggle("is-collapsed", !expanded);
  zone.replaceChildren();

  if (!expanded) {
    const label = mediaSummaryLabel(images, videos);
    const chip = document.createElement("button");
    chip.type = "button";
    chip.className = "chat-media-chip";
    chip.title = "Show media";
    chip.setAttribute("aria-label", `Show media: ${label}`);
    chip.addEventListener("click", () => toggleMediaZone(zone, ownerKey));
    if (images.length) {
      const span = document.createElement("span");
      span.className = "chat-media-chip-icon";
      span.innerHTML = icons.image;
      chip.appendChild(span);
    }
    if (videos.length) {
      const span = document.createElement("span");
      span.className = "chat-media-chip-icon";
      span.innerHTML = icons.video;
      chip.appendChild(span);
    }
    const text = document.createElement("span");
    text.textContent = label;
    const toggle = document.createElement("span");
    toggle.className = "chat-media-chip-toggle";
    toggle.innerHTML = icons.eye;
    chip.append(text, toggle);
    zone.appendChild(chip);
    return;
  }

  const label = mediaSummaryLabel(images, videos);
  const bar = document.createElement("div");
  bar.className = "chat-message-media-bar";
  const barText = document.createElement("span");
  barText.textContent = label;
  const toggle = document.createElement("button");
  toggle.type = "button";
  toggle.className = "icon-button chat-media-toggle";
  toggle.title = "Hide media";
  toggle.setAttribute("aria-label", `Hide media: ${label}`);
  toggle.innerHTML = icons.collapse;
  toggle.addEventListener("click", () => toggleMediaZone(zone, ownerKey));
  bar.append(barText, toggle);
  zone.appendChild(bar);

  const gallery = document.createElement("div");
  gallery.className = "chat-message-media-gallery";
  for (const attachment of attachments) gallery.append(buildMediaFigure(attachment));
  zone.appendChild(gallery);
}

function toggleMediaZone(zone, ownerKey) {
  const expanded = isMediaExpanded(ownerKey);
  if (expanded) collapseMediaState.delete(ownerKey);
  else collapseMediaState.add(ownerKey);
  // Re-render in place from the zone's canonical attachment lists; collapsing
  // unmounts the figures but never discards the payloads.
  renderMediaZone(zone, ownerKey);
}

const collapseMediaState = new Set();
function isMediaExpanded(ownerKey) { return collapseMediaState.has(ownerKey); }

function appendUserMedia(body, images, videos, turnId) {
  if (!images.length && !videos.length) return;
  const ownerKey = turnId || nextUserMediaId();
  body.append(createMediaZone("user", ownerKey, images, videos));
}

// buildMediaFigure renders one uploaded or tool-generated media attachment as a
// captioned <figure>. Video MIME types get a playable <video>, everything else
// an <img> (animated GIFs included). Shared by user uploads and assistant-side
// tool media so both directions look identical.
function buildMediaFigure(attachment) {
  const isVideo = String(attachment.mediaType || "").startsWith("video/");
  const figure = document.createElement("figure");
  figure.className = `chat-message-media-item ${isVideo ? "is-video" : "is-image"}`;
  const media = document.createElement(isVideo ? "video" : "img");
  media.src = attachment.dataUrl || "";
  if (isVideo) {
    media.controls = true;
    media.loop = true;
    media.muted = true;
    media.preload = "metadata";
    media.playsInline = true;
  } else {
    media.alt = attachment.name || "Attached image";
  }
  if (isVideo) {
    const stage = document.createElement("div");
    stage.className = "chat-media-stage";
    stage.append(media, attachVideoVolumeControl(media, "chat-media-volume"));
    figure.append(stage, mediaCaption(attachment));
  } else {
    figure.append(media, mediaCaption(attachment));
  }
  return figure;
}

// ensureAssistantMediaZone lazily creates the single media zone for an
// assistant message (after the final content) and remembers it on the stream.
// One zone per turn regardless of how many sub-turns/tools contribute keeps
// promoteFinalText cleanup away from media. The "-assistant" suffix keeps the
// expand state independent from the user upload zone of the same turn.
function ensureAssistantMediaZone(stream) {
  if (!stream.mediaZone) {
    const zone = createMediaZone("assistant", `${stream.id}-assistant`, [], []);
    zone.hidden = true;
    stream.el.querySelector(".chat-message-body").appendChild(zone);
    stream.mediaZone = zone;
  }
  return stream.mediaZone;
}

// appendToolMedia ingests structured media from a tool_result event (or a
// restored snapshot) into the assistant media zone, de-duplicating by
// attachment id so replayed events cannot double-append.
function appendToolMedia(stream, images = [], videos = []) {
  if (!stream) return;
  const attachments = [...images, ...videos];
  if (!attachments.length) return;
  const zone = ensureAssistantMediaZone(stream);
  let changed = false;
  for (const attachment of attachments) {
    const id = attachment.id || `${attachment.mediaType}|${attachment.bytes ?? 0}|${attachment.dataUrl ? attachment.dataUrl.length : 0}`;
    if (stream.mediaSeen.has(id)) continue;
    stream.mediaSeen.add(id);
    if (String(attachment.mediaType || "").startsWith("video/")) zone.mediaVideos.push(attachment);
    else zone.mediaImages.push(attachment);
    changed = true;
  }
  if (!changed) return;
  zone.hidden = false;
  renderMediaZone(zone, `${stream.id}-assistant`);
}

function mediaCaption(attachment) {
  const caption = document.createElement("figcaption");
  const name = document.createElement("strong");
  name.textContent = attachment.name || "Attachment";
  const detail = document.createElement("span");
  detail.textContent = `${attachment.mediaType || "media"} · ${formatMediaBytes(Number(attachment.bytes) || 0)}`;
  caption.append(name, detail);
  return caption;
}

function formatMediaBytes(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function startTurn(stream, number) {
  if (!stream) return;
  stream.currentTurn = ensureTurn(stream, Number.isInteger(number) ? number : 0);
}

function ensureTurn(stream, number) {
  let turn = stream.turns.get(number);
  if (turn) {
    if (!turn.el.isConnected && stream.timeline.isConnected) stream.timeline.appendChild(turn.el);
    return turn;
  }
  const el = document.createElement("div");
  el.className = "chat-turn";
  el.dataset.turn = String(number);
  stream.timeline.hidden = false;
  stream.timeline.appendChild(el);
  turn = {
    number, el, text: "", lastKind: "", textBlock: null,
    textBlockText: "", reasoning: null,
  };
  stream.turns.set(number, turn);
  return turn;
}

function appendTurnText(stream, turnNumber, text) {
  if (!stream || !text) return;
  const turn = ensureTurn(stream, Number.isInteger(turnNumber) ? turnNumber : 0);
  completeReasoning(turn);
  if (turn.lastKind !== "text" || !turn.textBlock) {
    turn.textBlock = document.createElement("div");
    turn.textBlock.className = "chat-progress-text markdown-body";
    turn.textBlockText = "";
    turn.el.appendChild(turn.textBlock);
  }
  turn.lastKind = "text";
  turn.text += text;
  turn.textBlockText += text;
  queueMarkdownPatch(turn.textBlock, turn.textBlockText);
}

function appendReasoning(stream, turnNumber, text) {
  if (!stream || !text) return;
  const turn = ensureTurn(stream, Number.isInteger(turnNumber) ? turnNumber : 0);
  if (turn.lastKind === "text") flushTurnTextBlock(turn);
  if (turn.lastKind !== "reasoning" || !turn.reasoning) {
    completeReasoning(turn);
    turn.reasoning = createReasoningItem();
    turn.el.appendChild(turn.reasoning.details);
  }
  turn.lastKind = "reasoning";
  turn.reasoning.text += text;
  turn.reasoning.body.textContent = turn.reasoning.text;
}

function createReasoningItem() {
  const details = document.createElement("details");
  details.className = "chat-activity-item chat-reasoning-item is-running";
  const summary = document.createElement("summary");
  const label = document.createElement("span");
  label.className = "chat-activity-name";
  label.textContent = "Thinking…";
  summary.appendChild(label);
  details.appendChild(summary);
  const body = document.createElement("div");
  body.className = "chat-activity-body chat-reasoning-content";
  details.appendChild(body);
  return { details, label, body, text: "", complete: false };
}

function ensureCompressionTimeline(stream) {
  if (!stream || stream.timeline.isConnected) return;
  const body = stream.content?.parentElement;
  if (!body) return;
  const work = document.createElement("details");
  work.className = "chat-work-disclosure";
  const summary = document.createElement("summary");
  summary.textContent = "Show work";
  const content = document.createElement("div");
  content.className = "chat-work-content";
  stream.timeline.hidden = false;
  content.appendChild(stream.timeline);
  work.append(summary, content);
  body.insertBefore(work, stream.content);
}

function compressionActivityHost(stream, activity) {
  ensureCompressionTimeline(stream);
  if (!stream?.timeline.isConnected) return null;
  stream.timeline.hidden = false;
  if (activity.phase === "idle") return stream.timeline;
  return ensureTurn(stream, Number.isInteger(activity.afterAssistantNumber) ? activity.afterAssistantNumber : 0).el;
}

function formatCompressionTokens(value) {
  const count = Number(value) || 0;
  return count.toLocaleString();
}

function formatCompressionDuration(value) {
  const milliseconds = Number(value) || 0;
  if (milliseconds < 1000) return `${milliseconds} ms`;
  return `${(milliseconds / 1000).toFixed(milliseconds < 10000 ? 1 : 0)} s`;
}

function updateCompressionItem(item, activity) {
  const status = String(activity.status || "queued");
  item.details.className = `chat-activity-item chat-compression-item is-${status}${status === "queued" || status === "running" ? " is-running" : ""}`;
  item.details.dataset.compressionId = activity.id || "";
  const names = {
    queued: "Context compression queued",
    running: "Compressing context…",
    completed: "Context compressed",
    skipped: "Context compression skipped",
    failed: "Context compression failed",
  };
  item.name.textContent = names[status] || "Context compression";
  if (status === "completed") {
    const before = Number(activity.beforeTokens) || 0;
    const after = Number(activity.afterTokens) || 0;
    const reclaimedPercent = before > 0 ? Math.round(((before - after) / before) * 100) : 0;
    item.status.textContent = `${formatCompressionTokens(before)} → ${formatCompressionTokens(after)} tokens · ${reclaimedPercent}% reclaimed · ${formatCompressionDuration(activity.durationMs)}`;
  } else if (status === "running") {
    item.status.textContent = "Summarizing safe history…";
  } else if (status === "queued") {
    item.status.textContent = "Waiting for a safe response boundary…";
  } else {
    item.status.textContent = activity.error || (status === "skipped" ? "Nothing safe to compress" : "Compression did not complete");
  }
  const metrics = [];
  metrics.push(`${activity.trigger === "manual" ? "Manual" : "Automatic"} trigger${activity.thresholdPercent ? ` · ${activity.thresholdPercent}% threshold` : ""}`);
  if (activity.beforeTokens || activity.afterTokens) {
    metrics.push(`Context estimate: ${formatCompressionTokens(activity.beforeTokens)} before, ${formatCompressionTokens(activity.afterTokens)} after`);
  }
  if (activity.usageSource) metrics.push(`Usage source: ${activity.usageSource}`);
  if (status === "completed") metrics.push(activity.recoveryAvailable ? "Compacted raw history remains searchable." : "History recovery is unavailable.");
  if (activity.error && !item.status.textContent.includes(activity.error)) metrics.push(activity.error);
  item.body.replaceChildren(...metrics.map((value) => {
    const line = document.createElement("p");
    line.textContent = value;
    return line;
  }));
}

function upsertCompressionActivity(stream, activity) {
  if (!stream || !activity?.id) return;
  let item = stream.compressions.get(activity.id);
  if (!item) {
    const details = document.createElement("details");
    const summary = document.createElement("summary");
    const name = document.createElement("span");
    name.className = "chat-activity-name";
    const status = document.createElement("span");
    status.className = "chat-activity-status";
    status.setAttribute("aria-live", "polite");
    const body = document.createElement("div");
    body.className = "chat-activity-body chat-compression-metrics";
    summary.append(name, status);
    details.append(summary, body);
    item = { details, name, status, body };
    stream.compressions.set(activity.id, item);
  }
  updateCompressionItem(item, activity);
  compressionActivityHost(stream, activity)?.appendChild(item.details);
}

function completeReasoning(turn) {
  const reasoning = turn?.reasoning;
  if (!reasoning || reasoning.complete) return;
  reasoning.complete = true;
  reasoning.label.textContent = "Thinking";
  reasoning.details.classList.remove("is-running");
}

function updateResearchAgentStatus(stream, agent) {
  if (!stream || !agent?.id) return;
  const status = String(agent.status || "running");
  const active = status === "queued" || status === "running" || status === "summarizing";
  if (!active) {
    stream.researchAgents.get(agent.id)?.remove();
    stream.researchAgents.delete(agent.id);
    if (stream.researchStatusContainer && !stream.researchStatusContainer.childElementCount) {
      stream.researchStatusContainer.remove();
      stream.researchStatusContainer = null;
    }
    const reasoning = stream.researchReasoning.get(agent.id);
    if (reasoning) {
      reasoning.details.classList.remove("is-running");
      reasoning.label.textContent = `${reasoning.agentName} thinking`;
    }
    return;
  }
  if (!stream.researchStatusContainer) {
    const container = document.createElement("div");
    container.className = "chat-research-statuses";
    stream.timeline.hidden = false;
    stream.timeline.appendChild(container);
    stream.researchStatusContainer = container;
  }
  let chip = stream.researchAgents.get(agent.id);
  if (!chip) {
    chip = document.createElement("span");
    chip.className = "chat-research-status";
    stream.researchAgents.set(agent.id, chip);
    stream.researchStatusContainer.appendChild(chip);
  }
  chip.className = `chat-research-status is-${status}`;
  chip.textContent = `${agent.name || "Researcher"}${agent.phase ? ` · ${agent.phase}` : ` · ${status}`}`;
  chip.title = agent.taskLabel || agent.error || chip.textContent;
}

function clearResearchAgentStatuses(stream) {
  if (!stream) return;
  stream.researchStatusContainer?.remove();
  stream.researchStatusContainer = null;
  stream.researchAgents.clear();
  for (const item of stream.researchReasoning.values()) {
    item.details.classList.remove("is-running");
    item.label.textContent = `${item.agentName} thinking`;
  }
}

function appendResearchReasoning(stream, data, complete = false) {
  if (!stream || !data?.agentId || !data.content) return;
  let item = stream.researchReasoning.get(data.agentId);
  if (!item) {
    const details = document.createElement("details");
    details.className = "chat-activity-item chat-reasoning-item chat-research-reasoning is-running";
    const summary = document.createElement("summary");
    const label = document.createElement("span");
    label.className = "chat-activity-name";
    const agentName = data.agentName || "Researcher";
    label.textContent = `${agentName} thinking…`;
    summary.appendChild(label);
    const body = document.createElement("div");
    body.className = "chat-activity-body chat-reasoning-content";
    details.append(summary, body);
    item = { details, label, body, text: "", agentName };
    stream.researchReasoning.set(data.agentId, item);
    stream.timeline.hidden = false;
    stream.timeline.appendChild(details);
  }
  item.text = data.replace ? data.content : item.text + data.content;
  item.body.textContent = item.text;
  if (data.truncated) item.details.classList.add("is-truncated");
  if (complete) {
    item.details.classList.remove("is-running");
    item.label.textContent = `${item.agentName} thinking`;
  }
}

function endTurn(stream, turnNumber, hasToolCalls) {
  if (!stream) return;
  const turn = ensureTurn(stream, Number.isInteger(turnNumber) ? turnNumber : 0);
  completeReasoning(turn);
  flushTurnTextBlock(turn);
  turn.lastKind = "";
  if (hasToolCalls) {
    turn.el.classList.add("is-progress-turn");
  } else {
    stream.finalTurn = turn;
    promoteFinalText(stream, turn);
  }
  if (stream.currentTurn === turn) stream.currentTurn = null;
}

function promoteFinalText(stream, turn) {
  stream.el.dataset.copyText = turn.text;
  patchMarkdownElement(stream.content, turn.text);
  stream.content.hidden = turn.text === "";
  for (const block of turn.el.querySelectorAll(":scope > .chat-progress-text")) {
    cancelMarkdownPatch(block);
    block.remove();
  }
  removeEmptyTurn(turn);
}

function flushTurnTextBlock(turn) {
  if (!turn?.textBlock) return;
  flushMarkdownPatch(turn.textBlock, turn.textBlockText);
  turn.textBlock = null;
  turn.textBlockText = "";
}

function appendToolCall(stream, data, turnNumber) {
  if (!stream) return;
  const callOrder = Number.isInteger(data.callOrder) ? data.callOrder : stream.tools.size;
  const callId = data.callId || `turn-${turnNumber}-call-${callOrder}`;
  if (stream.tools.has(callId)) return;
  const questionSet = data.tool === askUserQuestionsToolName
    ? normalizePlanQuestionSet(data.planQuestions, data.arguments, callId)
    : null;
  const item = questionSet
    ? createPlanQuestionItem(questionSet)
    : createToolItem(data.tool || "tool", data.arguments || "", data.agentName || "");
  if (data.research) item.details.classList.add("is-research-tool");
  item.details.dataset.callId = callId;
  stream.timeline.hidden = false;
  stream.timeline.appendChild(item.details);
  stream.tools.set(callId, item);
}

function createToolItem(toolName, args, agentName = "") {
  const details = document.createElement("details");
  details.className = "chat-activity-item chat-tool-item is-running";
  const summary = document.createElement("summary");
  const name = document.createElement("span");
  name.className = "chat-activity-name";
  name.textContent = agentName ? `${agentName} · ${toolName}` : toolName;
  const status = document.createElement("span");
  status.className = "chat-activity-status";
  status.textContent = "Running…";
  summary.append(name, status);
  details.appendChild(summary);
  const body = document.createElement("div");
  body.className = "chat-activity-body";
  body.appendChild(createToolSection("Arguments", formatStructured(args) || "No arguments").section);
  const result = createToolSection("Result", "Waiting for result…");
  body.appendChild(result.section);
  details.appendChild(body);
  return { details, body, status, result: result.content, toolName };
}

function normalizePlanQuestionSet(value, args, callId) {
  let source = value;
  if (!source && typeof args === "string") {
    try { source = JSON.parse(args); } catch { return null; }
  }
  const questions = Array.isArray(source?.questions)
    ? source.questions.map((question) => ({
      id: String(question?.id || "").trim(),
      question: String(question?.question || "").trim(),
      options: Array.isArray(question?.options)
        ? question.options.map((option) => String(option || "").trim()).filter(Boolean)
        : [],
    })).filter((question) => question.id && question.question)
    : [];
  if (!questions.length) return null;
  return { questionSetId: String(source?.questionSetId || callId), questions };
}

function createPlanQuestionItem(questionSet) {
  const details = document.createElement("details");
  details.className = "chat-activity-item chat-plan-question-item is-running is-awaiting-input";
  details.open = true;
  const summary = document.createElement("summary");
  const name = document.createElement("span");
  name.className = "chat-activity-name";
  name.textContent = questionSet.questions.length === 1 ? "Clarifying question" : "Clarifying questions";
  const status = document.createElement("span");
  status.className = "chat-activity-status";
  status.textContent = "Waiting for your answers…";
  summary.append(name, status);
  details.appendChild(summary);

  const body = document.createElement("div");
  body.className = "chat-activity-body chat-plan-question-body";
  const form = document.createElement("form");
  form.className = "chat-plan-question-form";
  const fields = [];
  questionSet.questions.forEach((question, questionIndex) => {
    const fieldset = document.createElement("fieldset");
    fieldset.className = "chat-plan-question-field";
    const legend = document.createElement("legend");
    legend.textContent = question.question;
    fieldset.appendChild(legend);
    const radios = [];
    question.options.forEach((option, optionIndex) => {
      const label = document.createElement("label");
      label.className = "chat-plan-question-option";
      const radio = document.createElement("input");
      radio.type = "radio";
      radio.name = `plan-question-${questionSet.questionSetId}-${questionIndex}`;
      radio.value = String(optionIndex);
      const text = document.createElement("span");
      text.textContent = option;
      label.append(radio, text);
      fieldset.appendChild(label);
      radios.push(radio);
    });
    const custom = document.createElement("input");
    custom.type = "text";
    custom.className = "chat-plan-question-custom";
    custom.maxLength = 2000;
    custom.placeholder = question.options.length ? "Or type another answer…" : "Type your answer…";
    custom.setAttribute("aria-label", `${question.question} — custom answer`);
    custom.addEventListener("input", () => {
      if (custom.value.trim()) radios.forEach((radio) => { radio.checked = false; });
      fieldset.classList.remove("is-invalid");
    });
    radios.forEach((radio) => radio.addEventListener("change", () => {
      if (radio.checked) custom.value = "";
      fieldset.classList.remove("is-invalid");
    }));
    fieldset.appendChild(custom);
    form.appendChild(fieldset);
    fields.push({ question, fieldset, radios, custom });
  });

  const error = document.createElement("p");
  error.className = "chat-plan-question-error";
  error.hidden = true;
  const actions = document.createElement("div");
  actions.className = "chat-plan-question-actions";
  const skip = document.createElement("button");
  skip.type = "button";
  skip.className = "secondary-button compact-button";
  skip.textContent = "Skip";
  const submit = document.createElement("button");
  submit.type = "submit";
  submit.className = "primary-button compact-button";
  submit.textContent = "Submit answers";
  actions.append(skip, submit);
  form.append(error, actions);
  body.appendChild(form);
  details.appendChild(body);

  const sendResolution = (type, answers = undefined) => {
    if (!binding?.workspaceId || !binding.activeChatId) return false;
    const requestId = globalThis.crypto?.randomUUID?.() || `request-${Date.now()}-${Math.random().toString(16).slice(2)}`;
    return ws.send({
      type, workspaceId: binding.workspaceId, chatId: binding.activeChatId,
      questionSetId: questionSet.questionSetId, requestId,
      ...(answers ? { answers } : {}),
      ...(binding.surface === "code" ? { surface: "code" } : {}),
    });
  };
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    const answers = [];
    let firstInvalid = null;
    for (const field of fields) {
      const customText = field.custom.value.trim();
      const selected = field.radios.find((radio) => radio.checked);
      if (!customText && !selected) {
        field.fieldset.classList.add("is-invalid");
        firstInvalid ||= field.fieldset;
        continue;
      }
      answers.push({
        questionId: field.question.id,
        optionIndex: customText ? -1 : Number(selected.value),
        ...(customText ? { text: customText } : {}),
      });
    }
    if (firstInvalid) {
      error.textContent = "Answer each question, or skip this set.";
      error.hidden = false;
      firstInvalid.querySelector("input")?.focus();
      return;
    }
    error.hidden = true;
    if (!sendResolution("plan_questions_submit", answers)) {
      error.textContent = "Could not send your answers. Please try again.";
      error.hidden = false;
    }
  });
  skip.addEventListener("click", () => {
    error.hidden = true;
    if (!sendResolution("plan_questions_skip")) {
      error.textContent = "Could not skip these questions. Please try again.";
      error.hidden = false;
    }
  });
  return { details, status, body, form, questionSet, planQuestions: true };
}

function createToolSection(labelText, value) {
  const section = document.createElement("section");
  section.className = "chat-tool-section";
  const label = document.createElement("div");
  label.className = "chat-tool-label";
  label.textContent = labelText;
  const content = document.createElement("pre");
  content.className = "chat-tool-value";
  content.textContent = value;
  section.append(label, content);
  return { section, content };
}

function completeToolCall(stream, data, turnNumber) {
  if (!stream) return;
  recordFileChanges(stream, data.fileChanges);
  const callOrder = Number.isInteger(data.callOrder) ? data.callOrder : stream.tools.size;
  const callId = data.callId || `turn-${turnNumber}-call-${callOrder}`;
  if (!stream.tools.has(callId)) appendToolCall(stream, { ...data, callId }, turnNumber);
  const item = stream.tools.get(callId);
  if (!item) return;
  const succeeded = data.success === true;
  if (item.planQuestions) {
    if (succeeded) {
      const resolution = planQuestionResolution(data);
      showPlanQuestionResolution(item, resolution.answers, resolution.skipped);
    } else {
      item.details.classList.remove("is-running", "is-awaiting-input");
      item.details.classList.add("is-error");
      item.status.textContent = "Failed";
      const failure = document.createElement("p");
      failure.className = "chat-plan-question-error";
      failure.textContent = toolFailureMessage(data.content) || "The questions could not be completed.";
      item.body.replaceChildren(failure);
    }
    return;
  }
  item.details.classList.remove("is-running");
  item.details.classList.add(succeeded ? "is-success" : "is-error");
  item.status.textContent = succeeded ? "Completed" : "Failed";
  item.result.textContent = formatStructured(data.content) || "No result";
  if (succeeded && item.toolName === "echo_plugin_stage") {
    const stage = pluginStageFromToolResult(data.content);
    if (stage && !stream.timeline.querySelector(`[data-plugin-approval-stage="${CSS.escape(stage.id)}"]`)) {
      stream.timeline.appendChild(createPluginApprovalCard(stage));
    }
  }
}

function pluginStageFromToolResult(content) {
  let value = content;
  if (typeof value === "string") {
    try { value = JSON.parse(value); } catch { return null; }
  }
  const output = value?.output || value;
  const stage = output?.stage;
  if (!stage || typeof stage.id !== "string" || !/^stage-[a-z0-9-]{16,}$/.test(stage.id)) return null;
  if (!stage.validation?.manifest?.id || !stage.validation?.digest) return null;
  return stage;
}

function createPluginApprovalCard(stage) {
  const card = document.createElement("section");
  card.className = "chat-plugin-approval-card";
  card.dataset.pluginApprovalStage = stage.id;
  const heading = document.createElement("div");
  heading.className = "chat-plugin-approval-heading";
  const title = document.createElement("div");
  const kicker = document.createElement("span");
  kicker.textContent = "Owner approval required";
  const name = document.createElement("strong");
  name.textContent = `${stage.validation.manifest.name || stage.validation.manifest.id} v${stage.validation.manifest.version || ""}`;
  title.append(kicker, name);
  const trust = document.createElement("span");
  trust.className = "chat-plugin-trust-badge";
  trust.textContent = "Echo verified stage";
  heading.append(title, trust);
  const description = document.createElement("p");
  description.textContent = stage.validation.manifest.description || "Review this generated plugin snapshot before Echo installs any of it.";
  const facts = document.createElement("dl");
  for (const [label, value] of [["Plugin ID", stage.validation.manifest.id], ["Target", stage.validation.target], ["Digest", stage.validation.digest]]) {
    const term = document.createElement("dt"); term.textContent = label;
    const detail = document.createElement("dd"); const code = document.createElement("code"); code.textContent = value; detail.append(code);
    facts.append(term, detail);
  }
  const warning = document.createElement("p");
  warning.className = "chat-plugin-approval-warning";
  warning.textContent = stage.validation.manifest.runtime
    ? "This package contains native code that will run with your OS account permissions. Permission declarations are not an OS sandbox."
    : "This UI-only package will run in an opaque-origin sandboxed iframe.";
  const contributions = document.createElement("p");
  const permissionNames = (stage.validation.manifest.permissions || []).map(permission => permission.name);
  const toolNames = (stage.validation.manifest.contributes?.tools || []).map(tool => tool.name);
  const settingNames = (stage.validation.manifest.contributes?.settings || []).map(setting => `${setting.key} (${setting.scope})`);
  contributions.textContent = `Permissions: ${permissionNames.join(", ") || "none"}. Agent tools: ${toolNames.join(", ") || "none"}. Settings: ${settingNames.join(", ") || "none"}.`;
	const toolReview = document.createElement("div");
	toolReview.className = "chat-plugin-tool-review";
	for (const tool of stage.validation.manifest.contributes?.tools || []) {
		const details = document.createElement("details");
		const summary = document.createElement("summary");
		summary.textContent = `${tool.name} — ${tool.readOnly ? "read-only" : "mutating"}`;
		const schema = document.createElement("pre");
		schema.textContent = `RPC: ${tool.method}\nInput schema:\n${JSON.stringify(tool.inputSchema || {}, null, 2)}${tool.outputSchema ? `\nOutput schema:\n${JSON.stringify(tool.outputSchema, null, 2)}` : ""}`;
		details.append(summary, schema);
		toolReview.append(details);
	}
  const status = document.createElement("p");
  status.className = "chat-plugin-approval-status";
  const actions = document.createElement("div");
  actions.className = "chat-plugin-approval-actions";
  const choices = [
    ["Install, keep current scopes", "none"],
    ["Enable in this workspace", "workspace"],
    ["Enable globally", "global"],
  ];
  for (const [label, scope] of choices) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = scope === "global" ? "primary-button compact-button" : "secondary-button compact-button";
    button.textContent = label;
    if (scope === "workspace" && !binding?.workspaceId) button.disabled = true;
    button.addEventListener("click", async () => {
      setPluginApprovalBusy(card, true);
      status.textContent = "Installing approved snapshot…";
      try {
        await post(`/api/plugins/stages/${encodeURIComponent(stage.id)}/approve`, { scope, workspaceId: binding?.workspaceId || "", enable: scope !== "none" });
        status.textContent = "Installed. Navigation and approved tools are refreshing.";
        card.classList.add("is-approved");
        await refreshPluginCatalog();
      } catch (error) {
        status.textContent = `Could not install: ${error.message}`;
        setPluginApprovalBusy(card, false);
      }
    });
    actions.append(button);
  }
  const reject = document.createElement("button");
  reject.type = "button";
  reject.className = "secondary-button compact-button danger-button";
  reject.textContent = "Reject";
  reject.addEventListener("click", async () => {
    setPluginApprovalBusy(card, true);
    try {
      await del(`/api/plugins/stages/${encodeURIComponent(stage.id)}`);
      status.textContent = "Rejected. The staged snapshot was removed.";
      card.classList.add("is-rejected");
    } catch (error) {
      status.textContent = `Could not reject: ${error.message}`;
      setPluginApprovalBusy(card, false);
    }
  });
  actions.append(reject);
	card.append(heading, description, facts, warning, contributions, toolReview, actions, status);
  return card;
}

function setPluginApprovalBusy(card, busy) {
  card.querySelectorAll("button").forEach(button => { button.disabled = busy; });
}

function resolvePlanQuestionItem(stream, data, turnNumber) {
  if (!stream) return;
  const callOrder = Number.isInteger(data.callOrder) ? data.callOrder : stream.tools.size;
  const callId = data.callId || `turn-${turnNumber}-call-${callOrder}`;
  if (!stream.tools.has(callId)) appendToolCall(stream, { ...data, callId, tool: askUserQuestionsToolName }, turnNumber);
  const item = stream.tools.get(callId);
  if (!item?.planQuestions) return;
  showPlanQuestionResolution(item, Array.isArray(data.answers) ? data.answers : [], Boolean(data.skipped));
}

function planQuestionResolution(data) {
  let parsed = null;
  if (typeof data.content === "string") {
    try { parsed = JSON.parse(data.content); } catch { parsed = null; }
  } else if (data.content && typeof data.content === "object") {
    parsed = data.content;
  }
  const output = parsed?.output || {};
  return {
    answers: Array.isArray(data.answers) ? data.answers : (Array.isArray(output.answers) ? output.answers : []),
    skipped: Boolean(data.skipped || output.skipped),
  };
}

function toolFailureMessage(content) {
  if (typeof content !== "string") return "";
  try { return String(JSON.parse(content)?.error?.message || ""); } catch { return ""; }
}

function showPlanQuestionResolution(item, answers, skipped) {
  item.details.classList.remove("is-running", "is-awaiting-input", "is-error");
  item.details.classList.add("is-success");
  item.status.textContent = skipped ? "Skipped" : "Answered";
  const resolved = document.createElement("div");
  resolved.className = "chat-plan-question-answers";
  if (skipped) {
    const note = document.createElement("p");
    note.className = "chat-plan-question-skipped";
    note.textContent = "Questions skipped.";
    resolved.appendChild(note);
  } else {
    const byQuestion = new Map((answers || []).map((answer) => [answer.questionId, answer]));
    for (const question of item.questionSet.questions) {
      const answer = byQuestion.get(question.id);
      const row = document.createElement("div");
      row.className = "chat-plan-question-answer";
      const prompt = document.createElement("div");
      prompt.className = "chat-plan-question-answer-prompt";
      prompt.textContent = question.question;
      const value = document.createElement("div");
      value.className = "chat-plan-question-answer-value";
      value.textContent = answer?.text || question.options[Number(answer?.optionIndex)] || "No answer";
      row.append(prompt, value);
      resolved.appendChild(row);
    }
  }
  item.body.replaceChildren(resolved);
  item.details.open = false;
}

function formatStructured(value) {
  if (value == null) return "";
  if (typeof value !== "string") {
    try { return JSON.stringify(value, null, 2); } catch { return String(value); }
  }
  const trimmed = value.trim();
  if (!trimmed) return "";
  try { return JSON.stringify(JSON.parse(trimmed), null, 2); } catch { return value; }
}

function finishStream(stream, outcome, message = "") {
  if (!stream || stream.done) return;
  stream.done = true;
  stream.el.classList.remove("is-streaming");
  clearResearchAgentStatuses(stream);
  for (const turn of stream.turns.values()) {
    completeReasoning(turn);
    flushTurnTextBlock(turn);
  }
  if (!stream.finalTurn && stream.currentTurn) {
    promoteFinalText(stream, stream.currentTurn);
    stream.finalTurn = stream.currentTurn;
  }
  if (outcome === "done") {
    finalizeSuccessfulResponse(stream);
  } else {
    markRunningToolsInterrupted(stream, outcome);
    appendStreamStatus(stream, outcome, message);
  }
  renderFileChangeSummary(stream);
  if (activeStream === stream) {
    activeStream = null;
    setStreaming(false);
  }
}

function finalizeSuccessfulResponse(stream) {
  for (const turn of stream.turns.values()) removeEmptyTurn(turn);
  for (const details of stream.timeline.querySelectorAll("details")) details.open = false;
  const questionItems = [...stream.timeline.children].filter((child) => child.classList.contains("chat-plan-question-item"));
  for (const item of questionItems) {
    stream.content.parentElement.insertBefore(item, stream.content);
  }
  const pluginApprovals = [...stream.timeline.children].filter((child) => child.classList.contains("chat-plugin-approval-card"));
  for (const card of pluginApprovals) stream.content.parentElement.insertBefore(card, stream.content);
  if (!stream.timeline.childElementCount) {
    stream.timeline.remove();
    return;
  }
  const work = document.createElement("details");
  work.className = "chat-work-disclosure";
  const summary = document.createElement("summary");
  summary.textContent = "Show work";
  const body = document.createElement("div");
  body.className = "chat-work-content";
  body.appendChild(stream.timeline);
  work.append(summary, body);
  stream.content.parentElement.insertBefore(work, stream.content);
}

function recordFileChanges(stream, changes) {
  if (!stream || !Array.isArray(changes)) return;
  for (const change of changes) {
    const path = String(change?.path || "").trim();
    const operation = String(change?.operation || "").trim().toLowerCase();
    if (!path || !["created", "edited", "deleted"].includes(operation)) continue;
    const rootId = String(change?.ref?.rootId || "").trim();
    const refPath = typeof change?.ref?.path === "string" ? change.ref.path : "";
    stream.fileChanges.push({
      path,
      operation,
      ...(rootId ? { ref: { rootId, path: refPath } } : {}),
    });
  }
}

function aggregateFileChanges(changes) {
  const byPath = new Map();
  for (const change of changes || []) {
    let aggregate = byPath.get(change.path);
    if (!aggregate) {
      aggregate = {
        path: change.path,
        ref: change.ref || null,
        initiallyExisted: change.operation !== "created",
        exists: change.operation !== "deleted",
      };
      byPath.set(change.path, aggregate);
      continue;
    }
    aggregate.exists = change.operation !== "deleted";
    if (change.ref) aggregate.ref = change.ref;
  }
  return [...byPath.values()].map((change) => ({
    path: change.path,
    ref: change.ref,
    operation: !change.exists ? "deleted" : (change.initiallyExisted ? "edited" : "created"),
  }));
}

function renderFileChangeSummary(stream) {
  stream.el.querySelector(":scope .chat-file-changes")?.remove();
  const changes = aggregateFileChanges(stream.fileChanges);
  if (!changes.length) return;

  const section = document.createElement("section");
  section.className = "chat-file-changes";
  section.setAttribute("aria-label", "Files changed");
  const header = document.createElement("div");
  header.className = "chat-file-changes-header";
  const title = document.createElement("strong");
  title.textContent = "Files changed";
  const count = document.createElement("span");
  count.textContent = `${changes.length} ${changes.length === 1 ? "file" : "files"}`;
  header.append(title, count);

  const list = document.createElement("ul");
  list.className = "chat-file-change-list";
  for (const change of changes) {
    const item = document.createElement("li");
    const canOpen = change.operation !== "deleted" && change.ref && typeof binding?.onActivateFile === "function";
    const row = document.createElement(canOpen ? "button" : "div");
    row.className = `chat-file-change-row is-${change.operation}`;
    if (canOpen) {
      row.type = "button";
      row.title = `Open ${change.path} in Echo Code`;
      row.addEventListener("click", async () => {
        try {
          await binding.onActivateFile(change.ref);
        } catch (error) {
          toast(error instanceof Error ? error.message : "Could not open the changed file.", { sticky: true });
        }
      });
    } else if (change.operation === "deleted") {
      row.setAttribute("aria-disabled", "true");
      row.title = `${change.path} was deleted`;
    }
    const icon = document.createElement("span");
    icon.className = `codicon codicon-${change.operation === "deleted" ? "trash" : "file-code"}`;
    icon.setAttribute("aria-hidden", "true");
    const path = document.createElement("code");
    path.className = "chat-file-change-path";
    path.textContent = change.path;
    const badge = document.createElement("span");
    badge.className = "chat-file-change-operation";
    badge.textContent = change.operation === "created" ? "Created" : change.operation === "edited" ? "Edited" : "Deleted";
    row.append(icon, path, badge);
    item.appendChild(row);
    list.appendChild(item);
  }
  section.append(header, list);
  stream.content.parentElement.appendChild(section);
}

function removeEmptyTurn(turn) { if (turn?.el && !turn.el.childElementCount) turn.el.remove(); }

function cancelBindingMarkdownPatches() {
  if (!binding?.log) return;
  for (const element of binding.log.querySelectorAll(".chat-progress-text")) {
    cancelMarkdownPatch(element);
  }
}

function markRunningToolsInterrupted(stream, outcome) {
  for (const item of stream.tools.values()) {
    if (!item.details.classList.contains("is-running")) continue;
    item.details.classList.remove("is-running", "is-awaiting-input");
    item.details.classList.add("is-error");
    item.status.textContent = outcome === "stopped" ? "Stopped" : "Interrupted";
    const message = outcome === "stopped" ? "Tool execution was stopped." : "Tool execution was interrupted.";
    if (item.planQuestions) {
      const note = document.createElement("p");
      note.className = "chat-plan-question-error";
      note.textContent = message;
      item.body.replaceChildren(note);
    } else {
      item.result.textContent = message;
    }
  }
}

function appendStreamStatus(stream, outcome, message) {
  stream.timeline.hidden = false;
  const status = document.createElement("div");
  status.className = `chat-stream-status is-${outcome}`;
  status.textContent = message || (outcome === "stopped" ? "Response stopped." : "The response failed.");
  stream.timeline.appendChild(status);
}
