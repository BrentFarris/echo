import type { SourceControlRepository } from "./sourceControlTypes";
import type { FileRef, PersistedTab } from "./types";

type PersistedRepository = NonNullable<NonNullable<PersistedTab["diff"]>["repository"]>;

/** Older saves persisted a host path after editing a diff. Recover its provider
 * path from the saved file reference and the repository's registered scope. */
export function persistedSourceControlPath(repository: SourceControlRepository, path: string, ref?: FileRef | null): string {
  if (!/^(?:[a-z]:[\\/]|[\\/])/i.test(path) || !ref) return path;
  const scope = repository.scopes.find((item) => item.rootId === ref.rootId);
  if (!scope) return path;
  return [scope.repoPrefix.replace(/\/$/, ""), ref.path.replace(/\\/g, "/").replace(/^\//, "")].filter(Boolean).join("/");
}

/** Upgrade a Git-shaped persisted repository into the provider-qualified v4 model. */
export function normalizePersistedSourceControlRepository(repository: PersistedRepository): SourceControlRepository {
  return {
    ...repository,
    providerId: repository.providerId || "git",
    providerLabel: repository.providerLabel || "Git",
    available: repository.available !== false,
    capabilities: (repository.capabilities || [
      "status", "diff", "history", "stage", "commitAll", "commitSelected", "sync", "pull", "push",
      "branches", "merge", "stashes", "initialize", "clone",
    ]) as SourceControlRepository["capabilities"],
  };
}

/** Translate legacy presentation scopes into provider-native group targets. */
export function persistedSourceControlGroupId(
  repository: SourceControlRepository,
  scope: NonNullable<PersistedTab["diff"]>["scope"],
  groupId?: string,
): string | undefined {
  if (groupId) return groupId;
  if (repository.providerId === "git") {
    if (scope === "staged" || scope === "included") return "staged";
    if (scope === "unstaged" || scope === "working") return "unstaged";
  }
  if (repository.providerId === "fossil") {
    if (scope === "included") return "protected";
    if (scope === "working") return "working";
  }
  return undefined;
}
