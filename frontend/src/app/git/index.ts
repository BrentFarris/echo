import { ensureCodeViewRootLoaded, openWorkspaceCodeFileAtLine, refreshOpenCodeTabsFromDisk } from "../../codeView";
import { codeIcons } from "../../codeView/icons";
import { CommitWorkspaceGitChanges, CreateWorkspaceGitBranch, DiscardWorkspaceGitChanges, DiscardWorkspaceGitFile, LoadWorkspaceChangeReview, LoadWorkspaceGitCommit, LoadWorkspaceGitRepository, MergeWorkspaceGitBranch, StageWorkspaceGitChanges, StageWorkspaceGitFile, SwitchWorkspaceGitBranch, SyncWorkspaceGitBranch, UnstageWorkspaceGitChanges, UnstageWorkspaceGitFile } from "../../backend/services";
import { services } from "../../../wailsjs/go/models";
import type { CodeEntryKind, CodeGitChangeState } from "../../codeView/types";
import { getAppCallbacks } from "../callbacks";
import { renderSpinnerLabel } from "../components";
import { icons } from "../icons";
import { activeWorkspace, changeReviewFor, gitChangeReviewFor, gitRepositoryViewFor, state } from "../state";
import { pushToast } from "../toasts";
import { changeOperationLabel, errorMessage, escapeAttribute, escapeHtml } from "../utils";
import { markCurrentChangeTarget, renderChangeReviewPage, renderGitChangedFile, renderGitDiff } from "../changes";

type GitChangeTreeFolder = {
  kind: "folder";
  name: string;
  path: string;
  count: number;
  children: Map<string, GitChangeTreeNode>;
};

type GitChangeTreeFile = {
  kind: "file";
  name: string;
  path: string;
  displayPath: string;
  file: services.WorkspaceGitChangedFile;
};

type GitChangeTreeNode = GitChangeTreeFolder | GitChangeTreeFile;

export function renderGitRepositoryPage(
  workspace: services.Workspace,
  view: services.WorkspaceGitRepositoryView,
): string {
  const repository = view.repository ?? null;
  const repositories = view.repositories ?? [];
  const loading = state.loadingGitRepositoryWorkspaces.has(workspace.id);
  const operation = state.gitRepositoryOperations.get(workspace.id) ?? "";
  const selectedFolderID = selectedGitRepositoryFolderID(workspace.id, view);
  if (!repository && !loading && !repositories.some((item) => item.available)) {
    return renderChangeReviewPage(workspace, changeReviewFor(workspace.id));
  }
  const sidebarCollapsed = repository ? isGitChangeTreeCollapsed(workspace.id, repository.folderId) : false;
  return `
    <section class="work-panel git-repository" aria-labelledby="git-repository-title" data-change-review data-git-repository>
      <header class="panel-heading git-repository-header">
        <div>
          <p class="eyebrow">${escapeHtml(workspace.displayName)}</p>
          <h2 id="git-repository-title">Git</h2>
        </div>
      </header>

      ${repository
        ? `
          <div class="git-source-layout ${sidebarCollapsed ? "is-sidebar-collapsed" : ""}">
            ${sidebarCollapsed
              ? renderGitSourceSidebarRail(repository)
              : renderGitSourceSidebar(workspace.id, repository, repositories, selectedFolderID, loading, operation)}
            <section class="git-source-diff-panel" aria-labelledby="git-diff-title">
              <header class="git-source-diff-header">
                <div>
                  <h3 id="git-diff-title">Changes</h3>
                  <span>${escapeHtml(String(repository.fileCount ?? 0))} files</span>
                </div>
                <div class="git-source-diff-actions">
                  <button class="secondary-button icon-text-button git-diff-view-toggle" type="button" title="${state.gitDiffViewMode === "split" ? "Show inline diff" : "Show split diff"}" aria-label="${state.gitDiffViewMode === "split" ? "Show inline diff" : "Show split diff"}" data-action="toggle-git-diff-view">
                    ${state.gitDiffViewMode === "split" ? icons.code : icons.split}
                    <span>${state.gitDiffViewMode === "split" ? "Inline" : "Split"}</span>
                  </button>
                  <button class="icon-button" type="button" title="Previous change" aria-label="Previous change" data-action="previous-change" ${repository.fileCount ? "" : "disabled"}>
                    ${icons.arrowUp}
                  </button>
                  <button class="icon-button" type="button" title="Next change" aria-label="Next change" data-action="next-change" ${repository.fileCount ? "" : "disabled"}>
                    ${icons.arrowDown}
                  </button>
                </div>
              </header>
              ${renderGitWorkingChanges(repository)}
            </section>
          </div>
        `
        : `<div class="empty-state compact">${loading ? renderSpinnerLabel("Loading Git") : "No manageable Git repository."}</div>`}
    </section>
  `;
}

function renderGitSourceSidebar(
  workspaceID: string,
  repository: services.WorkspaceGitRepositoryStatus,
  repositories: services.WorkspaceGitRepositorySummary[],
  selectedFolderID: string,
  loading: boolean,
  operation: string,
): string {
  return `
    <aside class="git-source-sidebar" aria-label="Source Control">
      <div class="git-source-sidebar-top">
        <div class="git-source-title-row">
          ${renderGitRepositorySummary(repository, loading, operation)}
          <div class="git-source-title-actions">
            <button class="icon-button git-change-tree-toggle" type="button" title="Collapse source control" aria-label="Collapse source control" data-action="toggle-git-sidebar">
              ${icons.collapse}
            </button>
            ${renderGitSourceMenu(workspaceID, repository, loading, operation)}
          </div>
        </div>
        ${renderGitRepositoryPicker(repositories, selectedFolderID, loading, operation)}
        ${renderGitCommitForm(workspaceID, repository, operation)}
        ${renderGitSourceChangeSections(repository, operation)}
      </div>
      <section class="git-source-history" aria-labelledby="git-history-title">
        <header>
          <h3 id="git-history-title">History</h3>
          <span>${escapeHtml(String((repository.commits ?? []).length))}</span>
        </header>
        ${renderGitCommitHistory(workspaceID, repository)}
      </section>
    </aside>
  `;
}

function renderGitRepositoryPicker(
  repositories: services.WorkspaceGitRepositorySummary[],
  selectedFolderID: string,
  loading: boolean,
  operation: string,
): string {
  const available = repositories.filter((item) => item.available);
  if (available.length <= 1) {
    return "";
  }
  return `
    <label class="field git-repository-picker git-source-repository-picker">
      <span>Repository</span>
      <select data-git-repository-select ${loading || operation ? "disabled" : ""}>
        ${repositories.length
          ? repositories.map((item) => renderGitRepositoryOption(item, selectedFolderID)).join("")
          : `<option value="">No repositories</option>`}
      </select>
    </label>
  `;
}

function renderGitSourceSidebarRail(repository: services.WorkspaceGitRepositoryStatus): string {
  return `
    <aside class="git-source-sidebar-rail" aria-label="Source Control collapsed">
      <button class="icon-button git-change-tree-toggle" type="button" title="Expand source control" aria-label="Expand source control" data-action="toggle-git-sidebar">
        ${icons.expand}
      </button>
      <span>${escapeHtml(String(repository.fileCount ?? 0))}</span>
    </aside>
  `;
}

