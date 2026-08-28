import {
  CODE_NAVIGATION_HISTORY_STATE_KEY,
  isCodeNavigationHistoryState,
} from "../navigation";
import type { FileRef } from "./types";
import { isRefWithin } from "./types";

export type CodeNavigationSelection = {
  selectionStartLineNumber: number;
  selectionStartColumn: number;
  positionLineNumber: number;
  positionColumn: number;
};

export type CodeNavigationLocation = {
  workspaceId: string;
  ref: FileRef;
  tabId?: string;
  editorKind?: "file" | "diff";
  selections: CodeNavigationSelection[];
  scrollTop: number;
  scrollLeft: number;
};

export type CodeNavigationEntry = {
  version: 1;
  sessionId: string;
  entryId: string;
  sequence: number;
  location: CodeNavigationLocation;
};

export type CodeNavigationTraversal = {
  location: CodeNavigationLocation;
  direction: -1 | 0 | 1;
};

type HistoryLike = {
  readonly state: unknown;
  pushState(data: unknown, unused: string, url?: string | URL | null): void;
  replaceState(data: unknown, unused: string, url?: string | URL | null): void;
};

type StorageLike = Pick<Storage, "getItem" | "setItem" | "removeItem">;

type NavigationHistoryOptions = {
  history?: HistoryLike;
  storage?: StorageLike;
  currentURL?: () => string;
  createId?: () => string;
};

const storagePrefix = "echo-code-navigation:";

function entryFromState(state: unknown): CodeNavigationEntry | null {
  if (!isCodeNavigationHistoryState(state)) return null;
  const candidate = (state as Record<string, unknown>)[CODE_NAVIGATION_HISTORY_STATE_KEY] as Partial<CodeNavigationEntry>;
  if (candidate.version !== 1 || typeof candidate.sessionId !== "string" || typeof candidate.entryId !== "string"
    || typeof candidate.sequence !== "number" || !isLocation(candidate.location)) return null;
  return candidate as CodeNavigationEntry;
}

function isLocation(value: unknown): value is CodeNavigationLocation {
  if (!value || typeof value !== "object") return false;
  const location = value as Partial<CodeNavigationLocation>;
  const selectionsValid = Array.isArray(location.selections) && location.selections.every((value) => {
    if (!value || typeof value !== "object") return false;
    const selection = value as Partial<CodeNavigationSelection>;
    return Number.isFinite(selection.selectionStartLineNumber)
      && Number.isFinite(selection.selectionStartColumn)
      && Number.isFinite(selection.positionLineNumber)
      && Number.isFinite(selection.positionColumn);
  });
  return typeof location.workspaceId === "string"
    && Boolean(location.ref && typeof location.ref.rootId === "string" && typeof location.ref.path === "string")
    && selectionsValid
    && Number.isFinite(location.scrollTop)
    && Number.isFinite(location.scrollLeft);
}

function containingState(state: unknown, entry: CodeNavigationEntry): Record<string, unknown> {
  const base = state && typeof state === "object" && !Array.isArray(state)
    ? { ...(state as Record<string, unknown>) }
    : {};
  base[CODE_NAVIGATION_HISTORY_STATE_KEY] = entry;
  return base;
}

function sameLocation(left: CodeNavigationLocation, right: CodeNavigationLocation): boolean {
  return left.workspaceId === right.workspaceId
    && left.ref.rootId === right.ref.rootId
    && left.ref.path === right.ref.path
    && left.tabId === right.tabId
    && left.editorKind === right.editorKind
    && left.scrollTop === right.scrollTop
    && left.scrollLeft === right.scrollLeft
    && JSON.stringify(left.selections) === JSON.stringify(right.selections);
}

export function isLargeCodeNavigationJump(
  previous: CodeNavigationLocation | null,
  next: CodeNavigationLocation,
  minimumLines = 10,
): boolean {
  if (!previous || previous.workspaceId !== next.workspaceId
    || previous.ref.rootId !== next.ref.rootId || previous.ref.path !== next.ref.path) return false;
  const previousLine = previous.selections[0]?.positionLineNumber;
  const nextLine = next.selections[0]?.positionLineNumber;
  return typeof previousLine === "number" && typeof nextLine === "number"
    && Math.abs(nextLine - previousLine) >= minimumLines;
}

