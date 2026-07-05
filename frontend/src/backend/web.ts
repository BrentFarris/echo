import { services } from "../../wailsjs/go/models";

const tokenStorageKey = "echo.webAccessToken";

let eventSource: EventSource | null = null;
let eventSourceWaitingForPageLoad = false;
const eventCallbacks = new Map<string, Set<(event: unknown) => void>>();
const eventHandlers = new Map<string, (event: MessageEvent) => void>();

export function isWailsRuntime(): boolean {
  return Boolean((window as unknown as { go?: unknown }).go);
}

export function initializeWebAccessTokenFromURL() {
  if (isWailsRuntime() || !window.location.hash.includes("token=")) {
    return;
  }
  const hash = new URLSearchParams(window.location.hash.slice(1));
  const token = hash.get("token")?.trim();
  if (!token) {
    return;
  }
  window.localStorage.setItem(tokenStorageKey, token);
  window.history.replaceState(null, document.title, window.location.pathname + window.location.search);
}

export async function webRpc<T>(method: string, args: unknown[], retry = true): Promise<T> {
  const token = webAccessToken(true);
  const response = await fetch(`/api/rpc/SystemService/${encodeURIComponent(method)}`, {
    method: "POST",
    headers: {
      "Authorization": `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ args }),
  });

  if (response.status === 401 && retry) {
    clearWebAccessToken();
    resetWebEventSource();
    return webRpc<T>(method, args, false);
  }

  let payload: { result?: T; error?: { message?: string } } = {};
  try {
    payload = await response.json();
  } catch {
    payload = {};
  }
  if (!response.ok || payload.error) {
    throw new Error(payload.error?.message || `Request failed with status ${response.status}`);
  }
  return payload.result as T;
}

export function webEventsOn(eventName: string, callback: (event: unknown) => void) {
  const callbacks = eventCallbacks.get(eventName) ?? new Set<(event: unknown) => void>();
  callbacks.add(callback);
  eventCallbacks.set(eventName, callbacks);
  ensureWebEventSource();
  ensureWebEventHandler(eventName);
  return () => {
    callbacks.delete(callback);
    if (callbacks.size === 0) {
      eventCallbacks.delete(eventName);
    }
  };
}

export async function chooseWorkspaceFolderWeb(): Promise<services.AppState> {
  const path = window.prompt("Enter the desktop folder path to add as a workspace.");
  if (!path?.trim()) {
    return webRpc<services.AppState>("LoadState", []);
  }
  return webRpc<services.AppState>("AddWorkspace", [path.trim()]);
}

export async function chooseWorkspaceFolderForWorkspaceWeb(workspaceID: string): Promise<services.AppState> {
  const path = window.prompt("Enter the desktop folder path to add to this workspace.");
  if (!path?.trim()) {
    return webRpc<services.AppState>("LoadState", []);
  }
  return webRpc<services.AppState>("AddWorkspaceFolder", [workspaceID, path.trim()]);
}

export async function chooseWorkspaceIconWeb(workspaceID: string): Promise<services.AppState> {
  const choice = window.prompt(
    "Enter a desktop image path, or type upload to choose an image from this browser.",
    "upload",
  );
  if (!choice?.trim()) {
    return webRpc<services.AppState>("LoadState", []);
  }
  if (choice.trim().toLowerCase() !== "upload") {
    return webRpc<services.AppState>("SetWorkspaceIconFromPath", [workspaceID, choice.trim()]);
  }

  const file = await selectImageFile();
  if (!file) {
    return webRpc<services.AppState>("LoadState", []);
  }
  const dataUrl = await fileToDataURL(file);
  return webRpc<services.AppState>("SetWorkspaceIconFromUpload", [
    workspaceID,
    services.WorkspaceIconInput.createFrom({
      name: file.name,
      mediaType: file.type,
      dataUrl,
      bytes: file.size,
    }),
  ]);
}

export async function chooseWorkspaceFileSavePathWeb(
  workspaceID: string,
  suggestedName: string,
): Promise<string> {
  const state = await webRpc<services.AppState>("LoadState", []);
  const workspace = (state.workspaces ?? []).find(
    (candidate) => candidate.id === workspaceID,
  );
  const folder = (workspace?.folders ?? []).find((candidate) => !candidate.missing);
  const defaultPath = folder?.label
    ? `${folder.label}/${suggestedName}`
    : suggestedName;
  return window.prompt(
    "Enter a workspace-relative save path (for example, workspace/notes.txt).",
    defaultPath,
  )?.trim() ?? "";
}

function webAccessToken(promptIfMissing: boolean): string {
  const existing = window.localStorage.getItem(tokenStorageKey)?.trim();
  if (existing) {
    return existing;
  }
  if (!promptIfMissing) {
    return "";
  }
  const token = window.prompt("Enter the Echo web access token.")?.trim();
  if (!token) {
    throw new Error("Web access token is required.");
  }
  window.localStorage.setItem(tokenStorageKey, token);
  return token;
}

function clearWebAccessToken() {
  window.localStorage.removeItem(tokenStorageKey);
}

function ensureWebEventSource() {
  if (eventSource || eventCallbacks.size === 0) {
    return;
  }
  // Opening a long-lived SSE request while the module script is still
  // evaluating can keep mobile browsers' initial navigation pending.
  // The RPC-backed initial render does not depend on runtime events, so
  // connect immediately after the document has completed loading instead.
  if (document.readyState !== "complete") {
    if (!eventSourceWaitingForPageLoad) {
      eventSourceWaitingForPageLoad = true;
      window.addEventListener("load", () => {
        eventSourceWaitingForPageLoad = false;
        // Start in a new task so the load event can fully settle before the
        // browser sees the long-lived event-stream request.
        window.setTimeout(ensureWebEventSource, 0);
      }, { once: true });
    }
    return;
  }
  let token = "";
  try {
    token = webAccessToken(true);
  } catch {
    return;
  }
  eventSource = new EventSource(`/api/events?access_token=${encodeURIComponent(token)}`);
  eventSource.onerror = () => {
    if (eventSource?.readyState === EventSource.CLOSED) {
      eventSource = null;
    }
  };
  for (const eventName of eventCallbacks.keys()) {
    ensureWebEventHandler(eventName);
  }
}

function ensureWebEventHandler(eventName: string) {
  if (!eventSource || eventHandlers.has(eventName)) {
    return;
  }
  const handler = (event: MessageEvent) => {
    const callbacks = eventCallbacks.get(eventName);
    if (!callbacks?.size) {
      return;
    }
    let payload: unknown = event.data;
    try {
      payload = JSON.parse(event.data);
    } catch {
    }
    callbacks.forEach((callback) => callback(payload));
  };
  eventHandlers.set(eventName, handler);
  eventSource.addEventListener(eventName, handler);
}

function resetWebEventSource() {
  if (eventSource) {
    eventSource.close();
    eventSource = null;
  }
  eventHandlers.clear();
  ensureWebEventSource();
}

function selectImageFile(): Promise<File | null> {
  return new Promise((resolve) => {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = "image/png,image/jpeg,image/gif,image/webp";
    input.style.position = "fixed";
    input.style.left = "-9999px";
    input.addEventListener("change", () => {
      const file = input.files?.[0] ?? null;
      input.remove();
      resolve(file);
    }, { once: true });
    document.body.appendChild(input);
    input.click();
  });
}

function fileToDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.addEventListener("load", () => resolve(String(reader.result ?? "")), { once: true });
    reader.addEventListener("error", () => reject(reader.error ?? new Error("Could not read image.")), { once: true });
    reader.readAsDataURL(file);
  });
}
