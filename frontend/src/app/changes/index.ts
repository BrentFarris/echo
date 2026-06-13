
import { refreshOpenCodeTabsFromDisk } from "../../codeView";
import { LoadWorkspaceChangeReview } from "../../../wailsjs/go/services/SystemService";
import { services } from "../../../wailsjs/go/models";
import { getAppCallbacks } from "../callbacks";
import { renderSpinnerLabel } from "../components";
import { appRoot } from "../dom";
import { icons } from "../icons";
import { activeWorkspace, changeReviewFor, state } from "../state";
import { pushToast } from "../toasts";
import type { FileChangesEvent } from "../types";
import { changeOperationLabel, changeSourceLabel, errorMessage, escapeAttribute, escapeHtml, fileName, formatBytes } from "../utils";

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

export function renderGitChangesDrawer(
  workspace: services.Workspace,
  review: services.WorkspaceGitChangeReview,
): string {
  const files = review.files ?? [];
  const expanded = state.expandedGitChangeWorkspaces.has(workspace.id);
  const loading = state.loadingGitChangeWorkspaces.has(workspace.id);
  const sizeLabel = expanded ? "Collapse Git changes" : "Expand Git changes";
  return `
    <aside class="change-review-backdrop ${expanded ? "is-expanded" : ""}" role="dialog" aria-modal="true" aria-labelledby="git-change-review-title">
      <section class="change-review ${expanded ? "is-expanded" : ""}" data-change-review>
        <header class="change-review-header">
          <div>
            <p class="eyebrow">${escapeHtml(workspace.displayName)}</p>
            <h2 id="git-change-review-title">Git Changes</h2>
          </div>
          <div class="change-review-header-actions">
            <button class="icon-button" type="button" title="${sizeLabel}" aria-label="${sizeLabel}" aria-pressed="${expanded}" data-action="toggle-git-changes-size">
              ${expanded ? icons.collapse : icons.expand}
            </button>
            <button class="icon-button close-button" type="button" title="Close" aria-label="Close Git changes" data-action="close-git-changes">
              ${icons.x}
            </button>
          </div>
        </header>

        <div class="change-review-summary" aria-label="Git change summary">
          <span>${escapeHtml(String(review.fileCount ?? files.length))} files</span>
          ${loading ? `<span><span class="spinner" aria-hidden="true"></span>Refreshing</span>` : ""}
        </div>

        <div class="change-review-actions">
          <button class="icon-button" type="button" title="Previous change" aria-label="Previous change" data-action="previous-change" ${files.length ? "" : "disabled"}>
            ${icons.arrowUp}
          </button>
          <button class="icon-button" type="button" title="Next change" aria-label="Next change" data-action="next-change" ${files.length ? "" : "disabled"}>
            ${icons.arrowDown}
          </button>
          <button class="secondary-button icon-text-button" type="button" data-action="refresh-git-changes" ${loading ? "disabled" : ""}>
            ${loading ? `<span class="spinner" aria-hidden="true"></span>` : icons.refresh}
            <span>Refresh</span>
          </button>
        </div>

        ${
          files.length
            ? `<div class="change-file-list">${files.map(renderGitChangedFile).join("")}</div>`
            : `<div class="empty-state compact">${loading ? renderSpinnerLabel("Loading Git changes") : "No Git changes."}</div>`
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
  return `
    <article class="change-file" data-change-file>
      <header>
        <div class="change-file-title">
          ${icons.file}
          <strong title="${escapeAttribute(file.path)}">${escapeHtml(file.path)}</strong>
        </div>
        <span class="change-operation is-${escapeAttribute(file.operation)}">${escapeHtml(changeOperationLabel(file.operation))}</span>
      </header>
      ${renderGitChangeStatus(file)}
      ${file.diffAvailable && file.diff ? renderChangeDiff(file.diff) : renderGitChangeMetadata(file)}
    </article>
  `;
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
  const changes = Array.from(review.querySelectorAll<HTMLElement>("[data-change-line]"));
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
  const fileChanges = Array.from(targetFile.querySelectorAll<HTMLElement>("[data-change-line]"));
  const targetLine = direction > 0 ? fileChanges[0] : fileChanges[fileChanges.length - 1];
  markCurrentChangeTarget(review, targetLine ?? targetFile);
  targetFile.scrollIntoView({ behavior: "smooth", block: "start" });
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
