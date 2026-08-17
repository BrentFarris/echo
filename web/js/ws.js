// ws.js — WebSocket client for the Echo real-time channel.
//
// Connects to /ws, auto-reconnects with exponential backoff, and dispatches
// inbound messages to registered handlers keyed by the message `type` field.

const listeners = new Map(); // type -> Set<handler>

let socket = null;
let shouldReconnect = true;
let reconnectDelay = 500; // ms, grows with backoff
const maxReconnectDelay = 15000;

const stateListeners = new Set();

// Tracks the latest known connection state so listeners that subscribe later
// (e.g. after a lazy view module loads) immediately learn the current state
// instead of missing the transition that already happened.
let currentState = "connecting";

/**
 * Register a handler for a WebSocket message type.
 * @param {string} type message `type` to listen for ("*" for all).
 * @param {(data: object) => void} handler
 * @returns {() => void} unsubscribe function.
 */
export function on(type, handler) {
  if (!listeners.has(type)) listeners.set(type, new Set());
  listeners.get(type).add(handler);
  return () => listeners.get(type)?.delete(handler);
}

/**
 * Subscribe to connection state changes. The handler is invoked immediately
 * with the current state, then on every subsequent change.
 * @param {(state: "connecting"|"open"|"closed") => void} handler
 * @returns {() => void} unsubscribe function.
 */
export function onState(handler) {
  stateListeners.add(handler);
  try {
    handler(currentState);
  } catch (err) {
    console.error("ws state handler error:", err);
  }
  return () => stateListeners.delete(handler);
}

function setState(state) {
  currentState = state;
  for (const h of stateListeners) {
    try {
      h(state);
    } catch (err) {
      console.error("ws state handler error:", err);
    }
  }
}

function dispatch(message) {
  const data = typeof message === "string" ? JSON.parse(message) : message;
  const type = data?.type;
  for (const h of listeners.get(type) || []) {
    try {
      h(data);
    } catch (err) {
      console.error(`ws handler error for "${type}":`, err);
    }
  }
  for (const h of listeners.get("*") || []) {
    try {
      h(data);
    } catch (err) {
      console.error("ws wildcard handler error:", err);
    }
  }
}

function connect() {
  if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) {
    return;
  }
  setState("connecting");

  const proto = location.protocol === "https:" ? "wss" : "ws";
  const url = `${proto}://${location.host}/ws`;
  socket = new WebSocket(url);

  socket.onopen = () => {
    reconnectDelay = 500;
    setState("open");
  };

  socket.onmessage = (event) => {
    try {
      dispatch(event.data);
    } catch (err) {
      console.error("ws dispatch error:", err);
    }
  };

  socket.onclose = () => {
    setState("closed");
    if (shouldReconnect) {
      setTimeout(connect, reconnectDelay);
      reconnectDelay = Math.min(reconnectDelay * 2, maxReconnectDelay);
    }
  };

  socket.onerror = () => {
    // onclose follows; closing there handles reconnect.
  };
}

/**
 * Establish the WebSocket connection.
 */
export function start() {
  shouldReconnect = true;
  connect();
}

/**
 * Close the WebSocket and stop reconnecting.
 */
export function stop() {
  shouldReconnect = false;
  socket?.close();
}
