export type SelectionState = { selected: Set<string>; anchor: string | null };

export function updateSelection(
  current: SelectionState,
  orderedKeys: string[],
  key: string,
  options: { toggle: boolean; range: boolean },
): SelectionState {
  if (options.range && current.anchor) {
    const anchorIndex = orderedKeys.indexOf(current.anchor);
    const targetIndex = orderedKeys.indexOf(key);
    if (anchorIndex >= 0 && targetIndex >= 0) {
      const start = Math.min(anchorIndex, targetIndex);
      const end = Math.max(anchorIndex, targetIndex);
      const selected = options.toggle ? new Set(current.selected) : new Set<string>();
      for (let index = start; index <= end; index++) selected.add(orderedKeys[index]);
      return { selected, anchor: current.anchor };
    }
  }
  if (options.toggle) {
    const selected = new Set(current.selected);
    if (selected.has(key)) selected.delete(key);
    else selected.add(key);
    return { selected, anchor: key };
  }
  return { selected: new Set([key]), anchor: key };
}

export function actionKeys(current: SelectionState, clickedKey: string): string[] {
  return current.selected.has(clickedKey) && current.selected.size > 0 ? [...current.selected] : [clickedKey];
}
