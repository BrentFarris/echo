import { ensureCodeViewRootLoaded, openWorkspaceCodeFileAtLine, refreshOpenCodeTabsFromDisk } from "../../codeView";
import { codeIcons } from "../../codeView/icons";
import { ChooseWorkspaceGitCloneParent, CloneWorkspaceGitRepository, CommitWorkspaceGitChanges, CreateWorkspaceGitBranch, DiscardWorkspaceGitChanges, DiscardWorkspaceGitFile, LoadWorkspaceChangeReview, LoadWorkspaceGitCommit, LoadWorkspaceGitFileDiffForScope, LoadWorkspaceGitRepository, LoadWorkspaceGitStash, MergeWorkspaceGitBranch, RunWorkspaceGitAction, StageWorkspaceGitChanges, StageWorkspaceGitFile, SwitchWorkspaceGitBranch, SyncWorkspaceGitBranch, UnstageWorkspaceGitChanges, UnstageWorkspaceGitFile } from "../../backend/services";
import { services } from "../../../wailsjs/go/models";
import type { CodeEntryKind, CodeGitChangeState } from "../../codeView/types";
import { getAppCallbacks } from "../callbacks";
import { renderSpinnerLabel } from "../components";
import { appRoot } from "../dom";
import { icons } from "../icons";
import { activeWorkspace, changeReviewFor, gitChangeReviewFor, gitRepositoryViewFor, state } from "../state";
import { pushToast } from "../toasts";
import { changeOperationLabel, errorMessage, escapeAttribute, escapeHtml } from "../utils";
import { markCurrentChangeTarget, renderChangeReviewPage, renderGitChangedFile, renderGitChangeMetadata, renderGitChangeStatus, renderGitDiff } from "../changes";
import type { GitMenuPage } from "../types";

type GitChangeTreeFolder = {
  kind: "folder";
  name: string;
  path: string;
  count: number;
  children: Map<string, GitChangeTreeNode>;
};

let gitMenuHistoryBound = false;

type GitChangeTreeFile = {
  kind: "file";
  name: string;
  path: string;
  displayPath: string;
  file: services.WorkspaceGitChangedFile;
};

type GitChangeTreeNode = GitChangeTreeFolder | GitChangeTreeFile;
type GitWorkingDiffScope = "staged" | "unstaged";

let gitWorkingDiffObserver: IntersectionObserver | null = null;

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
    <section class="work-panel git-repository" aria-label="Git" data-change-review data-git-repository>
      ${repository
        ? `
          <div class="git-source-layout ${sidebarCollapsed ? "is-sidebar-collapsed" : ""}" data-git-source-layout>
            ${renderGitSourceSidebarRail(repository)}
            ${renderGitSourceSidebar(workspace.id, repository, repositories, selectedFolderID, loading, operation)}
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
              ${renderGitWorkingChanges(workspace.id, repository)}
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
  const historyExpanded = state.expandedGitHistories.has(gitRepositoryDraftKey(workspaceID, repository.folderId));
  return `
    <aside class="git-source-sidebar ${historyExpanded ? "is-history-expanded" : "is-history-collapsed"}" aria-label="Source Control">
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
        ${renderGitSourceChangeSections(workspaceID, repository, operation)}
      </div>
      <section class="git-source-history ${historyExpanded ? "is-expanded" : "is-collapsed"}" aria-labelledby="git-history-title">
        <header>
          <h3 id="git-history-title">History</h3>
          <div class="git-source-history-actions">
            <span>${escapeHtml(String((repository.commits ?? []).length))}</span>
            <button class="icon-button git-source-history-toggle" type="button" title="${historyExpanded ? "Collapse history" : "Expand history"}" aria-label="${historyExpanded ? "Collapse history" : "Expand history"}" aria-expanded="${historyExpanded}" data-action="toggle-git-history">
              ${historyExpanded ? icons.arrowDown : icons.arrowUp}
            </button>
          </div>
        </header>
        ${historyExpanded ? renderGitCommitHistory(workspaceID, repository) : ""}
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
  const key = gitRepositoryDraftKey(workspaceID, repository.folderId);
  const menuOpen = state.gitMenuPages.has(key);
  const page = state.gitMenuPages.get(key) ?? "root";
  const content = renderGitMenuPage(repository, page, busy, loading);
  return `
    <details class="git-source-menu" ${menuOpen ? "open" : ""} data-git-source-menu>
      <summary class="icon-button" title="More Git actions" aria-label="More Git actions">${icons.moreHorizontal}</summary>
      <button class="git-menu-backdrop" type="button" tabindex="-1" data-action="close-git-menu" aria-label="Close Git actions"></button>
      <div class="git-source-menu-popover" role="menu">
        ${content}
      </div>
    </details>
  `;
}

function renderGitMenuPage(repository: services.WorkspaceGitRepositoryStatus, page: GitMenuPage, busy: boolean, loading: boolean): string {
  if (page === "root") {
    return `
      <div class="git-menu-header"><strong>Git actions</strong><button class="icon-button" type="button" data-action="close-git-menu" aria-label="Close">${icons.x}</button></div>
      ${gitMenuButton("refresh", "Refresh", busy, false, loading ? `<span class="spinner" aria-hidden="true"></span>` : icons.refresh)}
      ${gitMenuButton("pull", "Pull", busy)}
      ${gitMenuButton("push", "Push", busy)}
      ${gitMenuButton("clone", "Clone", busy)}
      ${gitMenuButton("checkout", "Checkout to…", busy)}
      ${gitMenuButton("fetch", "Fetch", busy)}
      <hr />
      ${gitMenuCategory("commit", "Commit")}
      ${gitMenuCategory("changes", "Changes")}
      ${gitMenuCategory("pull-push", "Pull, Push")}
      ${gitMenuCategory("branch", "Branch")}
      ${gitMenuCategory("remote", "Remote")}
      ${gitMenuCategory("stash", "Stash")}
      ${gitMenuCategory("tags", "Tags")}
    `;
  }
  const title = ({ commit: "Commit", changes: "Changes", "pull-push": "Pull, Push", branch: "Branch", remote: "Remote", stash: "Stash", tags: "Tags" } as Record<string, string>)[page];
  return `<div class="git-menu-header"><button class="icon-button" type="button" data-action="open-git-menu-page" data-git-menu-page="root" aria-label="Back">${icons.undo}</button><strong>${escapeHtml(title)}</strong><button class="icon-button" type="button" data-action="close-git-menu" aria-label="Close">${icons.x}</button></div>${renderGitMenuSection(repository, page, busy)}`;
}

