import { ensureCodeViewRootLoaded, openWorkspaceCodeFileAtLine, refreshOpenCodeTabsFromDisk } from "../../codeView";
import { CommitWorkspaceGitChanges, CreateWorkspaceGitBranch, DiscardWorkspaceGitChanges, DiscardWorkspaceGitFile, LoadWorkspaceChangeReview, LoadWorkspaceGitCommit, LoadWorkspaceGitRepository, MergeWorkspaceGitBranch, SwitchWorkspaceGitBranch, SyncWorkspaceGitBranch } from "../../backend/services";
import { services } from "../../../wailsjs/go/models";
import { getAppCallbacks } from "../callbacks";
import { renderSpinnerLabel } from "../components";
import { icons } from "../icons";
import { activeWorkspace, changeReviewFor, gitRepositoryViewFor, state } from "../state";
import { pushToast } from "../toasts";
import { changeOperationLabel, errorMessage, escapeAttribute, escapeHtml } from "../utils";
import { renderChangeReviewPage, renderGitChangedFile, renderGitDiff } from "../changes";

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
  return `
    <section class="work-panel git-repository" aria-labelledby="git-repository-title" data-change-review data-git-repository>
      <header class="panel-heading git-repository-header">
        <div>
          <p class="eyebrow">${escapeHtml(workspace.displayName)}</p>
          <h2 id="git-repository-title">Git</h2>
        </div>
      </header>

      <div class="git-repository-topbar">
        <label class="field git-repository-picker">
          <span>Repository</span>
          <select data-git-repository-select ${loading || operation ? "disabled" : ""}>
            ${repositories.length
              ? repositories.map((item) => renderGitRepositoryOption(item, selectedFolderID)).join("")
              : `<option value="">No repositories</option>`}
          </select>
        </label>
        ${renderGitRepositorySummary(repository, loading, operation)}
      </div>

      <div class="change-review-actions">
        <button class="icon-button" type="button" title="Previous change" aria-label="Previous change" data-action="previous-change" ${repository?.fileCount ? "" : "disabled"}>
          ${icons.arrowUp}
        </button>
        <button class="icon-button" type="button" title="Next change" aria-label="Next change" data-action="next-change" ${repository?.fileCount ? "" : "disabled"}>
          ${icons.arrowDown}
        </button>
        ${renderGitRefreshOrSyncButton(repository, loading, operation)}
        <button class="secondary-button icon-text-button danger-button" type="button" data-action="revert-git-changes" ${repository?.fileCount && !loading && !operation ? "" : "disabled"}>
          ${operation === "Reverting changes" ? `<span class="spinner" aria-hidden="true"></span>` : icons.undo}
          <span>Revert All</span>
        </button>
      </div>

      ${repository
        ? `
          <div class="git-management-grid">
            ${renderGitCommitForm(workspace.id, repository, operation)}
            ${renderGitBranchControls(workspace.id, repository, operation)}
          </div>
          <div class="git-repository-layout">
            <section class="git-panel git-working-tree" aria-labelledby="git-working-title">
              <header>
                <h3 id="git-working-title">Working Changes</h3>
                <span>${escapeHtml(String(repository.fileCount ?? 0))} files</span>
              </header>
              ${(repository.files ?? []).length
                ? `<div class="change-file-list">${(repository.files ?? []).map(renderGitChangedFile).join("")}</div>`
                : `<div class="empty-state compact">No Git changes.</div>`}
            </section>
            <section class="git-panel git-history" aria-labelledby="git-history-title">
              <header>
                <h3 id="git-history-title">History</h3>
                <span>${escapeHtml(String((repository.commits ?? []).length))} commits</span>
              </header>
              ${renderGitCommitHistory(workspace.id, repository)}
              ${renderGitSelectedCommit(workspace.id, repository)}
            </section>
          </div>
        `
        : `<div class="empty-state compact">${loading ? renderSpinnerLabel("Loading Git") : "No manageable Git repository."}</div>`}
    </section>
  `;
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
    <div class="change-review-summary git-repository-summary" aria-label="Git summary">
      <span>${escapeHtml(branch)}</span>
      <span>${repository.dirty ? "Dirty" : "Clean"}</span>
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
      <button class="primary-button icon-text-button" type="submit" ${repository.dirty && draft.trim() && !busy ? "" : "disabled"}>
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
      ${commits.map((commit) => renderGitCommitItem(commit, selectedHash)).join("")}
    </div>
  `;
}

function renderGitCommitItem(commit: services.WorkspaceGitCommit, selectedHash: string): string {
  const selected = selectedHash === commit.hash;
  return `
    <button class="git-commit-item ${selected ? "is-selected" : ""}" type="button" data-action="select-git-commit" data-commit-hash="${escapeAttribute(commit.hash)}">
      <strong title="${escapeAttribute(commit.subject)}">${escapeHtml(commit.subject || commit.shortHash)}</strong>
      <span>${escapeHtml(commit.shortHash)} · ${escapeHtml(commit.authorName || "Unknown")} · ${escapeHtml(formatGitDate(commit.authoredAt))}</span>
    </button>
  `;
}

function renderGitSelectedCommit(workspaceID: string, repository: services.WorkspaceGitRepositoryStatus): string {
  const selectedHash = state.selectedGitCommitHashes.get(workspaceID) ?? "";
  if (!selectedHash) {
    return "";
  }
  const key = gitCommitDetailKey(workspaceID, repository.folderId, selectedHash);
  if (state.loadingGitCommitDetails.has(key)) {
    return `<div class="git-commit-detail">${renderSpinnerLabel("Loading commit")}</div>`;
  }
  const detail = state.gitCommitDetails.get(key);
  if (!detail) {
    return "";
  }
  const files = detail.files ?? [];
  return `
    <article class="git-commit-detail">
      <header>
        <div>
          <h3 title="${escapeAttribute(detail.commit.subject)}">${escapeHtml(detail.commit.subject || detail.commit.shortHash)}</h3>
          <span>${escapeHtml(detail.commit.shortHash)} · ${escapeHtml(detail.commit.authorName || "Unknown")} · ${escapeHtml(formatGitDate(detail.commit.authoredAt))}</span>
        </div>
      </header>
      ${files.length
        ? `<div class="change-file-list">${files.map(renderGitCommitChangedFile).join("")}</div>`
        : `<div class="empty-state compact">No changed files.</div>`}
    </article>
  `;
}

function renderGitCommitChangedFile(file: services.WorkspaceGitChangedFile): string {
  return `
    <article class="change-file" data-change-file>
      <header>
        <div class="change-file-title">
          ${icons.file}
          <strong title="${escapeAttribute(file.path)}">${escapeHtml(file.path)}</strong>
        </div>
        <span class="change-operation is-${escapeAttribute(file.operation)}">${escapeHtml(changeOperationLabel(file.operation))}</span>
      </header>
      ${file.diffAvailable && file.diff ? renderGitDiff(file.diff, "") : `<div class="change-metadata"><span>Diff is unavailable.</span></div>`}
    </article>
  `;
}

export function bindGitEvents(root: ParentNode) {
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
    .querySelectorAll<HTMLSelectElement>("[data-git-switch-branch-select], [data-git-merge-branch-select]")
    .forEach((select) => select.addEventListener("change", handleGitBranchSelectChange));
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

function isNoManageableGitRepositoryError(error: unknown): boolean {
  return errorMessage(error).toLowerCase().includes("no manageable git repositories");
}

export async function selectGitCommit(hash: string) {
  const workspace = activeWorkspace();
  if (!workspace || !hash) {
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
    button.disabled = !repository?.dirty || !(textarea?.value.trim()) || busy;
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
