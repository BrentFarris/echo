import type { TreeNode } from "./explorerTree";
import { isRefWithin } from "./types";

/** A selected directory already includes its selected descendants in an operation. */
export function topLevelTreeNodes(nodes: readonly TreeNode[]): TreeNode[] {
  return nodes.filter((node) => !nodes.some((parent) => parent.key !== node.key
    && parent.kind === "directory" && isRefWithin(node.ref, parent.ref)));
}

export function canDeleteTreeNodes(nodes: readonly TreeNode[]): boolean {
  return nodes.length > 0 && nodes.every((node) => !node.isRoot && !node.readOnly);
}

export function treeMoveDestination(nodes: readonly TreeNode[], target: TreeNode | undefined): TreeNode | null {
  if (!nodes.length || !target || target.kind !== "directory" || target.readOnly || target.blockedReason) return null;
  if (nodes.some((node) => node.isRoot || node.readOnly || node.blockedReason
    || node.ref.rootId !== target.ref.rootId || node.parentKey === target.key || isRefWithin(target.ref, node.ref))) return null;
  return target;
}

/** Keep individual failures from preventing independent items from completing. */
export async function runTreeOperation<T, R>(items: readonly T[], run: (item: T) => Promise<R>): Promise<{
  succeeded: Array<{ item: T; value: R }>;
  failed: Array<{ item: T; error: string }>;
}> {
  const succeeded: Array<{ item: T; value: R }> = [];
  const failed: Array<{ item: T; error: string }> = [];
  for (const item of items) {
    try { succeeded.push({ item, value: await run(item) }); }
    catch (error) { failed.push({ item, error: error instanceof Error ? error.message : String(error) }); }
  }
  return { succeeded, failed };
}