function renderGitSourceMenu(
  workspaceID: string,
  repository: services.WorkspaceGitRepositoryStatus,
  loading: boolean,
  operation: string,
): string {
  const busy = loading || Boolean(operation);
  return `
    <details class="git-source-menu">
      <summary class="icon-button" title="More Git actions" aria-label="More Git actions">${icons.moreHorizontal}</summary>
      <div class="git-source-menu-popover" role="menu">
        <button type="button" role="menuitem" data-action="refresh-git-changes" ${busy ? "disabled" : ""}>
          ${loading ? `<span class="spinner" aria-hidden="true"></span>` : icons.refresh}
          <span>Refresh</span>
        </button>
        <button type="button" role="menuitem" data-action="sync-git-branch" ${busy ? "disabled" : ""}>
          ${operation === "Syncing branch" ? `<span class="spinner" aria-hidden="true"></span>` : icons.refresh}
          <span>${escapeHtml(gitSyncMenuLabel(repository))}</span>
        </button>
        <button class="is-danger" type="button" role="menuitem" data-action="revert-git-changes" ${repository.fileCount && !busy ? "" : "disabled"}>
          ${operation === "Reverting changes" ? `<span class="spinner" aria-hidden="true"></span>` : icons.undo}
          <span>Revert All Changes</span>
        </button>
        <hr />
        ${renderGitBranchControls(workspaceID, repository, operation)}
      </div>
    </details>
  `;
}

function gitSyncMenuLabel(repository: services.WorkspaceGitRepositoryStatus): string {
  const ahead = Math.max(0, repository.aheadCount ?? 0);
  const behind = Math.max(0, repository.behindCount ?? 0);
  return ahead > 0 || behind > 0 ? `Sync (${behind} down / ${ahead} up)` : "Sync";
}

function renderGitSourceChangeSections(repository: services.WorkspaceGitRepositoryStatus, operation: string): string {
  const files = repository.files ?? [];
  const staged = files.filter((file) => file.staged);
  const unstaged = files.filter((file) => file.unstaged);
  return `
    <div class="git-source-change-sections ${staged.length ? "has-staged" : "has-unstaged-only"}">
      ${staged.length ? renderGitSourceFileSection("Staged Changes", staged, "unstage", operation) : ""}
      ${renderGitSourceFileSection("Changes", unstaged, "stage", operation)}
    </div>
  `;
}

function renderGitSourceFileSection(
  title: string,
  files: services.WorkspaceGitChangedFile[],
  mode: "stage" | "unstage",
  operation: string,
): string {
  const busy = Boolean(operation);
  const action = mode === "stage" ? "stage-git-file" : "unstage-git-file";
  const allAction = mode === "stage" ? "stage-git-changes" : "unstage-git-changes";
  const icon = mode === "stage" ? icons.plus : icons.undo;
  const fileActionLabel = mode === "stage" ? "Stage file" : "Unstage file";
  const allActionLabel = mode === "stage" ? "Stage all changes" : "Unstage all changes";
  return `
    <section class="git-source-file-section">
      <header>
        <button class="git-source-section-heading" type="button" aria-label="${escapeAttribute(title)}">
          ${icons.arrowDown}
          <span>${escapeHtml(title)}</span>
          <em>${escapeHtml(String(files.length))}</em>
        </button>
        <button class="icon-button" type="button" title="${escapeAttribute(allActionLabel)}" aria-label="${escapeAttribute(allActionLabel)}" data-action="${allAction}" ${files.length && !busy ? "" : "disabled"}>
          ${icon}
        </button>
      </header>
      ${files.length
        ? `<div class="git-source-file-list">${files.map((file) => renderGitSourceFileRow(file, action, icon, fileActionLabel, busy)).join("")}</div>`
        : `<div class="git-source-empty">No files.</div>`}
    </section>
  `;
}

function renderGitSourceFileRow(
  file: services.WorkspaceGitChangedFile,
  action: string,
  icon: string,
  actionLabel: string,
  busy: boolean,
): string {
  const displayPath = displayGitChangePath(file.path);
  const name = displayPath.split("/").pop() || displayPath;
  const normalizedPath = normalizeGitChangePath(file.path);
  return `
    <div class="git-source-file-row" title="${escapeAttribute(displayPath)}">
      <button class="git-source-file-main" type="button" data-git-change-file="${escapeAttribute(normalizedPath)}">
        <span class="git-source-file-status is-${escapeAttribute(file.operation)}">${escapeHtml(gitSourceStatusLetter(file))}</span>
        <span class="git-source-file-name">${escapeHtml(name)}</span>
        <span class="git-source-file-path">${escapeHtml(displayPath === name ? "" : displayPath.slice(0, Math.max(0, displayPath.length - name.length - 1)))}</span>
      </button>
      <button class="icon-button git-source-file-action" type="button" title="${escapeAttribute(actionLabel)}" aria-label="${escapeAttribute(`${actionLabel}: ${displayPath}`)}" data-action="${escapeAttribute(action)}" data-git-file-path="${escapeAttribute(file.path)}" ${busy ? "disabled" : ""}>
        ${icon}
      </button>
    </div>
  `;
}

function gitSourceStatusLetter(file: services.WorkspaceGitChangedFile): string {
  switch (file.operation) {
    case "created":
      return "A";
    case "deleted":
      return "D";
    case "renamed":
      return "R";
    case "copied":
      return "C";
    case "conflicted":
      return "U";
    default:
      return "M";
  }
}

function renderGitWorkingChanges(repository: services.WorkspaceGitRepositoryStatus): string {
  const files = repository.files ?? [];
  if (!files.length) {
    return `<div class="empty-state compact">No Git changes.</div>`;
  }
  return `
    <div class="change-file-list git-change-file-list" data-git-change-file-list>
      ${files.map(renderGitChangedFile).join("")}
    </div>
  `;
}

function renderGitChangeFileTree(workspaceID: string, repository: services.WorkspaceGitRepositoryStatus): string {
  const root = buildGitChangeTree(repository.files ?? []);
  const collapsed = gitCollapsedChangeFolders(workspaceID, repository.folderId);
  const children = sortedGitChangeTreeChildren(root);
  return `
    <nav class="git-change-tree" role="tree" data-git-change-tree>
      <header>
        <strong>Files</strong>
        <div class="git-change-tree-header-actions">
          <span>${escapeHtml(String(repository.fileCount ?? children.length))}</span>
          <button class="icon-button git-change-tree-toggle" type="button" title="Collapse changed files" aria-label="Collapse changed files" data-git-change-tree-toggle>
            ${icons.collapse}
          </button>
        </div>
      </header>
      <div class="git-change-tree-list">
        ${children.map((child) => renderGitChangeTreeNode(child, collapsed, 0)).join("")}
      </div>
    </nav>
  `;
}

function renderGitChangeTreeCollapsedRail(repository: services.WorkspaceGitRepositoryStatus): string {
  return `
    <div class="git-change-tree-collapsed" aria-label="Changed files collapsed">
      <button class="icon-button git-change-tree-toggle" type="button" title="Expand changed files" aria-label="Expand changed files" data-git-change-tree-toggle>
        ${icons.expand}
      </button>
      <span>${escapeHtml(String(repository.fileCount ?? 0))}</span>
    </div>
  `;
}

