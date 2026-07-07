
import { refreshOpenCodeTabsFromDisk } from "../../codeView";
import { LoadWorkspaceChangeReview } from "../../backend/services";
import { services } from "../../../wailsjs/go/models";
import { getAppCallbacks } from "../callbacks";
import { appRoot } from "../dom";
import { icons } from "../icons";
import { activeWorkspace, changeReviewFor, gitSplitDiffViewEnabled, state } from "../state";
import { pushToast } from "../toasts";
import type { FileChangesEvent } from "../types";
import { changeOperationLabel, changeSourceLabel, errorMessage, escapeAttribute, escapeHtml, fileName, formatBytes } from "../utils";

type GitDiffHunkTarget = {
  lineIndex: number;
  targetLine: number;
};

type GitSplitDiffRow = {
  kind: "meta" | "context" | "changed";
  left?: string;
  leftKind?: "context" | "removed";
  right?: string;
  rightKind?: "context" | "added";
  targetLine?: number;
};

export function renderChangeReviewDrawer(
  workspace: services.Workspace,
  review: services.WorkspaceChangeReview,
): string {
  const files = review.files ?? [];
  const hasChanges = (review.changeCount ?? 0) > 0;
  const expanded = state.expandedChangeReviewWorkspaces.has(workspace.id);
  const sizeLabel = expanded ? "Collapse AI changes" : "Expand AI changes";
  return `
    <aside class="change-review-backdrop ${expanded ? "is-expanded" : ""}" role="dialog" aria-modal="true" aria-labelledby="change-review-title">
      <section class="change-review ${expanded ? "is-expanded" : ""}" data-change-review>
        <header class="change-review-header">
          <div>
            <p class="eyebrow">${escapeHtml(workspace.displayName)}</p>
            <h2 id="change-review-title">AI Changes</h2>
          </div>
          <div class="change-review-header-actions">
            <button class="icon-button" type="button" title="${sizeLabel}" aria-label="${sizeLabel}" aria-pressed="${expanded}" data-action="toggle-change-review-size">
              ${expanded ? icons.collapse : icons.expand}
            </button>
            <button class="icon-button close-button" type="button" title="Close" aria-label="Close AI changes" data-action="close-change-review">
              ${icons.x}
            </button>
          </div>
        </header>

        <div class="change-review-summary" aria-label="Change summary">
          <span>${escapeHtml(String(review.fileCount ?? files.length))} files</span>
          <span>${escapeHtml(String(review.changeCount ?? 0))} tool changes</span>
        </div>

        <div class="change-review-actions">
          <button class="icon-button" type="button" title="Previous change" aria-label="Previous change" data-action="previous-change" ${files.length ? "" : "disabled"}>
            ${icons.arrowUp}
          </button>
          <button class="icon-button" type="button" title="Next change" aria-label="Next change" data-action="next-change" ${files.length ? "" : "disabled"}>
            ${icons.arrowDown}
          </button>
          <button class="secondary-button icon-text-button" type="button" data-action="clear-change-review" ${hasChanges ? "" : "disabled"}>
            ${icons.trash}
            <span>Clear</span>
          </button>
        </div>

        ${
          files.length
            ? `<div class="change-file-list">${files.map(renderChangedFile).join("")}</div>`
            : `<div class="empty-state compact">No AI file changes recorded.</div>`
        }
      </section>
    </aside>
  `;
}

export function renderChangedFile(file: services.WorkspaceChangedFile): string {
  return `
    <article class="change-file" data-change-file>
      <header>
        <div class="change-file-title">
          ${icons.file}
          <strong title="${escapeAttribute(file.path)}">${escapeHtml(file.path)}</strong>
        </div>
        <span class="change-operation is-${escapeAttribute(file.operation)}">${escapeHtml(changeOperationLabel(file.operation))}</span>
      </header>
      ${renderChangeSources(file.sources ?? [])}
      ${file.diffAvailable && file.diff ? renderChangeDiff(file.diff) : renderChangeMetadata(file)}
    </article>
  `;
}

