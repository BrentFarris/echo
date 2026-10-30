import type {
  SourceControlActionRequest, SourceControlActionResult,
  SourceControlChangeGroup, SourceControlStatus,
} from "./sourceControlTypes";

const PREDICTED_ACTIONS = new Set([
  "protect", "protect_all", "unprotect", "unprotect_all", "commit_protected", "discard", "discard_all",
]);

type GroupChange = SourceControlChangeGroup["changes"][number];

// Group rows omit the presentation scope; re-derive it from the destination
// group, mirroring normalizeStatus.
function withScope(change: GroupChange, groupId: string): GroupChange {
  const next = { ...change, groupId };
  (next as { scope?: string }).scope = groupId;
  return next;
}

// predictFossilStatus mirrors only membership changes that a completed Fossil
// mutation guarantees. Like predictGitStatus, it never runs before the action
// response, and an authoritative snapshot at a newer revision replaces it.
export function predictFossilStatus(current: SourceControlStatus, request: SourceControlActionRequest, result: SourceControlActionResult): SourceControlStatus {
  if (current.revision >= result.revision || !PREDICTED_ACTIONS.has(request.action)) return current;
  const status: SourceControlStatus = {
    ...current,
    revision: result.revision,
    groups: current.groups.map((group) => ({ ...group, changes: [...group.changes] })),
  };
  const paths = new Set(result.affectedPaths || request.paths || []);
  const group = (id: string) => status.groups.find((candidate) => candidate.id === id);
  const take = (source: SourceControlChangeGroup, predicate: (change: GroupChange) => boolean): GroupChange[] => {
    const moving = source.changes.filter((change) => predicate(change));
    source.changes = source.changes.filter((change) => !predicate(change));
    return moving.map((change) => ({ ...change }));
  };
  const pathInSelection = (change: GroupChange) => paths.has(change.path);
  const workingGroup = group("working");
  const untrackedGroup = group("untracked");
  const protectedGroup = group("protected");
  let moved: GroupChange[] = [];

  if (request.action === "protect" || request.action === "protect_all") {
    moved = [
      ...(workingGroup ? take(workingGroup, pathInSelection) : []),
      ...(untrackedGroup ? take(untrackedGroup, pathInSelection) : []),
    ];
    if (!moved.length) return current;
    const target = protectedGroup || (() => {
      const created: SourceControlChangeGroup = { id: "protected", label: "Protected Changes", role: "included", changes: [], actions: ["unprotect", "commit_protected"] };
      status.groups.push(created);
      return created;
    })();
    for (const change of moved) {
      if (!target.changes.some((candidate) => candidate.path === change.path)) {
        target.changes.push(withScope(change, "protected"));
      }
    }
  } else if (request.action === "unprotect" || request.action === "unprotect_all") {
    if (!protectedGroup) return current;
    moved = take(protectedGroup, request.action === "unprotect_all"
      ? () => true
      : pathInSelection);
    if (!moved.length) return current;
    // An unprotected file that was added after the baseline is untracked
    // again; everything else reappears as an ordinary working change.
    for (const change of moved) {
      const isUntrackedAgain = change.kind === "untracked" || change.kind === "added";
      let destination = isUntrackedAgain ? untrackedGroup : workingGroup;
      if (!destination) {
        destination = isUntrackedAgain
          ? { id: "untracked", label: "Untracked Files", role: "untracked", changes: [], actions: ["protect", "track", "discard"] }
          : { id: "working", label: "Changes", role: "working", changes: [], actions: ["discard", "protect", "untrack"] };
        status.groups.push(destination);
      }
      destination.changes.push(withScope(change, destination.id));
    }
    if (!protectedGroup.changes.length) status.groups = status.groups.filter((candidate) => candidate !== protectedGroup);
  } else if (request.action === "commit_protected") {
    if (!protectedGroup) return current;
    protectedGroup.changes = [];
    status.groups = status.groups.filter((candidate) => candidate !== protectedGroup);
  } else {
    // discard/discard_all: discarded rows (including restored protected
    // versions) no longer differ from the baseline.
    const removeSelected = (source: SourceControlChangeGroup) => {
      source.changes = source.changes.filter((change) => !pathInSelection(change));
    };
    if (request.action === "discard_all") {
      for (const candidate of status.groups) {
        if (candidate.id === "working" || candidate.id === "untracked" || candidate.id === "protected") candidate.changes = [];
      }
    } else {
      for (const candidate of status.groups) removeSelected(candidate);
    }
    for (const id of ["working", "untracked", "protected"]) {
      const emptied = group(id);
      if (emptied && !emptied.changes.length && id !== "working") {
        status.groups = status.groups.filter((candidate) => candidate !== emptied);
      }
    }
  }
  status.totalChangeCount = new Set(
    status.groups.flatMap((candidate) => candidate.changes.map((change) => change.path)),
  ).size;
  return status;
}
