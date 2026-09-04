import type { SourceControlRepository } from "./sourceControlTypes";
import type { PersistedTab } from "./types";

type PersistedRepository = NonNullable<NonNullable<PersistedTab["diff"]>["repository"]>;

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

/** Translate pre-v4 Git scopes into provider-neutral group targets. */
export function persistedSourceControlGroupId(
  repository: SourceControlRepository,
  scope: NonNullable<PersistedTab["diff"]>["scope"],
  groupId?: string,
): string | undefined {
  if (groupId) return groupId;
  if (repository.providerId !== "git") return undefined;
  if (scope === "staged") return "staged";
  if (scope === "unstaged") return "unstaged";
  return undefined;
}
