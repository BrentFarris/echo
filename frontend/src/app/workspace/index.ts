
import { services } from "../../../wailsjs/go/models";
import { icons } from "../icons";
import { state } from "../state";
import { escapeAttribute, escapeHtml, workspaceFolderStatus, workspaceFolderSummary } from "../utils";

export function workspaceLetter(workspace: services.Workspace): string {
  return (workspace.letter ?? "").trim() || workspace.displayName.slice(0, 1).toUpperCase() || "W";
}

export function renderWorkspaceIcon(workspace: services.Workspace): string {
  const iconURL = (workspace.iconUrl ?? "").trim();
  if (iconURL) {
    return `<img class="workspace-icon-image" src="${escapeAttribute(iconURL)}" alt="">`;
  }
  return `<span class="workspace-icon-label">${escapeHtml(workspaceLetter(workspace))}</span>`;
}

export function workspaceLetterDraft(workspace: services.Workspace): string {
  return state.workspaceLetterDrafts.get(workspace.id) ?? (workspace.letter ?? "");
}

export function hydrateWorkspaceLetterDrafts(workspaces: services.Workspace[]) {
  state.workspaceLetterDrafts.clear();
  workspaces.forEach((workspace) => {
    state.workspaceLetterDrafts.set(workspace.id, workspace.letter ?? "");
  });
}

export function renderMissingWorkspace(workspace: services.Workspace): string {
  return `
    <section class="missing-panel" aria-labelledby="missing-title">
      <div>
        <p class="eyebrow">Workspace unavailable</p>
        <h2 id="missing-title">Folder missing</h2>
      </div>
      <p>${escapeHtml(workspace.error || "Echo cannot find one or more workspace folders.")}</p>
      ${(workspace.folders ?? [])
        .map((folder) => `<code>${escapeHtml(folder.label)}: ${escapeHtml(folder.path)}</code>`)
        .join("")}
      <div class="missing-actions">
        <button class="primary-button icon-text-button" type="button" data-action="refresh-workspaces">
          ${icons.refresh}
          <span>Retry</span>
        </button>
        <button class="secondary-button" type="button" data-action="delete-workspace" data-workspace-id="${escapeHtml(workspace.id)}">Remove</button>
      </div>
    </section>
  `;
}

export function renderWorkspaceFolderSettings(workspace: services.Workspace): string {
  const folders = workspace.folders ?? [];
  const folderRows = folders.length
    ? folders
        .map(
          (folder) => `
            <div class="workspace-folder-row ${folder.missing ? "is-missing" : ""}">
              <div class="workspace-folder-main">
                <strong>${escapeHtml(folder.label)}${folder.missing ? " - Missing" : ""}</strong>
                <span>${escapeHtml(folder.path)}</span>
                <small>${escapeHtml(workspaceFolderStatus(folder))}</small>
              </div>
              <label class="settings-toggle workspace-folder-agents">
                <span>Use AGENTS.md</span>
                <input
                  type="checkbox"
                  ${folder.useAgents ? "checked" : ""}
                  data-workspace-folder-agents
                  data-workspace-id="${escapeAttribute(workspace.id)}"
                  data-folder-id="${escapeAttribute(folder.id)}"
                />
              </label>
              <button class="icon-button danger-button" type="button" title="Remove folder" aria-label="Remove ${escapeAttribute(folder.label)}" data-action="remove-workspace-folder" data-workspace-id="${escapeAttribute(workspace.id)}" data-folder-id="${escapeAttribute(folder.id)}">
                ${icons.trash}
              </button>
            </div>
          `,
        )
        .join("")
    : `<p class="empty-state compact">Blank workspace.</p>`;

  return `
    <div class="workspace-folder-list">
      ${folderRows}
      <button class="secondary-button icon-text-button" type="button" data-action="add-workspace-folder" data-workspace-id="${escapeAttribute(workspace.id)}">
        ${icons.plus}
        <span>Add folder</span>
      </button>
    </div>
  `;
}
