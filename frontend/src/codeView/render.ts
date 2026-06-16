import { services } from "../../wailsjs/go/models";
import { codeIcons } from "./icons";
import { activeCodeTab, directoryStateFor, ensureCodeState, filteredEntries } from "./state";
import type { CodeFileTab, CodeWorkspaceState } from "./types";
import { escapeAttribute, escapeHtml, fileName, formatBytes } from "./utils";

export function renderCodeView(workspace: services.Workspace): string {
  const state = ensureCodeState(workspace.id);
  const activeTab = activeCodeTab(workspace.id);
  const dirtyCount = state.tabs.filter((tab) => tab.dirty).length;
  const saveDisabled = !activeTab || !activeTab.dirty || activeTab.saving;
  const filterLabel = state.showIgnored ? "Hide ignored" : "Show ignored";
  return `
    <section
      class="code-view"
      aria-labelledby="code-title"
      data-code-view
      data-code-view-workspace-id="${escapeAttribute(workspace.id)}"
    >
      <header class="code-view-heading">
        <div>
          <strong id="code-title">${escapeHtml(workspace.displayName)}</strong><span class="heading-path">${escapeHtml(codeWorkspaceFolderSummary(workspace))}</span>
        </div>
        <div class="code-view-actions">
          <button class="secondary-button icon-text-button" type="button" data-action="close-code-view">
            ${codeIcons.back}
            <span>Chat</span>
          </button>
          <button class="secondary-button icon-text-button" type="button" data-action="open-git-changes">
            ${codeIcons.git}
            <span>Git</span>
          </button>
          <button class="secondary-button icon-text-button" type="button" data-code-action="toggle-filter" aria-pressed="${state.showIgnored}">
            ${codeIcons.code}
            <span>${escapeHtml(filterLabel)}</span>
          </button>
          <button class="primary-button icon-text-button" type="button" data-code-action="save-active-file" data-code-save ${saveDisabled ? "disabled" : ""}>
            ${activeTab?.saving ? `<span class="spinner" aria-hidden="true"></span>` : codeIcons.save}
            <span>Save</span>
          </button>
        </div>
      </header>
      <div class="code-workspace" style="--code-explorer-width: ${state.explorerWidth}px">
        <aside class="code-explorer" aria-label="Workspace files">
          <div class="code-explorer-meta">
            <span data-code-dirty-summary>${dirtyCount ? `${dirtyCount} unsaved` : "Files"}</span>
            <div class="code-explorer-toolbar" aria-label="File explorer actions">
              <button class="icon-button" type="button" title="New file" aria-label="New file" data-code-action="create-selected-file">
                ${codeIcons.newFile}
              </button>
              <button class="icon-button" type="button" title="New folder" aria-label="New folder" data-code-action="create-selected-folder">
                ${codeIcons.newFolder}
              </button>
              <button class="icon-button" type="button" title="Refresh files" aria-label="Refresh files" data-code-action="refresh-tree">
                ${codeIcons.refresh}
              </button>
              <button class="icon-button" type="button" title="Collapse all" aria-label="Collapse all folders" data-code-action="collapse-tree">
                ${codeIcons.collapseAll}
              </button>
            </div>
          </div>
          <label class="code-search">
            <span>Search files</span>
            <input
              type="search"
              value="${escapeAttribute(state.searchQuery)}"
              placeholder="Search files..."
              aria-label="Search files"
              data-code-search
            />
          </label>
          <div class="code-tree" role="tree" data-code-tree>
            ${renderFileList(workspace.id)}
          </div>
        </aside>
        <div class="code-resizer" role="separator" aria-label="Resize file list" aria-orientation="vertical" tabindex="0" data-code-resizer></div>
        <section class="code-editor-pane" aria-label="Code editor">
          ${renderCodeTabs(workspace.id)}
          ${renderCodeTabSwitcher(workspace.id)}
          <div class="code-editor-frame">
            ${
              activeTab
                ? `<div class="code-editor-mount" data-code-editor-mount></div>`
                : `<div class="empty-state code-empty">
                    <strong>No file open</strong>
                    <span>Select a text file in the workspace tree.</span>
                  </div>`
            }
          </div>
          <footer class="code-status-line" data-code-status>
            ${renderCodeStatus(activeTab, state.openingPath)}
          </footer>
        </section>
      </div>
    </section>
  `;
}

function codeWorkspaceFolderSummary(workspace: services.Workspace): string {
  const folders = workspace.folders ?? [];
  if (!folders.length) {
    return "No folders";
  }
  return folders
    .map((folder) => `${folder.label}: ${folder.path}${folder.missing ? " (missing)" : ""}`)
    .join(" | ");
}

export function renderFileList(workspaceID: string): string {
  const state = ensureCodeState(workspaceID);
  if (state.searchQuery.trim()) {
    return renderSearchResults(workspaceID);
  }
  return renderDirectoryEntries(workspaceID, ".", 0);
}

