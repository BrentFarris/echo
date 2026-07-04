
import { appRoot } from "./dom";
import { icons } from "./icons";
import { state } from "./state";
import type { Toast } from "./types";
import { escapeAttribute, escapeHtml } from "./utils";

export function pushToast(message: string, tone: Toast["tone"] = "info") {
  const cleanMessage = message.trim();
  if (!cleanMessage) {
    return;
  }
  if (window.innerWidth <= 720) {
    return;
  }
  const toast = {
    id: `toast-${++state.toastSeq}`,
    tone,
    message: cleanMessage,
  };
  state.toasts = [...state.toasts.slice(-3), toast];
  patchToasts();
  window.setTimeout(() => {
    dismissToast(toast.id);
  }, tone === "error" ? 9000 : 5200);
}

export function dismissToast(id: string) {
  const next = state.toasts.filter((toast) => toast.id !== id);
  if (next.length === state.toasts.length) {
    return;
  }
  state.toasts = next;
  patchToasts();
}

export function renderToasts(): string {
  if (!state.toasts.length) {
    return "";
  }
  return `
    <div class="toast-region" role="status" aria-live="polite" aria-atomic="true" data-toast-region>
      ${state.toasts
        .map(
          (toast) => `
            <div class="toast toast-${toast.tone}">
              <span>${escapeHtml(toast.message)}</span>
              <button class="icon-button" type="button" title="Dismiss" aria-label="Dismiss notification" data-action="dismiss-toast" data-toast-id="${escapeAttribute(toast.id)}">
                ${icons.x}
              </button>
            </div>
          `,
        )
        .join("")}
    </div>
  `;
}

function patchToasts() {
  const existing = appRoot.querySelector<HTMLElement>("[data-toast-region]");
  if (!state.toasts.length) {
    existing?.remove();
    return;
  }

  const markup = renderToasts();
  if (existing) {
    existing.outerHTML = markup;
    return;
  }

  const shell = appRoot.querySelector<HTMLElement>(".app-shell");
  if (!shell) {
    return;
  }
  const contextMenu = shell.querySelector<HTMLElement>("[data-context-menu]");
  if (contextMenu) {
    contextMenu.insertAdjacentHTML("beforebegin", markup);
    return;
  }
  shell.insertAdjacentHTML("beforeend", markup);
}
