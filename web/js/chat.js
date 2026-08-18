// chat.js — chat view logic: send messages over WebSocket and render the
// assistant's streamed work as a chronological, inline timeline.

import * as ws from "./ws.js";

let activeStream = null;
const streamingListeners = new Set();

function setStreaming(streaming) {
  for (const cb of streamingListeners) {
    try {
      cb(streaming);
    } catch (err) {
      console.error("streaming state handler error:", err);
    }
  }
}

/**
 * Subscribe to streaming-state changes. The handler is invoked immediately
 * with the current state, then on every transition.
 * @param {(streaming: boolean) => void} cb
 * @returns {() => void}
 */
export function onStreamingChange(cb) {
  streamingListeners.add(cb);
  try {
    cb(activeStream != null);
  } catch (err) {
    console.error("streaming state handler error:", err);
  }
  return () => streamingListeners.delete(cb);
}

export function isStreaming() {
  return activeStream != null;
}

/**
 * Build the DOM for a single message row.
 * @param {"user"|"assistant"} role
 * @param {string} text
 * @returns {HTMLElement}
 */
function createMessageEl(role, text) {
  const el = document.createElement("div");
  el.className = `chat-message chat-message-${role}`;
  el.setAttribute("data-role", role);

  const body = document.createElement("div");
  body.className = "chat-message-body";
  el.appendChild(body);

  if (role === "assistant") {
    const timeline = document.createElement("div");
    timeline.className = "chat-timeline";
    timeline.hidden = true;
    body.appendChild(timeline);

    const content = document.createElement("div");
    content.className = "chat-message-content chat-final-content";
    content.textContent = text;
    content.hidden = text === "";
    body.appendChild(content);
  } else {
    const content = document.createElement("div");
    content.className = "chat-message-content";
    content.textContent = text;
    body.appendChild(content);
  }

  return el;
}

function appendMessage(log, role, text) {
  const el = createMessageEl(role, text);
  log.appendChild(el);
  scrollToBottom(log);
  return el;
}

function scrollToBottom(log) {
  requestAnimationFrame(() => {
    log.scrollTop = log.scrollHeight;
  });
}

function createStreamingMessage(log) {
  const el = createMessageEl("assistant", "");
  const timeline = el.querySelector(".chat-timeline");
  const content = el.querySelector(".chat-final-content");
  el.classList.add("is-streaming");
  log.appendChild(el);
  scrollToBottom(log);
  return { el, timeline, content };
}

/**
 * Send a chat message and stream the assistant reply into the log.
 * @param {HTMLElement} log
 * @param {string} text
 * @param {string} [model]
 */
export function sendMessage(log, text, model) {
  text = text.trim();
  if (!text || activeStream) return;

  log.querySelector(".chat-empty")?.remove();
  appendMessage(log, "user", text);

  const { el, timeline, content } = createStreamingMessage(log);
  const stream = {
    el,
    timeline,
    content,
    done: false,
    currentTurn: null,
    finalTurn: null,
    turns: new Map(),
    tools: new Map(),
    unsubs: [],
  };
  activeStream = stream;
  setStreaming(true);

  stream.unsubs.push(ws.on("chat_event", (data) => {
    handleChatEvent(stream, data);
    scrollToBottom(log);
  }));
  stream.unsubs.push(ws.on("chat_done", () => finishStream(stream, log, "done")));
  stream.unsubs.push(ws.on("chat_error", (data) => {
    finishStream(stream, log, "error", data.error || "The response failed.");
  }));
  stream.unsubs.push(ws.on("chat_stopped", () => {
    finishStream(stream, log, "stopped", "Response stopped.");
  }));

  ws.send({ type: "chat", message: text, ...(model ? { model } : {}) });
}

function handleChatEvent(stream, data) {
  const turnNumber = Number.isInteger(data.turn)
    ? data.turn
    : (stream.currentTurn?.number ?? 0);

  switch (data.eventType) {
    case "assistant_turn_start":
      startTurn(stream, turnNumber);
      break;
    case "token":
      appendTurnText(stream, turnNumber, data.content || "");
      break;
    case "reasoning":
      appendReasoning(stream, turnNumber, data.content || "");
      break;
    case "assistant_turn_end":
      endTurn(stream, turnNumber, Boolean(data.hasToolCalls));
      break;
    case "tool_call":
      appendToolCall(stream, data, turnNumber);
      break;
    case "tool_result":
      completeToolCall(stream, data, turnNumber);
      break;
  }
}

