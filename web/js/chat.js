// chat.js — chat view logic: send messages over WebSocket and stream the
// assistant's reply into the DOM incrementally (no full DOM recreation).
//
// The chat flows over the WebSocket channel:
//   client -> {type:"chat", message:"..."}
//   server -> {type:"chat_start", message}
//   server -> {type:"chat_event", eventType:"token"|"reasoning"|..., content}
//   server -> {type:"chat_done"}  (or {type:"chat_error", error})
//
// Reasoning models stream `reasoning` events BEFORE any `token` events. We
// render reasoning into a separate "thinking" block so the user sees activity
// immediately, and stream the final answer tokens into the main content area.
// Both are mutated incrementally — no DOM recreation during streaming.

import * as ws from "./ws.js";

// Current streaming state so we can stop an in-progress reply.
let activeStream = null;

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
    // Reasoning/thinking block, shown separately from the answer.
    const reasoning = document.createElement("div");
    reasoning.className = "chat-message-reasoning";
    reasoning.hidden = true;
    body.appendChild(reasoning);

    const content = document.createElement("div");
    content.className = "chat-message-content";
    content.textContent = text;
    body.appendChild(content);
  } else {
    const content = document.createElement("div");
    content.className = "chat-message-content";
    content.textContent = text;
    body.appendChild(content);
  }

  return el;
}

/**
 * Append a completed message to the log and scroll it into view.
 * @param {HTMLElement} log
 * @param {"user"|"assistant"} role
 * @param {string} text
 * @returns {HTMLElement} the message element
 */
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

/**
 * Create an assistant message element that will receive streamed reasoning and
 * answer tokens.
 * @param {HTMLElement} log
 * @returns {{el: HTMLElement, reasoning: HTMLElement, content: HTMLElement}}
 */
function createStreamingMessage(log) {
  const el = createMessageEl("assistant", "");
  const reasoning = el.querySelector(".chat-message-reasoning");
  const content = el.querySelector(".chat-message-content");
  el.classList.add("is-streaming");
  log.appendChild(el);
  scrollToBottom(log);
  return { el, reasoning, content };
}

/**
 * Send a chat message and stream the assistant reply into the log.
 * @param {HTMLElement} log
 * @param {string} text
 */
export function sendMessage(log, text) {
  text = text.trim();
  if (!text) return;

  // If a stream is already in progress, ignore the new send.
  if (activeStream) return;

  // Remove the empty-state placeholder on the first message.
  const empty = log.querySelector(".chat-empty");
  if (empty) empty.remove();

  // Render the user's message immediately.
  appendMessage(log, "user", text);

  // Create the assistant message we'll stream into.
  const { el, reasoning, content } = createStreamingMessage(log);
  const stream = { el, reasoning, content, done: false, answer: "", thinking: "" };
  activeStream = stream;

  const unsubscribe = ws.on("chat_event", (data) => {
    if (data.eventType === "token") {
      stream.answer += data.content || "";
      content.textContent = stream.answer;
      scrollToBottom(log);
    } else if (data.eventType === "reasoning") {
      stream.thinking += data.content || "";
      // Reveal the thinking block once there is content to show.
      if (stream.thinking && reasoning.hidden) reasoning.hidden = false;
      reasoning.textContent = stream.thinking;
      scrollToBottom(log);
    }
  });

  const doneUnsub = ws.on("chat_done", () => {
    finishStream(stream, log, unsubscribe, doneUnsub);
  });

  const errorUnsub = ws.on("chat_error", (data) => {
    stream.answer += (data.error ? `\n[error] ${data.error}` : "\n[error]");
    content.textContent = stream.answer;
    finishStream(stream, log, unsubscribe, doneUnsub, errorUnsub);
  });

  // Send the message over the WebSocket.
  ws.send({ type: "chat", message: text });
}

function finishStream(stream, log, ...unsubs) {
  if (stream.done) return;
  stream.done = true;
  for (const unsub of unsubs) unsub();
  stream.el?.classList.remove("is-streaming");
  if (activeStream === stream) activeStream = null;
  scrollToBottom(log);
}

/**
 * Stop the currently in-progress stream (if any). The server will eventually
 * send chat_done or chat_error, which finalizes the message.
 */
export function stopStream() {
  // There is currently no server-side cancel; the stream completes naturally.
  // This is a no-op placeholder until cancellation is wired up.
  activeStream = null;
}
