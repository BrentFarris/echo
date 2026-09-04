import type { SourceControlChange, SourceControlRepository, SourceControlStatus } from "./sourceControlTypes";

function cleanPath(value: string | undefined): string {
  const parts: string[] = [];
  for (const part of (value || "").replace(/\\/g, "/").split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") parts.pop();
    else parts.push(part);
  }
  return parts.join("/").toLocaleLowerCase();
}

function joinPath(left: string | undefined, right: string): string {
  return cleanPath(`${left || ""}/${right}`);
}

/**
 * Return a provider-independent workspace file identity. A Git and Fossil
 * checkout rooted at the same directory therefore contribute one activity
 * badge entry, while equally named files in separate nested repositories do
 * not collapse together.
 */
export function sourceControlChangeIdentity(repository: SourceControlRepository, change: Pick<SourceControlChange, "path" | "ref">): string {
  if (change.ref) return `${change.ref.rootId}\0${cleanPath(change.ref.path)}`;
  if (repository.rootRef) return `${repository.rootRef.rootId}\0${joinPath(repository.rootRef.path, change.path)}`;

  const path = cleanPath(change.path);
  const scope = repository.scopes
    .filter((candidate) => {
      const prefix = cleanPath(candidate.repoPrefix);
      return !prefix || path === prefix || path.startsWith(`${prefix}/`);
    })
    .sort((left, right) => cleanPath(right.repoPrefix).length - cleanPath(left.repoPrefix).length)[0];
  if (scope) {
    const prefix = cleanPath(scope.repoPrefix);
    const relative = prefix ? path.slice(prefix.length).replace(/^\//, "") : path;
    return `${scope.rootId}\0${relative}`;
  }
  return `${repository.id}\0${path}`;
}

export function aggregateSourceControlChangeCount(
  repositories: Iterable<SourceControlRepository>,
  statuses: ReadonlyMap<string, SourceControlStatus>,
): number {
  const paths = new Set<string>();
  for (const repository of repositories) {
    const status = statuses.get(repository.id);
    if (!status) continue;
    for (const group of status.groups) {
      for (const change of group.changes || []) paths.add(sourceControlChangeIdentity(repository, change));
    }
    const listed = status.groups.reduce((sum, group) => sum + (group.changes?.length || 0), 0);
    for (let index = listed; index < status.totalChangeCount; index += 1) {
      paths.add(`${repository.id}\0__unlisted_change_${index}`);
    }
  }
  return paths.size;
}
