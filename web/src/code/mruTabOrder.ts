/**
 * Most-Recently-Used tab cycling, modeled after VS Code's Ctrl+Tab behavior.
 *
 * The component keeps a parallel list of open tab ids ordered by recency
 * (most-recently-opened first). Cycling walks that ordering in a stable
 * snapshot so that holding Ctrl and tapping Tab keeps stepping backward
 * through "last opened" order rather than the visual tab-strip order.
 */

export type MruCycleState = {
  /** Candidate tab ids, most-recent-first, with the active tab excluded. */
  order: string[];
  /** -1 means the session is fresh and no key press has landed yet. */
  index: number;
};

/** Begin (or resume) a cycling session from the MRU list. */
export function beginMruCycle(
  mruTabIds: string[],
  activeId: string | null,
  openIds: string[],
): MruCycleState {
  const order = mruTabIds.filter((id) => id !== activeId && openIds.includes(id));
  return { order, index: -1 };
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
  if (index === -1) index = 0;
  else index = (index + (reverse ? -1 : 1) + state.order.length) % state.order.length;
  return { nextId: state.order[index], state: { ...state, index } };
}

/** Drop candidate ids that are no longer open, keeping the session stable. */
export function pruneMruCycle(state: MruCycleState, openIds: string[]): MruCycleState {
  const order = state.order.filter((id) => openIds.includes(id));
  if (order.length === 0) return { order, index: -1 };
  if (state.index < 0) return { order, index: -1 };
  const id = state.order[state.index];
  const index = order.indexOf(id);
  return { order, index };
}

/** Remove a closed tab from the recency list. */
export function removeFromMru(ids: string[], closedId: string): string[] {
  return ids.filter((id) => id !== closedId);
}