function buildGitChangeTree(files: services.WorkspaceGitChangedFile[]): GitChangeTreeFolder {
  const root: GitChangeTreeFolder = {
    kind: "folder",
    name: "",
    path: "",
    count: 0,
    children: new Map<string, GitChangeTreeNode>(),
  };
  files.forEach((file) => {
    const displayPath = displayGitChangePath(file.path);
    if (!displayPath) {
      return;
    }
    const segments = displayPath.split("/").filter(Boolean);
    if (!segments.length) {
      return;
    }
    let folder = root;
    folder.count++;
    segments.slice(0, -1).forEach((segment) => {
      const nextPath = folder.path ? `${folder.path}/${normalizeGitChangePath(segment)}` : normalizeGitChangePath(segment);
      const key = normalizeGitChangePath(segment);
      let child = folder.children.get(key);
      if (!child || child.kind !== "folder") {
        child = {
          kind: "folder",
          name: segment,
          path: nextPath,
          count: 0,
          children: new Map<string, GitChangeTreeNode>(),
        };
        folder.children.set(key, child);
      }
      child.count++;
      folder = child;
    });
    const name = segments[segments.length - 1] ?? displayPath;
    folder.children.set(normalizeGitChangePath(name), {
      kind: "file",
      name,
      path: normalizeGitChangePath(displayPath),
      displayPath,
      file,
    });
  });
  return root;
}

function renderGitChangeTreeNode(node: GitChangeTreeNode, collapsed: Set<string>, depth: number): string {
  if (node.kind === "file") {
    const statusLabel = changeOperationLabel(node.file.operation);
    return `
      <button
        class="git-change-tree-row is-file is-${escapeAttribute(node.file.operation)}"
        type="button"
        role="treeitem"
        title="${escapeAttribute(node.displayPath)}"
        style="--tree-depth: ${depth}"
        data-git-change-file="${escapeAttribute(node.path)}"
      >
        <span class="git-change-tree-spacer"></span>
        <span class="git-change-tree-icon">${codeIcons.file}</span>
        <span class="git-change-tree-name">${escapeHtml(node.name)}</span>
        <span class="git-change-tree-status">${escapeHtml(statusLabel)}</span>
      </button>
    `;
  }

  const isCollapsed = collapsed.has(node.path);
  return `
    <div class="git-change-tree-folder ${isCollapsed ? "is-collapsed" : "is-expanded"}">
      <button
        class="git-change-tree-row is-folder ${isCollapsed ? "" : "is-expanded"}"
        type="button"
        role="treeitem"
        aria-expanded="${!isCollapsed}"
        title="${escapeAttribute(node.name)}"
        style="--tree-depth: ${depth}"
        data-git-change-folder="${escapeAttribute(node.path)}"
      >
        <span class="git-change-tree-chevron">${codeIcons.chevron}</span>
        <span class="git-change-tree-icon">${codeIcons.folder}</span>
        <span class="git-change-tree-name">${escapeHtml(node.name)}</span>
        <span class="git-change-tree-count">${escapeHtml(String(node.count))}</span>
      </button>
      ${isCollapsed
        ? ""
        : `<div class="git-change-tree-children">${sortedGitChangeTreeChildren(node).map((child) => renderGitChangeTreeNode(child, collapsed, depth + 1)).join("")}</div>`}
    </div>
  `;
}

function sortedGitChangeTreeChildren(folder: GitChangeTreeFolder): GitChangeTreeNode[] {
  return [...folder.children.values()].sort((left, right) => {
    if (left.kind !== right.kind) {
      return left.kind === "folder" ? -1 : 1;
    }
    return left.name.localeCompare(right.name, undefined, { sensitivity: "base" });
  });
}

function displayGitChangePath(path: string): string {
  return path.trim().replaceAll("\\", "/").replace(/^\/+/, "");
}

function renderGitRefreshOrSyncButton(
  repository: services.WorkspaceGitRepositoryStatus | null,
  loading: boolean,
  operation: string,
): string {
  const ahead = Math.max(0, repository?.aheadCount ?? 0);
  const behind = Math.max(0, repository?.behindCount ?? 0);
  const pending = ahead > 0 || behind > 0;
  const busy = loading || Boolean(operation);
  if (!pending) {
    return `
      <button class="secondary-button icon-text-button" type="button" data-action="refresh-git-changes" ${busy ? "disabled" : ""}>
        ${loading ? `<span class="spinner" aria-hidden="true"></span>` : icons.refresh}
        <span>Refresh</span>
      </button>
    `;
  }
  const label = `Sync (${behind} down, ${ahead} up)`;
  return `
    <button class="secondary-button icon-text-button git-sync-button" type="button" title="${escapeAttribute(label)}" aria-label="${escapeAttribute(label)}" data-action="sync-git-branch" ${busy ? "disabled" : ""}>
      ${operation === "Syncing branch" ? `<span class="spinner" aria-hidden="true"></span>` : icons.refresh}
      <span>Sync</span>
      <span class="git-sync-counts" aria-hidden="true">${escapeHtml(`${behind} down / ${ahead} up`)}</span>
    </button>
  `;
}

function renderGitRepositoryOption(repository: services.WorkspaceGitRepositorySummary, selectedFolderID: string): string {
  const label = `${repository.label}${repository.available ? "" : " unavailable"}`;
  return `
    <option value="${escapeAttribute(repository.folderId)}" ${repository.folderId === selectedFolderID ? "selected" : ""} ${repository.available ? "" : "disabled"}>
      ${escapeHtml(label)}
    </option>
  `;
}

function renderGitRepositorySummary(
  repository: services.WorkspaceGitRepositoryStatus | null,
  loading: boolean,
  operation: string,
): string {
  if (!repository) {
    return `
      <div class="change-review-summary git-repository-summary" aria-label="Git summary">
        ${loading ? `<span><span class="spinner" aria-hidden="true"></span>Loading</span>` : ""}
      </div>
    `;
  }
  const branch = repository.detached
    ? repository.shortHead
      ? `detached ${repository.shortHead}`
      : "detached"
    : repository.currentBranch || "unborn";
  return `
    <div class="git-repository-summary" aria-label="Git summary">
      <span>${escapeHtml(branch)}</span>
      <span>${escapeHtml(String(repository.fileCount ?? 0))} files</span>
      ${operation ? `<span><span class="spinner" aria-hidden="true"></span>${escapeHtml(operation)}</span>` : ""}
      ${loading && !operation ? `<span><span class="spinner" aria-hidden="true"></span>Refreshing</span>` : ""}
    </div>
  `;
}

function renderGitCommitForm(workspaceID: string, repository: services.WorkspaceGitRepositoryStatus, operation: string): string {
  const key = gitRepositoryDraftKey(workspaceID, repository.folderId);
  const draft = state.gitCommitMessageDrafts.get(key) ?? "";
  const busy = Boolean(operation);
  return `
    <form class="git-form git-commit-form" data-git-commit-form>
      <label>
        <span>Commit message</span>
        <textarea rows="3" spellcheck="true" data-git-commit-message ${busy ? "disabled" : ""}>${escapeHtml(draft)}</textarea>
      </label>
      <button class="primary-button icon-text-button git-commit-button" type="submit" ${(repository.stagedFileCount ?? 0) && draft.trim() && !busy ? "" : "disabled"}>
        ${busy && operation === "Committing" ? `<span class="spinner" aria-hidden="true"></span>` : icons.check}
        <span>Commit</span>
      </button>
    </form>
  `;
}

