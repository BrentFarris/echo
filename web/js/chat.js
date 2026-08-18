// chat.js — workspace-scoped shared chat state and rendering.

import * as ws from "./ws.js";
import {
  cancelMarkdownPatch,
  flushMarkdownPatch,
  patchMarkdownElement,
  queueMarkdownPatch,
} from "../src/markdown.ts";

let binding = null;
let activeStream = null;
const streamingListeners = new Set();
const workspaceListeners = new Set();
const commandErrorListeners = new Set();

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

export function canClearChat(log) {
  return Boolean(
    binding?.log === log
    && binding.workspaceId
    && binding.hasSnapshot
    && binding.turns.size > 0
    && activeStream == null,
  );
}

export function clearChat(log) {
  if (!canClearChat(log)) return false;
  return ws.send({ type: "chat_clear", workspaceId: binding.workspaceId, chatId: binding.activeChatId });
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

export function openWorkspaceSession(log, workspaceId) {
  cancelBindingMarkdownPatches();
  activeStream = null;
  setStreaming(false);
  binding = {
    log, workspaceId: workspaceId || "", sequence: 0, hasSnapshot: false,
    activeChatId: "", tabs: [], turns: new Map(),
  };
  emitWorkspaceState();
  renderEmpty(log, workspaceId ? "Loading conversation…" : "Select a workspace to start chatting.");
  if (workspaceId) ws.send({ type: "session_subscribe", workspaceId });
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
    ws.send({ type: "session_subscribe", workspaceId: binding.workspaceId });
  }
});

ws.on("session_snapshot", (snapshot) => {
  if (!binding || snapshot.workspaceId !== binding.workspaceId) return;
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
  scrollToBottom(binding.log);
});

ws.on("session_event", (message) => {
  if (!binding || message.workspaceId !== binding.workspaceId) return;
  const sequence = Number(message.sequence) || 0;
  if (sequence <= binding.sequence) return;
  if (!binding.hasSnapshot) {
    ws.send({ type: "session_subscribe", workspaceId: binding.workspaceId });
    return;
  }
  if (binding.sequence && sequence !== binding.sequence + 1) {
    ws.send({ type: "session_subscribe", workspaceId: binding.workspaceId });
    return;
  }
  binding.sequence = sequence;
  const event = message.event || {};
  const chatId = message.chatId || binding.activeChatId;
  const tab = binding.tabs.find((candidate) => candidate.chatId === chatId);
  if (tab && event.type === "turn_started") {
    tab.preview = normalizePreview(event.message) || "New chat";
    tab.busy = true;
  } else if (tab && event.type === "turn_finished") {
    tab.busy = false;
  }
  emitWorkspaceState();
  if (chatId === binding.activeChatId) {
    applyEvent(event);
    scrollToBottom(binding.log);
  }
});

ws.on("command_error", (message) => {
  if (!binding || (message.workspaceId && message.workspaceId !== binding.workspaceId)) return;
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
  scrollToBottom(binding.log);
});

function normalizePreview(value) { return String(value || "").trim().replace(/\s+/g, " "); }

