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
// collapse both thinking and tool activity into a single progress line pinned
// at the bottom of the assistant message, showing the latest line of what is
// happening (chronologically) while the answer prints above it. It has a
// subtle "breathing" pulse while the model is working. Once complete it stops
// breathing, dims, and stays on screen as a clickable summary that reveals the
// full activity log.
//
// The server emits events in chronological order: reasoning, then tool_call /
// tool_result for each tool, then (possibly more reasoning) and finally the
// answer tokens. The progress line updates to reflect whichever is current.

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
    // The answer content prints above, and a single collapsed progress line
    // sits at the bottom showing the latest thinking/tool activity as it
    // happens (chronologically). It stays hidden until there is activity, and
    // remains on screen after completion — clickable to reveal the full log.
    const content = document.createElement("div");
    content.className = "chat-message-content";
    content.textContent = text;
    body.appendChild(content);

    const progress = document.createElement("div");
    progress.className = "chat-progress-line";
    progress.hidden = true;
    progress.setAttribute("role", "button");
    progress.tabIndex = 0;
    body.appendChild(progress);

    // Hidden log of all activity, revealed when the progress line is clicked.
    const activityLog = document.createElement("div");
    activityLog.className = "chat-activity-log";
    activityLog.hidden = true;
    body.appendChild(activityLog);
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
 * @returns {{el: HTMLElement, progress: HTMLElement, content: HTMLElement, activityLog: HTMLElement}}
 */
function createStreamingMessage(log) {
  const el = createMessageEl("assistant", "");
  const progress = el.querySelector(".chat-progress-line");
  const content = el.querySelector(".chat-message-content");
  const activityLog = el.querySelector(".chat-activity-log");
  el.classList.add("is-streaming");
  log.appendChild(el);
  scrollToBottom(log);
  return { el, progress, content, activityLog };
}

/**
 * Send a chat message and stream the assistant reply into the log.
 * @param {HTMLElement} log
 * @param {string} text
 * @param {string} [model] optional model name to route this prompt to.
 */
export function sendMessage(log, text, model) {
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
  const { el, progress, content, activityLog } = createStreamingMessage(log);
  const stream = {
    el, progress, content, activityLog,
    done: false, answer: "", thinking: "",
    activity: [], // chronological entries for the reveal-on-click log
  };
  activeStream = stream;

  const unsubscribe = ws.on("chat_event", (data) => {
    if (data.eventType === "token") {
      // The final answer prints above the progress line, which stays pinned at
      // the bottom of the message.
      stream.answer += data.content || "";
      content.textContent = stream.answer;
      scrollToBottom(log);
    } else if (data.eventType === "reasoning") {
      stream.thinking += data.content || "";
      // Show the last non-empty line of thinking as the current progress line.
      setProgress(stream, lastLine(stream.thinking), "thinking");
      // Accumulate the full thinking text for the reveal-on-click log.
      stream.activity.push({ kind: "thinking", text: data.content || "" });
      scrollToBottom(log);
    } else if (data.eventType === "tool_call" || data.eventType === "tool_result") {
      // Collapse tool activity into the same progress line (chronological).
      const tool = data.tool || "tool";
      if (data.eventType === "tool_call") {
        setProgress(stream, `🔧 ${tool}…`, "tool");
        stream.activity.push({ kind: "tool_call", text: `🔧 ${tool}` });
      } else {
        setProgress(stream, `✓ ${tool} completed`, "tool-done");
        stream.activity.push({ kind: "tool_result", text: `✓ ${tool} completed` });
      }
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
  ws.send({ type: "chat", message: text, ...(model ? { model } : {}) });
}

/**
 * Update the collapsed progress line to show the given text. Reveals it if
 * hidden and applies a "breathing" pulse while activity is ongoing.
 * @param {{progress: HTMLElement}} stream
 * @param {string} text
 * @param {"thinking"|"tool"|"tool-done"} kind
 */
function setProgress(stream, text, kind) {
  if (stream.progress.hidden) stream.progress.hidden = false;
  stream.progress.textContent = text;
  stream.progress.dataset.kind = kind;
  // Re-trigger the breathing animation so each update pulses visibly.
  stream.progress.classList.remove("is-active");
  void stream.progress.offsetWidth; // force reflow to restart the animation
  stream.progress.classList.add("is-active");
}

/**
 * Finalize the progress line once streaming completes: stop breathing and make
 * it a static, dimmed, clickable summary that reveals the activity log.
 * @param {{progress: HTMLElement, activityLog: HTMLElement, activity: Array}} stream
 */
function completeProgress(stream) {
  stream.progress.classList.remove("is-active");
  stream.progress.classList.add("is-complete");
  // Rebuild the reveal-on-click activity log from the accumulated entries.
  stream.activityLog.textContent = "";
  for (const entry of stream.activity) {
    const line = document.createElement("div");
    line.className = `chat-activity-line chat-activity-line-${entry.kind}`;
    line.textContent = entry.text;
    stream.activityLog.appendChild(line);
  }
  // Toggle the log open/closed on click (and keyboard Enter/Space).
  const toggle = () => {
    const willOpen = stream.activityLog.hidden;
    stream.activityLog.hidden = !willOpen;
    stream.progress.classList.toggle("is-open", willOpen);
  };
  stream.progress.addEventListener("click", toggle);
  stream.progress.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      toggle();
    }
  });
}

/**
 * Return the last non-empty line of a multi-line string, or the whole string.
 * @param {string} text
 * @returns {string}
 */
function lastLine(text) {
  const lines = text.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  return lines.length ? lines[lines.length - 1] : text.trim();
}

function finishStream(stream, log, ...unsubs) {
  if (stream.done) return;
  stream.done = true;
  for (const unsub of unsubs) unsub();
  stream.el?.classList.remove("is-streaming");
  // Keep the progress line on screen but stop it breathing once done.
  completeProgress(stream);
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
