export type Indentation = { insertSpaces: boolean; tabSize: number };

export function indentationDefaults(settings: { editorInsertSpaces?: unknown; editorTabSize?: unknown }): Indentation {
  const size = settings.editorTabSize;
  return {
    insertSpaces: settings.editorInsertSpaces === true,
    tabSize: typeof size === "number" && Number.isInteger(size) && size >= 1 && size <= 8 ? size : 4,
  };
}

export function indentationLabel(options: Indentation): string {
  return `${options.insertSpaces ? "Spaces" : "Tabs"}: ${options.tabSize}`;
}

// Serialize read/merge/write operations so font and indentation updates preserve
// one another, as well as settings changed since the editor was mounted.
export function editorSettingsWriter(
  read: () => Promise<Record<string, unknown>>,
  write: (settings: Record<string, unknown>) => Promise<unknown>,
): (patch: Record<string, unknown>) => Promise<void> {
  let pending = Promise.resolve();
  return (patch) => {
    const next = pending.then(async () => { await write({ ...await read(), ...patch }); });
    pending = next.catch(() => {});
    return next;
  };
}

export function openIndentationPopover(
  button: HTMLButtonElement,
  current: Indentation,
  apply: (options: Indentation) => void,
): () => void {
  const abort = new AbortController();
  const popover = document.createElement("div");
  popover.className = "code-indentation-popover";
  popover.setAttribute("role", "dialog");
  popover.setAttribute("aria-label", "Indentation");
  popover.innerHTML = `
    <strong>Indentation</strong>
    <p>Applies to the current file and the saved default.</p>
    <label>Indent using<select data-indent-style><option value="tabs">Tabs</option><option value="spaces">Spaces</option></select></label>
    <label>Width<select data-indent-width>${Array.from({ length: 8 }, (_, index) => `<option value="${index + 1}">${index + 1}</option>`).join("")}</select></label>`;
  const style = popover.querySelector<HTMLSelectElement>("[data-indent-style]")!;
  const width = popover.querySelector<HTMLSelectElement>("[data-indent-width]")!;
  style.value = current.insertSpaces ? "spaces" : "tabs";
  // Detected widths outside the selectable range remain untouched until edited.
  width.value = String(current.tabSize);
  button.setAttribute("aria-expanded", "true");
  document.body.appendChild(popover);
  const anchor = button.getBoundingClientRect();
  const rect = popover.getBoundingClientRect();
  popover.style.left = `${Math.max(4, Math.min(anchor.right - rect.width, window.innerWidth - rect.width - 4))}px`;
  popover.style.top = `${Math.max(4, anchor.top - rect.height - 4)}px`;
  const close = (restoreFocus = true) => {
    if (abort.signal.aborted) return;
    abort.abort();
    popover.remove();
    button.setAttribute("aria-expanded", "false");
    if (restoreFocus && button.isConnected) button.focus();
  };
  popover.addEventListener("change", () => {
    if (!width.value) width.value = "4";
    apply({ insertSpaces: style.value === "spaces", tabSize: Number(width.value) });
  }, { signal: abort.signal });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") { event.preventDefault(); close(); }
  }, { signal: abort.signal });
  document.addEventListener("pointerdown", (event) => {
    if (!popover.contains(event.target as Node) && event.target !== button) close();
  }, { signal: abort.signal, capture: true });
  document.addEventListener("focusin", (event) => {
    if (!popover.contains(event.target as Node) && event.target !== button) close(false);
  }, { signal: abort.signal });
  window.addEventListener("resize", () => close(), { signal: abort.signal });
  style.focus();
  return () => close();
}
