import { activeCodeTab, ensureCodeState } from "./state";
import type { CodeFileTab, CodeNavigationLocation } from "./types";
import { clamp, editorDocumentLengthForFileContent } from "./utils";

export function captureActiveCodeNavigationLocation(
  workspaceID: string,
): CodeNavigationLocation | null {
  const tab = activeCodeTab(workspaceID);
  return tab ? codeNavigationLocationFromTab(tab) : null;
}

export function codeNavigationLocationFromTab(
  tab: CodeFileTab,
): CodeNavigationLocation {
  return {
    path: tab.path,
    selectionAnchor: tab.selectionAnchor,
    selectionHead: tab.selectionHead,
    scrollTop: tab.scrollTop,
    scrollLeft: tab.scrollLeft,
  };
}

export function applyCodeNavigationLocationToTab(
  tab: CodeFileTab,
  location: CodeNavigationLocation,
) {
  const docLength = editorDocumentLengthForFileContent(tab.content, tab.lineSeparator);
  tab.selectionAnchor = clamp(location.selectionAnchor, 0, docLength);
  tab.selectionHead = clamp(location.selectionHead, 0, docLength);
  tab.scrollTop = Math.max(0, location.scrollTop);
  tab.scrollLeft = Math.max(0, location.scrollLeft);
  tab.pendingRevealPosition = tab.selectionHead;
  tab.pendingRevealScroll = "center";
}

export function recordCodeNavigationTransition(
  workspaceID: string,
  source: CodeNavigationLocation | null,
  target: CodeNavigationLocation | null,
) {
  if (!target?.path) {
    return;
  }

  const history = ensureCodeState(workspaceID).navigationHistory;
  trimForwardHistory(workspaceID);

  if (source?.path) {
    const current = history.entries[history.currentIndex];
    if (!current) {
      appendCodeNavigationLocation(workspaceID, source);
    } else if (sameCodeNavigationLocation(current, source)) {
      history.entries[history.currentIndex] = source;
    } else {
      appendCodeNavigationLocation(workspaceID, source);
    }
  } else if (history.currentIndex < 0 || history.entries.length === 0) {
    appendCodeNavigationLocation(workspaceID, target);
    return;
  }

  const current = history.entries[history.currentIndex];
  if (current && sameCodeNavigationLocation(current, target)) {
    history.entries[history.currentIndex] = target;
    return;
  }
  appendCodeNavigationLocation(workspaceID, target);
}

export function syncCurrentCodeNavigationLocation(
  workspaceID: string,
  location: CodeNavigationLocation | null,
) {
  if (!location?.path) {
    return;
  }
  const history = ensureCodeState(workspaceID).navigationHistory;
  if (history.entries.length === 0) {
    appendCodeNavigationLocation(workspaceID, location);
    return;
  }
  history.currentIndex = clamp(
    history.currentIndex,
    0,
    history.entries.length - 1,
  );
  history.entries[history.currentIndex] = location;
}

export function peekCodeNavigationHistoryTarget(
  workspaceID: string,
  direction: -1 | 1,
): { location: CodeNavigationLocation; index: number } | null {
  const history = ensureCodeState(workspaceID).navigationHistory;
  if (!history.entries.length) {
    return null;
  }
  history.currentIndex = clamp(
    history.currentIndex,
    0,
    history.entries.length - 1,
  );
  const index = history.currentIndex + direction;
  if (index < 0 || index >= history.entries.length) {
    return null;
  }
  return { location: history.entries[index], index };
}

export function commitCodeNavigationHistoryIndex(
  workspaceID: string,
  index: number,
) {
  const history = ensureCodeState(workspaceID).navigationHistory;
  if (!history.entries.length) {
    history.currentIndex = -1;
    return;
  }
  history.currentIndex = clamp(index, 0, history.entries.length - 1);
}

