import { renderDesktopPluginButtons, renderMobilePluginOverflowButton } from "./plugins/catalog";

export type PrimaryNavView = "chat" | "explorer" | "search" | "git" | "debug" | "sandbox" | "settings" | "plugin";

export type PrimaryNavOptions = {
  active: PrimaryNavView;
  workspaceName?: string;
  workspaceSelector?: boolean;
  workspaceIconUrl?: string;
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
  const workspaceIcon = options.workspaceIconUrl
    ? `<img src="${escapeHTML(options.workspaceIconUrl)}" alt="" />`
    : escapeHTML(initial);
  const workspaceSelectorClass = options.workspaceSelector ? " workspace-dropdown-trigger" : "";
  const workspaceTitle = options.workspaceSelector
    ? "Select workspace"
    : `Workspace: ${workspaceName}`;
  const workspaceLabel = options.workspaceSelector ? "Workspace selector" : workspaceTitle;

  return `
    <aside class="left-nav code-left-nav" aria-label="Primary">
      <div class="left-nav-workspace">
        <button class="nav-icon-button${workspaceSelectorClass}" type="button" title="${escapeHTML(workspaceTitle)}" aria-label="${escapeHTML(workspaceLabel)}"${options.workspaceSelector ? ' aria-expanded="false"' : ""} data-nav="workspace">
          <span class="workspace-icon-label">${workspaceIcon}</span>
        </button>
      </div>
      <nav class="left-nav-buttons" aria-label="Views">
        <button class="nav-icon-button${activeClass(options.active === "chat")}" type="button" title="Chat (Ctrl+1)" aria-label="Chat" aria-keyshortcuts="Control+1" data-nav="chat"><span class="codicon codicon-comment-discussion"></span></button>
        <button class="nav-icon-button${activeClass(options.active === "explorer")}" type="button" title="Code (Ctrl+2)" aria-label="Explorer" aria-keyshortcuts="Control+2" data-nav="code" data-code-sidebar="explorer"><span class="codicon codicon-code"></span></button>
        <button class="nav-icon-button${activeClass(options.active === "search")}" type="button" title="Search (Ctrl+3)" aria-label="Search" aria-keyshortcuts="Control+3" data-nav="search" data-code-sidebar="search"><span class="codicon codicon-search"></span></button>
        <button class="nav-icon-button code-git-activity source-control-activity${activeClass(options.active === "git")}" type="button" title="Source Control (Ctrl+4)" aria-label="Source Control" aria-keyshortcuts="Control+4" data-nav="git" data-code-sidebar="git"><span class="codicon codicon-source-control"></span><b data-source-control-badge data-git-badge hidden></b></button>
        <button class="nav-icon-button${activeClass(options.active === "debug")}" type="button" title="Run and Debug (Ctrl+5)" aria-label="Run and Debug" aria-keyshortcuts="Control+5" data-nav="debug" data-code-sidebar="debug"><span class="codicon codicon-debug-alt"></span></button>
        <button class="nav-icon-button${activeClass(options.active === "sandbox")}" type="button" title="Linux Sandbox (Ctrl+6)" aria-label="Linux Sandbox" aria-keyshortcuts="Control+6" data-nav="sandbox"><span class="codicon codicon-vm"></span></button>
      </nav>
      <nav class="left-nav-plugins" aria-label="Plugin views" data-plugin-nav-section>${renderDesktopPluginButtons()}</nav>
      <div class="left-nav-actions">
        <button class="nav-icon-button" type="button" title="Chat Map (Ctrl+/)" aria-label="Chat Map" aria-keyshortcuts="Control+/" aria-haspopup="menu" aria-expanded="false" data-nav="map"><span class="codicon codicon-map"></span></button>
        <button class="nav-icon-button echo-update-target${activeClass(options.active === "settings")}" type="button" title="Settings (Ctrl+0)" aria-label="Settings" aria-keyshortcuts="Control+0" data-echo-update-target data-echo-update-label="Settings" data-nav="settings"><span class="codicon codicon-settings-gear"></span><b class="echo-update-badge" data-echo-update-badge hidden aria-hidden="true"><span class="codicon codicon-arrow-down" aria-hidden="true"></span></b></button>
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
        <button class="mobile-nav-tab${activeClass(options.active === "chat")}" type="button" title="Chat (Ctrl+1)" aria-label="Chat" aria-keyshortcuts="Control+1" data-nav="chat"${currentPage(options.active === "chat")}><span class="codicon codicon-comment-discussion" aria-hidden="true"></span></button>
        <button class="mobile-nav-tab${activeClass(options.active === "explorer")}" type="button" title="Code (Ctrl+2)" aria-label="Code" aria-keyshortcuts="Control+2" data-nav="code" data-code-sidebar="explorer"${currentPage(options.active === "explorer")}><span class="codicon codicon-code" aria-hidden="true"></span></button>
        <button class="mobile-nav-tab${activeClass(options.active === "search")}" type="button" title="Search (Ctrl+3)" aria-label="Search" aria-keyshortcuts="Control+3" data-nav="search" data-code-sidebar="search"${currentPage(options.active === "search")}><span class="codicon codicon-search" aria-hidden="true"></span></button>
        <button class="mobile-nav-tab code-git-activity source-control-activity${activeClass(options.active === "git")}" type="button" title="Source Control (Ctrl+4)" aria-label="Source Control" aria-keyshortcuts="Control+4" data-nav="git" data-code-sidebar="git"${currentPage(options.active === "git")}><span class="codicon codicon-source-control" aria-hidden="true"></span><b data-source-control-badge data-git-badge hidden></b></button>
        <button class="mobile-nav-tab${activeClass(options.active === "debug")}" type="button" title="Run and Debug (Ctrl+5)" aria-label="Run and Debug" aria-keyshortcuts="Control+5" data-nav="debug" data-code-sidebar="debug"${currentPage(options.active === "debug")}><span class="codicon codicon-debug-alt" aria-hidden="true"></span></button>
        <button class="mobile-nav-tab${activeClass(options.active === "sandbox")}" type="button" title="Linux Sandbox (Ctrl+6)" aria-label="Linux Sandbox" aria-keyshortcuts="Control+6" data-nav="sandbox"${currentPage(options.active === "sandbox")}><span class="codicon codicon-vm" aria-hidden="true"></span></button>
        <span class="mobile-plugin-slot" data-plugin-mobile-slot>${renderMobilePluginOverflowButton()}</span>
        <span class="mobile-nav-global-actions">
          <button class="mobile-nav-tab" type="button" title="Chat Map (Ctrl+/)" aria-label="Chat Map" aria-keyshortcuts="Control+/" aria-haspopup="menu" aria-expanded="false" data-nav="map"><span class="codicon codicon-map" aria-hidden="true"></span></button>
          <button class="mobile-nav-tab echo-update-target${activeClass(options.active === "settings")}" type="button" title="Settings (Ctrl+0)" aria-label="Settings" aria-keyshortcuts="Control+0" data-echo-update-target data-echo-update-label="Settings" data-nav="settings"${currentPage(options.active === "settings")}><span class="codicon codicon-settings-gear" aria-hidden="true"></span><b class="echo-update-badge" data-echo-update-badge hidden aria-hidden="true"><span class="codicon codicon-arrow-down" aria-hidden="true"></span></b></button>
        </span>
      </div>
    </nav>
  `;
}