function renderSearchResults(workspaceID: string): string {
  const state = ensureCodeState(workspaceID);
  if (state.searchLoading) {
    return `<div class="code-tree-note"><span class="spinner" aria-hidden="true"></span><span>Searching...</span></div>`;
  }
  if (!state.searchResults.length) {
    return `<div class="code-tree-note">No matches.</div>`;
  }
  const results = state.searchResults
    .map((entry) => renderSearchEntry(workspaceID, state, entry))
    .join("");
  return `
    <div class="code-search-results">
      ${state.searchTruncated ? `<div class="code-tree-note">Showing first 200 matches.</div>` : ""}
      ${results}
    </div>
  `;
}

function renderSearchEntry(
  workspaceID: string,
  state: CodeWorkspaceState,
  entry: services.WorkspaceFileEntry,
): string {
  const active = state.activePath === entry.path;
  const selected = state.selectedPath === entry.path;
  const icon = entry.kind === "directory" ? codeIcons.folder : codeIcons.file;
  if (entry.kind !== "file") {
    return `
      <div
        class="code-tree-row code-tree-search-row ${selected ? "is-selected" : ""}"
        role="treeitem"
        title="${escapeAttribute(entry.path)}"
        style="--tree-depth: 0"
        data-code-browser-row
        data-code-path="${escapeAttribute(entry.path)}"
        data-code-kind="${escapeAttribute(entry.kind)}"
      >
        <span class="code-tree-spacer"></span>
        <span class="code-tree-entry-icon">${icon}</span>
        <span class="code-tree-search-name">
          <strong>${escapeHtml(entry.name)}</strong>
          <span>${escapeHtml(entry.path)}</span>
        </span>
        <span class="code-tree-size">Folder</span>
      </div>
    `;
  }
  return `
    <button
      class="code-tree-row code-tree-file code-tree-search-row ${active ? "is-active" : ""} ${selected ? "is-selected" : ""}"
      type="button"
      role="treeitem"
      title="${escapeAttribute(entry.path)}"
      style="--tree-depth: 0"
      data-code-browser-row
      data-code-file-row
      data-code-path="${escapeAttribute(entry.path)}"
      data-code-kind="${escapeAttribute(entry.kind)}"
    >
      <span class="code-tree-spacer"></span>
      <span class="code-tree-entry-icon">${icon}</span>
      <span class="code-tree-search-name">
        <strong>${escapeHtml(entry.name)}</strong>
        <span>${escapeHtml(entry.path)}</span>
      </span>
      <span class="code-tree-size">${escapeHtml(formatBytes(entry.bytes ?? 0))}</span>
    </button>
  `;
}

function renderDirectoryEntries(
  workspaceID: string,
  path: string,
  depth: number,
): string {
  const state = ensureCodeState(workspaceID);
  const directory = directoryStateFor(state, path);
  if (directory.loading && !directory.loaded) {
    return `<div class="code-tree-note"><span class="spinner" aria-hidden="true"></span><span>Loading files...</span></div>`;
  }
  if (directory.error) {
    return `<div class="code-tree-error">${escapeHtml(directory.error)}</div>`;
  }
  if (!directory.loaded) {
    return `<div class="code-tree-note">Open Code to load files.</div>`;
  }

  const entries = filteredEntries(state, directory.entries);
  const pendingCreate = state.pendingCreate?.parentPath === path
    ? renderPendingCreateRow(state, depth)
    : "";
  if (!entries.length) {
    return pendingCreate || `<div class="code-tree-note">No files.</div>`;
  }
  return pendingCreate + entries
    .map((entry) => renderFileEntry(workspaceID, state, entry, depth))
    .join("");
}

function renderPendingCreateRow(state: CodeWorkspaceState, depth: number): string {
  const pending = state.pendingCreate;
  if (!pending) {
    return "";
  }
  const icon = pending.kind === "folder" ? codeIcons.folder : codeIcons.file;
  const label = pending.kind === "folder" ? "Folder name" : "File name";
  return `
    <div
      class="code-tree-row code-tree-create-row is-selected"
      role="treeitem"
      style="--tree-depth: ${depth}"
      data-code-create-row
    >
      <span class="code-tree-spacer"></span>
      <span class="code-tree-entry-icon">${icon}</span>
      <input
        class="code-tree-create-input"
        type="text"
        value="${escapeAttribute(pending.name)}"
        placeholder="${escapeAttribute(label)}"
        aria-label="${escapeAttribute(label)}"
        data-code-create-input
        ${pending.submitting ? "disabled" : ""}
      />
      <span class="code-tree-create-state">
        ${pending.submitting ? `<span class="spinner" aria-hidden="true"></span>` : ""}
      </span>
    </div>
  `;
}