function renderGitBranchControls(workspaceID: string, repository: services.WorkspaceGitRepositoryStatus, operation: string): string {
  const key = gitRepositoryDraftKey(workspaceID, repository.folderId);
  const branches = repository.branches ?? [];
  const currentBranch = repository.currentBranch ?? "";
  const branchDraft = state.gitNewBranchDrafts.get(key) ?? "";
  const switchDraft = state.gitSwitchBranchDrafts.get(key) ?? branchSelectDefault(branches, currentBranch);
  const mergeDraft = state.gitMergeBranchDrafts.get(key) ?? branchSelectDefault(branches, currentBranch);
  const busy = Boolean(operation);
  return `
    <div class="git-branch-controls">
      <form class="git-form" data-git-create-branch-form>
        <label>
          <span>New branch</span>
          <input type="text" autocomplete="off" data-git-new-branch-name value="${escapeAttribute(branchDraft)}" ${busy ? "disabled" : ""} />
        </label>
        <button class="secondary-button icon-text-button" type="submit" ${branchDraft.trim() && !busy ? "" : "disabled"}>
          ${busy && operation === "Creating branch" ? `<span class="spinner" aria-hidden="true"></span>` : icons.plus}
          <span>Create</span>
        </button>
      </form>
      <form class="git-form git-branch-form" data-git-switch-branch-form>
        <label>
          <span>Switch branch</span>
          <select data-git-switch-branch-select ${busy || repository.dirty ? "disabled" : ""}>
            ${renderBranchOptions(branches, switchDraft, currentBranch, true)}
          </select>
        </label>
        <button class="secondary-button icon-text-button" type="submit" ${switchDraft && switchDraft !== currentBranch && !repository.dirty && !busy ? "" : "disabled"}>
          ${busy && operation === "Switching branch" ? `<span class="spinner" aria-hidden="true"></span>` : icons.git}
          <span>Switch</span>
        </button>
      </form>
      <form class="git-form git-branch-form" data-git-merge-branch-form>
        <label>
          <span>Merge branch</span>
          <select data-git-merge-branch-select ${busy || repository.dirty ? "disabled" : ""}>
            ${renderBranchOptions(branches, mergeDraft, currentBranch, true)}
          </select>
        </label>
        <button class="secondary-button icon-text-button" type="submit" ${mergeDraft && mergeDraft !== currentBranch && !repository.dirty && !busy ? "" : "disabled"}>
          ${busy && operation === "Merging branch" ? `<span class="spinner" aria-hidden="true"></span>` : icons.git}
          <span>Merge</span>
        </button>
      </form>
    </div>
  `;
}

function renderBranchOptions(
  branches: services.WorkspaceGitBranch[],
  selected: string,
  currentBranch: string,
  disableCurrent: boolean,
): string {
  if (!branches.length) {
    return `<option value="">No branches</option>`;
  }
  return branches
    .map((branch) => {
      const current = branch.name === currentBranch;
      return `
      <option value="${escapeAttribute(branch.name)}" ${branch.name === selected ? "selected" : ""} ${disableCurrent && current ? "disabled" : ""}>
        ${escapeHtml(`${branch.name}${current ? " (current)" : ""}`)}
      </option>
    `;
    })
    .join("");
}

function branchSelectDefault(branches: services.WorkspaceGitBranch[], currentBranch: string): string {
  return branches.find((branch) => branch.name !== currentBranch)?.name ?? "";
}

function renderGitCommitHistory(workspaceID: string, repository: services.WorkspaceGitRepositoryStatus): string {
  const commits = repository.commits ?? [];
  if (!commits.length) {
    return `<div class="empty-state compact">No commits.</div>`;
  }
  const selectedHash = state.selectedGitCommitHashes.get(workspaceID) ?? "";
  return `
    <div class="git-commit-list" role="list">
      ${commits.map((commit) => renderGitCommitItem(workspaceID, repository, commit, selectedHash)).join("")}
    </div>
  `;
}

function renderGitCommitItem(
  workspaceID: string,
  repository: services.WorkspaceGitRepositoryStatus,
  commit: services.WorkspaceGitCommit,
  selectedHash: string,
): string {
  const selected = selectedHash === commit.hash;
  const key = gitCommitDetailKey(workspaceID, repository.folderId, commit.hash);
  return `
    <article class="git-commit-item ${selected ? "is-selected" : ""}">
      <button class="git-commit-main" type="button" aria-expanded="${selected}" data-action="select-git-commit" data-commit-hash="${escapeAttribute(commit.hash)}">
        <span class="git-commit-dot" aria-hidden="true"></span>
        <span class="git-commit-text">
          <strong title="${escapeAttribute(commit.subject)}">${escapeHtml(commit.subject || commit.shortHash)}</strong>
          <span>${escapeHtml(commit.shortHash)} - ${escapeHtml(commit.authorName || "Unknown")} - ${escapeHtml(formatGitDate(commit.authoredAt))}</span>
        </span>
      </button>
      ${selected ? renderGitCommitExpandedFiles(key) : ""}
    </article>
  `;
}

function renderGitCommitExpandedFiles(key: string): string {
  if (state.loadingGitCommitDetails.has(key)) {
    return `<div class="git-commit-files">${renderSpinnerLabel("Loading commit")}</div>`;
  }
  const detail = state.gitCommitDetails.get(key);
  if (!detail) {
    return "";
  }
  const files = detail.files ?? [];
  if (!files.length) {
    return `<div class="git-commit-files"><span>No changed files.</span></div>`;
  }
  return `<div class="git-commit-files">${files.map(renderGitCommitChangedFile).join("")}</div>`;
}

function renderGitCommitChangedFile(file: services.WorkspaceGitChangedFile): string {
  const displayPath = displayGitChangePath(file.path);
  return `
    <div class="git-commit-file-row" title="${escapeAttribute(displayPath)}">
      <span class="git-source-file-status is-${escapeAttribute(file.operation)}">${escapeHtml(gitSourceStatusLetter(file))}</span>
      <span>${escapeHtml(displayPath)}</span>
    </div>
  `;
}

export function bindGitEvents(root: ParentNode) {
  bindGitSplitDiffScroll(root);
  bindGitChangeTree(root);
  root
    .querySelectorAll<HTMLSelectElement>("[data-git-repository-select]")
    .forEach((select) => select.addEventListener("change", () => handleGitRepositorySelect(select)));
  root
    .querySelectorAll<HTMLFormElement>("[data-git-commit-form]")
    .forEach((form) => form.addEventListener("submit", handleGitCommitSubmit));
  root
    .querySelectorAll<HTMLFormElement>("[data-git-create-branch-form]")
    .forEach((form) => form.addEventListener("submit", handleGitCreateBranchSubmit));
  root
    .querySelectorAll<HTMLFormElement>("[data-git-switch-branch-form]")
    .forEach((form) => form.addEventListener("submit", handleGitSwitchBranchSubmit));
  root
    .querySelectorAll<HTMLFormElement>("[data-git-merge-branch-form]")
    .forEach((form) => form.addEventListener("submit", handleGitMergeBranchSubmit));
  root
    .querySelectorAll<HTMLTextAreaElement | HTMLInputElement>("[data-git-commit-message], [data-git-new-branch-name]")
    .forEach((input) => input.addEventListener("input", handleGitDraftInput));
  root
    .querySelectorAll<HTMLTextAreaElement>("[data-git-commit-message]")
    .forEach((textarea) => textarea.addEventListener("keydown", handleGitCommitMessageKeydown));
  root
    .querySelectorAll<HTMLSelectElement>("[data-git-switch-branch-select], [data-git-merge-branch-select]")
    .forEach((select) => select.addEventListener("change", handleGitBranchSelectChange));
}