export function removeCodeNavigationHistoryEntry(
  workspaceID: string,
  index: number,
) {
  const history = ensureCodeState(workspaceID).navigationHistory;
  if (index < 0 || index >= history.entries.length) {
    return;
  }
  history.entries.splice(index, 1);
  if (history.entries.length === 0) {
    history.currentIndex = -1;
    return;
  }
  if (index <= history.currentIndex) {
    history.currentIndex -= 1;
  }
  history.currentIndex = clamp(
    history.currentIndex,
    0,
    history.entries.length - 1,
  );
}

export function rewriteCodeNavigationHistoryPaths(
  workspaceID: string,
  sourcePath: string,
  destinationPath: string,
) {
  const history = ensureCodeState(workspaceID).navigationHistory;
  history.entries.forEach((entry) => {
    entry.path = movedCodeNavigationPath(entry.path, sourcePath, destinationPath);
  });
}

export function removeCodeNavigationHistoryPaths(
  workspaceID: string,
  paths: string[],
) {
  const normalizedPaths = paths
    .filter(Boolean)
    .map((path) => normalizedCodeNavigationPath(path));
  if (!normalizedPaths.length) {
    return;
  }
  const history = ensureCodeState(workspaceID).navigationHistory;
  const current = history.currentIndex >= 0
    ? history.entries[history.currentIndex]
    : null;
  history.entries = history.entries.filter((entry) => {
    const normalizedEntry = normalizedCodeNavigationPath(entry.path);
    return !normalizedPaths.some(
      (path) => normalizedEntry === path || normalizedEntry.startsWith(`${path}/`),
    );
  });
  if (!history.entries.length) {
    history.currentIndex = -1;
    return;
  }
  const currentIndex = current
    ? history.entries.findIndex((entry) => sameCodeNavigationLocation(entry, current))
    : -1;
  history.currentIndex = currentIndex >= 0
    ? currentIndex
    : clamp(history.currentIndex, 0, history.entries.length - 1);
}

function appendCodeNavigationLocation(
  workspaceID: string,
  location: CodeNavigationLocation,
) {
  const history = ensureCodeState(workspaceID).navigationHistory;
  const last = history.entries[history.entries.length - 1];
  if (last && sameCodeNavigationLocation(last, location)) {
    history.entries[history.entries.length - 1] = location;
    history.currentIndex = history.entries.length - 1;
    return;
  }

  history.entries.push(location);
  while (history.entries.length > history.maxSize) {
    history.entries.shift();
  }
  history.currentIndex = history.entries.length - 1;
}

function trimForwardHistory(workspaceID: string) {
  const history = ensureCodeState(workspaceID).navigationHistory;
  if (history.currentIndex < 0 || history.currentIndex >= history.entries.length - 1) {
    return;
  }
  history.entries = history.entries.slice(0, history.currentIndex + 1);
}

function sameCodeNavigationLocation(
  left: CodeNavigationLocation,
  right: CodeNavigationLocation,
) {
  return (
    normalizedCodeNavigationPath(left.path) === normalizedCodeNavigationPath(right.path) &&
    left.selectionAnchor === right.selectionAnchor &&
    left.selectionHead === right.selectionHead
  );
}

function movedCodeNavigationPath(
  path: string,
  sourcePath: string,
  destinationPath: string,
) {
  if (!path) {
    return path;
  }
  if (sameCodeNavigationPath(path, sourcePath)) {
    return destinationPath;
  }
  const normalizedPath = normalizedCodeNavigationPath(path);
  const normalizedSource = normalizedCodeNavigationPath(sourcePath);
  if (normalizedPath.startsWith(`${normalizedSource}/`)) {
    return `${destinationPath}${path.slice(sourcePath.length)}`;
  }
  return path;
}

function sameCodeNavigationPath(left: string, right: string) {
  return normalizedCodeNavigationPath(left) === normalizedCodeNavigationPath(right);
}

function normalizedCodeNavigationPath(path: string) {
  return path.replaceAll("\\", "/").toLowerCase();
}