export function renderGitChangedFile(file: services.WorkspaceGitChangedFile): string {
  const workspace = activeWorkspace();
  const busy = Boolean(workspace && state.gitRepositoryOperations.has(workspace.id));
  const openable = isGitChangedFileOpenable(file);
  const openLine = gitChangedFileOpenLine(file);
  return `
    <article class="change-file" data-change-file>
      <header>
        <div class="change-file-title">
          ${icons.file}
          <strong title="${escapeAttribute(file.path)}">${escapeHtml(file.path)}</strong>
        </div>
        <div class="change-file-actions">
          <span class="change-operation is-${escapeAttribute(file.operation)}">${escapeHtml(changeOperationLabel(file.operation))}</span>
          ${openable
            ? `<button class="secondary-button icon-text-button git-file-open-button" type="button" title="Open changed file" data-action="open-git-change-in-code" data-git-file-path="${escapeAttribute(file.path)}" data-git-target-line="${escapeAttribute(String(openLine))}">
                ${icons.code}
                <span>Open</span>
              </button>`
            : ""}
          <button class="icon-button danger-button git-file-revert-button" type="button" title="Revert file" aria-label="Revert ${escapeAttribute(file.path)}" data-action="revert-git-file" data-git-file-path="${escapeAttribute(file.path)}" ${busy ? "disabled" : ""}>
            ${icons.undo}
          </button>
        </div>
      </header>
      ${renderGitChangeStatus(file)}
      ${file.diffAvailable && file.diff ? renderGitDiff(file.diff, openable ? file.path : "") : renderGitChangeMetadata(file)}
    </article>
  `;
}

function isGitChangedFileOpenable(file: services.WorkspaceGitChangedFile): boolean {
  return Boolean(file.path && file.operation !== "deleted");
}

function gitChangedFileOpenLine(file: services.WorkspaceGitChangedFile): number {
  if (!file.diffAvailable || !file.diff) {
    return 1;
  }
  return gitDiffHunkTargets(file.diff)[0]?.targetLine ?? 1;
}

export function renderGitChangeStatus(file: services.WorkspaceGitChangedFile): string {
  const chips: string[] = [];
  if (file.oldPath) {
    chips.push(`<span title="${escapeAttribute(file.oldPath)}">from <em>${escapeHtml(file.oldPath)}</em></span>`);
  }
  if (file.status) {
    chips.push(`<span>status <em>${escapeHtml(file.status)}</em></span>`);
  }
  if (file.indexStatus) {
    chips.push(`<span>index <em>${escapeHtml(gitStatusLabel(file.indexStatus))}</em></span>`);
  }
  if (file.worktreeStatus) {
    chips.push(`<span>worktree <em>${escapeHtml(gitStatusLabel(file.worktreeStatus))}</em></span>`);
  }
  if (!chips.length) {
    return "";
  }
  return `<div class="change-sources" aria-label="Git status">${chips.join("")}</div>`;
}

export function renderChangeSources(sources: services.WorkspaceChangeSource[]): string {
  if (!sources.length) {
    return "";
  }
  return `
    <div class="change-sources" aria-label="Change sources">
      ${sources
        .map(
          (source) => `
            <span title="${escapeAttribute(source.toolName || "AI tool")}">
              ${escapeHtml(changeSourceLabel(source))}
              ${source.toolName ? `<em>${escapeHtml(source.toolName)}</em>` : ""}
            </span>
          `,
        )
        .join("")}
    </div>
  `;
}

export function renderGitChangeMetadata(file: services.WorkspaceGitChangedFile): string {
  return `
    <div class="change-metadata">
      <span>${escapeHtml(gitDiffUnavailableLabel(file))}</span>
    </div>
  `;
}

export function gitDiffUnavailableLabel(file: services.WorkspaceGitChangedFile): string {
  if (file.operation === "created" && file.status === "??") {
    return "Diff is unavailable for this untracked file.";
  }
  return "Diff is unavailable for this Git change.";
}

export function gitStatusLabel(status: string): string {
  switch (status) {
    case "A":
      return "added";
    case "C":
      return "copied";
    case "D":
      return "deleted";
    case "M":
      return "modified";
    case "R":
      return "renamed";
    case "U":
      return "unmerged";
    case "?":
      return "untracked";
    default:
      return status;
  }
}

