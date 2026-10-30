export type CodeSidebar = "explorer" | "search" | "bookmarks" | "source-control";
export const CODE_NAVIGATION_HISTORY_STATE_KEY = "echo2CodeNavigation";
export const isCodeNavigationHistoryState = (state: unknown): boolean => !!state && typeof state === "object" && CODE_NAVIGATION_HISTORY_STATE_KEY in state;