function bindGitChangeTree(root: ParentNode) {
  root
    .querySelectorAll<HTMLButtonElement>("[data-git-change-tree-toggle]")
    .forEach((button) => button.addEventListener("click", handleGitChangeTreeToggle));
  root
    .querySelectorAll<HTMLButtonElement>("[data-git-change-folder]")
    .forEach((button) => button.addEventListener("click", handleGitChangeFolderToggle));
  root
    .querySelectorAll<HTMLButtonElement>("[data-git-change-file]")
    .forEach((button) => button.addEventListener("click", handleGitChangeFileSelect));
}

function handleGitChangeTreeToggle() {
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  if (!workspace || !repository) {
    return;
  }
  const key = gitChangeTreeStateKey(workspace.id, repository.folderId);
  if (state.collapsedGitChangeTrees.has(key)) {
    state.collapsedGitChangeTrees.delete(key);
  } else {
    state.collapsedGitChangeTrees.add(key);
  }
  getAppCallbacks().render();
}

function handleGitChangeFolderToggle(event: MouseEvent) {
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  const button = event.currentTarget as HTMLButtonElement;
  const folderPath = button.dataset.gitChangeFolder ?? "";
  if (!workspace || !repository || !folderPath) {
    return;
  }
  const collapsed = gitCollapsedChangeFolders(workspace.id, repository.folderId);
  if (collapsed.has(folderPath)) {
    collapsed.delete(folderPath);
  } else {
    collapsed.add(folderPath);
  }
  getAppCallbacks().render();
}

function handleGitChangeFileSelect(event: MouseEvent) {
  const button = event.currentTarget as HTMLButtonElement;
  const targetPath = button.dataset.gitChangeFile ?? "";
  if (!targetPath) {
    return;
  }
  const repositoryRoot = button.closest<HTMLElement>("[data-git-repository]");
  const target = Array.from(repositoryRoot?.querySelectorAll<HTMLElement>("[data-git-change-file-path]") ?? [])
    .find((element) => element.dataset.gitChangeFilePath === targetPath);
  if (!target) {
    return;
  }
  button
    .closest<HTMLElement>("[data-git-change-tree]")
    ?.querySelectorAll<HTMLElement>(".git-change-tree-row.is-selected")
    .forEach((row) => row.classList.remove("is-selected"));
  button.classList.add("is-selected");
  const review = target.closest<HTMLElement>("[data-change-review]");
  if (review) {
    markCurrentChangeTarget(review, target);
  }
  target.scrollIntoView({ behavior: "smooth", block: "start" });
}

function bindGitSplitDiffScroll(root: ParentNode) {
  root.querySelectorAll<HTMLElement>("[data-git-split-diff]").forEach((diff) => {
    const panes = Array.from(diff.querySelectorAll<HTMLElement>("[data-git-split-scroll]"));
    const shared = diff.querySelector<HTMLElement>("[data-git-split-shared-scroll]");
    const spacer = diff.querySelector<HTMLElement>("[data-git-split-shared-spacer]");
    if (!shared || !spacer || shared.dataset.gitSplitScrollBound) {
      return;
    }
    const update = () => {
      updateGitSplitDiffSharedScroll(shared, spacer, panes);
      syncGitSplitDiffFromShared(shared, panes);
    };
    shared.dataset.gitSplitScrollBound = "true";
    shared.addEventListener("scroll", () => {
      updateGitSplitDiffSharedScroll(shared, spacer, panes);
      syncGitSplitDiffFromShared(shared, panes);
    });
    panes.forEach((pane) => {
      pane.addEventListener("wheel", (event) => {
        handleGitSplitDiffPaneWheel(event, shared, spacer, panes);
      }, { passive: false });
    });
    shared.addEventListener("pointerenter", update);
    shared.addEventListener("focus", update);
    window.requestAnimationFrame(update);
  });
}

function handleGitSplitDiffPaneWheel(event: WheelEvent, shared: HTMLElement, spacer: HTMLElement, panes: HTMLElement[]) {
  const deltaX = normalizedGitSplitWheelDeltaX(event, shared);
  if (!deltaX || Math.abs(deltaX) < Math.abs(event.deltaY)) {
    return;
  }
  updateGitSplitDiffSharedScroll(shared, spacer, panes);
  const maxScroll = Math.max(0, shared.scrollWidth - shared.clientWidth);
  if (!maxScroll) {
    return;
  }
  const nextScrollLeft = Math.max(0, Math.min(maxScroll, shared.scrollLeft + deltaX));
  if (nextScrollLeft === shared.scrollLeft) {
    return;
  }
  event.preventDefault();
  shared.scrollLeft = nextScrollLeft;
  syncGitSplitDiffFromShared(shared, panes);
}

function normalizedGitSplitWheelDeltaX(event: WheelEvent, shared: HTMLElement): number {
  const rawDelta = event.deltaX || (event.shiftKey ? event.deltaY : 0);
  if (!rawDelta) {
    return 0;
  }
  if (event.deltaMode === WheelEvent.DOM_DELTA_LINE) {
    return rawDelta * 16;
  }
  if (event.deltaMode === WheelEvent.DOM_DELTA_PAGE) {
    return rawDelta * shared.clientWidth;
  }
  return rawDelta;
}

function updateGitSplitDiffSharedScroll(shared: HTMLElement, spacer: HTMLElement, panes: HTMLElement[]) {
  const maxScroll = Math.max(0, ...panes.map((pane) => pane.scrollWidth - pane.clientWidth));
  spacer.style.width = `${shared.clientWidth + maxScroll}px`;
  if (shared.scrollLeft > maxScroll) {
    shared.scrollLeft = maxScroll;
  }
}

function syncGitSplitDiffFromShared(shared: HTMLElement, panes: HTMLElement[]) {
  const offset = shared.scrollLeft;
  panes.forEach((pane) => {
    const maxScroll = Math.max(0, pane.scrollWidth - pane.clientWidth);
    pane.scrollLeft = Math.min(offset, maxScroll);
  });
}

export async function openWorkspaceGitRepository(workspaceID: string) {
  state.openGitChangeWorkspaces.add(workspaceID);
  await refreshWorkspaceGitRepository(workspaceID, selectedGitRepositoryFolderID(workspaceID, gitRepositoryViewFor(workspaceID)), true);
}

export async function loadWorkspaceChangesSummary(workspaceID: string) {
  if (!workspaceID || state.loadingGitRepositoryWorkspaces.has(workspaceID)) {
    return;
  }
  state.loadingGitRepositoryWorkspaces.add(workspaceID);
  try {
    const view = await LoadWorkspaceGitRepository(
      workspaceID,
      selectedGitRepositoryFolderID(workspaceID, gitRepositoryViewFor(workspaceID)),
    );
    storeGitRepositoryView(workspaceID, view);
  } catch (error) {
    if (!isNoManageableGitRepositoryError(error)) {
      return;
    }
    state.gitRepositoryViews.set(
      workspaceID,
      services.WorkspaceGitRepositoryView.createFrom({
        workspaceId: workspaceID,
        selectedFolderId: "",
        repositories: [],
        repository: null,
      }),
    );
    try {
      state.changeReviews.set(workspaceID, await LoadWorkspaceChangeReview(workspaceID));
    } catch {
    }
  } finally {
    state.loadingGitRepositoryWorkspaces.delete(workspaceID);
    if (activeWorkspace()?.id === workspaceID) {
      getAppCallbacks().render();
    }
  }
}

