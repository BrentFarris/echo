export const commandHistoryStorageKey = "echo.code.commandPalette.recent.v1";
export const commandHistoryLimit = 5;

type StorageLike = Pick<Storage, "getItem" | "setItem">;
type CommandLike = { id: string; label: string };

export type CommandPaletteGroup<T extends CommandLike> = {
  label: "Recent" | "Commands" | null;
  commands: T[];
};

export type CommandPalettePresentation<T extends CommandLike> = {
  commands: T[];
  groups: CommandPaletteGroup<T>[];
};

function normalizeRecentCommandIds(values: unknown, limit = commandHistoryLimit): string[] {
  const maximum = Math.max(0, Math.floor(limit));
  if (!Array.isArray(values) || maximum === 0) return [];
  const seen = new Set<string>();
  const result: string[] = [];
  for (const value of values) {
    if (typeof value !== "string" || !value || seen.has(value)) continue;
    seen.add(value);
    result.push(value);
    if (result.length >= maximum) break;
  }
  return result;
}

export function parseRecentCommandIds(raw: string | null, limit = commandHistoryLimit): string[] {
  let parsed: unknown;
  try { parsed = JSON.parse(raw || "[]"); } catch { return []; }
  return normalizeRecentCommandIds(parsed, limit);
}

export function loadRecentCommandIds(storage?: StorageLike): string[] {
  try {
    const target = storage || window.localStorage;
    return parseRecentCommandIds(target.getItem(commandHistoryStorageKey));
  } catch {
    return [];
  }
}

export function saveRecentCommandIds(ids: string[], storage?: StorageLike): void {
  try {
    const target = storage || window.localStorage;
    target.setItem(commandHistoryStorageKey, JSON.stringify(normalizeRecentCommandIds(ids)));
  } catch {
    // Command history is optional when persistent browser storage is unavailable.
  }
}

export function recordRecentCommandId(
  ids: string[],
  commandId: string,
  limit = commandHistoryLimit,
): string[] {
  if (!commandId || limit <= 0) return [];
  return normalizeRecentCommandIds([commandId, ...ids.filter((id) => id !== commandId)], limit);
}

export function pruneRecentCommandIds(ids: string[], availableCommandIds: Iterable<string>): string[] {
  const available = new Set(availableCommandIds);
  return normalizeRecentCommandIds(ids).filter((id) => available.has(id));
}

export function commandPalettePresentation<T extends CommandLike>(
  commands: T[],
  recentCommandIds: string[],
  query: string,
): CommandPalettePresentation<T> {
  const normalizedQuery = query.trim().toLowerCase();
  const matching = commands.filter((command) => command.label.toLowerCase().includes(normalizedQuery));
  const matchingById = new Map(matching.map((command) => [command.id, command]));
  const recent = pruneRecentCommandIds(recentCommandIds, matchingById.keys())
    .map((id) => matchingById.get(id)!)
    .filter(Boolean);
  const recentIds = new Set(recent.map((command) => command.id));
  const remaining = matching.filter((command) => !recentIds.has(command.id));
  const ordered = [...recent, ...remaining];

  if (normalizedQuery) return { commands: ordered, groups: [{ label: null, commands: ordered }] };
  if (!recent.length) return { commands: remaining, groups: [{ label: null, commands: remaining }] };
  return {
    commands: ordered,
    groups: [
      { label: "Recent", commands: recent },
      { label: "Commands", commands: remaining },
    ],
  };
}
