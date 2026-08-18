export function escapeHTML(value: unknown): string {
  return String(value ?? "").replace(/[&<>"']/g, (character) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[character] ?? character));
}

export type MenuAction = {
  label: string;
  detail?: string;
  icon?: string;
  danger?: boolean;
  disabled?: boolean;
  separatorBefore?: boolean;
  run(): unknown | Promise<unknown>;
};

let activeMenu: HTMLElement | null = null;

export function closeContextMenu(): void {
  activeMenu?.remove();
  activeMenu = null;
}

export function showContextMenu(x: number, y: number, actions: MenuAction[]): void {
  closeContextMenu();
  const menu = document.createElement("div");
  menu.className = "code-context-menu";
  menu.setAttribute("role", "menu");
  menu.innerHTML = actions.map((action, index) => `
    ${action.separatorBefore ? `<div class="code-menu-separator" role="separator"></div>` : ""}
    <button type="button" role="menuitem" data-menu-index="${index}" class="${action.danger ? "is-danger" : ""}" ${action.disabled ? "disabled" : ""}>
      <span class="codicon codicon-${escapeHTML(action.icon || "blank")}" aria-hidden="true"></span>
      <span>${escapeHTML(action.label)}</span>
      ${action.detail ? `<kbd>${escapeHTML(action.detail)}</kbd>` : ""}
    </button>
  `).join("");
  document.body.appendChild(menu);
  const rect = menu.getBoundingClientRect();
  menu.style.left = `${Math.max(4, Math.min(x, window.innerWidth - rect.width - 4))}px`;
  menu.style.top = `${Math.max(4, Math.min(y, window.innerHeight - rect.height - 4))}px`;
  menu.addEventListener("click", (event) => {
    const button = (event.target as Element).closest<HTMLButtonElement>("[data-menu-index]");
    if (!button || button.disabled) return;
    const action = actions[Number(button.dataset.menuIndex)];
    closeContextMenu();
    void action.run();
  });
  menu.addEventListener("keydown", (event) => {
    const buttons = [...menu.querySelectorAll<HTMLButtonElement>("button:not(:disabled)")];
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
    let next = current;
    if (event.key === "ArrowDown") next = (current + 1 + buttons.length) % buttons.length;
    else if (event.key === "ArrowUp") next = (current - 1 + buttons.length) % buttons.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = buttons.length - 1;
    else return;
    event.preventDefault();
    buttons[next]?.focus();
  });
  activeMenu = menu;
  requestAnimationFrame(() => menu.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus());
}

export function installMenuDismissal(signal: AbortSignal): void {
  document.addEventListener("pointerdown", (event) => {
    if (activeMenu && !activeMenu.contains(event.target as Node)) closeContextMenu();
  }, { signal, capture: true });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") closeContextMenu();
  }, { signal, capture: true });
  window.addEventListener("blur", closeContextMenu, { signal });
}

export function choiceDialog(options: {
  title: string;
  message: string;
  detail?: string;
  choices: Array<{ id: string; label: string; primary?: boolean; danger?: boolean }>;
}): Promise<string | null> {
  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "code-modal-overlay";
    overlay.innerHTML = `
      <section class="code-modal" role="dialog" aria-modal="true" aria-labelledby="code-dialog-title">
        <h2 id="code-dialog-title">${escapeHTML(options.title)}</h2>
        <p>${escapeHTML(options.message)}</p>
        ${options.detail ? `<p class="code-modal-detail">${escapeHTML(options.detail)}</p>` : ""}
        <div class="code-modal-actions">
          ${options.choices.map((choice) => `<button type="button" data-choice="${escapeHTML(choice.id)}" class="${choice.primary ? "is-primary" : ""} ${choice.danger ? "is-danger" : ""}">${escapeHTML(choice.label)}</button>`).join("")}
        </div>
      </section>
    `;
    const finish = (choice: string | null) => {
      overlay.remove();
      resolve(choice);
    };
    overlay.addEventListener("click", (event) => {
      const button = (event.target as Element).closest<HTMLButtonElement>("[data-choice]");
      if (button) finish(button.dataset.choice || null);
      else if (event.target === overlay) finish(null);
    });
    overlay.addEventListener("keydown", (event) => {
      if (event.key === "Escape") finish(null);
    });
    document.body.appendChild(overlay);
    requestAnimationFrame(() => overlay.querySelector<HTMLButtonElement>(".is-primary, button")?.focus());
  });
}

export function promptDialog(options: {
  title: string;
  label: string;
  initial?: string;
  placeholder?: string;
  confirmLabel?: string;
  required?: boolean;
}): Promise<string | null> {
  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "code-modal-overlay";
    overlay.innerHTML = `
      <form class="code-modal code-prompt-modal" role="dialog" aria-modal="true">
        <h2>${escapeHTML(options.title)}</h2>
        <label>${escapeHTML(options.label)}<input name="value" value="${escapeHTML(options.initial || "")}" placeholder="${escapeHTML(options.placeholder || "")}" ${options.required === false ? "" : "required"}></label>
        <p class="code-modal-error" data-prompt-error></p>
        <div class="code-modal-actions">
          <button type="button" data-cancel>Cancel</button>
          <button type="submit" class="is-primary">${escapeHTML(options.confirmLabel || "OK")}</button>
        </div>
      </form>
    `;
    const form = overlay.querySelector<HTMLFormElement>("form")!;
    const input = form.elements.namedItem("value") as HTMLInputElement;
    const finish = (value: string | null) => { overlay.remove(); resolve(value); };
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      const value = input.value.trim();
      if (value || options.required === false) finish(value);
    });
    overlay.querySelector("[data-cancel]")?.addEventListener("click", () => finish(null));
    overlay.addEventListener("keydown", (event) => { if (event.key === "Escape") finish(null); });
    document.body.appendChild(overlay);
    requestAnimationFrame(() => { input.focus(); input.select(); });
  });
}

export function toast(message: string, options?: { actionLabel?: string; action?: () => void | Promise<void>; sticky?: boolean }): void {
  let container = document.querySelector<HTMLElement>(".code-toast-container");
  if (!container) {
    container = document.createElement("div");
    container.className = "code-toast-container";
    container.setAttribute("aria-live", "polite");
    document.body.appendChild(container);
  }
  const item = document.createElement("div");
  item.className = "code-toast";
  item.innerHTML = `<span>${escapeHTML(message)}</span>${options?.actionLabel ? `<button type="button">${escapeHTML(options.actionLabel)}</button>` : ""}<button type="button" class="code-toast-close" aria-label="Dismiss"><span class="codicon codicon-close"></span></button>`;
  const remove = () => item.remove();
  item.querySelector(".code-toast-close")?.addEventListener("click", remove);
  if (options?.action) {
    item.querySelector<HTMLButtonElement>("button:not(.code-toast-close)")?.addEventListener("click", async () => {
      await options.action?.();
      remove();
    });
  }
  container.appendChild(item);
  if (!options?.sticky) window.setTimeout(remove, 7000);
}

export async function copyText(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return;
    } catch {
      // Trusted-LAN HTTP may not expose the modern clipboard API.
    }
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.select();
  const copied = document.execCommand("copy");
  textarea.remove();
  if (!copied) throw new Error("Clipboard access was denied");
}
