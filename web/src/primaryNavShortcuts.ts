export type PrimaryNavShortcut = "chat" | "code" | "search" | "git" | "sandbox" | "settings" | "map";

const shortcutByKey: Record<string, PrimaryNavShortcut> = {
  Digit0: "settings",
  Digit1: "chat",
  Digit2: "code",
  Digit3: "search",
  Digit4: "git",
  Digit5: "sandbox",
  Slash: "map",
  "0": "settings",
  "1": "chat",
  "2": "code",
  "3": "search",
  "4": "git",
  "5": "sandbox",
  "/": "map",
};

/** Resolves Echo's global primary-navigation shortcut, if any. */
export function primaryNavShortcut(event: KeyboardEvent): PrimaryNavShortcut | null {
  if (!event.ctrlKey || event.metaKey || event.altKey || event.shiftKey || event.repeat || event.isComposing) return null;
  return shortcutByKey[event.code] || shortcutByKey[event.key] || null;
}

/**
 * Routes shortcuts through the mounted view's navigation buttons. This keeps
 * view-specific behavior such as Settings persistence and Code sidebar reuse.
 */
export function installPrimaryNavShortcuts(root: Document = document): () => void {
  const onKeyDown = (event: KeyboardEvent) => {
    const nav = primaryNavShortcut(event);
    if (!nav) return;
    const trigger = root.querySelector<HTMLElement>(`[data-nav="${nav}"]:not([disabled]):not([aria-disabled="true"])`);
    if (!trigger) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    trigger.click();
  };

  root.addEventListener("keydown", onKeyDown, true);
  return () => root.removeEventListener("keydown", onKeyDown, true);
}
