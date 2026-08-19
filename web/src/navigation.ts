export const CHAT_ROUTE = "/home";
export const SETTINGS_ROUTE = "/settings";
export const CODE_ROUTE = "/code";

export type CodeSidebar = "explorer" | "git";
export type CodeOpenTarget = { rootId: string; path: string };

/** Returns the routable path while leaving hash query parameters to the view. */
export function routePathFromHash(hash: string): string {
  const route = hash.replace(/^#/, "");
  return route.split("?", 1)[0] || "/";
}

/** Resolves the requested Echo Code sidebar from a deep-link hash. */
export function codeSidebarFromHash(hash: string): CodeSidebar {
  if (routePathFromHash(hash) !== CODE_ROUTE) return "explorer";
  const queryIndex = hash.indexOf("?");
  if (queryIndex < 0) return "explorer";
  return new URLSearchParams(hash.slice(queryIndex + 1)).get("sidebar") === "git" ? "git" : "explorer";
}

/** Builds the canonical hash for an Echo Code sidebar. */
export function codeRouteHash(sidebar: CodeSidebar): string {
  return `#${CODE_ROUTE}${sidebar === "git" ? "?sidebar=git" : ""}`;
}

/** Builds a transient Echo Code route that opens a specific workspace file. */
export function codeFileRouteHash(target: CodeOpenTarget): string {
  const query = new URLSearchParams({ rootId: target.rootId, path: target.path });
  return `#${CODE_ROUTE}?${query}`;
}

/** Returns the file-open target embedded in a Code hash, if present. */
export function codeOpenTargetFromHash(hash: string): CodeOpenTarget | null {
  if (routePathFromHash(hash) !== CODE_ROUTE) return null;
  const queryIndex = hash.indexOf("?");
  if (queryIndex < 0) return null;
  const query = new URLSearchParams(hash.slice(queryIndex + 1));
  const rootId = query.get("rootId") || "";
  const path = query.get("path") || "";
  return rootId && path ? { rootId, path } : null;
}

/**
 * Tracks view transitions in memory so Settings can distinguish an in-app
 * visit from a page that was loaded directly at #/settings.
 */
export class NavigationTracker {
  private activeRoute: string | null = null;
  private settingsOrigin: string | null = null;

  record(route: string): void {
    if (route === SETTINGS_ROUTE && this.activeRoute && this.activeRoute !== SETTINGS_ROUTE) {
      this.settingsOrigin = this.activeRoute;
    }
    this.activeRoute = route;
  }

  hasSettingsOrigin(): boolean {
    return this.settingsOrigin !== null;
  }

  settingsReturnRoute(): string {
    return this.settingsOrigin || CHAT_ROUTE;
  }
}

const navigation = new NavigationTracker();

export function recordNavigationRoute(route: string): void {
  navigation.record(route);
}

export function navigateBackFromSettings(): void {
  if (navigation.hasSettingsOrigin()) {
    // Settings is entered through a normal hash navigation, so popping the
    // entry preserves expected Back/Forward history behavior.
    window.history.back();
    return;
  }
  window.location.hash = `#${CHAT_ROUTE}`;
}
