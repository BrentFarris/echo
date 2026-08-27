import { get } from "../js/api.js";
import { chatTargetRouteHash, type ChatTarget } from "./navigation";

export type ChatMapEntry = ChatTarget & {
  workspaceName: string;
  preview: string;
  lastActivityAt: string;
};

export type ChatMapWarning = {
  workspaceId: string;
  workspaceName: string;
  message: string;
};

export type ChatMapResponse = {
  chats: ChatMapEntry[];
  warnings: ChatMapWarning[];
};

export type ChatMapOptions = {
  navigate?: (target: ChatTarget) => void | boolean | Promise<void | boolean>;
  signal?: AbortSignal;
  onClose?: () => void;
};

let popupSequence = 0;
let closeActivePopup: (() => void) | null = null;

function normalizedResponse(value: unknown): ChatMapResponse {
  const response = value && typeof value === "object" ? value as Record<string, unknown> : {};
  const chats = Array.isArray(response.chats) ? response.chats.flatMap((candidate): ChatMapEntry[] => {
    if (!candidate || typeof candidate !== "object") return [];
    const entry = candidate as Record<string, unknown>;
    const surface = entry.surface === "code" ? "code" : entry.surface === "chat" ? "chat" : null;
    if (!surface || typeof entry.workspaceId !== "string" || typeof entry.chatId !== "string") return [];
    return [{
      workspaceId: entry.workspaceId,
      workspaceName: typeof entry.workspaceName === "string" ? entry.workspaceName : "Unknown workspace",
      chatId: entry.chatId,
      surface,
      preview: typeof entry.preview === "string" ? entry.preview : "Chat",
      lastActivityAt: typeof entry.lastActivityAt === "string" ? entry.lastActivityAt : "",
    }];
  }) : [];
  const warnings = Array.isArray(response.warnings) ? response.warnings.flatMap((candidate): ChatMapWarning[] => {
    if (!candidate || typeof candidate !== "object") return [];
    const warning = candidate as Record<string, unknown>;
    if (typeof warning.workspaceId !== "string") return [];
    return [{
      workspaceId: warning.workspaceId,
      workspaceName: typeof warning.workspaceName === "string" ? warning.workspaceName : "Unknown workspace",
      message: typeof warning.message === "string" ? warning.message : "Chat history is unavailable.",
    }];
  }) : [];
  return { chats, warnings };
}

function appendStatus(host: HTMLElement, message: string, className: string): void {
  const status = document.createElement("p");
  status.className = className;
  status.textContent = message;
  host.append(status);
}

function renderChats(host: HTMLElement, response: ChatMapResponse): void {
  host.replaceChildren();
  const list = document.createElement("div");
  list.className = "chat-map-list";
  list.dataset.chatMapList = "";
  if (!response.chats.length) {
    appendStatus(list, "No chats yet.", "chat-map-empty");
  } else {
    response.chats.forEach((chat, index) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "chat-map-item";
      button.setAttribute("role", "menuitem");
      button.dataset.chatMapIndex = String(index);
      const icon = document.createElement("span");
      icon.className = `codicon codicon-${chat.surface === "code" ? "code" : "comment-discussion"}`;
      icon.setAttribute("aria-hidden", "true");
      const content = document.createElement("span");
      content.className = "chat-map-item-content";
      const title = document.createElement("strong");
      title.textContent = chat.preview || (chat.surface === "code" ? "Code Chat" : "Chat");
      const detail = document.createElement("span");
      detail.textContent = `${chat.workspaceName || "Unknown workspace"} · ${chat.surface === "code" ? "Code Chat" : "Chat"}`;
      content.append(title, detail);
      button.append(icon, content);
      list.append(button);
    });
  }
  host.append(list);
  if (response.warnings.length) {
    const names = response.warnings.map((warning) => warning.workspaceName).filter(Boolean);
    const warning = document.createElement("p");
    warning.className = "chat-map-warning";
    warning.setAttribute("role", "status");
    warning.textContent = names.length
      ? `Some chat history could not be loaded: ${names.join(", ")}.`
      : "Some chat history could not be loaded.";
    host.append(warning);
  }
}

function renderError(host: HTMLElement, message: string): void {
  host.replaceChildren();
  const error = document.createElement("div");
  error.className = "chat-map-error";
  error.setAttribute("role", "alert");
  const text = document.createElement("p");
  text.textContent = message;
  const retry = document.createElement("button");
  retry.type = "button";
  retry.dataset.chatMapRetry = "";
  retry.textContent = "Retry";
  error.append(text, retry);
  host.append(error);
}

