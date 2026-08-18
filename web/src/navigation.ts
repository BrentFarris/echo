export const CHAT_ROUTE = "/home";
export const SETTINGS_ROUTE = "/settings";

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