function renderGitMenuSection(repository: services.WorkspaceGitRepositoryStatus, page: Exclude<GitMenuPage, "root">, busy: boolean): string {
  const hasStaged = (repository.stagedFileCount ?? 0) > 0;
  const hasChanges = (repository.fileCount ?? 0) > 0;
  const hasRemotes = (repository.remotes ?? []).length > 0;
  const hasStashes = (repository.stashes ?? []).length > 0;
  const hasTags = (repository.tags ?? []).length > 0;
  switch (page) {
    case "commit":
      return `${gitMenuButton("commit", "Commit", busy || (!hasStaged && !hasChanges))}${gitMenuButton("commit_staged", "Commit staged", busy || !hasStaged)}${gitMenuButton("commit_all", "Commit all", busy || !hasChanges)}${repository.rebaseInProgress ? gitMenuButton("abort_rebase", "Abort rebase", busy, true) : ""}<hr />${gitMenuButton("commit_staged_amend", "Commit (amend)", busy, true)}${gitMenuButton("commit_staged_amend", "Commit staged (amend)", busy || !hasStaged, true)}${gitMenuButton("commit_all_amend", "Commit all (amend)", busy, true)}<hr />${gitMenuButton("commit_staged_signoff", "Commit (signed off)", busy || !hasStaged)}${gitMenuButton("commit_staged_signoff", "Commit staged (signed off)", busy || !hasStaged)}${gitMenuButton("commit_all_signoff", "Commit all (signed off)", busy || !hasChanges)}`;
    case "changes":
      return `${gitMenuButton("stage_all", "Stage all changes", busy || !hasChanges)}${gitMenuButton("unstage_all", "Unstage all changes", busy || !hasStaged)}${gitMenuButton("discard_all", "Discard all changes", busy || !hasChanges, true)}`;
    case "pull-push":
      return `${gitMenuButton("sync", gitSyncMenuLabel(repository), busy || !repository.upstream)}<hr />${gitMenuButton("pull", "Pull", busy)}${gitMenuButton("pull_rebase", "Pull (rebase)", busy)}${gitMenuButton("pull_from", "Pull from…", busy || !hasRemotes)}<hr />${gitMenuButton("push", "Push", busy)}${gitMenuButton("push_to", "Push to…", busy || !hasRemotes)}<hr />${gitMenuButton("fetch", "Fetch", busy || !hasRemotes)}${gitMenuButton("fetch_prune", "Fetch (prune)", busy || !hasRemotes)}${gitMenuButton("fetch_all", "Fetch all from remotes", busy || !hasRemotes)}`;
    case "branch":
      return `${gitMenuButton("merge", "Merge…", busy || !(repository.branches ?? []).length)}${gitMenuButton("rebase", "Rebase branch…", busy || !(repository.branches ?? []).length)}<hr />${gitMenuButton("create_branch", "Create branch…", busy)}${gitMenuButton("create_branch_from", "Create branch from…", busy)}<hr />${gitMenuButton("rename_branch", "Rename branch…", busy || !(repository.branches ?? []).length)}${gitMenuButton("delete_branch", "Delete branch…", busy || (repository.branches ?? []).length < 2, true)}${gitMenuButton("delete_remote_branch", "Delete remote branch…", busy || !(repository.remoteBranches ?? []).length, true)}<hr />${gitMenuButton("publish_branch", "Publish branch…", busy || repository.detached || !hasRemotes)}`;
    case "remote":
      return `${gitMenuButton("add_remote", "Add remote…", busy)}${gitMenuButton("remove_remote", "Remove remote…", busy || !hasRemotes, true)}`;
    case "stash":
      return `${gitMenuButton("stash", "Stash", busy || !hasChanges)}${gitMenuButton("stash_untracked", "Stash (include untracked)", busy || !hasChanges)}${gitMenuButton("stash_staged", "Stash staged", busy || !hasStaged || !repository.supportsStashStaged)}<hr />${gitMenuButton("apply_latest_stash", "Apply latest stash", busy || !hasStashes)}${gitMenuButton("apply_stash", "Apply stash…", busy || !hasStashes)}<hr />${gitMenuButton("pop_latest_stash", "Pop latest stash", busy || !hasStashes)}${gitMenuButton("pop_stash", "Pop stash…", busy || !hasStashes)}<hr />${gitMenuButton("drop_stash", "Drop stash…", busy || !hasStashes, true)}${gitMenuButton("drop_all_stashes", "Drop all stashes", busy || !hasStashes, true)}<hr />${gitMenuButton("view_stash", "View stash…", busy || !hasStashes)}`;
    case "tags":
      return `${gitMenuButton("create_tag", "Create tag…", busy)}${gitMenuButton("delete_tag", "Delete tag…", busy || !hasTags, true)}${gitMenuButton("delete_remote_tag", "Delete remote tag…", busy || !hasRemotes, true)}<hr />${gitMenuButton("push_tags", "Push tags", busy || !hasRemotes || !hasTags)}`;
  }
}

function gitMenuButton(command: string, label: string, disabled: boolean, danger = false, icon = icons.git): string {
  return `<button class="${danger ? "is-danger" : ""}" type="button" role="menuitem" data-action="run-git-menu-command" data-git-command="${escapeAttribute(command)}" ${disabled ? "disabled" : ""}>${icon}<span>${escapeHtml(label)}</span></button>`;
}

function gitMenuCategory(page: Exclude<GitMenuPage, "root">, label: string): string {
  return `<button type="button" role="menuitem" data-action="open-git-menu-page" data-git-menu-page="${escapeAttribute(page)}"><span class="git-menu-category-icon">${icons.git}</span><span>${escapeHtml(label)}</span><span class="git-menu-chevron">${icons.arrowRight}</span></button>`;
}

function gitSyncMenuLabel(repository: services.WorkspaceGitRepositoryStatus): string {
  const ahead = Math.max(0, repository.aheadCount ?? 0);
  const behind = Math.max(0, repository.behindCount ?? 0);
  return ahead > 0 || behind > 0 ? `Sync (${behind} down / ${ahead} up)` : "Sync";
}

function renderGitSourceChangeSections(workspaceID: string, repository: services.WorkspaceGitRepositoryStatus, operation: string): string {
  const files = repository.files ?? [];
  const staged = files.filter((file) => file.staged);
  const unstaged = files.filter((file) => file.unstaged);
  return `
    <div class="git-source-change-sections ${staged.length ? "has-staged" : "has-unstaged-only"}">
      ${staged.length ? renderGitSourceFileSection(workspaceID, repository.folderId, "Staged Changes", staged, "unstage", operation) : ""}
      ${renderGitSourceFileSection(workspaceID, repository.folderId, "Changes", unstaged, "stage", operation)}
    </div>
  `;
}

