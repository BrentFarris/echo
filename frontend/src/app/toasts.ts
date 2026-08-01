
import { appRoot } from "./dom";
import { getAppCallbacks } from "./callbacks";
import { icons } from "./icons";
import { state } from "./state";
import type { Toast } from "./types";
import { escapeAttribute, escapeHtml } from "./utils";

export function pushToast(message: string, tone: Toast["tone"] = "info") {
  const cleanMessage = message.trim();
  if (!cleanMessage) {
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
  const overlayRegion = appRoot.querySelector<HTMLElement>('[data-region="overlays"]');
  const toastRegions = Array.from(
    appRoot.querySelectorAll<HTMLElement>("[data-toast-region]"),
  );
  if (!state.toasts.length) {
    toastRegions.forEach((region) => region.remove());
    return;
  }

  if (!overlayRegion) {
    return;
  }

  const existing = overlayRegion.querySelector<HTMLElement>("[data-toast-region]");
  toastRegions
    .filter((region) => region !== existing)
    .forEach((region) => region.remove());

  const markup = renderToasts();
  if (existing) {
    existing.outerHTML = markup;
    bindToastActions();
    return;
  }

  const contextMenu = overlayRegion.querySelector<HTMLElement>("[data-context-menu]");
  if (contextMenu) {
    contextMenu.insertAdjacentHTML("beforebegin", markup);
    bindToastActions();
    return;
  }
  overlayRegion.insertAdjacentHTML("beforeend", markup);
  bindToastActions();
}

function bindToastActions() {
  const region = appRoot.querySelector<HTMLElement>("[data-toast-region]");
  if (region) {
    getAppCallbacks().bindActionEvents(region);
  }
}
