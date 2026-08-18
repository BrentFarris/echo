export type PrimaryNavView = "chat" | "explorer" | "git" | "settings";

export type PrimaryNavOptions = {
  active: PrimaryNavView;
  workspaceName?: string;
  workspaceSelector?: boolean;
};

function escapeHTML(value: string): string {
  return value.replace(/[&<>"']/g, (character) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[character]!));
}

function activeClass(active: boolean): string {
  return active ? " is-active" : "";
}

function currentPage(active: boolean): string {
  return active ? ' aria-current="page"' : "";
}

/**
 * Renders the canonical Echo activity bar shared by Chat and Code.
 * View-specific behavior is attached by the owning view through data-nav.
 */
export function renderPrimaryNav(options: PrimaryNavOptions): string {
  const workspaceName = options.workspaceName?.trim() || "No workspace";
  const initial = workspaceName.charAt(0).toUpperCase() || "E";
  const workspaceSelectorClass = options.workspaceSelector ? " workspace-dropdown-trigger" : "";
  const workspaceTitle = options.workspaceSelector
    ? "Select workspace"
    : `Workspace: ${workspaceName}`;
  const workspaceLabel = options.workspaceSelector ? "Workspace selector" : workspaceTitle;

  return `
    <aside class="left-nav code-left-nav" aria-label="Primary">
      <div class="left-nav-workspace">
        <button class="nav-icon-button${workspaceSelectorClass}" type="button" title="${escapeHTML(workspaceTitle)}" aria-label="${escapeHTML(workspaceLabel)}"${options.workspaceSelector ? ' aria-expanded="false"' : ""} data-nav="workspace">
          <span class="workspace-icon-label">${escapeHTML(initial)}</span>
        </button>
      </div>
      <nav class="left-nav-buttons" aria-label="Views">
        <button class="nav-icon-button${activeClass(options.active === "chat")}" type="button" title="Chat" aria-label="Chat" data-nav="chat"><span class="codicon codicon-comment-discussion"></span></button>
      </nav>
      <div class="left-nav-actions">
        <button class="nav-icon-button${activeClass(options.active === "explorer")}" type="button" title="Explorer" aria-label="Explorer" data-nav="code" data-code-sidebar="explorer"><span class="codicon codicon-code"></span></button>
        <button class="nav-icon-button" type="button" title="Tasks" aria-label="Tasks" disabled><span class="codicon codicon-checklist"></span></button>
        <button class="nav-icon-button code-git-activity${activeClass(options.active === "git")}" type="button" title="Source Control" aria-label="Source Control" data-nav="git" data-code-sidebar="git"><span class="codicon codicon-source-control"></span><b data-git-badge hidden></b></button>
        <button class="nav-icon-button" type="button" title="Settings" aria-label="Settings" data-nav="settings"><span class="codicon codicon-settings-gear"></span></button>
      </div>
    </aside>
  `;
}

/**
 * Renders the compact navigation used by every Echo route on narrow screens.
 * The owning view wires the shared data-nav/data-code-sidebar actions.
 */
export function renderMobilePrimaryNav(options: PrimaryNavOptions): string {
  const workspaceName = options.workspaceName?.trim() || "No workspace";
  const workspaceSelectorClass = options.workspaceSelector ? " workspace-dropdown-trigger" : "";
  const workspaceTitle = options.workspaceSelector
    ? `Switch workspace (${workspaceName})`
    : `Workspace: ${workspaceName}`;

  return `
    <nav class="mobile-bottom-nav" aria-label="Mobile primary navigation" data-mobile-primary-nav>
      <div class="mobile-nav-brand">
        <button class="mobile-nav-pill${workspaceSelectorClass}" type="button" title="${escapeHTML(workspaceTitle)}" aria-label="${escapeHTML(workspaceTitle)}"${options.workspaceSelector ? ' aria-expanded="false"' : ""} data-nav="workspace">
          <span class="codicon codicon-folder-library" aria-hidden="true"></span>
          <span class="mobile-nav-workspace-name" data-mobile-workspace-name>${escapeHTML(workspaceName)}</span>
        </button>
      </div>
      <div class="mobile-nav-tabs">
        <button class="mobile-nav-tab${activeClass(options.active === "chat")}" type="button" title="Chat" aria-label="Chat" data-nav="chat"${currentPage(options.active === "chat")}><span class="codicon codicon-comment-discussion" aria-hidden="true"></span></button>
        <button class="mobile-nav-tab${activeClass(options.active === "explorer")}" type="button" title="Code" aria-label="Code" data-nav="code" data-code-sidebar="explorer"${currentPage(options.active === "explorer")}><span class="codicon codicon-code" aria-hidden="true"></span></button>
        <button class="mobile-nav-tab code-git-activity${activeClass(options.active === "git")}" type="button" title="Source Control" aria-label="Source Control" data-nav="git" data-code-sidebar="git"${currentPage(options.active === "git")}><span class="codicon codicon-source-control" aria-hidden="true"></span><b data-git-badge hidden></b></button>
        <button class="mobile-nav-tab${activeClass(options.active === "settings")}" type="button" title="Settings" aria-label="Settings" data-nav="settings"${currentPage(options.active === "settings")}><span class="codicon codicon-settings-gear" aria-hidden="true"></span></button>
      </div>
    </nav>
  `;
}