function applyEvent(event) {
  switch (event.type) {
    case "turn_started": {
      binding.log.querySelector(".chat-empty")?.remove();
      const stream = createTurnView(event.turnId, event.message || "");
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
      completeToolCall(findStream(event.turnId), event, event.turn);
      break;
    case "turn_finished": {
      const stream = findStream(event.turnId);
      finishStream(stream, event.status || "error", event.error || "");
      break;
    }
  }
}

function findStream(turnId) { return binding?.turns.get(turnId) || null; }

export function sendMessage(log, text, model, agentModeId) {
  text = text.trim();
  if (!text || activeStream || !binding?.workspaceId || !binding.activeChatId || binding.log !== log) return false;
  const requestId = globalThis.crypto?.randomUUID?.() || `request-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return ws.send({
    type: "chat_send", workspaceId: binding.workspaceId, chatId: binding.activeChatId, requestId,
    message: text, ...(model ? { model } : {}), ...(agentModeId ? { agentModeId } : {}),
  });
}

export function stopStream() {
  if (binding?.workspaceId && activeStream) {
    ws.send({ type: "chat_stop", workspaceId: binding.workspaceId, chatId: binding.activeChatId });
  }
}

function renderStoredTurn(turn, active) {
  const stream = createTurnView(turn.id, turn.userContent || "");
  binding.turns.set(turn.id, stream);
  for (const assistant of turn.assistantTurns || []) {
    startTurn(stream, assistant.number);
    appendReasoning(stream, assistant.number, assistant.reasoning || "");
    appendTurnText(stream, assistant.number, assistant.content || "");
    endTurn(stream, assistant.number, Boolean(assistant.hasToolCalls));
    for (const tool of assistant.tools || []) {
      const data = {
        turn: assistant.number, callId: tool.callId, callOrder: tool.callOrder,
        tool: tool.name, arguments: tool.arguments || "",
      };
      appendToolCall(stream, data, assistant.number);
      if (tool.status !== "running") {
        completeToolCall(stream, { ...data, success: tool.success, content: tool.result || "" }, assistant.number);
      }
    }
  }
  if (active || turn.status === "streaming") {
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

function createMessageEl(role, text) {
  const el = document.createElement("div");
  el.className = `chat-message chat-message-${role}`;
  el.dataset.role = role;
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
    const content = document.createElement("div");
    content.className = "chat-message-content markdown-body";
    patchMarkdownElement(content, text);
    body.appendChild(content);
  }
  return el;
}

function createTurnView(turnId, userText) {
  binding.log.appendChild(createMessageEl("user", userText));
  const el = createMessageEl("assistant", "");
  el.classList.add("is-streaming");
  el.dataset.turnId = turnId;
  binding.log.appendChild(el);
  return {
    id: turnId, el, timeline: el.querySelector(".chat-timeline"),
    content: el.querySelector(".chat-final-content"), done: false,
    currentTurn: null, finalTurn: null, turns: new Map(), tools: new Map(),
  };
}

function scrollToBottom(log) {
  requestAnimationFrame(() => { log.scrollTop = log.scrollHeight; });
}

function startTurn(stream, number) {
  if (!stream) return;
  stream.currentTurn = ensureTurn(stream, Number.isInteger(number) ? number : 0);
}

function ensureTurn(stream, number) {
  let turn = stream.turns.get(number);
  if (turn) return turn;
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
  const log = binding?.log;
  queueMarkdownPatch(turn.textBlock, turn.textBlockText, () => {
    if (log && stream.el.isConnected && stream.el.parentElement === log) scrollToBottom(log);
  });
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

function completeReasoning(turn) {
  const reasoning = turn?.reasoning;
  if (!reasoning || reasoning.complete) return;
  reasoning.complete = true;
  reasoning.label.textContent = "Thinking";
  reasoning.details.classList.remove("is-running");
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
  const item = createToolItem(data.tool || "tool", data.arguments || "");
  item.details.dataset.callId = callId;
  stream.timeline.hidden = false;
  stream.timeline.appendChild(item.details);
  stream.tools.set(callId, item);
}

function createToolItem(toolName, args) {
  const details = document.createElement("details");
  details.className = "chat-activity-item chat-tool-item is-running";
  const summary = document.createElement("summary");
  const name = document.createElement("span");
  name.className = "chat-activity-name";
  name.textContent = toolName;
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
  return { details, status, result: result.content };
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
  const callOrder = Number.isInteger(data.callOrder) ? data.callOrder : stream.tools.size;
  const callId = data.callId || `turn-${turnNumber}-call-${callOrder}`;
  if (!stream.tools.has(callId)) appendToolCall(stream, { ...data, callId }, turnNumber);
  const item = stream.tools.get(callId);
  if (!item) return;
  const succeeded = data.success === true;
  item.details.classList.remove("is-running");
  item.details.classList.add(succeeded ? "is-success" : "is-error");
  item.status.textContent = succeeded ? "Completed" : "Failed";
  item.result.textContent = formatStructured(data.content) || "No result";
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
  if (activeStream === stream) {
    activeStream = null;
    setStreaming(false);
  }
}

function finalizeSuccessfulResponse(stream) {
  for (const turn of stream.turns.values()) removeEmptyTurn(turn);
  for (const details of stream.timeline.querySelectorAll("details")) details.open = false;
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
    item.details.classList.remove("is-running");
    item.details.classList.add("is-error");
    item.status.textContent = outcome === "stopped" ? "Stopped" : "Interrupted";
    item.result.textContent = outcome === "stopped" ? "Tool execution was stopped." : "Tool execution was interrupted.";
  }
}

function appendStreamStatus(stream, outcome, message) {
  stream.timeline.hidden = false;
  const status = document.createElement("div");
  status.className = `chat-stream-status is-${outcome}`;
  status.textContent = message || (outcome === "stopped" ? "Response stopped." : "The response failed.");
  stream.timeline.appendChild(status);
}
