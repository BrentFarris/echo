export type TabDropPosition = "before" | "after";

/**
 * Return a reordered copy of the tab list for a drop, or null when the drop is
 * invalid or would leave the order unchanged. A null target means the trailing
 * space after the final tab.
 */
export function reorderTabs<T extends { id: string }>(
  tabs: readonly T[],
  sourceId: string,
  targetId: string | null,
  position: TabDropPosition,
): T[] | null {
  const sourceIndex = tabs.findIndex((tab) => tab.id === sourceId);
  if (sourceIndex < 0) return null;

  const targetIndex = targetId === null ? tabs.length : tabs.findIndex((tab) => tab.id === targetId);
  if (targetIndex < 0) return null;

  const boundary = targetId === null || position === "after" ? targetIndex + (targetId === null ? 0 : 1) : targetIndex;
  const destinationIndex = boundary - (sourceIndex < boundary ? 1 : 0);
  if (destinationIndex === sourceIndex) return null;

  const reordered = [...tabs];
  const [source] = reordered.splice(sourceIndex, 1);
  reordered.splice(destinationIndex, 0, source);
  return reordered;
}