export async function refreshWorkspaceGitRepository(
  workspaceID: string,
  folderID = selectedGitRepositoryFolderID(workspaceID, gitRepositoryViewFor(workspaceID)),
  fallbackToChanges = false,
) {
  if (state.loadingGitRepositoryWorkspaces.has(workspaceID)) {
    return;
  }
  state.loadingGitRepositoryWorkspaces.add(workspaceID);
  getAppCallbacks().render();
  try {
    const view = await LoadWorkspaceGitRepository(workspaceID, folderID);
    storeGitRepositoryView(workspaceID, view);
  } catch (error) {
    if (fallbackToChanges && isNoManageableGitRepositoryError(error)) {
      state.gitRepositoryViews.set(
        workspaceID,
        services.WorkspaceGitRepositoryView.createFrom({
          workspaceId: workspaceID,
          selectedFolderId: "",
          repositories: [],
          repository: null,
        }),
      );
      try {
        state.changeReviews.set(workspaceID, await LoadWorkspaceChangeReview(workspaceID));
      } catch (changeError) {
        pushToast(errorMessage(changeError), "error");
      }
    } else {
      pushToast(errorMessage(error), "error");
    }
  } finally {
    state.loadingGitRepositoryWorkspaces.delete(workspaceID);
    getAppCallbacks().render();
  }
}

export function gitChangedLineNumbersForFile(workspaceID: string, path: string): number[] {
  const normalizedPath = normalizeGitChangePath(path);
  if (!workspaceID || !normalizedPath) {
    return [];
  }
  const seen = new Set<number>();
  for (const file of gitChangedFilesForWorkspace(workspaceID)) {
    if (normalizeGitChangePath(file.path) !== normalizedPath || !file.diffAvailable || !file.diff) {
      continue;
    }
    gitChangedLineNumbersFromDiff(file.diff).forEach((line) => seen.add(line));
  }
  return [...seen].sort((left, right) => left - right);
}

export function gitChangeStateForPath(
  workspaceID: string,
  path: string,
  kind: CodeEntryKind,
): CodeGitChangeState {
  const normalizedPath = normalizeGitChangePath(path);
  if (!workspaceID || !normalizedPath) {
    return "";
  }
  const files = gitChangedFilesForWorkspace(workspaceID);
  if (kind === "directory") {
    const prefix = normalizedPath.endsWith("/") ? normalizedPath : `${normalizedPath}/`;
    let hasModified = false;
    for (const file of files) {
      const changedPath = normalizeGitChangePath(file.path);
      if (!changedPath.startsWith(prefix)) {
        continue;
      }
      if (gitFileTreeChangeState(file) === "created") {
        return "created";
      }
      hasModified = true;
    }
    return hasModified ? "modified" : "";
  }
  const file = files.find((candidate) => normalizeGitChangePath(candidate.path) === normalizedPath);
  return file ? gitFileTreeChangeState(file) : "";
}

function gitChangedFilesForWorkspace(workspaceID: string): services.WorkspaceGitChangedFile[] {
  return [
    ...(gitRepositoryViewFor(workspaceID).repository?.files ?? []),
    ...(gitChangeReviewFor(workspaceID).files ?? []),
  ];
}

function gitFileTreeChangeState(file: services.WorkspaceGitChangedFile): CodeGitChangeState {
  return file.operation === "created" ? "created" : "modified";
}

function gitChangedLineNumbersFromDiff(diff: string): number[] {
  const lines = diff.replaceAll("\r\n", "\n").split("\n");
  if (lines[lines.length - 1] === "") {
    lines.pop();
  }
  const changed = new Set<number>();
  let nextNewLine = 0;
  let inHunk = false;

  for (const line of lines) {
    const hunk = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line);
    if (hunk) {
      const start = Number.parseInt(hunk[1], 10);
      nextNewLine = Number.isFinite(start) ? Math.max(1, start) : 1;
      inHunk = true;
      continue;
    }
    if (!inHunk || line.startsWith("\\ No newline")) {
      continue;
    }
    if (line.startsWith("+") && !line.startsWith("+++")) {
      changed.add(Math.max(1, nextNewLine));
      nextNewLine++;
      continue;
    }
    if (line.startsWith("-") && !line.startsWith("---")) {
      continue;
    }
    nextNewLine++;
  }

  return [...changed];
}

function normalizeGitChangePath(path: string): string {
  return path.trim().replaceAll("\\", "/").replace(/^\/+/, "").toLowerCase();
}

function isNoManageableGitRepositoryError(error: unknown): boolean {
  return errorMessage(error).toLowerCase().includes("no manageable git repositories");
}

export async function selectGitCommit(hash: string) {
  const workspace = activeWorkspace();
  if (!workspace || !hash) {
    return;
  }
  if (state.selectedGitCommitHashes.get(workspace.id) === hash) {
    state.selectedGitCommitHashes.delete(workspace.id);
    getAppCallbacks().render();
    return;
  }
  const view = gitRepositoryViewFor(workspace.id);
  const folderID = selectedGitRepositoryFolderID(workspace.id, view);
  if (!folderID) {
    return;
  }
  state.selectedGitCommitHashes.set(workspace.id, hash);
  const key = gitCommitDetailKey(workspace.id, folderID, hash);
  if (state.gitCommitDetails.has(key)) {
    getAppCallbacks().render();
    return;
  }
  state.loadingGitCommitDetails.add(key);
  getAppCallbacks().render();
  try {
    state.gitCommitDetails.set(key, await LoadWorkspaceGitCommit(workspace.id, folderID, hash));
  } catch (error) {
    pushToast(errorMessage(error), "error");
  } finally {
    state.loadingGitCommitDetails.delete(key);
    getAppCallbacks().render();
  }
}

export function dropWorkspaceGitRepositoryState(workspaceID: string) {
  state.gitRepositoryViews.delete(workspaceID);
  state.selectedGitRepositoryFolders.delete(workspaceID);
  state.selectedGitCommitHashes.delete(workspaceID);
  state.loadingGitRepositoryWorkspaces.delete(workspaceID);
  state.gitRepositoryOperations.delete(workspaceID);
  for (const key of Array.from(state.collapsedGitChangeFolders.keys())) {
    if (key.startsWith(`${workspaceID}:`)) {
      state.collapsedGitChangeFolders.delete(key);
    }
  }
  for (const key of Array.from(state.collapsedGitChangeTrees)) {
    if (key.startsWith(`${workspaceID}:`)) {
      state.collapsedGitChangeTrees.delete(key);
    }
  }
  for (const key of Array.from(state.gitCommitDetails.keys())) {
    if (key.startsWith(`${workspaceID}:`)) {
      state.gitCommitDetails.delete(key);
    }
  }
  for (const key of Array.from(state.loadingGitCommitDetails.keys())) {
    if (key.startsWith(`${workspaceID}:`)) {
      state.loadingGitCommitDetails.delete(key);
    }
  }
  for (const key of Array.from(state.gitCommitMessageDrafts.keys())) {
    if (key.startsWith(`${workspaceID}:`)) {
      state.gitCommitMessageDrafts.delete(key);
    }
  }
  for (const key of Array.from(state.gitNewBranchDrafts.keys())) {
    if (key.startsWith(`${workspaceID}:`)) {
      state.gitNewBranchDrafts.delete(key);
    }
  }
  for (const key of Array.from(state.gitSwitchBranchDrafts.keys())) {
    if (key.startsWith(`${workspaceID}:`)) {
      state.gitSwitchBranchDrafts.delete(key);
    }
  }
  for (const key of Array.from(state.gitMergeBranchDrafts.keys())) {
    if (key.startsWith(`${workspaceID}:`)) {
      state.gitMergeBranchDrafts.delete(key);
    }
  }
}

