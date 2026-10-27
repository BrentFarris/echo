/**
 * Most-Recently-Used tab cycling, modeled after VS Code's Ctrl+Tab behavior.
 *
 * The component keeps a parallel list of tab ids ordered by recency
 * (most-recently-opened first). Cycling walks that ordering in a stable
 * snapshot, then falls back to tab-strip order for open tabs that do not yet
 * have history (for example, immediately after restoring a session).
 */

export type MruCycleState = {
  /** Stable tab ids with the initiating tab first, followed by MRU entries. */
  order: string[];
  /** -1 means the session is fresh and no key press has landed yet. */
  index: number;
  /** Tab that was active when the cycle began, when it is still open. */
  sourceId: string | null;
};

/** Begin a cycle with the source first, then known MRU and unseen open tabs. */
export function beginMruCycle(
  mruTabIds: string[],
  activeId: string | null,
  openIds: string[],
): MruCycleState {
  const open = new Set(openIds);
  const seen = new Set<string>();
  const order: string[] = [];
  const sourceId = activeId && open.has(activeId) ? activeId : null;
  const append = (id: string) => {
    if (!open.has(id) || seen.has(id)) return;
    seen.add(id);
    order.push(id);
  };
  if (sourceId) append(sourceId);
  mruTabIds.forEach(append);
  openIds.forEach(append);
  return { order, index: -1, sourceId };
}

/**
 * Advance the cycle. The first press lands on the most recent other tab;
 * subsequent pressed taps step backward (or forward when `reverse` is set).
 */
export function nextMruCycle(
  state: MruCycleState,
  reverse: boolean,
): { nextId: string | null; state: MruCycleState } {
  if (state.order.length === 0) return { nextId: null, state };
  let index = state.index;
  if (index === -1) {
    const sourceIndex = state.sourceId ? state.order.indexOf(state.sourceId) : -1;
    index = sourceIndex >= 0 && state.order.length > 1 ? (sourceIndex + 1) % state.order.length : 0;
  }
  else index = (index + (reverse ? -1 : 1) + state.order.length) % state.order.length;
  return { nextId: state.order[index], state: { ...state, index } };
}

/** Drop candidate ids that are no longer open, keeping the session stable. */
export function pruneMruCycle(state: MruCycleState, openIds: string[]): MruCycleState {
  const order = state.order.filter((id) => openIds.includes(id));
  if (order.length === 0) return { ...state, order, index: -1 };
  if (state.index < 0) return { ...state, order, index: -1 };
  const id = state.order[state.index];
  const index = order.indexOf(id);
  return { ...state, order, index };
}

/** Remove a closed tab from the recency list. */
export function removeFromMru(ids: string[], closedId: string): string[] {
  return ids.filter((id) => id !== closedId);
}