export function renderChangeDiff(diff: string): string {
  const lines = diff.split("\n");
  const rendered = lines
    .map((line) => {
      let kind = "context";
      if (line.startsWith("+") && !line.startsWith("+++")) {
        kind = "added";
      } else if (line.startsWith("-") && !line.startsWith("---")) {
        kind = "removed";
      } else if (line.startsWith("@@") || line.startsWith("---") || line.startsWith("+++")) {
        kind = "meta";
      }
      const marker = kind === "added" || kind === "removed" ? " data-change-line" : "";
      return `<span class="change-diff-line is-${kind}"${marker}>${escapeHtml(line || " ")}</span>`;
    })
    .join("");
  return `<pre class="change-diff"><code>${rendered}</code></pre>`;
}

export function renderGitChangeDiff(diff: string, path: string): string {
  const targets = new Map(gitDiffHunkTargets(diff).map((target) => [target.lineIndex, target]));
  const lines = diff.split("\n");
  const rendered = lines
    .map((line, index) => {
      let kind = "context";
      if (line.startsWith("+") && !line.startsWith("+++")) {
        kind = "added";
      } else if (line.startsWith("-") && !line.startsWith("---")) {
        kind = "removed";
      } else if (line.startsWith("@@") || line.startsWith("---") || line.startsWith("+++")) {
        kind = "meta";
      }
      const marker = kind === "added" || kind === "removed" ? " data-change-line" : "";
      const target = path ? targets.get(index) : undefined;
      if (!target) {
        return `<span class="change-diff-line is-${kind}"${marker}>${escapeHtml(line || " ")}</span>`;
      }
      return `<span class="change-diff-line is-${kind} is-hunk"${marker}><button class="secondary-button git-hunk-open-button" type="button" title="Open this hunk" data-action="open-git-change-in-code" data-git-file-path="${escapeAttribute(path)}" data-git-target-line="${escapeAttribute(String(target.targetLine))}">Open</button><span>${escapeHtml(line || " ")}</span></span>`;
    })
    .join("");
  return `<pre class="change-diff"><code>${rendered}</code></pre>`;
}

export function renderGitDiff(diff: string, path: string): string {
  const unified = renderGitChangeDiff(diff, path);
  if (!gitSplitDiffViewEnabled(state.appState?.settings)) {
    return unified;
  }
  return `
    <div class="git-diff-views">
      <div class="git-diff-unified">${unified}</div>
      ${renderGitSplitDiff(diff, path)}
    </div>
  `;
}

function renderGitSplitDiff(diff: string, path: string): string {
  const rows = gitSplitDiffRows(diff);
  const rendered = rows.map((row) => {
    if (row.kind === "meta") {
      return `
        <tr class="git-split-diff-row is-meta">
          <td class="git-split-diff-cell" colspan="2" title="${escapeAttribute(row.left ?? "")}"><span>${escapeHtml(row.left || " ")}</span></td>
        </tr>
      `;
    }
    const marker = row.kind === "changed" ? " data-change-line" : "";
    const openButton = path && row.targetLine
      ? `<button class="secondary-button git-hunk-open-button" type="button" title="Open this hunk" data-action="open-git-change-in-code" data-git-file-path="${escapeAttribute(path)}" data-git-target-line="${escapeAttribute(String(row.targetLine))}">Open</button>`
      : "";
    return `
      <tr class="git-split-diff-row is-${row.kind}"${marker}>
        <td class="git-split-diff-cell is-${row.leftKind ?? "blank"}"><span>${escapeHtml(row.left || " ")}</span></td>
        <td class="git-split-diff-cell is-${row.rightKind ?? "blank"}">
          ${openButton}
          <span>${escapeHtml(row.right || " ")}</span>
        </td>
      </tr>
    `;
  }).join("");
  return `
    <div class="git-split-diff" aria-label="Side-by-side Git diff">
      <table class="git-split-diff-table">
        <thead class="git-split-diff-header" aria-hidden="true">
          <tr>
            <th scope="col">Before</th>
            <th scope="col">After</th>
          </tr>
        </thead>
        <tbody class="git-split-diff-body">${rendered}</tbody>
      </table>
    </div>
  `;
}