function handleGitRepositorySelect(select: HTMLSelectElement) {
  const workspace = activeWorkspace();
  if (!workspace || !select.value) {
    return;
  }
  state.selectedGitRepositoryFolders.set(workspace.id, select.value);
  state.selectedGitCommitHashes.delete(workspace.id);
  void refreshWorkspaceGitRepository(workspace.id, select.value);
}

function handleGitDraftInput(event: Event) {
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  if (!workspace || !repository) {
    return;
  }
  const target = event.currentTarget as HTMLTextAreaElement | HTMLInputElement;
  const key = gitRepositoryDraftKey(workspace.id, repository.folderId);
  if (target.matches("[data-git-commit-message]")) {
    state.gitCommitMessageDrafts.set(key, target.value);
  } else if (target.matches("[data-git-new-branch-name]")) {
    state.gitNewBranchDrafts.set(key, target.value);
  }
  updateGitFormButtons(target.form);
}

function handleGitCommitMessageKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" || !event.ctrlKey) {
    return;
  }
  event.preventDefault();
  const textarea = event.currentTarget as HTMLTextAreaElement;
  const form = textarea.form;
  if (!form) {
    return;
  }
  handleGitDraftInput(event);
  updateGitFormButtons(form);
  const button = form.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (!button || button.disabled) {
    return;
  }
  form.requestSubmit(button);
}

function handleGitBranchSelectChange(event: Event) {
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  if (!workspace || !repository) {
    return;
  }
  const target = event.currentTarget as HTMLSelectElement;
  const key = gitRepositoryDraftKey(workspace.id, repository.folderId);
  if (target.matches("[data-git-switch-branch-select]")) {
    state.gitSwitchBranchDrafts.set(key, target.value);
  } else if (target.matches("[data-git-merge-branch-select]")) {
    state.gitMergeBranchDrafts.set(key, target.value);
  }
  updateGitFormButtons(target.form);
}

async function handleGitCommitSubmit(event: SubmitEvent) {
  event.preventDefault();
  await runGitOperation("Committing", async (workspace, repository) => {
    const key = gitRepositoryDraftKey(workspace.id, repository.folderId);
    const message = state.gitCommitMessageDrafts.get(key)?.trim() ?? "";
    if (!message) {
      return;
    }
    const view = await CommitWorkspaceGitChanges(workspace.id, repository.folderId, message);
    state.gitCommitMessageDrafts.delete(key);
    storeGitRepositoryView(workspace.id, view);
    pushToast("Committed changes.", "success");
  });
}

async function handleGitCreateBranchSubmit(event: SubmitEvent) {
  event.preventDefault();
  await runGitOperation("Creating branch", async (workspace, repository) => {
    const key = gitRepositoryDraftKey(workspace.id, repository.folderId);
    const name = state.gitNewBranchDrafts.get(key)?.trim() ?? "";
    if (!name) {
      return;
    }
    const view = await CreateWorkspaceGitBranch(workspace.id, repository.folderId, name);
    state.gitNewBranchDrafts.delete(key);
    storeGitRepositoryView(workspace.id, view);
    pushToast("Created branch.", "success");
  });
}

async function handleGitSwitchBranchSubmit(event: SubmitEvent) {
  event.preventDefault();
  const form = event.currentTarget instanceof HTMLFormElement ? event.currentTarget : null;
  const selectedBranch = form?.querySelector<HTMLSelectElement>("[data-git-switch-branch-select]")?.value.trim() ?? "";
  await runGitOperation("Switching branch", async (workspace, repository) => {
    const key = gitRepositoryDraftKey(workspace.id, repository.folderId);
    const name = selectedBranch || state.gitSwitchBranchDrafts.get(key) || "";
    if (!name) {
      return;
    }
    const view = await SwitchWorkspaceGitBranch(workspace.id, repository.folderId, name);
    state.gitSwitchBranchDrafts.delete(key);
    storeGitRepositoryView(workspace.id, view);
    await refreshOpenCodeTabsFromDisk(workspace.id, getAppCallbacks().codeViewCallbacks());
    pushToast("Switched branch.", "success");
  }, true);
}

async function handleGitMergeBranchSubmit(event: SubmitEvent) {
  event.preventDefault();
  const form = event.currentTarget instanceof HTMLFormElement ? event.currentTarget : null;
  const selectedBranch = form?.querySelector<HTMLSelectElement>("[data-git-merge-branch-select]")?.value.trim() ?? "";
  await runGitOperation("Merging branch", async (workspace, repository) => {
    const key = gitRepositoryDraftKey(workspace.id, repository.folderId);
    const name = selectedBranch || state.gitMergeBranchDrafts.get(key) || "";
    if (!name) {
      return;
    }
    const view = await MergeWorkspaceGitBranch(workspace.id, repository.folderId, name);
    state.gitMergeBranchDrafts.delete(key);
    storeGitRepositoryView(workspace.id, view);
    await refreshOpenCodeTabsFromDisk(workspace.id, getAppCallbacks().codeViewCallbacks());
    pushToast("Merged branch.", "success");
  }, true);
}

export async function syncWorkspaceGitRepository(workspaceID: string) {
  if (!workspaceID) {
    return;
  }
  await runGitOperation("Syncing branch", async (workspace, repository) => {
    const view = await SyncWorkspaceGitBranch(workspace.id, repository.folderId);
    storeGitRepositoryView(workspace.id, view);
    await refreshOpenCodeTabsFromDisk(workspace.id, getAppCallbacks().codeViewCallbacks());
    pushToast("Synced branch.", "success");
  }, true);
}

export function toggleGitSourceSidebar() {
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  if (!workspace || !repository) {
    return;
  }
  const key = gitChangeTreeStateKey(workspace.id, repository.folderId);
  if (state.collapsedGitChangeTrees.has(key)) {
    state.collapsedGitChangeTrees.delete(key);
  } else {
    state.collapsedGitChangeTrees.add(key);
  }
  getAppCallbacks().render();
}

export function toggleGitDiffViewMode() {
  state.gitDiffViewMode = state.gitDiffViewMode === "split" ? "inline" : "split";
  getAppCallbacks().render();
}

export async function stageWorkspaceGitFile(path: string) {
  path = path.trim();
  if (!path) {
    return;
  }
  await runGitOperation("Staging file", async (workspace, repository) => {
    const view = await StageWorkspaceGitFile(workspace.id, repository.folderId, path);
    storeGitRepositoryView(workspace.id, view);
    pushToast("Staged file.", "success");
  }, true);
}

