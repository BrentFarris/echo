
import type { ScrollSnapshot } from "./types";

const app = document.querySelector<HTMLDivElement>("#app");

if (!app) {
  throw new Error("Echo app root was not found.");
}

export const appRoot = app;

const scrollStickinessThreshold = 48;

export function focusInitialElement() {
  const dialog = appRoot.querySelector<HTMLElement>('[role="dialog"]');
  if (!dialog) {
    return;
  }
  if (document.activeElement && dialog.contains(document.activeElement)) {
    return;
  }
  const focusTarget = dialog.querySelector<HTMLElement>(
    "[data-initial-focus], input, textarea, button:not(:disabled), [tabindex]:not([tabindex='-1'])",
  );
  focusTarget?.focus();
}

export function captureScrollSnapshot(selector: string): ScrollSnapshot | null {
  const element = appRoot.querySelector<HTMLElement>(selector);
  if (!element) {
    return null;
  }
  return {
    scrollTop: element.scrollTop,
    atBottom: isElementScrolledNearBottom(element),
  };
}

export function restoreScrollSnapshot(selector: string, snapshot: ScrollSnapshot | null) {
  if (!snapshot) {
    return;
  }
  const element = appRoot.querySelector<HTMLElement>(selector);
  if (!element) {
    return;
  }
  const maxScrollTop = Math.max(0, element.scrollHeight - element.clientHeight);
  element.scrollTop = snapshot.atBottom
    ? element.scrollHeight
    : Math.min(snapshot.scrollTop, maxScrollTop);
}

export function isElementScrolledNearBottom(element: HTMLElement | null): boolean {
  if (!element) {
    return true;
  }
  return (
    element.scrollHeight - element.scrollTop - element.clientHeight <=
    scrollStickinessThreshold
  );
}