function renderGitSourceFileSection(
  workspaceID: string,
  folderID: string,
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
        ? renderGitSourceFileTree(workspaceID, folderID, files, mode, action, icon, fileActionLabel, busy)
        : `<div class="git-source-empty">No files.</div>`}
    </section>
  `;
}

function renderGitSourceFileTree(
  workspaceID: string,
  folderID: string,
  files: services.WorkspaceGitChangedFile[],
  mode: "stage" | "unstage",
  action: string,
  icon: string,
  actionLabel: string,
  busy: boolean,
): string {
  const root = buildGitChangeTree(files);
  const collapsed = gitCollapsedChangeFolders(workspaceID, folderID);
  return `<div class="git-source-file-list" role="tree">
    ${sortedGitChangeTreeChildren(root).map((node) => renderGitSourceFileTreeNode(node, collapsed, mode, action, icon, actionLabel, busy, 0)).join("")}
  </div>`;
}

function renderGitSourceFileTreeNode(
  node: GitChangeTreeNode,
  collapsed: Set<string>,
  mode: "stage" | "unstage",
  action: string,
  icon: string,
  actionLabel: string,
  busy: boolean,
  depth: number,
): string {
  if (node.kind === "folder") {
    const collapseKey = `${mode}:${node.path}`;
    const isCollapsed = collapsed.has(collapseKey);
    return `
      <div class="git-source-folder ${isCollapsed ? "is-collapsed" : "is-expanded"}" role="none">
        <button class="git-source-folder-row" type="button" role="treeitem" aria-expanded="${!isCollapsed}" title="${escapeAttribute(node.path)}" style="--tree-depth: ${depth}" data-git-change-folder="${escapeAttribute(collapseKey)}">
          <span class="git-source-folder-chevron">${codeIcons.chevron}</span>
          <span class="git-source-folder-icon">${codeIcons.folder}</span>
          <span class="git-source-folder-name">${escapeHtml(node.name)}</span>
          <span class="git-source-folder-count">${escapeHtml(String(node.count))}</span>
        </button>
        ${isCollapsed ? "" : `<div class="git-source-folder-children" role="group">${sortedGitChangeTreeChildren(node).map((child) => renderGitSourceFileTreeNode(child, collapsed, mode, action, icon, actionLabel, busy, depth + 1)).join("")}</div>`}
      </div>
    `;
  }

  const file = node.file;
  const displayPath = node.displayPath;
  return `
    <div class="git-source-file-row" role="none" title="${escapeAttribute(displayPath)}" style="--tree-depth: ${depth}">
      <button class="git-source-file-main" type="button" role="treeitem" data-git-change-file="${escapeAttribute(node.path)}" data-git-diff-scope="${mode === "stage" ? "unstaged" : "staged"}">
        <span class="git-source-file-status is-${escapeAttribute(file.operation)}">${escapeHtml(gitSourceStatusLetter(file))}</span>
        <span class="git-source-file-icon">${codeIcons.file}</span>
        <span class="git-source-file-name">${escapeHtml(node.name)}</span>
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

function renderGitWorkingChanges(workspaceID: string, repository: services.WorkspaceGitRepositoryStatus): string {
  const stashDetail = state.gitStashDetails.get(gitRepositoryDraftKey(workspaceID, repository.folderId));
  if (stashDetail) {
    return renderSelectedGitStashReview(stashDetail);
  }
  const files = repository.files ?? [];
  const selectedCommitReview = renderSelectedGitCommitReview(workspaceID, repository);
  if (!files.length && !selectedCommitReview) {
    return `<div class="empty-state compact">No Git changes.</div>`;
  }
  return `
    <div class="change-file-list git-change-file-list" data-git-change-file-list>
      ${files.length
        ? files.filter((file) => file.staged).map((file) => renderGitWorkingChangedFile(workspaceID, repository.folderId, file, "staged")).join("")
          + files.filter((file) => file.unstaged).map((file) => renderGitWorkingChangedFile(workspaceID, repository.folderId, file, "unstaged")).join("")
        : `<div class="empty-state compact">No working tree changes.</div>`}
      ${selectedCommitReview}
    </div>
  `;
}

function renderSelectedGitStashReview(detail: services.WorkspaceGitStashDetail): string {
  const files = detail.files ?? [];
  return `
    <section class="git-stash-review" aria-label="Stash review">
      <header class="git-commit-review-header">
        <div><strong>${escapeHtml(detail.stash?.ref ?? "Stash")}</strong><span>${escapeHtml(detail.stash?.message ?? "")}</span></div>
        <button class="secondary-button" type="button" data-action="close-git-stash-review">Close</button>
      </header>
      ${files.length ? `<div class="git-commit-review-files">${files.map((file) => renderGitCommitReviewFile(detail.stash?.ref ?? "stash", file)).join("")}</div>` : `<div class="empty-state compact">No files in this stash.</div>`}
    </section>
  `;
}

function renderGitWorkingChangedFile(workspaceID: string, folderID: string, file: services.WorkspaceGitChangedFile, scope: GitWorkingDiffScope): string {
  const key = gitWorkingDiffKey(workspaceID, folderID, file.path, scope);
  const hydrated = state.gitWorkingDiffs.get(key);
  const loaded = Boolean(hydrated) || state.gitWorkingDiffFailures.has(key);
  return renderGitChangedFile(hydrated ?? gitChangedFileForScope(file, scope), !loaded, scope);
}

function gitChangedFileForScope(file: services.WorkspaceGitChangedFile, scope: GitWorkingDiffScope): services.WorkspaceGitChangedFile {
  const status = scope === "staged" ? file.indexStatus ?? "" : file.worktreeStatus ?? "";
  const untracked = scope === "unstaged" && file.indexStatus === "?" && file.worktreeStatus === "?";
  return {
    ...file,
    operation: untracked ? file.operation : gitOperationForScopedStatus(status, file.operation),
    status: untracked ? file.status : scope === "staged" ? `${status} ` : ` ${status}`,
    indexStatus: scope === "staged" ? file.indexStatus : undefined,
    worktreeStatus: scope === "unstaged" ? file.worktreeStatus : undefined,
    staged: scope === "staged",
    unstaged: scope === "unstaged",
  };
}