export async function unstageWorkspaceGitFile(path: string) {
  path = path.trim();
  if (!path) {
    return;
  }
  await runGitOperation("Unstaging file", async (workspace, repository) => {
    const view = await UnstageWorkspaceGitFile(workspace.id, repository.folderId, path);
    storeGitRepositoryView(workspace.id, view);
    pushToast("Unstaged file.", "success");
  }, true);
}

export async function stageWorkspaceGitChanges() {
  await runGitOperation("Staging changes", async (workspace, repository) => {
    const view = await StageWorkspaceGitChanges(workspace.id, repository.folderId);
    storeGitRepositoryView(workspace.id, view);
    pushToast("Staged changes.", "success");
  }, true);
}

export async function unstageWorkspaceGitChanges() {
  await runGitOperation("Unstaging changes", async (workspace, repository) => {
    const view = await UnstageWorkspaceGitChanges(workspace.id, repository.folderId);
    storeGitRepositoryView(workspace.id, view);
    pushToast("Unstaged changes.", "success");
  }, true);
}

export async function openGitChangeInCode(path: string, line: number) {
  path = path.trim();
  const workspace = activeWorkspace();
  if (!workspace || !path) {
    return;
  }
  const targetLine = Number.isFinite(line) ? Math.max(1, Math.floor(line)) : 1;
  state.appMode = "code";
  const loading = ensureCodeViewRootLoaded(workspace.id);
  getAppCallbacks().render();
  await loading;
  const opened = await openWorkspaceCodeFileAtLine(
    workspace.id,
    path,
    targetLine,
    getAppCallbacks().codeViewCallbacks(),
  );
  if (!opened) {
    return;
  }
  state.openGitChangeWorkspaces.delete(workspace.id);
  state.expandedGitChangeWorkspaces.delete(workspace.id);
  getAppCallbacks().render();
}

export async function revertWorkspaceGitFile(path: string) {
  path = path.trim();
  if (!path || !window.confirm(`Revert changes to ${path}? This cannot be undone.`)) {
    return;
  }
  await runGitOperation("Reverting file", async (workspace, repository) => {
    const view = await DiscardWorkspaceGitFile(workspace.id, repository.folderId, path);
    storeGitRepositoryView(workspace.id, view);
    await refreshOpenCodeTabsFromDisk(workspace.id, getAppCallbacks().codeViewCallbacks());
    pushToast("Reverted file changes.", "success");
  }, true);
}

export async function revertWorkspaceGitChanges() {
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  if (!workspace || !repository?.dirty || !window.confirm(`Revert all Git changes in ${repository.label}? This cannot be undone.`)) {
    return;
  }
  await runGitOperation("Reverting changes", async (active, selectedRepository) => {
    const view = await DiscardWorkspaceGitChanges(active.id, selectedRepository.folderId);
    storeGitRepositoryView(active.id, view);
    await refreshOpenCodeTabsFromDisk(active.id, getAppCallbacks().codeViewCallbacks());
    pushToast("Reverted all Git changes.", "success");
  }, true);
}

async function runGitOperation(
  label: string,
  operation: (workspace: services.Workspace, repository: services.WorkspaceGitRepositoryStatus) => Promise<void>,
  refreshOnFailure = false,
) {
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  if (!workspace || !repository || state.gitRepositoryOperations.has(workspace.id)) {
    return;
  }
  state.gitRepositoryOperations.set(workspace.id, label);
  getAppCallbacks().render();
  try {
    await operation(workspace, repository);
  } catch (error) {
    pushToast(errorMessage(error), "error");
    if (refreshOnFailure) {
      await reloadGitRepositorySilently(workspace.id, repository.folderId);
    }
  } finally {
    state.gitRepositoryOperations.delete(workspace.id);
    getAppCallbacks().render();
  }
}

async function reloadGitRepositorySilently(workspaceID: string, folderID: string) {
  try {
    const view = await LoadWorkspaceGitRepository(workspaceID, folderID);
    storeGitRepositoryView(workspaceID, view);
  } catch {
  }
}

function storeGitRepositoryView(workspaceID: string, view: services.WorkspaceGitRepositoryView) {
  state.gitRepositoryViews.set(workspaceID, view);
  const folderID = view.selectedFolderId || view.repository?.folderId || "";
  if (folderID) {
    state.selectedGitRepositoryFolders.set(workspaceID, folderID);
  }
  const selectedHash = state.selectedGitCommitHashes.get(workspaceID);
  if (selectedHash && !(view.repository?.commits ?? []).some((commit) => commit.hash === selectedHash)) {
    state.selectedGitCommitHashes.delete(workspaceID);
  }
}

function selectedGitRepositoryFolderID(workspaceID: string, view: services.WorkspaceGitRepositoryView): string {
  return (
    state.selectedGitRepositoryFolders.get(workspaceID) ||
    view.selectedFolderId ||
    view.repository?.folderId ||
    (view.repositories ?? []).find((repository) => repository.available)?.folderId ||
    ""
  );
}

function gitRepositoryDraftKey(workspaceID: string, folderID: string): string {
  return `${workspaceID}:${folderID}`;
}

function gitChangeTreeStateKey(workspaceID: string, folderID: string): string {
  return `${workspaceID}:${folderID}`;
}

function isGitChangeTreeCollapsed(workspaceID: string, folderID: string): boolean {
  return state.collapsedGitChangeTrees.has(gitChangeTreeStateKey(workspaceID, folderID));
}

function gitCollapsedChangeFolders(workspaceID: string, folderID: string): Set<string> {
  const key = gitChangeTreeStateKey(workspaceID, folderID);
  let collapsed = state.collapsedGitChangeFolders.get(key);
  if (!collapsed) {
    collapsed = new Set<string>();
    state.collapsedGitChangeFolders.set(key, collapsed);
  }
  return collapsed;
}

function gitCommitDetailKey(workspaceID: string, folderID: string, hash: string): string {
  return `${workspaceID}:${folderID}:${hash}`;
}

function updateGitFormButtons(form: HTMLFormElement | null) {
  if (!form) {
    return;
  }
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  const busy = Boolean(workspace && state.gitRepositoryOperations.has(workspace.id));
  const button = form.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (!button) {
    return;
  }
  if (form.matches("[data-git-commit-form]")) {
    const textarea = form.querySelector<HTMLTextAreaElement>("[data-git-commit-message]");
    button.disabled = !(repository?.stagedFileCount ?? 0) || !(textarea?.value.trim()) || busy;
    return;
  }
  if (form.matches("[data-git-create-branch-form]")) {
    const input = form.querySelector<HTMLInputElement>("[data-git-new-branch-name]");
    button.disabled = !(input?.value.trim()) || busy;
    return;
  }
  if (form.matches("[data-git-switch-branch-form]")) {
    const select = form.querySelector<HTMLSelectElement>("[data-git-switch-branch-select]");
    button.disabled = !select?.value || select.value === repository?.currentBranch || Boolean(repository?.dirty) || busy;
    return;
  }
  if (form.matches("[data-git-merge-branch-form]")) {
    const select = form.querySelector<HTMLSelectElement>("[data-git-merge-branch-select]");
    button.disabled = !select?.value || select.value === repository?.currentBranch || Boolean(repository?.dirty) || busy;
  }
}

function formatGitDate(value: string): string {
  if (!value) {
    return "";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}