function gitSplitDiffRows(diff: string): GitSplitDiffRow[] {
  const rows: GitSplitDiffRow[] = [];
  const targets = new Map(gitDiffHunkTargets(diff).map((target) => [target.lineIndex, target]));
  const removed: string[] = [];
  let nextTargetLine = 1;

  const flushRemoved = () => {
    while (removed.length) {
      rows.push({
        kind: "changed",
        left: removed.shift(),
        leftKind: "removed",
        right: "",
        targetLine: nextTargetLine,
      });
    }
  };

  diff.split("\n").forEach((line, index) => {
    const hunkTarget = targets.get(index);
    if (line.startsWith("@@")) {
      flushRemoved();
      nextTargetLine = hunkTarget?.targetLine ?? nextTargetLine;
      rows.push({ kind: "meta", left: line, right: line });
      return;
    }
    if (line.startsWith("---") || line.startsWith("+++") || line.startsWith("diff ") || line.startsWith("index ") || line.startsWith("new file") || line.startsWith("deleted file") || line.startsWith("similarity ") || line.startsWith("rename ")) {
      flushRemoved();
      rows.push({ kind: "meta", left: line, right: line });
      return;
    }
    if (line.startsWith("\\ No newline")) {
      rows.push({ kind: "meta", left: line, right: line });
      return;
    }
    if (line.startsWith("-")) {
      removed.push(line);
      return;
    }
    if (line.startsWith("+")) {
      const left = removed.shift() ?? "";
      rows.push({
        kind: "changed",
        left,
        leftKind: left ? "removed" : undefined,
        right: line,
        rightKind: "added",
        targetLine: nextTargetLine,
      });
      nextTargetLine++;
      return;
    }
    flushRemoved();
    rows.push({
      kind: "context",
      left: line,
      leftKind: "context",
      right: line,
      rightKind: "context",
    });
    nextTargetLine++;
  });
  flushRemoved();
  return rows;
}

function gitDiffHunkTargets(diff: string): GitDiffHunkTarget[] {
  const lines = diff.split("\n");
  const targets: GitDiffHunkTarget[] = [];
  let current: {
    lineIndex: number;
    fallbackLine: number;
    nextNewLine: number;
    firstAddedLine: number | null;
  } | null = null;

  const finishCurrent = () => {
    if (!current) {
      return;
    }
    targets.push({
      lineIndex: current.lineIndex,
      targetLine: Math.max(1, current.firstAddedLine ?? current.fallbackLine),
    });
    current = null;
  };

  lines.forEach((line, index) => {
    const hunk = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line);
    if (hunk) {
      finishCurrent();
      const startLine = Number.parseInt(hunk[1], 10);
      const fallbackLine = Number.isFinite(startLine) ? Math.max(1, startLine) : 1;
      current = {
        lineIndex: index,
        fallbackLine,
        nextNewLine: fallbackLine,
        firstAddedLine: null,
      };
      return;
    }
    if (!current || line.startsWith("\\ No newline")) {
      return;
    }
    if (line.startsWith("+") && !line.startsWith("+++")) {
      current.firstAddedLine ??= Math.max(1, current.nextNewLine);
      current.nextNewLine++;
      return;
    }
    if (line.startsWith("-") && !line.startsWith("---")) {
      return;
    }
    current.nextNewLine++;
  });
  finishCurrent();
  return targets;
}

export function renderChangeMetadata(file: services.WorkspaceChangedFile): string {
  const before = file.before;
  const after = file.after;
  const beforeLabel = before ? `${formatBytes(before.bytes || 0)} ${before.binary ? "binary" : before.large ? "large" : "file"}` : "not present";
  const afterLabel = after ? `${formatBytes(after.bytes || 0)} ${after.binary ? "binary" : after.large ? "large" : "file"}` : "not present";
  return `
    <div class="change-metadata">
      <span>Before: ${escapeHtml(beforeLabel)}</span>
      <span>After: ${escapeHtml(afterLabel)}</span>
      ${before?.sha256 ? `<code title="${escapeAttribute(before.sha256)}">before ${escapeHtml(before.sha256.slice(0, 12))}</code>` : ""}
      ${after?.sha256 ? `<code title="${escapeAttribute(after.sha256)}">after ${escapeHtml(after.sha256.slice(0, 12))}</code>` : ""}
    </div>
  `;
}


