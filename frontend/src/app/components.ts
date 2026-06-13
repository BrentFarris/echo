
import { escapeHtml } from "./utils";

export function renderSpinnerLabel(label: string): string {
  return `
    <span class="busy-status" aria-live="polite">
      <span class="spinner" aria-hidden="true"></span>
      <span>${escapeHtml(label)}</span>
    </span>
  `;
}