function startTurn(stream, number) {
  const turn = ensureTurn(stream, number);
  stream.currentTurn = turn;
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
    number,
    el,
    text: "",
    lastKind: "",
    textBlock: null,
    reasoning: null,
  };
  stream.turns.set(number, turn);
  return turn;
}

function appendTurnText(stream, turnNumber, text) {
  if (!text) return;
  const turn = ensureTurn(stream, turnNumber);
  completeReasoning(turn);

  if (turn.lastKind !== "text" || !turn.textBlock) {
    turn.textBlock = document.createElement("div");
    turn.textBlock.className = "chat-progress-text";
    turn.el.appendChild(turn.textBlock);
  }
  turn.lastKind = "text";
  turn.text += text;
  turn.textBlock.textContent += text;
}

function appendReasoning(stream, turnNumber, text) {
  if (!text) return;
  const turn = ensureTurn(stream, turnNumber);

  if (turn.lastKind !== "reasoning" || !turn.reasoning) {
    completeReasoning(turn);
    turn.reasoning = createReasoningItem();
    turn.el.appendChild(turn.reasoning.details);
  }
  turn.lastKind = "reasoning";
  turn.textBlock = null;
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
  const turn = ensureTurn(stream, turnNumber);
  completeReasoning(turn);
  turn.lastKind = "";
  turn.textBlock = null;

  if (hasToolCalls) {
    turn.el.classList.add("is-progress-turn");
  } else {
    stream.finalTurn = turn;
    promoteFinalText(stream, turn);
  }
  if (stream.currentTurn === turn) stream.currentTurn = null;
}

function promoteFinalText(stream, turn) {
  stream.content.textContent = turn.text;
  stream.content.hidden = turn.text === "";
  for (const textBlock of turn.el.querySelectorAll(":scope > .chat-progress-text")) {
    textBlock.remove();
  }
  removeEmptyTurn(turn);
}

function appendToolCall(stream, data, turnNumber) {
  const callOrder = Number.isInteger(data.callOrder) ? data.callOrder : stream.tools.size;
  const callId = data.callId || `turn-${turnNumber}-call-${callOrder}`;
  const toolName = data.tool || "tool";
  const item = createToolItem(toolName, data.arguments || "");

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
  const resultSection = createToolSection("Result", "Waiting for result…");
  body.appendChild(resultSection.section);
  details.appendChild(body);

  return { details, status, result: resultSection.content };
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
  const callOrder = Number.isInteger(data.callOrder) ? data.callOrder : stream.tools.size;
  const callId = data.callId || `turn-${turnNumber}-call-${callOrder}`;
  let item = stream.tools.get(callId);
  if (!item) {
    appendToolCall(stream, { ...data, callId }, turnNumber);
    item = stream.tools.get(callId);
  }
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
    try {
      return JSON.stringify(value, null, 2);
    } catch {
      return String(value);
    }
  }

  const trimmed = value.trim();
  if (!trimmed) return "";
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2);
  } catch {
    return value;
  }
}

function finishStream(stream, log, outcome, message = "") {
  if (stream.done) return;
  stream.done = true;
  for (const unsub of stream.unsubs) unsub();
  stream.el.classList.remove("is-streaming");

  for (const turn of stream.turns.values()) completeReasoning(turn);

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
  scrollToBottom(log);
}

function finalizeSuccessfulResponse(stream) {
  for (const turn of stream.turns.values()) removeEmptyTurn(turn);
  for (const details of stream.timeline.querySelectorAll("details")) {
    details.open = false;
  }

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

function removeEmptyTurn(turn) {
  if (turn?.el && !turn.el.childElementCount) turn.el.remove();
}

function markRunningToolsInterrupted(stream, outcome) {
  for (const item of stream.tools.values()) {
    if (!item.details.classList.contains("is-running")) continue;
    item.details.classList.remove("is-running");
    item.details.classList.add("is-error");
    item.status.textContent = outcome === "stopped" ? "Stopped" : "Interrupted";
    item.result.textContent = outcome === "stopped"
      ? "Tool execution was stopped."
      : "Tool execution was interrupted by the response error.";
  }
}

function appendStreamStatus(stream, outcome, message) {
  stream.timeline.hidden = false;
  const status = document.createElement("div");
  status.className = `chat-stream-status is-${outcome}`;
  status.textContent = message || (outcome === "stopped" ? "Response stopped." : "The response failed.");
  stream.timeline.appendChild(status);
}

export function stopStream() {
  ws.send({ type: "stop" });
}