function rewrittenLocation(location: CodeNavigationLocation, previous: FileRef, next: FileRef): CodeNavigationLocation {
  if (!isRefWithin(location.ref, previous)) return location;
  const suffix = location.ref.path.slice(previous.path.length).replace(/^\//, "");
  return {
    ...location,
    ref: { rootId: next.rootId, path: suffix ? `${next.path}/${suffix}` : next.path },
  };
}

export function codeNavigationEntryFromState(state: unknown): CodeNavigationEntry | null {
  return entryFromState(state);
}

export class CodeNavigationHistory {
  private readonly workspaceId: string;
  private readonly browserHistory: HistoryLike;
  private readonly storage: StorageLike | null;
  private readonly currentURL: () => string;
  private readonly createId: () => string;
  private readonly locations = new Map<string, CodeNavigationLocation>();
  private currentEntry: CodeNavigationEntry | null = null;

  constructor(workspaceId: string, options: NavigationHistoryOptions = {}) {
    this.workspaceId = workspaceId;
    this.browserHistory = options.history || window.history;
    this.storage = options.storage === undefined ? window.sessionStorage : options.storage;
    this.currentURL = options.currentURL || (() => window.location.href);
    this.createId = options.createId || (() => crypto.randomUUID());
    const current = entryFromState(this.browserHistory.state);
    if (current?.location.workspaceId === workspaceId) {
      this.currentEntry = current;
      this.locations.set(current.entryId, this.loadLocation(current) || current.location);
    }
  }

  initialLocation(): CodeNavigationLocation | null {
    if (!this.currentEntry) return null;
    return this.locations.get(this.currentEntry.entryId) || this.currentEntry.location;
  }

  attachInitial(location: CodeNavigationLocation): void {
    if (location.workspaceId !== this.workspaceId) return;
    if (this.currentEntry) {
      this.updateCurrent(location);
      return;
    }
    const entry: CodeNavigationEntry = {
      version: 1,
      sessionId: this.createId(),
      entryId: this.createId(),
      sequence: 0,
      location,
    };
    this.currentEntry = entry;
    this.locations.set(entry.entryId, location);
    this.browserHistory.replaceState(containingState(this.browserHistory.state, entry), "", this.currentURL());
    this.persist(entry, location);
  }

  updateCurrent(location: CodeNavigationLocation): void {
    if (!this.currentEntry || location.workspaceId !== this.workspaceId) return;
    this.locations.set(this.currentEntry.entryId, location);
  }

  recordTransition(source: CodeNavigationLocation | null, destination: CodeNavigationLocation | null): boolean {
    if (!destination || destination.workspaceId !== this.workspaceId) return false;
    if (!this.currentEntry) {
      this.attachInitial(destination);
      return false;
    }
    const currentLocation = source || this.locations.get(this.currentEntry.entryId) || this.currentEntry.location;
    this.writeCurrentFallback(currentLocation);
    if (sameLocation(currentLocation, destination)) {
      this.updateCurrent(destination);
      this.writeCurrentFallback(destination);
      return false;
    }
    const entry: CodeNavigationEntry = {
      version: 1,
      sessionId: this.currentEntry.sessionId,
      entryId: this.createId(),
      sequence: this.currentEntry.sequence + 1,
      location: destination,
    };
    this.browserHistory.pushState(containingState(this.browserHistory.state, entry), "", this.currentURL());
    this.currentEntry = entry;
    this.locations.set(entry.entryId, destination);
    this.persist(entry, destination);
    return true;
  }

  beginTraversal(state: unknown, departing: CodeNavigationLocation | null): CodeNavigationTraversal | null {
    const previous = this.currentEntry;
    if (previous && departing?.workspaceId === this.workspaceId) {
      this.locations.set(previous.entryId, departing);
      this.persist(previous, departing);
    }
    const target = entryFromState(state);
    if (!target || target.location.workspaceId !== this.workspaceId) {
      this.currentEntry = null;
      return null;
    }
    const direction = previous?.sessionId === target.sessionId
      ? Math.sign(target.sequence - previous.sequence) as -1 | 0 | 1
      : 0;
    this.currentEntry = target;
    const location = this.loadLocation(target) || target.location;
    this.locations.set(target.entryId, location);
    return { location, direction };
  }

  finishTraversal(location: CodeNavigationLocation): void {
    if (!this.currentEntry || location.workspaceId !== this.workspaceId) return;
    this.locations.set(this.currentEntry.entryId, location);
    this.writeCurrentFallback(location);
  }

  remapRef(previous: FileRef, next: FileRef): void {
    for (const [entryId, location] of this.locations) {
      const rewritten = rewrittenLocation(location, previous, next);
      if (rewritten === location) continue;
      this.locations.set(entryId, rewritten);
      const entry = this.currentEntry?.entryId === entryId ? this.currentEntry : null;
      if (entry) this.persist(entry, rewritten);
      else this.persistLocation(entryId, rewritten);
    }
    if (this.currentEntry) {
      const location = this.locations.get(this.currentEntry.entryId);
      if (location) this.writeCurrentFallback(location);
    }
  }

  dispose(location: CodeNavigationLocation | null): void {
    if (!this.currentEntry || location?.workspaceId !== this.workspaceId) return;
    this.locations.set(this.currentEntry.entryId, location);
    this.persist(this.currentEntry, location);
    const browserEntry = entryFromState(this.browserHistory.state);
    if (browserEntry?.entryId === this.currentEntry.entryId) this.writeCurrentFallback(location);
  }

  private writeCurrentFallback(location: CodeNavigationLocation): void {
    if (!this.currentEntry) return;
    const entry = { ...this.currentEntry, location };
    this.currentEntry = entry;
    this.locations.set(entry.entryId, location);
    const browserEntry = entryFromState(this.browserHistory.state);
    if (browserEntry?.entryId === entry.entryId) {
      this.browserHistory.replaceState(containingState(this.browserHistory.state, entry), "", this.currentURL());
    }
    this.persist(entry, location);
  }

  private storageKey(sessionId: string, entryId: string): string {
    return `${storagePrefix}${sessionId}:${entryId}`;
  }

  private loadLocation(entry: CodeNavigationEntry): CodeNavigationLocation | null {
    if (!this.storage) return null;
    try {
      const raw = this.storage.getItem(this.storageKey(entry.sessionId, entry.entryId));
      const parsed = raw ? JSON.parse(raw) : null;
      return isLocation(parsed) && parsed.workspaceId === this.workspaceId ? parsed : null;
    } catch {
      return null;
    }
  }

  private persist(entry: CodeNavigationEntry, location: CodeNavigationLocation): void {
    if (!this.storage) return;
    try {
      this.storage.setItem(this.storageKey(entry.sessionId, entry.entryId), JSON.stringify(location));
    } catch {
      // Browser history remains a complete fallback when session storage is unavailable.
    }
  }

  private persistLocation(entryId: string, location: CodeNavigationLocation): void {
    if (!this.currentEntry || !this.storage) return;
    try {
      this.storage.setItem(this.storageKey(this.currentEntry.sessionId, entryId), JSON.stringify(location));
    } catch {
      // Ignore storage quotas; the browser entry still has its original fallback.
    }
  }
}