function gitOperationForScopedStatus(status: string, fallback: string): string {
  switch (status) {
    case "A": return "created";
    case "D": return "deleted";
    case "R": return "renamed";
    case "C": return "copied";
    case "U": return "conflicted";
    case "M":
    case "?": return "edited";
    default: return fallback;
  }
}

function renderSelectedGitCommitReview(workspaceID: string, repository: services.WorkspaceGitRepositoryStatus): string {
  const selectedHash = state.selectedGitCommitHashes.get(workspaceID) ?? "";
  if (!selectedHash) {
    return "";
  }
  const key = gitCommitDetailKey(workspaceID, repository.folderId, selectedHash);
  const detail = state.gitCommitDetails.get(key);
  const commit = detail?.commit ?? (repository.commits ?? []).find((item) => item.hash === selectedHash);
  if (!commit) {
    return "";
  }
  const files = detail?.files ?? [];
  return `
    <section class="git-commit-review" aria-labelledby="git-commit-review-${escapeAttribute(commit.hash)}">
      <header class="git-commit-review-header" data-git-commit-review-header data-commit-hash="${escapeAttribute(commit.hash)}">
        <div>
          <h3 id="git-commit-review-${escapeAttribute(commit.hash)}">${escapeHtml(commit.subject || commit.shortHash)}</h3>
          <button class="git-commit-review-copy" type="button" title="Copy commit hash" data-action="copy-git-commit-hash" data-commit-hash="${escapeAttribute(commit.hash)}">
            ${escapeHtml(`${commit.shortHash} - ${commit.authorName || "Unknown"} - ${formatGitDate(commit.authoredAt)}`)}
          </button>
        </div>
      </header>
      ${state.loadingGitCommitDetails.has(key)
        ? `<div class="empty-state compact">${renderSpinnerLabel("Loading commit")}</div>`
        : files.length
          ? `<div class="git-commit-review-files">${files.map((file) => renderGitCommitReviewFile(commit.hash, file)).join("")}</div>`
          : `<div class="empty-state compact">No changed files in this commit.</div>`}
    </section>
  `;
}