function renderFileEntry(
  workspaceID: string,
  state: CodeWorkspaceState,
  entry: services.WorkspaceFileEntry,
  depth: number,
): string {
  const active = state.activePath === entry.path;
  const selected = state.selectedPath === entry.path;
  if (entry.kind === "directory") {
    const expanded = state.expandedPaths.has(entry.path);
    const childDirectory = directoryStateFor(state, entry.path);
    return `
      <div class="code-tree-item">
        <button
          class="code-tree-row code-tree-directory ${expanded ? "is-expanded" : ""} ${selected ? "is-selected" : ""}"
          type="button"
          role="treeitem"
          aria-expanded="${expanded}"
          title="${escapeAttribute(entry.path)}"
          style="--tree-depth: ${depth}"
          data-code-action="toggle-directory"
          data-code-browser-row
          data-code-path="${escapeAttribute(entry.path)}"
          data-code-kind="${escapeAttribute(entry.kind)}"
        >
          <span class="code-tree-chevron">${codeIcons.chevron}</span>
          <span class="code-tree-entry-icon">${codeIcons.folder}</span>
          <span class="code-tree-name">${escapeHtml(entry.name)}</span>
        </button>
        ${
          expanded
            ? `<div role="group">
                ${
                  childDirectory.loading && !childDirectory.loaded
                    ? `<div class="code-tree-note nested" style="--tree-depth: ${depth + 1}"><span class="spinner" aria-hidden="true"></span><span>Loading...</span></div>`
                    : renderDirectoryEntries(workspaceID, entry.path, depth + 1)
                }
              </div>`
            : ""
        }
      </div>
    `;
  }
  return `
    <button
      class="code-tree-row code-tree-file ${active ? "is-active" : ""} ${selected ? "is-selected" : ""}"
      type="button"
      role="treeitem"
      title="${escapeAttribute(entry.path)}"
      style="--tree-depth: ${depth}"
      data-code-browser-row
      data-code-file-row
      data-code-path="${escapeAttribute(entry.path)}"
      data-code-kind="${escapeAttribute(entry.kind)}"
    >
      <span class="code-tree-spacer"></span>
      <span class="code-tree-entry-icon">${codeIcons.file}</span>
      <span class="code-tree-name">${escapeHtml(entry.name)}</span>
      <span class="code-tree-size">${escapeHtml(formatBytes(entry.bytes ?? 0))}</span>
    </button>
  `;
}

function renderCodeTabs(workspaceID: string): string {
  const state = ensureCodeState(workspaceID);
  if (!state.tabs.length) {
    return `<div class="code-tabs is-empty"></div>`;
  }
  return `
    <div class="code-tabs" role="tablist" aria-label="Open files">
      ${state.tabs
        .map((tab) => {
          const active = state.activePath === tab.path;
          return `
            <div class="code-tab ${active ? "is-active" : ""} ${tab.dirty ? "is-dirty" : ""} ${tab.temporary ? "is-temporary" : ""}" data-code-tab="${escapeAttribute(tab.path)}">
              <button class="code-tab-main" type="button" role="tab" aria-selected="${active}" title="${escapeAttribute(tab.path)}" data-code-action="activate-tab" data-code-tab-main data-code-path="${escapeAttribute(tab.path)}">
                <span>${escapeHtml(fileName(tab.path))}</span>
                ${tab.dirty ? `<span class="dirty-dot" aria-label="Unsaved changes"></span>` : ""}
              </button>
              <button class="code-tab-close" type="button" title="Close ${escapeAttribute(fileName(tab.path))}" aria-label="Close ${escapeAttribute(fileName(tab.path))}" data-code-action="close-tab" data-code-path="${escapeAttribute(tab.path)}">
                ${codeIcons.close}
              </button>
            </div>
          `;
        })
        .join("")}
    </div>
  `;
}

function renderCodeTabSwitcher(workspaceID: string): string {
  const state = ensureCodeState(workspaceID);
  const switcher = state.tabSwitcher;
  if (!switcher || switcher.paths.length <= 1) {
    return "";
  }
  const tabsByPath = new Map(state.tabs.map((tab) => [tab.path, tab]));
  return `
    <div class="code-tab-switcher" role="listbox" aria-label="Open file tabs">
      ${switcher.paths
        .map((path, index) => {
          const tab = tabsByPath.get(path);
          if (!tab) {
            return "";
          }
          const selected = index === switcher.selectedIndex;
          return `
            <button
              class="code-tab-switcher-item ${selected ? "is-selected" : ""}"
              type="button"
              role="option"
              aria-selected="${selected}"
              title="${escapeAttribute(tab.path)}"
              data-code-action="activate-switcher-tab"
              data-code-path="${escapeAttribute(tab.path)}"
            >
              <span class="code-tab-switcher-name">${escapeHtml(fileName(tab.path))}</span>
              <span class="code-tab-switcher-path">${escapeHtml(tab.path)}</span>
              ${tab.dirty ? `<span class="dirty-dot" aria-label="Unsaved changes"></span>` : ""}
            </button>
          `;
        })
        .join("")}
    </div>
  `;
}

export function renderCodeStatus(tab: CodeFileTab | null, openingPath: string): string {
  if (openingPath) {
    return `Opening ${escapeHtml(openingPath)}...`;
  }
  if (!tab) {
    return "No file selected.";
  }
  const state = tab.saving ? "Saving" : tab.dirty ? "Unsaved changes" : "Saved";
  return `${escapeHtml(tab.path)} - ${escapeHtml(formatBytes(tab.bytes))} - ${state}`;
}