export function scrollChangeReview(direction: 1 | -1) {
  const review = appRoot.querySelector<HTMLElement>("[data-change-review]");
  if (!review) {
    return;
  }
  const changes = Array.from(review.querySelectorAll<HTMLElement>("[data-change-line]")).filter(isVisibleChangeTarget);
  if (!changes.length) {
    return;
  }

  const currentIndex = changes.findIndex((change) =>
    change.classList.contains("is-current"),
  );
  let targetIndex: number;
  if (direction > 0) {
    targetIndex = currentIndex < 0 ? 0 : (currentIndex + 1) % changes.length;
  } else {
    targetIndex = currentIndex <= 0 ? changes.length - 1 : currentIndex - 1;
  }
  const target = changes[targetIndex];
  markCurrentChangeTarget(review, target);
  target.scrollIntoView({ behavior: "smooth", block: "center" });
}

export function scrollChangeReviewFile(direction: 1 | -1) {
  const review = appRoot.querySelector<HTMLElement>("[data-change-review]");
  if (!review) {
    return;
  }
  const files = Array.from(review.querySelectorAll<HTMLElement>("[data-change-file]"));
  if (!files.length) {
    return;
  }

  const currentIndex = currentChangeFileIndex(files);
  let targetIndex: number;
  if (direction > 0) {
    targetIndex = currentIndex < 0 ? 0 : (currentIndex + 1) % files.length;
  } else {
    targetIndex = currentIndex <= 0 ? files.length - 1 : currentIndex - 1;
  }
  const targetFile = files[targetIndex];
  const fileChanges = Array.from(targetFile.querySelectorAll<HTMLElement>("[data-change-line]")).filter(isVisibleChangeTarget);
  const targetLine = direction > 0 ? fileChanges[0] : fileChanges[fileChanges.length - 1];
  markCurrentChangeTarget(review, targetLine ?? targetFile);
  targetFile.scrollIntoView({ behavior: "smooth", block: "start" });
}

function isVisibleChangeTarget(target: HTMLElement): boolean {
  return Boolean(target.offsetParent);
}

export function currentChangeFileIndex(files: HTMLElement[]): number {
  const currentFileIndex = files.findIndex((file) =>
    file.classList.contains("is-current"),
  );
  if (currentFileIndex >= 0) {
    return currentFileIndex;
  }
  return files.findIndex((file) =>
    Boolean(file.querySelector("[data-change-line].is-current")),
  );
}

export function markCurrentChangeTarget(review: HTMLElement, target: HTMLElement) {
  review
    .querySelectorAll<HTMLElement>("[data-change-line].is-current")
    .forEach((change) => change.classList.remove("is-current"));
  review
    .querySelectorAll<HTMLElement>("[data-change-file].is-current")
    .forEach((file) => file.classList.remove("is-current"));

  const targetFile = target.closest<HTMLElement>("[data-change-file]");
  targetFile?.classList.add("is-current");
  if (target.matches("[data-change-line]")) {
    target.classList.add("is-current");
  }
}


export async function loadActiveChangeReview() {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  state.changeReviews.set(workspace.id, await LoadWorkspaceChangeReview(workspace.id));
}

export function applyFileChangesEvent(event: FileChangesEvent) {
  void refreshOpenCodeTabsFromDisk(event.workspaceId, getAppCallbacks().codeViewCallbacks());
  const existing = changeReviewFor(event.workspaceId);
  state.changeReviews.set(
    event.workspaceId,
    services.WorkspaceChangeReview.createFrom({
      ...existing,
      workspaceId: event.workspaceId,
      fileCount: event.fileCount,
      changeCount: event.changeCount,
    }),
  );
  if (state.openChangeReviewWorkspaces.has(event.workspaceId)) {
    void refreshWorkspaceChangeReview(event.workspaceId);
    return;
  }
  if (activeWorkspace()?.id === event.workspaceId) {
    getAppCallbacks().render();
  }
}

export async function refreshWorkspaceChangeReview(workspaceID: string) {
  try {
    state.changeReviews.set(workspaceID, await LoadWorkspaceChangeReview(workspaceID));
    if (activeWorkspace()?.id === workspaceID) {
      getAppCallbacks().render();
    }
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
}
