let host = null;
let sequence = 0;
const pending = new Map();
const result = document.querySelector("#result");

function request(method, params = {}) {
  if (!host) return Promise.reject(new Error("Echo has not initialized the plugin bridge."));
  const id = `showcase-${++sequence}`;
  parent.postMessage({
    type: "echo-plugin-request",
    nonce: host.nonce,
    pluginId: host.pluginId,
    viewId: host.viewId,
    id,
    method,
    params,
  }, "*");
  return new Promise((resolve, reject) => pending.set(id, { resolve, reject }));
}

function show(value) {
  result.textContent = typeof value === "string" ? value : JSON.stringify(value, null, 2);
}

addEventListener("message", event => {
  if (event.source !== parent || !event.data || typeof event.data !== "object") return;
  const message = event.data;
  if (message.type === "echo-plugin-init" && message.protocol === "echo-ui-bridge-1") {
    host = message;
    for (const [key, value] of Object.entries(message.theme || {})) document.documentElement.style.setProperty(key, value);
    document.querySelector("#plugin").textContent = `${message.pluginId} / ${message.viewId}`;
    document.querySelector("#workspace").textContent = message.workspaceId || "Global context";
    document.querySelector("#display-name").textContent = message.config?.["display-name"] || "Echo developer";
    show("Bridge initialized. No secret values were sent to this iframe.");
    return;
  }
  if (!host || message.nonce !== host.nonce || message.pluginId !== host.pluginId || message.viewId !== host.viewId) return;
  if (message.type === "echo-plugin-theme") {
    for (const [key, value] of Object.entries(message.theme || {})) document.documentElement.style.setProperty(key, value);
  } else if (message.type === "echo-plugin-response") {
    const call = pending.get(message.id);
    if (!call) return;
    pending.delete(message.id);
    message.error ? call.reject(new Error(message.error)) : call.resolve(message.result);
  } else if (message.type === "echo-plugin-event") {
    show({ topic: message.topic, data: message.data });
  }
});

async function run(action) {
  try { show(await action()); } catch (error) { show(error instanceof Error ? error.message : String(error)); }
}

document.querySelector("#ping").addEventListener("click", () => run(() => request("rpc.invoke", { method: "showcase.ping", params: { sentAt: new Date().toISOString() } })));
document.querySelector("#event").addEventListener("click", () => run(() => request("rpc.invoke", { method: "showcase.emit", params: { message: "Hello from the isolated showcase view" } })));
document.querySelector("#remember").addEventListener("click", () => run(async () => {
  const value = new Date().toISOString();
  await request("storage.set", { scope: host.workspaceId ? "workspace" : "global", key: "last-visit", value });
  return { stored: true, value };
}));
document.querySelector("#notify").addEventListener("click", () => run(() => request("notification.show", { message: "The showcase asked Echo to display this notification." })));

parent.postMessage({ type: "echo-plugin-ready", protocol: "echo-ui-bridge-1" }, "*");