export function openChatMap(trigger: HTMLElement, options: ChatMapOptions = {}): () => void {
  closeActivePopup?.();
  const anchor = document.createElement("div");
  const popupID = `chat-map-${++popupSequence}`;
  anchor.className = "chat-map-anchor";
  anchor.id = popupID;
  anchor.innerHTML = `
    <section class="chat-map-popup" role="menu" aria-label="Chat Map">
      <header class="chat-map-header"><span class="codicon codicon-map" aria-hidden="true"></span><strong>Chat Map</strong></header>
      <div class="chat-map-content" data-chat-map-content><p class="chat-map-loading" role="status">Loading chats…</p></div>
    </section>
  `;
  document.body.append(anchor);
  trigger.setAttribute("aria-expanded", "true");
  trigger.setAttribute("aria-controls", popupID);
  const content = anchor.querySelector<HTMLElement>("[data-chat-map-content]")!;
  const request = new AbortController();
  let response: ChatMapResponse = { chats: [], warnings: [] };
  let closed = false;

  const position = () => {
    if (closed) return;
    const rect = trigger.getBoundingClientRect();
    const margin = 8;
    const gap = 8;
    const width = Math.min(anchor.offsetWidth || 360, window.innerWidth - margin * 2);
    const height = Math.min(anchor.offsetHeight || 420, window.innerHeight - margin * 2);
    const mobile = Boolean(trigger.closest(".mobile-bottom-nav"));
    const naturalLeft = mobile ? rect.right - width : rect.right + gap;
    const fallbackLeft = rect.left - width - gap;
    const left = Math.max(margin, Math.min(!mobile && naturalLeft + width > window.innerWidth - margin ? fallbackLeft : naturalLeft, window.innerWidth - width - margin));
    const naturalTop = mobile ? rect.top - height - gap : rect.bottom - height;
    const top = Math.max(margin, Math.min(naturalTop, window.innerHeight - height - margin));
    anchor.style.left = `${left}px`;
    anchor.style.top = `${top}px`;
    anchor.dataset.placement = mobile ? "above" : "right";
  };

  const focusableItems = () => [...anchor.querySelectorAll<HTMLButtonElement>('[role="menuitem"], [data-chat-map-retry]')]
    .filter((button) => !button.disabled);

  const load = async () => {
    content.innerHTML = '<p class="chat-map-loading" role="status">Loading chats…</p>';
    position();
    try {
      response = normalizedResponse(await get("/api/chats", { signal: request.signal }));
      if (closed) return;
      renderChats(content, response);
      position();
      requestAnimationFrame(() => focusableItems()[0]?.focus());
    } catch (error) {
      if (closed) return;
      const message = error instanceof Error ? error.message : String(error);
      renderError(content, message || "Unable to load chats.");
      position();
      requestAnimationFrame(() => focusableItems()[0]?.focus());
    }
  };

  const onDocumentPointerDown = (event: PointerEvent) => {
    const target = event.target as Node | null;
    if (target && !anchor.contains(target) && !trigger.contains(target)) close(false);
  };
  const onWindowResize = () => position();
  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape") {
      event.preventDefault();
      close(true);
      return;
    }
    if (!anchor.contains(document.activeElement) || !["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    const buttons = focusableItems();
    if (!buttons.length) return;
    event.preventDefault();
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
    const next = event.key === "Home" ? 0 : event.key === "End" ? buttons.length - 1
      : (current + (event.key === "ArrowDown" ? 1 : -1) + buttons.length) % buttons.length;
    buttons[next]?.focus();
  };
  const onPopupClick = (event: MouseEvent) => {
    const target = event.target as Element;
    const retry = target.closest<HTMLButtonElement>("[data-chat-map-retry]");
    if (retry) {
      void load();
      return;
    }
    const item = target.closest<HTMLButtonElement>("[data-chat-map-index]");
    if (!item) return;
    const chat = response.chats[Number(item.dataset.chatMapIndex)];
    if (!chat) return;
    close(false);
    const navigate = options.navigate || ((target: ChatTarget) => { window.location.hash = chatTargetRouteHash(target); });
    void Promise.resolve(navigate(chat)).catch((error) => console.error("chat map navigation failed", error));
  };

  function close(restoreFocus = false): void {
    if (closed) return;
    closed = true;
    request.abort();
    document.removeEventListener("pointerdown", onDocumentPointerDown, true);
    document.removeEventListener("keydown", onKeyDown, true);
    window.removeEventListener("resize", onWindowResize);
    anchor.removeEventListener("click", onPopupClick);
    anchor.remove();
    trigger.setAttribute("aria-expanded", "false");
    trigger.removeAttribute("aria-controls");
    if (closeActivePopup === close) closeActivePopup = null;
    options.onClose?.();
    if (restoreFocus && trigger.isConnected) trigger.focus();
  }

  closeActivePopup = close;
  document.addEventListener("pointerdown", onDocumentPointerDown, true);
  document.addEventListener("keydown", onKeyDown, true);
  window.addEventListener("resize", onWindowResize);
  anchor.addEventListener("click", onPopupClick);
  position();
  void load();
  return close;
}

export function installChatMap(root: ParentNode, options: ChatMapOptions = {}): () => void {
  let closePopup: (() => void) | null = null;
  const listeners: Array<{ trigger: HTMLElement; listener: (event: Event) => void }> = [];
  root.querySelectorAll<HTMLElement>("[data-nav=map]").forEach((trigger) => {
    const listener = (event: Event) => {
      event.stopPropagation();
      if (closePopup) {
        closePopup();
        closePopup = null;
        return;
      }
      closePopup = openChatMap(trigger, {
        ...options,
        onClose: () => { closePopup = null; },
        navigate: async (target) => {
          closePopup = null;
          if (options.navigate) return options.navigate(target);
          window.location.hash = chatTargetRouteHash(target);
        },
      });
    };
    trigger.addEventListener("click", listener);
    listeners.push({ trigger, listener });
  });
  const dispose = () => {
    closePopup?.();
    closePopup = null;
    listeners.forEach(({ trigger, listener }) => trigger.removeEventListener("click", listener));
  };
  options.signal?.addEventListener("abort", dispose, { once: true });
  return dispose;
}
