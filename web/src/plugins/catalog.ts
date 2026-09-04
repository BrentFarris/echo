import { get } from "../../js/api.js";
import type { CatalogPlugin, PluginCatalog, PluginView } from "./types";

const emptyCatalog: PluginCatalog = { safeMode: false, plugins: [], stages: [] };
let catalog: PluginCatalog = emptyCatalog;
let activeWorkspaceId = "";
let refreshPromise: Promise<PluginCatalog> | null = null;
let refreshQueued = false;

export function getPluginCatalog(): PluginCatalog { return catalog; }
export function getPluginWorkspaceId(): string { return activeWorkspaceId; }

export function getEffectivePluginViews(): Array<{ plugin: CatalogPlugin; view: PluginView }> {
  const views: Array<{ plugin: CatalogPlugin; view: PluginView }> = [];
  for (const plugin of catalog.plugins) {
    if (!plugin.effective) continue;
    for (const view of plugin.views || []) views.push({ plugin, view });
  }
  return views;
}

export async function refreshPluginCatalog(): Promise<PluginCatalog> {
  refreshQueued = true;
  if (refreshPromise) return refreshPromise;

  const run = async (): Promise<PluginCatalog> => {
    do {
      refreshQueued = false;
      const workspaceData = await get("/api/workspaces") as { activeId?: string };
      activeWorkspaceId = workspaceData.activeId || "";
      catalog = await get("/api/plugins", { query: { workspaceId: activeWorkspaceId } }) as PluginCatalog;
      window.dispatchEvent(new CustomEvent("echo:plugin-catalog", { detail: catalog }));
    } while (refreshQueued);
    return catalog;
  };

  const active = run();
  refreshPromise = active.then(
    (latest) => {
      refreshPromise = null;
      return refreshQueued ? refreshPluginCatalog() : latest;
    },
    (error) => {
      refreshPromise = null;
      if (refreshQueued) return refreshPluginCatalog();
      throw error;
    },
  );
  return refreshPromise;
}

export function clearPluginCatalog(): void {
  catalog = emptyCatalog;
  activeWorkspaceId = "";
  window.dispatchEvent(new CustomEvent("echo:plugin-catalog", { detail: catalog }));
}

function escapeHTML(value: string): string {
  return value.replace(/[&<>"']/g, character => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]!);
}

function icon(view: PluginView): string {
  return view.icon
    ? `<img class="plugin-nav-icon" src="${escapeHTML(view.icon)}" alt="" draggable="false">`
    : '<span class="codicon codicon-extensions" aria-hidden="true"></span>';
}

function isActive(pluginId: string, viewId: string): boolean {
  const route = location.hash.replace(/^#/, "").split("?")[0];
  return route === `/plugins/${encodeURIComponent(pluginId)}/${encodeURIComponent(viewId)}`;
}

export function renderDesktopPluginButtons(): string {
  return getEffectivePluginViews().map(({ plugin, view }) => `
    <button class="nav-icon-button plugin-nav-button${isActive(plugin.id, view.id) ? " is-active" : ""}" type="button"
      title="${escapeHTML(view.title)}" aria-label="${escapeHTML(view.title)}" data-plugin-id="${escapeHTML(plugin.id)}"
      data-plugin-view-id="${escapeHTML(view.id)}" data-plugin-view-kind="${view.kind}">${icon(view)}</button>
  `).join("");
}

export function renderMobilePluginOverflowButton(): string {
  const count = getEffectivePluginViews().length;
  if (!count) return "";
  return `<button class="mobile-nav-tab" type="button" title="Plugins" aria-label="Plugins" data-plugin-overflow aria-expanded="false"><span class="codicon codicon-extensions" aria-hidden="true"></span><b class="plugin-count">${count}</b></button>`;
}