function renderGitCommitReviewFile(hash: string, file: services.WorkspaceGitChangedFile): string {
  const normalizedPath = normalizeGitChangePath(file.path);
  return `
    <article class="change-file git-commit-review-file" data-change-file data-git-commit-hash="${escapeAttribute(hash)}" data-git-commit-file-path="${escapeAttribute(normalizedPath)}">
      <header>
        <div class="change-file-title">
          ${icons.file}
          <strong title="${escapeAttribute(file.path)}">${escapeHtml(file.path)}</strong>
        </div>
        <span class="change-operation is-${escapeAttribute(file.operation)}">${escapeHtml(changeOperationLabel(file.operation))}</span>
      </header>
      ${renderGitChangeStatus(file)}
      ${file.diffAvailable && file.diff ? renderGitDiff(file.diff, "") : renderGitChangeMetadata(file)}
    </article>
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

function renderGitSummarySyncButton(
  repository: services.WorkspaceGitRepositoryStatus,
  loading: boolean,
  operation: string,
): string {
  const ahead = Math.max(0, repository.aheadCount ?? 0);
  const behind = Math.max(0, repository.behindCount ?? 0);
  const pending = ahead > 0 || behind > 0;
  const label = pending ? `Sync (${behind} down, ${ahead} up)` : "Sync";
  const busy = loading || Boolean(operation);
  return `
    <button class="secondary-button icon-text-button git-sync-button git-summary-sync-button" type="button" title="${escapeAttribute(label)}" aria-label="${escapeAttribute(label)}" data-action="sync-git-branch" ${busy ? "disabled" : ""}>
      ${operation === "Syncing branch" ? `<span class="spinner" aria-hidden="true"></span>` : icons.refresh}
      <span>Sync</span>
      ${pending ? `<span class="git-sync-counts" aria-hidden="true">${escapeHtml(`${behind} down / ${ahead} up`)}</span>` : ""}
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
      ${renderGitSummarySyncButton(repository, loading, operation)}
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
      ${selected ? renderGitCommitExpandedFiles(key, commit.hash) : ""}
    </article>
  `;
}

function renderGitCommitExpandedFiles(key: string, hash: string): string {
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
  return `<div class="git-commit-files">${files.map((file) => renderGitCommitChangedFile(hash, file)).join("")}</div>`;
}

function renderGitCommitChangedFile(hash: string, file: services.WorkspaceGitChangedFile): string {
  const displayPath = displayGitChangePath(file.path);
  const normalizedPath = normalizeGitChangePath(file.path);
  return `
    <button class="git-commit-file-row" type="button" title="${escapeAttribute(displayPath)}" data-git-commit-file data-commit-hash="${escapeAttribute(hash)}" data-git-commit-file-path="${escapeAttribute(normalizedPath)}">
      <span class="git-source-file-status is-${escapeAttribute(file.operation)}">${escapeHtml(gitSourceStatusLetter(file))}</span>
      <span>${escapeHtml(displayPath)}</span>
    </button>
  `;
}

export function bindGitEvents(root: ParentNode) {
  if (!gitMenuHistoryBound) {
    gitMenuHistoryBound = true;
    window.addEventListener("popstate", () => {
      const workspace = activeWorkspace();
      const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
      if (!workspace || !repository) return;
      const key = gitRepositoryDraftKey(workspace.id, repository.folderId);
      if ((state.gitMenuPages.get(key) ?? "root") !== "root") {
        state.gitMenuPages.set(key, "root");
        getAppCallbacks().render();
      }
    });
  }
  root.querySelectorAll<HTMLDetailsElement>("[data-git-source-menu]").forEach((menu) => {
    menu.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        event.preventDefault();
        closeGitMenu();
      }
    });
    menu.addEventListener("toggle", () => {
      if (!menu.open) {
        const workspace = activeWorkspace();
        const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
        if (workspace && repository) state.gitMenuPages.delete(gitRepositoryDraftKey(workspace.id, repository.folderId));
        if ((window.history.state as { echoGitMenu?: boolean } | null)?.echoGitMenu) window.history.back();
      }
    });
  });
  bindGitSplitDiffScroll(root);
  bindGitChangeTree(root);
  bindGitWorkingDiffs(root);
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
  root
    .querySelectorAll<HTMLButtonElement>("[data-git-commit-file]")
    .forEach((button) => button.addEventListener("click", handleGitCommitFileSelect));
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
  const scope = gitWorkingDiffScope(button.dataset.gitDiffScope);
  if (!targetPath || !scope) {
    return;
  }
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  const file = repository?.files?.find((candidate) => normalizeGitChangePath(candidate.path) === targetPath);
  if (workspace && repository && file) {
    void loadGitWorkingDiff(workspace.id, repository.folderId, file, scope);
  }
  const repositoryRoot = button.closest<HTMLElement>("[data-git-repository]");
  const target = Array.from(repositoryRoot?.querySelectorAll<HTMLElement>("[data-git-change-file-path]") ?? [])
    .find((element) => element.dataset.gitChangeFilePath === targetPath && element.dataset.gitDiffScope === scope);
  if (!target) {
    return;
  }
  button
    .closest<HTMLElement>(".git-source-change-sections")
    ?.querySelectorAll<HTMLElement>("[data-git-change-file].is-selected")
    .forEach((row) => row.classList.remove("is-selected"));
  button.classList.add("is-selected");
  const review = target.closest<HTMLElement>("[data-change-review]");
  if (review) {
    markCurrentChangeTarget(review, target);
  }
  target.scrollIntoView({ behavior: "smooth", block: "start" });
}

function bindGitWorkingDiffs(root: ParentNode) {
  gitWorkingDiffObserver?.disconnect();
  gitWorkingDiffObserver = null;
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  const list = root.querySelector<HTMLElement>("[data-git-change-file-list]");
  if (!workspace || !repository || !list) {
    return;
  }
  const pending = Array.from(list.querySelectorAll<HTMLElement>("[data-git-diff-pending]"));
  if (!pending.length) {
    return;
  }
  if (!("IntersectionObserver" in window)) {
    const element = pending[0];
    const file = repository.files?.find((candidate) => normalizeGitChangePath(candidate.path) === element?.dataset.gitChangeFilePath);
    const scope = gitWorkingDiffScope(element?.dataset.gitDiffScope);
    if (file && scope) {
      void loadGitWorkingDiff(workspace.id, repository.folderId, file, scope);
    }
    return;
  }
  gitWorkingDiffObserver = new IntersectionObserver((entries) => {
    for (const entry of entries) {
      if (!entry.isIntersecting) {
        continue;
      }
      const element = entry.target as HTMLElement;
      gitWorkingDiffObserver?.unobserve(element);
      const path = element.dataset.gitChangeFilePath ?? "";
      const file = repository.files?.find((candidate) => normalizeGitChangePath(candidate.path) === path);
      const scope = gitWorkingDiffScope(element.dataset.gitDiffScope);
      if (file && scope) {
        void loadGitWorkingDiff(workspace.id, repository.folderId, file, scope);
      }
    }
  }, { root: list, rootMargin: "320px 0px" });
  pending.forEach((element) => gitWorkingDiffObserver?.observe(element));
}

async function loadGitWorkingDiff(workspaceID: string, folderID: string, file: services.WorkspaceGitChangedFile, scope: GitWorkingDiffScope) {
  const key = gitWorkingDiffKey(workspaceID, folderID, file.path, scope);
  if (state.gitWorkingDiffs.has(key) || state.gitWorkingDiffFailures.has(key) || state.loadingGitWorkingDiffs.has(key)) {
    return;
  }
  const generationKey = gitWorkingDiffGenerationKey(workspaceID, folderID);
  const generation = state.gitWorkingDiffGenerations.get(generationKey) ?? 0;
  state.loadingGitWorkingDiffs.add(key);
  try {
    const hydrated = await LoadWorkspaceGitFileDiffForScope(workspaceID, folderID, file.path, scope);
    if ((state.gitWorkingDiffGenerations.get(generationKey) ?? 0) !== generation) {
      return;
    }
    state.gitWorkingDiffs.set(key, hydrated);
    patchGitWorkingDiffCard(hydrated, scope);
  } catch (error) {
    if ((state.gitWorkingDiffGenerations.get(generationKey) ?? 0) !== generation) {
      return;
    }
    state.gitWorkingDiffFailures.add(key);
    pushToast(errorMessage(error), "error");
    patchGitWorkingDiffCard(file, scope);
  } finally {
    state.loadingGitWorkingDiffs.delete(key);
  }
}

function handleGitCommitFileSelect(event: MouseEvent) {
  const button = event.currentTarget as HTMLButtonElement;
  const hash = button.dataset.commitHash ?? "";
  const targetPath = button.dataset.gitCommitFilePath ?? "";
  if (!hash || !targetPath) {
    return;
  }
  const repositoryRoot = button.closest<HTMLElement>("[data-git-repository]");
  const target = Array.from(repositoryRoot?.querySelectorAll<HTMLElement>("[data-git-commit-file-path]") ?? [])
    .find((element) =>
      element.dataset.gitCommitHash === hash &&
      element.dataset.gitCommitFilePath === targetPath &&
      element.matches("[data-change-file]")
    );
  if (!target) {
    return;
  }
  button
    .closest<HTMLElement>(".git-commit-files")
    ?.querySelectorAll<HTMLElement>(".git-commit-file-row.is-selected")
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
    scheduleScrollToGitCommitReview(hash);
    return;
  }
  state.loadingGitCommitDetails.add(key);
  getAppCallbacks().render();
  scheduleScrollToGitCommitReview(hash);
  try {
    state.gitCommitDetails.set(key, await LoadWorkspaceGitCommit(workspace.id, folderID, hash));
  } catch (error) {
    pushToast(errorMessage(error), "error");
  } finally {
    state.loadingGitCommitDetails.delete(key);
    getAppCallbacks().render();
    scheduleScrollToGitCommitReview(hash);
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
  for (const key of Array.from(state.expandedGitHistories)) {
    if (key.startsWith(`${workspaceID}:`)) {
      state.expandedGitHistories.delete(key);
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
  for (const key of Array.from(state.gitMenuPages.keys())) {
    if (key.startsWith(`${workspaceID}:`)) state.gitMenuPages.delete(key);
  }
  for (const key of Array.from(state.gitStashDetails.keys())) {
    if (key.startsWith(`${workspaceID}:`)) state.gitStashDetails.delete(key);
  }
}

function patchGitWorkingDiffCard(file: services.WorkspaceGitChangedFile, scope: GitWorkingDiffScope) {
  const path = normalizeGitChangePath(file.path);
  const list = appRoot.querySelector<HTMLElement>("[data-git-change-file-list]");
  const current = Array.from(list?.querySelectorAll<HTMLElement>("[data-git-change-file-path]") ?? [])
    .find((element) => element.dataset.gitChangeFilePath === path && element.dataset.gitDiffScope === scope && !element.dataset.gitCommitHash);
  if (!current) {
    return;
  }
  const template = document.createElement("template");
  template.innerHTML = renderGitChangedFile(file, false, scope).trim();
  const replacement = template.content.firstElementChild;
  if (!(replacement instanceof HTMLElement)) {
    return;
  }
  current.replaceWith(replacement);
  getAppCallbacks().bindActionEvents(replacement);
  bindGitSplitDiffScroll(replacement);
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

export function openGitMenuPage(page: string) {
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  const allowed: GitMenuPage[] = ["root", "commit", "changes", "pull-push", "branch", "remote", "stash", "tags"];
  if (!workspace || !repository || !allowed.includes(page as GitMenuPage)) {
    return;
  }
  state.gitMenuPages.set(gitRepositoryDraftKey(workspace.id, repository.folderId), page as GitMenuPage);
  if (page !== "root" && !(window.history.state as { echoGitMenu?: boolean } | null)?.echoGitMenu) {
    window.history.pushState({ ...(window.history.state ?? {}), echoGitMenu: true }, "");
  } else if (page === "root" && (window.history.state as { echoGitMenu?: boolean } | null)?.echoGitMenu) {
    window.history.back();
  }
  getAppCallbacks().render();
  window.requestAnimationFrame(() => {
    appRoot.querySelector<HTMLElement>("[data-git-source-menu] .git-menu-header button, [data-git-source-menu] .git-source-menu-popover > button")?.focus();
  });
}

export function closeGitMenu() {
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  if (workspace && repository) {
    state.gitMenuPages.delete(gitRepositoryDraftKey(workspace.id, repository.folderId));
  }
  if ((window.history.state as { echoGitMenu?: boolean } | null)?.echoGitMenu) {
    window.history.back();
  }
  const menu = appRoot.querySelector<HTMLDetailsElement>("[data-git-source-menu]");
  if (menu) {
    menu.open = false;
    menu.querySelector<HTMLElement>("summary")?.focus();
  }
}

export function closeGitStashReview() {
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  if (!workspace || !repository) {
    return;
  }
  state.gitStashDetails.delete(gitRepositoryDraftKey(workspace.id, repository.folderId));
  getAppCallbacks().render();
}

export async function runGitMenuCommand(command: string) {
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  if (!workspace || !repository || !command) {
    return;
  }
  if (command === "refresh") {
    closeGitMenu();
    await refreshWorkspaceGitRepository(workspace.id);
    return;
  }
  if (command === "clone") {
    await cloneGitRepositoryIntoWorkspace(workspace.id);
    return;
  }
  if (command === "view_stash") {
    const ref = await chooseGitValue("View stash", (repository.stashes ?? []).map((item) => ({ value: item.ref, label: `${item.ref}: ${item.message}` })));
    if (!ref) return;
    try {
      const detail = await LoadWorkspaceGitStash(workspace.id, repository.folderId, ref);
      state.gitStashDetails.set(gitRepositoryDraftKey(workspace.id, repository.folderId), detail);
      closeGitMenu();
      getAppCallbacks().render();
    } catch (error) {
      pushToast(errorMessage(error), "error");
    }
    return;
  }
  const request = await buildGitActionRequest(command, repository);
  if (!request) {
    return;
  }
  const label = gitActionOperationLabel(command);
  closeGitMenu();
  await runGitOperation(label, async (active, selectedRepository) => {
    const view = await RunWorkspaceGitAction(active.id, selectedRepository.folderId, services.WorkspaceGitActionRequest.createFrom(request));
    storeGitRepositoryView(active.id, view);
    if (gitActionChangesWorktree(command)) {
      await refreshOpenCodeTabsFromDisk(active.id, getAppCallbacks().codeViewCallbacks());
    }
    state.gitCommitMessageDrafts.delete(gitRepositoryDraftKey(active.id, selectedRepository.folderId));
    pushToast(`${label.replace(/ing$/, "ed")}.`, "success");
  }, true);
}

async function buildGitActionRequest(command: string, repository: services.WorkspaceGitRepositoryStatus): Promise<Partial<services.WorkspaceGitActionRequest> | null> {
  const request: Partial<services.WorkspaceGitActionRequest> = { action: command };
  const key = gitRepositoryDraftKey(activeWorkspace()?.id ?? "", repository.folderId);
  const message = state.gitCommitMessageDrafts.get(key)?.trim() ?? "";
  if (command === "commit") {
    if (!message) {
      pushToast("Enter a commit message first.", "error");
      return null;
    }
    if ((repository.stagedFileCount ?? 0) > 0) {
      request.action = "commit_staged";
    } else if (window.confirm("No changes are staged. Stage all visible changes and commit them?")) {
      request.action = "commit_all";
    } else {
      return null;
    }
    request.message = message;
    return request;
  }
  if (command.startsWith("commit_")) {
    const amend = command.includes("amend");
    if (!message && !amend) {
      pushToast("Enter a commit message first.", "error");
      return null;
    }
    if (amend && !window.confirm("Amend the latest commit? This rewrites commit history.")) return null;
    request.message = message;
  }
  const localBranches = (repository.branches ?? []).map((item) => ({ value: item.name, label: item.name }));
  const otherBranches = localBranches.filter((item) => item.value !== repository.currentBranch);
  const remoteBranches = (repository.remoteBranches ?? []).map((item) => ({ value: item.name, label: item.name }));
  const tags = (repository.tags ?? []).map((item) => ({ value: item.name, label: item.name }));
  const remotes = (repository.remotes ?? []).map((item) => ({ value: item.name, label: `${item.name}${item.fetchUrl ? ` — ${item.fetchUrl}` : ""}` }));
  const stashes = (repository.stashes ?? []).map((item) => ({ value: item.ref, label: `${item.ref}: ${item.message}` }));
  if (command === "checkout") {
    request.ref = await chooseGitValue("Checkout to", [...localBranches, ...remoteBranches, ...tags], true);
    if (!request.ref) return null;
  } else if (command === "merge" || command === "rebase") {
    request.ref = await chooseGitValue(command === "merge" ? "Merge branch" : "Rebase onto branch", otherBranches);
    if (!request.ref) return null;
  } else if (command === "create_branch" || command === "create_branch_from") {
    request.name = window.prompt("New branch name")?.trim() ?? "";
    if (!request.name) return null;
    if (command === "create_branch_from") {
      request.ref = await chooseGitValue("Create branch from", [...localBranches, ...remoteBranches, ...tags], true);
      if (!request.ref) return null;
    }
  } else if (command === "rename_branch") {
    request.ref = await chooseGitValue("Branch to rename", localBranches);
    if (!request.ref) return null;
    request.name = window.prompt("New branch name", request.ref)?.trim() ?? "";
    if (!request.name || !window.confirm(`Rename ${request.ref} to ${request.name}?`)) return null;
  } else if (command === "delete_branch") {
    request.ref = await chooseGitValue("Delete branch", otherBranches);
    if (!request.ref || !window.confirm(`Delete local branch ${request.ref}?`)) return null;
  } else if (command === "delete_remote_branch") {
    const selected = await chooseGitValue("Delete remote branch", remoteBranches);
    const item = (repository.remoteBranches ?? []).find((candidate) => candidate.name === selected);
    if (!item || !window.confirm(`Delete remote branch ${item.name}?`)) return null;
    request.remote = item.remote;
    request.branch = item.branch;
  } else if (command === "publish_branch" || command === "push_tags") {
    request.remote = await chooseGitValue(command === "publish_branch" ? "Publish to remote" : "Push tags to remote", remotes);
    if (!request.remote) return null;
  } else if (command === "pull_from" || command === "push_to") {
    request.remote = await chooseGitValue(command === "pull_from" ? "Pull from remote" : "Push to remote", remotes);
    if (!request.remote) return null;
    request.branch = window.prompt("Remote branch name", repository.currentBranch ?? "")?.trim() ?? "";
    if (!request.branch) return null;
  } else if (command === "add_remote") {
    request.name = window.prompt("Remote name", "origin")?.trim() ?? "";
    if (!request.name) return null;
    request.url = window.prompt("Remote URL")?.trim() ?? "";
    if (!request.url) return null;
  } else if (command === "remove_remote") {
    request.remote = await chooseGitValue("Remove remote", remotes);
    if (!request.remote || !window.confirm(`Remove remote ${request.remote}?`)) return null;
  } else if (["apply_stash", "pop_stash", "drop_stash"].includes(command)) {
    request.ref = await chooseGitValue(command === "apply_stash" ? "Apply stash" : command === "pop_stash" ? "Pop stash" : "Drop stash", stashes);
    if (!request.ref || command === "drop_stash" && !window.confirm(`Drop ${request.ref}? This cannot be undone.`)) return null;
  } else if (command === "drop_all_stashes") {
    if (!window.confirm("Drop all stashes? This cannot be undone.")) return null;
  } else if (["stash", "stash_untracked", "stash_staged"].includes(command)) {
    request.message = window.prompt("Stash message (optional)")?.trim() ?? "";
  } else if (command === "create_tag") {
    request.name = window.prompt("Tag name")?.trim() ?? "";
    if (!request.name) return null;
    request.message = window.prompt("Annotated tag message (optional; leave blank for a lightweight tag)")?.trim() ?? "";
  } else if (command === "delete_tag") {
    request.ref = await chooseGitValue("Delete tag", tags);
    if (!request.ref || !window.confirm(`Delete local tag ${request.ref}?`)) return null;
  } else if (command === "delete_remote_tag") {
    request.remote = await chooseGitValue("Delete tag from remote", remotes);
    if (!request.remote) return null;
    request.ref = window.prompt("Remote tag name")?.trim() ?? "";
    if (!request.ref || !window.confirm(`Delete tag ${request.ref} from ${request.remote}?`)) return null;
  } else if (command === "discard_all") {
    if (!window.confirm(`Discard all Git changes in ${repository.label}? This cannot be undone.`)) return null;
  } else if (command === "abort_rebase") {
    if (!window.confirm("Abort the current rebase and restore its original state?")) return null;
  }
  return request;
}

function chooseGitValue(title: string, options: Array<{ value: string; label: string }>, allowCustom = false): Promise<string> {
  return new Promise((resolve) => {
    const id = `git-picker-${Date.now()}-${Math.random().toString(16).slice(2)}`;
    const backdrop = document.createElement("aside");
    backdrop.className = "git-picker-backdrop";
    backdrop.setAttribute("role", "dialog");
    backdrop.setAttribute("aria-modal", "true");
    backdrop.setAttribute("aria-labelledby", `${id}-title`);
    backdrop.innerHTML = `
      <form class="git-picker-dialog">
        <header><strong id="${id}-title">${escapeHtml(title)}</strong><button class="icon-button" type="button" data-git-picker-cancel aria-label="Cancel">${icons.x}</button></header>
        <label><span>Search or select</span><input type="text" autocomplete="off" list="${id}-options" data-git-picker-input ${allowCustom ? "" : "required"} /></label>
        <datalist id="${id}-options">${options.map((item) => `<option value="${escapeAttribute(item.value)}">${escapeHtml(item.label)}</option>`).join("")}</datalist>
        <div class="git-picker-options" role="listbox">${options.map((item) => `<button type="button" data-git-picker-value="${escapeAttribute(item.value)}" title="${escapeAttribute(item.label)}">${escapeHtml(item.label)}</button>`).join("")}</div>
        <footer><button class="secondary-button" type="button" data-git-picker-cancel>Cancel</button><button class="primary-button" type="submit">Select</button></footer>
      </form>`;
    const finish = (value: string) => {
      backdrop.remove();
      resolve(value);
    };
    const input = backdrop.querySelector<HTMLInputElement>("[data-git-picker-input]")!;
    backdrop.querySelectorAll<HTMLElement>("[data-git-picker-cancel]").forEach((button) => button.addEventListener("click", () => finish("")));
    backdrop.querySelectorAll<HTMLButtonElement>("[data-git-picker-value]").forEach((button) => button.addEventListener("click", () => finish(button.dataset.gitPickerValue ?? "")));
    backdrop.querySelector<HTMLFormElement>("form")?.addEventListener("submit", (event) => {
      event.preventDefault();
      const value = input.value.trim();
      const exact = options.find((item) => item.value === value);
      if (exact || allowCustom) finish(exact?.value ?? value);
      else input.setCustomValidity("Select a value from the list.");
      input.reportValidity();
    });
    input.addEventListener("input", () => {
      input.setCustomValidity("");
      const query = input.value.trim().toLowerCase();
      backdrop.querySelectorAll<HTMLButtonElement>("[data-git-picker-value]").forEach((button) => {
        button.hidden = Boolean(query) && !button.textContent?.toLowerCase().includes(query) && !button.dataset.gitPickerValue?.toLowerCase().includes(query);
      });
    });
    backdrop.addEventListener("keydown", (event) => {
      if (event.key === "Escape") finish("");
    });
    appRoot.append(backdrop);
    input.focus();
  });
}

async function cloneGitRepositoryIntoWorkspace(workspaceID: string) {
  const url = window.prompt("Repository URL")?.trim() ?? "";
  if (!url) return;
  let parent = "";
  try {
    parent = (await ChooseWorkspaceGitCloneParent()).trim();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    return;
  }
  if (!parent) return;
  const suggested = gitCloneDirectoryName(url);
  const directory = window.prompt("Clone directory name", suggested)?.trim() ?? "";
  if (!directory) return;
  closeGitMenu();
  state.gitRepositoryOperations.set(workspaceID, "Cloning repository");
  getAppCallbacks().render();
  try {
    state.appState = await CloneWorkspaceGitRepository(workspaceID, url, parent, directory);
    await refreshWorkspaceGitRepository(workspaceID, "", false);
    pushToast("Cloned repository and added it to this workspace.", "success");
  } catch (error) {
    pushToast(errorMessage(error), "error");
  } finally {
    state.gitRepositoryOperations.delete(workspaceID);
    getAppCallbacks().render();
  }
}

function gitCloneDirectoryName(url: string): string {
  return url.trim().replace(/[\\/]+$/, "").split(/[\\/:]/).pop()?.replace(/\.git$/i, "") ?? "repository";
}

function gitActionOperationLabel(command: string): string {
  const labels: Record<string, string> = { pull: "Pulling", pull_rebase: "Pulling with rebase", pull_from: "Pulling", push: "Pushing", push_to: "Pushing", fetch: "Fetching", fetch_prune: "Fetching and pruning", fetch_all: "Fetching remotes", sync: "Syncing branch", checkout: "Checking out", merge: "Merging", rebase: "Rebasing", create_branch: "Creating branch", create_branch_from: "Creating branch", rename_branch: "Renaming branch", delete_branch: "Deleting branch", delete_remote_branch: "Deleting remote branch", publish_branch: "Publishing branch", add_remote: "Adding remote", remove_remote: "Removing remote", stash: "Stashing changes", stash_untracked: "Stashing changes", stash_staged: "Stashing staged changes", apply_latest_stash: "Applying stash", apply_stash: "Applying stash", pop_latest_stash: "Popping stash", pop_stash: "Popping stash", drop_stash: "Dropping stash", drop_all_stashes: "Dropping stashes", create_tag: "Creating tag", delete_tag: "Deleting tag", delete_remote_tag: "Deleting remote tag", push_tags: "Pushing tags", stage_all: "Staging changes", unstage_all: "Unstaging changes", discard_all: "Discarding changes", abort_rebase: "Aborting rebase", commit_staged: "Committing", commit_all: "Committing", commit_staged_amend: "Amending commit", commit_all_amend: "Amending commit", commit_staged_signoff: "Committing", commit_all_signoff: "Committing" };
  return labels[command] ?? "Running Git action";
}

function gitActionChangesWorktree(command: string): boolean {
  return ["pull", "pull_rebase", "pull_from", "sync", "checkout", "merge", "rebase", "abort_rebase", "apply_latest_stash", "apply_stash", "pop_latest_stash", "pop_stash", "discard_all", "create_branch", "create_branch_from"].includes(command);
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
  if (!patchGitSourceSidebarCollapsedState(state.collapsedGitChangeTrees.has(key))) {
    getAppCallbacks().render();
  }
}

export function toggleGitHistory() {
  const workspace = activeWorkspace();
  const repository = gitRepositoryViewFor(workspace?.id ?? "").repository;
  if (!workspace || !repository) {
    return;
  }
  const key = gitRepositoryDraftKey(workspace.id, repository.folderId);
  if (state.expandedGitHistories.has(key)) {
    state.expandedGitHistories.delete(key);
  } else {
    state.expandedGitHistories.add(key);
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
    invalidateGitWorkingDiffs(workspaceID, folderID);
  }
  const selectedHash = state.selectedGitCommitHashes.get(workspaceID);
  if (selectedHash && !(view.repository?.commits ?? []).some((commit) => commit.hash === selectedHash)) {
    state.selectedGitCommitHashes.delete(workspaceID);
  }
}

function invalidateGitWorkingDiffs(workspaceID: string, folderID: string) {
  const generationKey = gitWorkingDiffGenerationKey(workspaceID, folderID);
  state.gitWorkingDiffGenerations.set(generationKey, (state.gitWorkingDiffGenerations.get(generationKey) ?? 0) + 1);
  const prefix = `${generationKey}\u0000`;
  for (const key of state.gitWorkingDiffs.keys()) {
    if (key.startsWith(prefix)) {
      state.gitWorkingDiffs.delete(key);
    }
  }
  for (const key of state.gitWorkingDiffFailures) {
    if (key.startsWith(prefix)) {
      state.gitWorkingDiffFailures.delete(key);
    }
  }
  for (const key of state.loadingGitWorkingDiffs) {
    if (key.startsWith(prefix)) {
      state.loadingGitWorkingDiffs.delete(key);
    }
  }
}

function gitWorkingDiffGenerationKey(workspaceID: string, folderID: string): string {
  return `${workspaceID}\u0000${folderID}`;
}

function gitWorkingDiffKey(workspaceID: string, folderID: string, path: string, scope: GitWorkingDiffScope): string {
  return `${gitWorkingDiffGenerationKey(workspaceID, folderID)}\u0000${scope}\u0000${normalizeGitChangePath(path)}`;
}

function gitWorkingDiffScope(value: string | undefined): GitWorkingDiffScope | null {
  return value === "staged" || value === "unstaged" ? value : null;
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

function patchGitSourceSidebarCollapsedState(collapsed: boolean): boolean {
  const layout = appRoot.querySelector<HTMLElement>("[data-git-source-layout]");
  if (!layout) {
    return false;
  }
  layout.classList.toggle("is-sidebar-collapsed", collapsed);
  layout
    .querySelectorAll<HTMLButtonElement>("[data-action='toggle-git-sidebar']")
    .forEach((button) => {
      button.title = collapsed ? "Expand source control" : "Collapse source control";
      button.setAttribute("aria-label", button.title);
    });
  return true;
}

function scheduleScrollToGitCommitReview(hash: string) {
  window.requestAnimationFrame(() => {
    window.requestAnimationFrame(() => {
      const target = Array.from(appRoot.querySelectorAll<HTMLElement>("[data-git-commit-review-header]"))
        .find((element) => element.dataset.commitHash === hash);
      target?.scrollIntoView({ behavior: "smooth", block: "start" });
    });
  });
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
